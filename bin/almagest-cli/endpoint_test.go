package main

import (
	"strings"
	"testing"
)

func TestParseEndpointWebDefaultsAPIPath(t *testing.T) {
	ep, err := parseEndpoint("web://127.0.0.1:8090")
	if err != nil {
		t.Fatal(err)
	}
	if ep.kind != endpointWeb || ep.url != "ws://127.0.0.1:8090/api/ws" {
		t.Fatalf("endpoint = %#v, want web websocket API URL", ep)
	}
}

func TestParseEndpointAdditionalValidation(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{name: "empty web host", raw: "web:/api/ws", want: "host is empty"},
		{name: "missing scheme", raw: "admin.sock", want: "must use"},
		{name: "unsupported scheme", raw: "https://example.test", want: "unsupported"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := parseEndpoint(test.raw)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("parseEndpoint(%q) error = %v, want %q", test.raw, err, test.want)
			}
		})
	}
	ep, err := parseEndpoint("web://example.test/custom?token=value")
	if err != nil || ep.url != "ws://example.test/custom?token=value" {
		t.Fatalf("custom web endpoint = %#v, %v", ep, err)
	}
}
