//go:build windows

package main

import (
	"testing"

	"golang.org/x/sys/windows"
)

func TestParseEndpointDefaultWindowsMachinePipe(t *testing.T) {
	ep, err := parseEndpoint("")
	if err != nil {
		t.Fatal(err)
	}
	if ep.kind != endpointPipe || ep.pipeTrust != pipeTrustMachine || ep.pipeName != "" {
		t.Fatalf("default endpoint = %#v, want machine pipe", ep)
	}
}

func TestTrustedPipeOwnerAndNameSeparatesUserAndMachine(t *testing.T) {
	userOwner, userName, err := trustedPipeOwnerAndName(endpoint{pipeTrust: pipeTrustUser})
	if err != nil {
		t.Fatal(err)
	}
	machineOwner, machineName, err := trustedPipeOwnerAndName(endpoint{pipeTrust: pipeTrustMachine})
	if err != nil {
		t.Fatal(err)
	}
	if !machineOwner.IsWellKnown(windows.WinLocalSystemSid) {
		t.Fatalf("machine pipe owner = %s, want Local System", machineOwner)
	}
	if userName != "almagest-admin-user-"+userOwner.String() {
		t.Fatalf("user pipe name = %q", userName)
	}
	if userName == machineName {
		t.Fatalf("user and machine pipe names are both %q", userName)
	}
}
