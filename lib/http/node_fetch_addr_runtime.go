package httpnodes

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"time"

	"github.com/asciimoth/pasta/pasta"
)

const (
	fetchAddrTimeout      = 15 * time.Second
	fetchAddrMaxBodyBytes = 1 << 20
)

type fetchAddrProvider struct {
	name  string
	url   string
	parse func([]byte) (fetchAddrData, error)
}

var defaultFetchAddrProviders = []fetchAddrProvider{
	{
		name:  FetchAddrProviderIPify,
		url:   "https://api.ipify.org?format=json",
		parse: parseIPifyFetchAddr,
	},
	{
		name:  FetchAddrProviderIfConfig,
		url:   "https://ifconfig.co/json",
		parse: parseIfConfigFetchAddr,
	},
	{
		name:  FetchAddrProviderYggdrasil,
		url:   "http://[200:56bd:a9e9:c1fa:8f99:1d3c:3c84:6507]/",
		parse: parseYggdrasilFetchAddr,
	},
}

type fetchAddrRequest struct {
	id           uint64
	network      NetworkCloser
	provider     fetchAddrProvider
	tlsConfig    *tls.Config
	tlsConfigErr string
}

type fetchAddrResult struct {
	id   uint64
	data fetchAddrData
	err  string
}

func (n *fetchAddrNode) startFetchAddrWorker() {
	if n.jobs != nil || n.stop != nil {
		return
	}
	jobs := make(chan fetchAddrRequest, 1)
	stop := make(chan struct{})
	if err := n.SpawnWorker("fetch address", func() {
		runFetchAddrWorker(n.Context(), n.Logger, n.W, n.ID, jobs, stop)
	}); err != nil {
		return
	}
	n.jobs = jobs
	n.stop = stop
	n.Logger.Debug("worker started")
}

func (n *fetchAddrNode) stopFetchAddrWorker() {
	if n.stop != nil {
		n.Logger.Debug("worker stopping")
		close(n.stop)
	}
	n.jobs = nil
	n.stop = nil
}

func (n *fetchAddrNode) restartFetchAddrWorker() {
	n.stopFetchAddrWorker()
	n.startFetchAddrWorker()
}

func runFetchAddrWorker(parent context.Context, logger pasta.Logger, w *pasta.Workspace, node uint64, jobs <-chan fetchAddrRequest, stop <-chan struct{}) {
	for {
		select {
		case <-stop:
			logger.Debug("worker stopped")
			return
		case request := <-jobs:
			ctx, cancel := context.WithCancel(parent)
			done := make(chan struct{})
			go func() {
				select {
				case <-stop:
					cancel()
				case <-done:
				}
			}()
			result := doFetchAddrRequest(ctx, request)
			cancel()
			close(done)
			if result.err != "" {
				logger.Debugf("request %d failed: %s", request.id, result.err)
			} else {
				logger.Debugf("request %d complete: %s", request.id, result.data.PublicIP)
			}
			w.SendInbox(pasta.InboxMessage{ReceiverNode: node, Payload: result})
		}
	}
}

func doFetchAddrRequest(parent context.Context, request fetchAddrRequest) fetchAddrResult {
	result := fetchAddrResult{id: request.id}
	if request.network == nil {
		result.err = "network is not ready"
		return result
	}
	if request.provider.parse == nil || request.provider.url == "" {
		result.err = "provider is not available"
		return result
	}

	ctx, cancel := context.WithTimeout(parent, fetchAddrTimeout)
	defer cancel()
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodGet, request.provider.url, nil)
	if err != nil {
		result.err = err.Error()
		return result
	}
	if httpRequest.URL.Scheme == "https" && request.tlsConfigErr != "" {
		result.err = "TLS config: " + request.tlsConfigErr
		return result
	}
	httpRequest.Header.Set("Accept", "application/json")
	httpRequest.Header.Set("User-Agent", "Almagest FetchAddr")

	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			return request.network.Dial(ctx, network, address)
		},
		TLSClientConfig: request.tlsConfig,
	}
	defer transport.CloseIdleConnections()
	response, err := (&http.Client{Transport: transport}).Do(httpRequest)
	if err != nil {
		result.err = err.Error()
		return result
	}
	defer response.Body.Close() //nolint
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		result.err = "provider returned " + response.Status
		return result
	}

	body, err := io.ReadAll(io.LimitReader(response.Body, fetchAddrMaxBodyBytes+1))
	if err != nil {
		result.err = err.Error()
		return result
	}
	if len(body) > fetchAddrMaxBodyBytes {
		result.err = "provider response is too large"
		return result
	}
	result.data, err = request.provider.parse(body)
	if err != nil {
		result.err = err.Error()
	}
	return result
}

