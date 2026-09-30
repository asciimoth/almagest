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

func TestParseEndpointPipeTrustClasses(t *testing.T) {
	for _, test := range []struct {
		raw   string
		trust pipeTrust
		name  string
	}{
		{raw: "pipe://user", trust: pipeTrustUser},
		{raw: "pipe://machine", trust: pipeTrustMachine},
		{raw: "pipe://user/test-instance", trust: pipeTrustUser, name: "test-instance"},
		{raw: "pipe://machine/test%20instance", trust: pipeTrustMachine, name: "test instance"},
	} {
		ep, err := parseEndpoint(test.raw)
		if err != nil {
			t.Fatalf("parseEndpoint(%q): %v", test.raw, err)
		}
		if ep.kind != endpointPipe || ep.pipeTrust != test.trust || ep.pipeName != test.name {
			t.Fatalf("parseEndpoint(%q) = %#v", test.raw, ep)
		}
	}
}

func TestParseEndpointRejectsUnsafePipeNames(t *testing.T) {
	for _, raw := range []string{
		"pipe://other",
		"pipe://user/../other",
		"pipe://user/a%2Fb",
		"pipe://machine/name?owner=user",
	} {
		if _, err := parseEndpoint(raw); err == nil {
			t.Fatalf("parseEndpoint(%q) succeeded", raw)
		}
	}
}
