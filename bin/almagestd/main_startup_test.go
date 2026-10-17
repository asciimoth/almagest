package main

import (
	"slices"
	"testing"

	sandbox "github.com/asciimoth/gobox"
)

func TestExecutionModeStartupWarnings(t *testing.T) {
	want := []string{
		"Daemon process isolation is disabled",
		"A compromised graph runtime can access all daemon privileges and host resources",
	}
	if got := executionModeStartupWarnings(executionModeDirect); !slices.Equal(got, want) {
		t.Fatalf("direct mode warnings = %#v, want %#v", got, want)
	}

	for _, mode := range []executionMode{executionModeIsolation, executionModeIsolationDevelopment} {
		if got := executionModeStartupWarnings(mode); len(got) != 0 {
			t.Errorf("%s mode warnings = %#v, want none", mode, got)
		}
	}
}

func TestMapSandboxStatus(t *testing.T) {
	status := mapSandboxStatus(sandbox.ServiceStatus{Mode: sandbox.Auto, Platform: "test-os", EffectiveIdentity: "test-user",
		Templates: map[sandbox.TemplateID]sandbox.Status{"template": {Profile: "profile-v1", EffectiveIdentity: "200000:200000", Features: []sandbox.FeatureResult{{Feature: sandbox.PrivateNetwork, State: sandbox.OmittedAuto, Reason: "not-qualified"}}, Warnings: []string{"warning"}}},
		Limits:    map[string]int64{"instances": 2}, ActiveOwners: 3, OrphanedOwners: 4, QuarantinedOwners: 1})
	if status.Mode != "auto" || status.Platform != "test-os" || status.Templates["template"].Profile != "profile-v1" || status.Templates["template"].EffectiveIdentity != "200000:200000" || status.Templates["template"].Features[0].State != "omitted-auto" || status.ActiveOwners != 3 || status.OrphanedOwners != 4 || status.QuarantinedOwners != 1 {
		t.Fatalf("status = %+v", status)
	}
}

func TestIsolatedSandboxServicePreservesNil(t *testing.T) {
	if service := isolatedSandboxService(nil); service != nil {
		t.Fatalf("nil isolated client became a non-nil service: %T", service)
	}
}
