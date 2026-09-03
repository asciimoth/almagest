package amnesianodes

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	wgoamnesia "github.com/asciimoth/wgo/amnesia"

	"github.com/asciimoth/almagest/lib/logging"
	"github.com/asciimoth/almagest/lib/nodekit"
)

var _ amnesiaStatefulClient = (*wgoamnesia.Client)(nil)

type amnesiaRoundTripFunc func(*http.Request) (*http.Response, error)

func (f amnesiaRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestAmnesiaAPITransportLogsRequestStateAndRedactsURLSecrets(t *testing.T) {
	var messages []string
	transport := newAmnesiaAPIRoundTripper(
		amnesiaRoundTripFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusCreated,
				Body:       io.NopCloser(strings.NewReader("secret response body")),
			}, nil
		}),
		func(format string, args ...any) {
			messages = append(messages, fmt.Sprintf(format, args...))
		},
	)
	request, err := http.NewRequest(
		http.MethodPost,
		"https://user:password@gateway.test:8443/private-prefix/v1/config?token=secret-query",
		strings.NewReader("secret request body"),
	)
	if err != nil {
		t.Fatalf("create request: %v", err)
	}
	request.Header.Set("X-Client-Request-ID", "safe-request-uuid")
	response, err := transport.RoundTrip(request)
	if err != nil {
		t.Fatalf("round trip: %v", err)
	}
	_ = response.Body.Close()

	logs := strings.Join(messages, "\n")
	for _, expected := range []string{
		"API request started",
		"kind=gateway-config",
		`host="gateway.test:8443"`,
		`request_id="safe-request-uuid"`,
		"API response received",
		"status=201",
	} {
		if !strings.Contains(logs, expected) {
			t.Errorf("API logs do not contain %q\nlogs:\n%s", expected, logs)
		}
	}
	for _, secret := range []string{
		"user",
		"password",
		"private-prefix",
		"secret-query",
		"secret request body",
		"secret response body",
	} {
		if strings.Contains(logs, secret) {
			t.Errorf("API logs expose secret %q", secret)
		}
	}
}

func TestAmnesiaAPIRequestKinds(t *testing.T) {
	tests := []struct {
		name      string
		url       string
		requestID string
		want      string
	}{
		{name: "gateway", url: "https://gateway.test/v1/config", requestID: "uuid", want: "gateway-config"},
		{name: "health", url: "https://proxy.test/lmbd-health", want: "proxy-health"},
		{name: "discovery", url: "https://storage.test/endpoints.json", want: "proxy-discovery"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			request, err := http.NewRequest(http.MethodGet, tt.url, nil)
			if err != nil {
				t.Fatalf("create request: %v", err)
			}
			request.Header.Set("X-Client-Request-ID", tt.requestID)
			if got := amnesiaAPIRequestKind(request); got != tt.want {
				t.Fatalf("request kind = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestAmnesiaEndpointHostPort(t *testing.T) {
	for _, tt := range []struct {
		endpoint string
		host     string
		port     string
	}{
		{endpoint: "vpn.example.test:51820", host: "vpn.example.test", port: "51820"},
		{endpoint: "[2001:db8::1]:51820", host: "2001:db8::1", port: "51820"},
		{endpoint: "invalid", host: "", port: ""},
	} {
		host, port := amnesiaEndpointHostPort(tt.endpoint)
		if host != tt.host || port != tt.port {
			t.Errorf("endpoint %q = host %q port %q, want host %q port %q", tt.endpoint, host, port, tt.host, tt.port)
		}
	}
}

func TestAmnesiaAPIClientErrorLogRedactsServerMessage(t *testing.T) {
	logFactory := logging.NewDebugLogFactory(16)
	node := &amnesiaNode{Base: nodekit.Base{Logger: logFactory.NodeLogger(42, NodeTypeAmnesiaCtrl)}}
	node.logAmnesiaClientFailure(&wgoamnesia.APIError{
		Code:       wgoamnesia.ErrorSubscriptionInactive,
		HTTPStatus: http.StatusPaymentRequired,
		Message:    "secret server message",
	}, wgoamnesia.InputFormatServiceKey, "failed")

	var messages strings.Builder
	for _, entry := range logFactory.Drain() {
		messages.WriteString(entry.Message)
		messages.WriteByte('\n')
	}
	logs := messages.String()
	for _, expected := range []string{"gateway error received", "subscription_inactive", "status=402"} {
		if !strings.Contains(logs, expected) {
			t.Errorf("API client logs do not contain %q\nlogs:\n%s", expected, logs)
		}
	}
	if strings.Contains(logs, "secret server message") {
		t.Errorf("API client logs expose the server message\nlogs:\n%s", logs)
	}
}
