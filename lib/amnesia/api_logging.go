package amnesianodes

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	wgoamnesia "github.com/asciimoth/wgo/amnesia"
)

type amnesiaStatefulClient interface {
	StartImport(input string, options wgoamnesia.NegotiationOptions) (*wgoamnesia.ImportSession, error)
}

type amnesiaAPIRoundTripper struct {
	base http.RoundTripper
	logf func(string, ...any)
	next atomic.Uint64
}

func newAmnesiaAPIRoundTripper(base http.RoundTripper, logf func(string, ...any)) *amnesiaAPIRoundTripper {
	return &amnesiaAPIRoundTripper{base: base, logf: logf}
}

func (t *amnesiaAPIRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	requestNumber := t.next.Add(1)
	kind := amnesiaAPIRequestKind(request)
	host := ""
	tlsEnabled := false
	requestID := ""
	if request != nil {
		requestID = request.Header.Get("X-Client-Request-ID")
		if request.URL != nil {
			host = request.URL.Host
			tlsEnabled = request.URL.Scheme == "https"
		}
	}
	amnesiaAPILog(
		t.logf,
		"Amnesia API request started: request=%d kind=%s method=%s host=%q tls=%t request_id=%q",
		requestNumber, kind, amnesiaAPIRequestMethod(request), host, tlsEnabled, requestID,
	)
	started := time.Now()
	response, err := t.base.RoundTrip(request)
	duration := time.Since(started).Round(time.Millisecond)
	if err != nil {
		amnesiaAPILog(
			t.logf,
			"Amnesia API request failed: request=%d kind=%s host=%q duration=%s error_type=%T context_cancelled=%t",
			requestNumber, kind, host, duration, err, request != nil && request.Context().Err() != nil,
		)
		return response, err
	}
	status := 0
	if response != nil {
		status = response.StatusCode
	}
	amnesiaAPILog(
		t.logf,
		"Amnesia API response received: request=%d kind=%s host=%q status=%d duration=%s",
		requestNumber, kind, host, status, duration,
	)
	return response, nil
}

func (t *amnesiaAPIRoundTripper) CloseIdleConnections() {
	if closer, ok := t.base.(interface{ CloseIdleConnections() }); ok {
		closer.CloseIdleConnections()
	}
}

func (n *amnesiaNode) importAmnesiaProfile(
	ctx context.Context,
	client Client,
	input string,
	options wgoamnesia.NegotiationOptions,
) (*wgoamnesia.Profile, error) {
	stateful, ok := client.(amnesiaStatefulClient)
	if !ok {
		n.logAmnesiaClientState("ready", "requesting", wgoamnesia.InputFormatServiceKey)
		profile, err := client.ImportNonInteractive(ctx, input, options)
		if err != nil {
			n.logAmnesiaClientFailure(err, wgoamnesia.InputFormatServiceKey, "failed")
			return nil, err
		}
		n.logAmnesiaClientState("requesting", "complete", wgoamnesia.InputFormatServiceKey)
		n.logAmnesiaProfileReceived(profile, wgoamnesia.InputFormatServiceKey)
		return profile, nil
	}

	session, err := stateful.StartImport(input, options)
	if err != nil {
		n.logDebugf("Amnesia API client import initialization failed: error_type=%T", err)
		n.logAmnesiaClientState("ready", "failed", wgoamnesia.InputFormatServiceKey)
		return nil, err
	}
	format := session.Format()
	currentState := amnesiaNegotiationStateText(session.State())
	n.logDebugf("Amnesia API client import initialized: format=%s state=%s", format, currentState)
	n.logAmnesiaClientState(currentState, "requesting", format)
	step, err := session.Next(ctx)
	if err != nil {
		n.logAmnesiaClientFailure(err, format, amnesiaNegotiationStateText(session.State()))
		return nil, err
	}
	nextState := amnesiaNegotiationStateText(step.State)
	n.logAmnesiaClientState("requesting", nextState, format)
	if step.Interaction != nil {
		n.logDebugf(
			"Amnesia API client interaction received: format=%s kind=%s attempt=%d captcha_present=%t",
			format, step.Interaction.Kind, step.Interaction.Attempt, step.Interaction.Captcha != nil,
		)
	}
	if step.Profile == nil {
		n.logAmnesiaProfileReceived(nil, format)
		return nil, errors.New("amnezia API client import returned no profile")
	}
	n.logAmnesiaProfileReceived(step.Profile, format)
	return step.Profile, nil
}

