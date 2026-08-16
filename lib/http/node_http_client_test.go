package httpnodes

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/asciimoth/gonnect"
)

func TestHTTPRequestUsesTLSConfig(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(rw http.ResponseWriter, _ *http.Request) {
		_, _ = rw.Write([]byte("ok"))
	}))
	t.Cleanup(server.Close)

	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	network := gonnect.DetachNetwork(gonnect.DefaultNetwork(nil), nil, nil)
	t.Cleanup(func() { _ = network.Close() })

	result := doHTTPRequest(context.Background(), httpClientRequest{
		id:      1,
		network: network,
		url:     server.URL,
		method:  http.MethodGet,
		tlsConfig: &tls.Config{
			RootCAs:    roots,
			MinVersion: tls.VersionTLS12,
		},
	})
	if result.err != "" || result.status != "200 OK" || result.body != "ok" {
		t.Fatalf("HTTPS result = %#v, want 200 OK body ok", result)
	}
}

func TestHTTPRequestReportsTLSConfigErrorForHTTPS(t *testing.T) {
	network := gonnect.DetachNetwork(gonnect.DefaultNetwork(nil), nil, nil)
	t.Cleanup(func() { _ = network.Close() })

	result := doHTTPRequest(context.Background(), httpClientRequest{
		id:           1,
		network:      network,
		url:          "https://example.test/",
		method:       http.MethodGet,
		tlsConfigErr: "policy failed",
	})
	if result.err != "TLS config: policy failed" {
		t.Fatalf("HTTPS TLS config error = %q, want policy failed", result.err)
	}
}
