package main

import "testing"

func TestParseEndpointDefault(t *testing.T) {
	ep, err := parseEndpoint("")
	if err != nil {
		t.Fatal(err)
	}
	if ep.kind != endpointUnix || ep.path != "/run/almagest/admin.sock" {
		t.Fatalf("endpoint = %#v, want unix /run/almagest/admin.sock", ep)
	}
}

func TestParseEndpointUnixAbsoluteHostPath(t *testing.T) {
	ep, err := parseEndpoint("unix://tmp/almagest.sock")
	if err != nil {
		t.Fatal(err)
	}
	if ep.path != "/tmp/almagest.sock" {
		t.Fatalf("path = %q, want /tmp/almagest.sock", ep.path)
	}
}

func TestParseEndpointRejectsRelativeUnixPath(t *testing.T) {
	if _, err := parseEndpoint("unix:run/almagest.sock"); err == nil {
		t.Fatal("expected relative unix path error")
	}
}

func TestParseEndpointWebDefaultsAPIPath(t *testing.T) {
	ep, err := parseEndpoint("web://127.0.0.1:8090")
	if err != nil {
		t.Fatal(err)
	}
	if ep.kind != endpointWeb || ep.url != "ws://127.0.0.1:8090/api/ws" {
		t.Fatalf("endpoint = %#v, want web websocket API URL", ep)
	}
}
