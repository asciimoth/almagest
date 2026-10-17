package main

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	sandboxpolicy "github.com/asciimoth/almagest/bin/internal/subprogram/policy"
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

func TestMapSandboxStatusUsesJSONArraysForEmptyTemplateDetails(t *testing.T) {
	status := mapSandboxStatus(sandbox.ServiceStatus{Mode: sandbox.Auto,
		Templates: map[sandbox.TemplateID]sandbox.Status{"tor": {Profile: "linux-full-v1"}}})
	template := status.Templates["tor"]
	if template.Features == nil || template.Warnings == nil || status.Limits == nil {
		t.Fatalf("status contains nil JSON collections: %+v", status)
	}
	data, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if strings.Contains(text, `"features":null`) || strings.Contains(text, `"warnings":null`) || strings.Contains(text, `"limits":null`) {
		t.Fatalf("status contains null JSON collection: %s", text)
	}
}

func TestIsolatedSandboxServicePreservesNil(t *testing.T) {
	if service := isolatedSandboxService(nil); service != nil {
		t.Fatalf("nil isolated client became a non-nil service: %T", service)
	}
}

func TestDirectDaemonBuildsDiscoveryFailureService(t *testing.T) {
	root := t.TempDir()
	conf := &Config{}
	conf.Protected.Sandbox = sandboxpolicy.Config{
		Mode: sandbox.Strict, TemporaryRoot: filepath.Join(root, "temporary"), RegistryRoot: filepath.Join(root, "registry"),
		UnavailableTemplates: map[sandbox.TemplateID]string{"tor": "Tor was not found in the host search path"},
	}
	service, err := directDaemonIntegrations().buildSandboxes(conf)
	if err != nil || service == nil {
		t.Fatalf("build sandbox service = %T, %v", service, err)
	}
	t.Cleanup(func() { _ = service.Close() })
	_, owner, err := service.(sandbox.OwnerBinder).IssueAndBindOwner(context.Background(), sandbox.Owner{
		Workspace: "main", Class: "almagest/TorDriver", Name: "Tor",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = owner.Acquire(context.Background(), "tor", "tor"); !errors.Is(err, sandbox.ErrUnavailable) || !strings.Contains(err.Error(), "Tor was not found") {
		t.Fatalf("Tor acquisition error = %v", err)
	}
}