func (n *amnesiaNode) logAmnesiaClientFailure(err error, format wgoamnesia.InputFormat, state string) {
	var apiError *wgoamnesia.APIError
	if errors.As(err, &apiError) {
		n.logDebugf(
			"Amnesia API client gateway error received: format=%s code=%s status=%d",
			format, apiError.Code, apiError.HTTPStatus,
		)
	}
	var protocolError *wgoamnesia.ProtocolError
	if errors.As(err, &protocolError) {
		n.logDebugf(
			"Amnesia API client protocol failure: format=%s operation=%q error_type=%T",
			format, protocolError.Op, protocolError.Err,
		)
	}
	var interaction *wgoamnesia.InteractionRequiredError
	if errors.As(err, &interaction) {
		n.logDebugf(
			"Amnesia API client interaction required: format=%s kind=%s attempt=%d captcha_present=%t",
			format, interaction.Request.Kind, interaction.Request.Attempt, interaction.Request.Captcha != nil,
		)
	}
	n.logDebugf("Amnesia API client state changed: format=%s previous=requesting current=%s error_type=%T", format, state, err)
}

func (n *amnesiaNode) logAmnesiaClientState(previous, current string, format wgoamnesia.InputFormat) {
	n.logDebugf("Amnesia API client state changed: format=%s previous=%s current=%s", format, previous, current)
}

func (n *amnesiaNode) logAmnesiaProfileReceived(profile *wgoamnesia.Profile, format wgoamnesia.InputFormat) {
	if profile == nil {
		n.logDebugf("Amnesia API client profile received: format=%s profile_present=false", format)
		return
	}
	endpointHost, endpointPort := amnesiaEndpointHostPort(profile.Peer.Endpoint)
	n.logDebugf(
		"Amnesia API client profile received: format=%s profile_present=true name=%q hostname=%q country=%q endpoint_host=%q endpoint_port=%q peer_public_key=%s addresses=%v dns=%v allowed_ips=%v pre_connect_actions=%d amneziawg_version=%q",
		format, profile.Name, profile.HostName, profile.API.ServerCountryCode, endpointHost, endpointPort,
		profile.Peer.PublicKey, profile.Interface.Addresses, profile.Interface.DNS, profile.Peer.AllowedIPs,
		len(profile.PreConnect), profile.AmneziaWG.ProtocolVersion,
	)
}

func amnesiaEndpointHostPort(endpoint string) (string, string) {
	host, port, err := net.SplitHostPort(strings.TrimSpace(endpoint))
	if err != nil {
		return "", ""
	}
	return host, port
}

func amnesiaNegotiationStateText(state wgoamnesia.NegotiationState) string {
	switch state {
	case wgoamnesia.NegotiationReady:
		return "ready"
	case wgoamnesia.NegotiationRequesting:
		return "requesting"
	case wgoamnesia.NegotiationAwaitingInteraction:
		return "awaiting-interaction"
	case wgoamnesia.NegotiationComplete:
		return "complete"
	case wgoamnesia.NegotiationFailed:
		return "failed"
	default:
		return "unknown"
	}
}

func amnesiaAPIRequestKind(request *http.Request) string {
	if request == nil {
		return "unknown"
	}
	if request.Header.Get("X-Client-Request-ID") != "" {
		return "gateway-config"
	}
	if request.URL != nil && strings.HasSuffix(strings.TrimSuffix(request.URL.Path, "/"), "/lmbd-health") {
		return "proxy-health"
	}
	return "proxy-discovery"
}

func amnesiaAPIRequestMethod(request *http.Request) string {
	if request == nil {
		return ""
	}
	return request.Method
}

func amnesiaAPILog(logf func(string, ...any), format string, args ...any) {
	if logf != nil {
		logf(format, args...)
	}
}
