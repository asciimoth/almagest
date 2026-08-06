package almagest

import (
	"context"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/asciimoth/pasta/pasta"
)

type httpClientRequest struct {
	id      uint64
	network NetworkCloser
	url     string
	method  string
	body    string

	tlsConfig    *tls.Config
	tlsConfigErr string
}

type httpClientResult struct {
	id     uint64
	status string
	body   string
	err    string
}

func (n *httpClientNode) startWorker() {
	if n.jobs != nil || n.stop != nil {
		return
	}
	jobs := make(chan httpClientRequest, 1)
	stop := make(chan struct{})
	n.jobs = jobs
	n.stop = stop
	n.logger.Debug("worker started")
	_ = n.SpawnWorker("http client", func() {
		runHTTPClientWorker(n.engine.ctx, n.logger, n.w, n.id, jobs, stop)
	})
}

func (n *httpClientNode) stopWorker() {
	if n.stop != nil {
		n.logger.Debug("worker stopping")
		close(n.stop)
		n.stop = nil
	}
	n.jobs = nil
}

func (n *httpClientNode) restartWorker() {
	n.stopWorker()
	n.startWorker()
}

func runHTTPClientWorker(ctx context.Context, logger pasta.Logger, w *pasta.Workspace, node uint64, jobs <-chan httpClientRequest, stop <-chan struct{}) {
	for {
		select {
		case <-stop:
			logger.Debug("worker stopped")
			return

		case req := <-jobs:
			logger.Debugf("request %d: worker processing", req.id)
			ctx, cancel := context.WithCancel(ctx)
			done := make(chan struct{})

			go func() {
				select {
				case <-stop:
					cancel()
				case <-done:
				}
			}()

			result := doHTTPRequest(ctx, req)

			cancel()
			close(done)

			if result.err != "" {
				logger.Debugf("request %d failed: %s", req.id, result.err)
			} else {
				logger.Debugf("request %d complete: %s", req.id, result.status)
			}
			w.SendInbox(pasta.InboxMessage{ReceiverNode: node, Payload: result})
		}
	}
}

func doHTTPRequest(parent context.Context, req httpClientRequest) httpClientResult {
	if req.network == nil {
		return httpClientResult{id: req.id, err: "network is not ready"}
	}

	ctx, cancel := context.WithTimeout(parent, 15*time.Second)
	defer cancel()

	body := io.Reader(nil)
	if req.body != "" {
		body = strings.NewReader(req.body)
	}

	httpReq, err := http.NewRequestWithContext(ctx, req.method, req.url, body)
	if err != nil {
		return httpClientResult{id: req.id, err: err.Error()}
	}
	if httpReq.URL.Scheme == "https" && req.tlsConfigErr != "" {
		return httpClientResult{id: req.id, err: "TLS config: " + req.tlsConfigErr}
	}

	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			return req.network.Dial(ctx, network, address)
		},
		TLSClientConfig: req.tlsConfig,
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport}

	resp, err := client.Do(httpReq)
	if err != nil {
		return httpClientResult{id: req.id, err: err.Error()}
	}
	defer resp.Body.Close() //nolint

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return httpClientResult{id: req.id, status: resp.Status, err: err.Error()}
	}

	return httpClientResult{id: req.id, status: resp.Status, body: string(data)}
}

func (n *httpClientNode) resetStatusReady() {
	n.requestID++
	n.pending = false
	n.statusText = httpClientStatusReady
	n.resp = ""
	n.errText = ""

	n.restartWorker()
	_ = n.updateLabel()
	n.sendRequestBlock()
	n.sendResultBlock()
}

func (n *httpClientNode) currentTLSConfig() *tls.Config {
	return cloneTLSClientConfig(n.tlsConfig)
}