func parseIPifyFetchAddr(body []byte) (fetchAddrData, error) {
	var value struct {
		IP string `json:"ip"`
	}
	if err := json.Unmarshal(body, &value); err != nil {
		return fetchAddrData{}, fmt.Errorf("decode ipify response: %w", err)
	}
	return normalizeFetchAddrData(fetchAddrData{PublicIP: value.IP})
}

func parseIfConfigFetchAddr(body []byte) (fetchAddrData, error) {
	var value struct {
		IP          string  `json:"ip"`
		CountryCode string  `json:"country_iso"`
		City        string  `json:"city"`
		Latitude    float64 `json:"latitude"`
		Longitude   float64 `json:"longitude"`
	}
	if err := json.Unmarshal(body, &value); err != nil {
		return fetchAddrData{}, fmt.Errorf("decode ifconfig.co response: %w", err)
	}
	return normalizeFetchAddrData(fetchAddrData{
		PublicIP: value.IP, CountryCode: value.CountryCode, City: value.City,
		Latitude: value.Latitude, Longitude: value.Longitude,
	})
}

func parseYggdrasilFetchAddr(body []byte) (fetchAddrData, error) {
	return normalizeFetchAddrData(fetchAddrData{PublicIP: string(body)})
}

func normalizeFetchAddrData(value fetchAddrData) (fetchAddrData, error) {
	addr, err := netip.ParseAddr(strings.TrimSpace(value.PublicIP))
	if err != nil {
		return fetchAddrData{}, errors.New("provider response does not contain a valid public IP address")
	}
	value.PublicIP = addr.String()
	value.CountryCode = normalizeFetchAddrCountryCode(value.CountryCode)
	value.City = strings.TrimSpace(value.City)
	return value, nil
}

func normalizeFetchAddrCountryCode(value string) string {
	value = strings.ToUpper(strings.TrimSpace(value))
	if len(value) != 2 || value[0] < 'A' || value[0] > 'Z' || value[1] < 'A' || value[1] > 'Z' {
		return ""
	}
	return value
}

func (n *fetchAddrNode) checkFetchAddr() {
	if n.pending {
		n.Logger.Debug("request ignored: already pending")
		return
	}
	provider, ok := n.currentFetchAddrProvider()
	if !ok {
		n.completeFetchAddrFailure("provider is not available")
		return
	}

	n.requestID++
	request := fetchAddrRequest{
		id: n.requestID, network: n.network, provider: provider,
		tlsConfig: cloneTLSClientConfig(n.tlsConfig), tlsConfigErr: n.tlsConfigErr,
	}
	n.pending = true
	n.statusText = fetchAddrStatusRunning
	n.errText = ""
	_ = n.updateFetchAddrLabel()
	n.sendFetchAddrCheckBlock()
	n.sendFetchAddrResultBlock()

	if n.jobs == nil {
		n.startFetchAddrWorker()
	}
	if n.jobs == nil {
		n.completeFetchAddrFailure("worker is not available")
		return
	}
	select {
	case n.jobs <- request:
	default:
		n.completeFetchAddrFailure("worker is busy")
	}
}

func (n *fetchAddrNode) currentFetchAddrProvider() (fetchAddrProvider, bool) {
	for _, provider := range n.providers {
		if provider.name == n.provider {
			return provider, true
		}
	}
	return fetchAddrProvider{}, false
}

func (n *fetchAddrNode) handleFetchAddrResult(result fetchAddrResult) {
	if result.id != n.requestID {
		return
	}
	if result.err != "" {
		n.completeFetchAddrFailure(result.err)
		return
	}
	n.pending = false
	n.statusText = fetchAddrStatusSuccess
	n.data = result.data
	n.errText = ""
	_ = n.updateFetchAddrLabel()
	n.sendFetchAddrCheckBlock()
	n.sendFetchAddrResultBlock()
	n.emitAllFetchAddrData()
	n.emitFetchAddrTrigger(n.successOut)
}

func (n *fetchAddrNode) completeFetchAddrFailure(errText string) {
	n.pending = false
	n.statusText = fetchAddrStatusFail
	n.data = fetchAddrData{}
	n.errText = errText
	_ = n.updateFetchAddrLabel()
	n.sendFetchAddrCheckBlock()
	n.sendFetchAddrResultBlock()
	n.emitAllFetchAddrData()
	n.emitFetchAddrTrigger(n.failOut)
}

func (n *fetchAddrNode) resetFetchAddrState() {
	n.requestID++
	n.pending = false
	n.statusText = fetchAddrStatusReady
	n.data = fetchAddrData{}
	n.errText = ""
	n.restartFetchAddrWorker()
	_ = n.updateFetchAddrLabel()
	n.sendFetchAddrCheckBlock()
	n.sendFetchAddrResultBlock()
	n.emitAllFetchAddrData()
}
