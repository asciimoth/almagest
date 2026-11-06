package httpnodes

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/asciimoth/gonnect"
)

func TestFetchAddrProviderParsersNormalizeResults(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		parse func([]byte) (fetchAddrData, error)
		body  string
		want  fetchAddrData
	}{
		{
			name:  "ipify defaults missing fields",
			parse: parseIPifyFetchAddr,
			body:  `{"ip":"2001:0db8::1"}`,
			want:  fetchAddrData{PublicIP: "2001:db8::1"},
		},
		{
			name:  "ifconfig normalizes all fields",
			parse: parseIfConfigFetchAddr,
			body:  `{"ip":"203.0.113.7","country_iso":" ge ","city":" Tbilisi ","latitude":41.7151,"longitude":44.8271}`,
			want: fetchAddrData{
				PublicIP: "203.0.113.7", CountryCode: "GE", City: "Tbilisi",
				Latitude: 41.7151, Longitude: 44.8271,
			},
		},
		{
			name:  "ifconfig discards invalid country",
			parse: parseIfConfigFetchAddr,
			body:  `{"ip":"192.0.2.3","country_iso":"Georgia"}`,
			want:  fetchAddrData{PublicIP: "192.0.2.3"},
		},
		{
			name:  "yggdrasil plain text address",
			parse: parseYggdrasilFetchAddr,
			body:  " 200:56bd:a9e9:c1fa:8f99:1d3c:3c84:6507\n",
			want:  fetchAddrData{PublicIP: "200:56bd:a9e9:c1fa:8f99:1d3c:3c84:6507"},
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := test.parse([]byte(test.body))
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("result = %#v, want %#v", got, test.want)
			}
		})
	}
}

func TestFetchAddrProviderParsersRejectMissingOrInvalidIP(t *testing.T) {
	t.Parallel()
	for _, body := range []string{`{}`, `{"ip":"not-an-ip"}`, `{"ip":""}`} {
		if _, err := parseIfConfigFetchAddr([]byte(body)); err == nil || !strings.Contains(err.Error(), "valid public IP") {
			t.Fatalf("parseIfConfigFetchAddr(%s) error = %v, want invalid public IP", body, err)
		}
	}
	for _, body := range []string{"", "not-an-ip", "200:56bd:a9e9:c1fa:8f99:1d3c:3c84:6507:8080"} {
		if _, err := parseYggdrasilFetchAddr([]byte(body)); err == nil || !strings.Contains(err.Error(), "valid public IP") {
			t.Fatalf("parseYggdrasilFetchAddr(%q) error = %v, want invalid public IP", body, err)
		}
	}
}

func TestDoFetchAddrRequestHandlesHTTPAndResponseFailures(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		handler http.HandlerFunc
		wantErr string
	}{
		{
			name: "http status",
			handler: func(response http.ResponseWriter, _ *http.Request) {
				response.WriteHeader(http.StatusServiceUnavailable)
			},
			wantErr: "503 Service Unavailable",
		},
		{
			name: "large response",
			handler: func(response http.ResponseWriter, _ *http.Request) {
				_, _ = response.Write([]byte(strings.Repeat("x", fetchAddrMaxBodyBytes+1)))
			},
			wantErr: "too large",
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(test.handler)
			t.Cleanup(server.Close)
			network := gonnect.DetachNetwork(gonnect.DefaultNetwork(nil), nil, nil)
			t.Cleanup(func() { _ = network.Close() })
			result := doFetchAddrRequest(context.Background(), fetchAddrRequest{
				id: 1, network: network,
				provider: fetchAddrProvider{name: "test", url: server.URL, parse: parseIfConfigFetchAddr},
			})
			if !strings.Contains(result.err, test.wantErr) {
				t.Fatalf("error = %q, want substring %q", result.err, test.wantErr)
			}
		})
	}
}

func TestDoFetchAddrRequestReportsTLSConfigErrorBeforeDial(t *testing.T) {
	t.Parallel()
	network := gonnect.DetachNetwork(gonnect.DefaultNetwork(nil), nil, nil)
	t.Cleanup(func() { _ = network.Close() })
	result := doFetchAddrRequest(context.Background(), fetchAddrRequest{
		id: 1, network: network, tlsConfigErr: "policy failed",
		provider: fetchAddrProvider{name: "test", url: "https://example.test/", parse: parseIPifyFetchAddr},
	})
	if result.err != "TLS config: policy failed" {
		t.Fatalf("error = %q, want TLS config error", result.err)
	}
}

func TestDoFetchAddrRequestUsesTLSConfig(t *testing.T) {
	t.Parallel()
	server := httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Accept") != "application/json" {
			t.Errorf("Accept header = %q, want application/json", request.Header.Get("Accept"))
		}
		_, _ = response.Write([]byte(`{"ip":"203.0.113.31"}`))
	}))
	t.Cleanup(server.Close)
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	network := gonnect.DetachNetwork(gonnect.DefaultNetwork(nil), nil, nil)
	t.Cleanup(func() { _ = network.Close() })

	result := doFetchAddrRequest(context.Background(), fetchAddrRequest{
		id: 1, network: network,
		provider:  fetchAddrProvider{name: "test", url: server.URL, parse: parseIPifyFetchAddr},
		tlsConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12},
	})
	if result.err != "" || result.data.PublicIP != "203.0.113.31" {
		t.Fatalf("result = %#v, want successful HTTPS result", result)
	}
}
