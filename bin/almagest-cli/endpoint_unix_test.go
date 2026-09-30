//go:build unix

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

func TestParseEndpointUnixAdditionalValidation(t *testing.T) {
	if _, err := parseEndpoint("unix:"); err == nil {
		t.Fatal("empty Unix endpoint succeeded")
	}
	ep, err := parseEndpoint("unix:/tmp/../tmp/admin.sock")
	if err != nil || ep.path != "/tmp/admin.sock" {
		t.Fatalf("clean Unix endpoint = %#v, %v", ep, err)
	}
}
