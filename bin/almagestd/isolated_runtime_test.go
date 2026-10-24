package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asciimoth/almagest/bin/almagestd/platform"
	"github.com/asciimoth/almagest/bin/internal/isolation/roleentry"
	"github.com/asciimoth/almagest/bin/internal/tlsplatform"
)

func TestIsolatedRuntimeLoadsBrokerPlatformTLSAuthorities(t *testing.T) {
	request := tlsplatform.DefaultRequest()
	snapshot, err := tlsplatform.NewAuthorityProvider(platform.FallbackAuthorityFetcher()).Snapshot(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	loader := newIsolatedTLSPlatformLoader(snapshot, nil)
	data, err := loader(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(data.Certs) == 0 {
		t.Fatal("isolated runtime public TLS authority catalog is empty")
	}
	if _, err := loader(context.Background()); err == nil {
		t.Fatal("isolated refresh fell back after the broker became unavailable")
	}
}

func TestDirectAndIsolatedTLSPlatformAdaptersAreEquivalent(t *testing.T) {
	provider := tlsplatform.NewAuthorityProvider(platform.FallbackAuthorityFetcher())
	direct, err := loadTLSPlatformData(context.Background(), provider)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := provider.Snapshot(context.Background(), tlsplatform.DefaultRequest())
	if err != nil {
		t.Fatal(err)
	}
	isolated, err := newIsolatedTLSPlatformLoader(snapshot, nil)(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(direct.Certs) != len(isolated.Certs) {
		t.Fatalf("certificate counts = %d, %d", len(direct.Certs), len(isolated.Certs))
	}
	for name, cert := range direct.Certs {
		other, ok := isolated.Certs[name]
		if !ok || other.PEM != cert.PEM || other.source != cert.source {
			t.Fatalf("certificate %q differs", name)
		}
	}
}

func TestReadRuntimeConfigConsumesFixedPrivateInputs(t *testing.T) {
	storage := t.TempDir()
	regular := []byte("{\"nodes\":{}}")
	protected := []byte("{}")
	tlsData := []byte(`{"authorities":[]}`)
	for name, data := range map[string][]byte{
		roleentry.RuntimeRegularConfigFile: regular, roleentry.RuntimeProtectedConfigFile: protected,
		roleentry.RuntimeTLSPlatformFile: tlsData,
	} {
		if err := os.WriteFile(filepath.Join(storage, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	gotRegular, gotProtected, gotTLS, err := readRuntimeConfig(storage)
	if err != nil || string(gotRegular) != string(regular) || string(gotProtected) != string(protected) || string(gotTLS) != string(tlsData) {
		t.Fatalf("readRuntimeConfig() = %q, %q, %q, %v", gotRegular, gotProtected, gotTLS, err)
	}
	for _, name := range []string{roleentry.RuntimeRegularConfigFile, roleentry.RuntimeProtectedConfigFile, roleentry.RuntimeTLSPlatformFile} {
		if _, err := os.Stat(filepath.Join(storage, name)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("bootstrap input %s remains: %v", name, err)
		}
	}
}

func TestReadRuntimeConfigFailsClosedForIncompleteInputs(t *testing.T) {
	storage := t.TempDir()
	if err := os.WriteFile(filepath.Join(storage, roleentry.RuntimeRegularConfigFile), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := readRuntimeConfig(storage); err == nil {
		t.Fatal("readRuntimeConfig accepted missing protected input")
	}
	if _, err := os.Stat(filepath.Join(storage, roleentry.RuntimeRegularConfigFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("consumed regular input remains: %v", err)
	}
	storage = t.TempDir()
	for _, name := range []string{roleentry.RuntimeRegularConfigFile, roleentry.RuntimeProtectedConfigFile} {
		if err := os.WriteFile(filepath.Join(storage, name), []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, _, err := readRuntimeConfig(storage); err == nil || !strings.Contains(err.Error(), "TLS platform snapshot") {
		t.Fatalf("missing TLS snapshot error = %v", err)
	}
}

func TestDaemonIntegrationsRequireEveryAuthorityAdapter(t *testing.T) {
	complete := directDaemonIntegrations()
	if err := complete.validate(); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		remove func(*daemonIntegrations)
	}{
		{"config", func(value *daemonIntegrations) { value.prepareConfig = nil }},
		{"storage", func(value *daemonIntegrations) { value.enterStorage = nil }},
		{"os authority", func(value *daemonIntegrations) { value.loadOSAuthority = nil }},
		{"admin", func(value *daemonIntegrations) { value.prepareAdmin = nil }},
		{"commands", func(value *daemonIntegrations) { value.buildCommands = nil }},
		{"network", func(value *daemonIntegrations) { value.buildNetwork = nil }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			value := complete
			test.remove(&value)
			if err := value.validate(); err == nil {
				t.Fatal("incomplete integration accepted")
			}
		})
	}
}

func TestIsolatedAdminAPIMapsAdaptiveIsolationStatus(t *testing.T) {
	status := roleentry.RuntimeIsolationStatus{ProtocolVersion: 1, StatusSchemaVersion: 1, RecoveryState: "complete",
		RequestedPolicy: "auto", ProfileID: "linux-user-v1", ProfileRevision: 2, VerificationState: "verified", Reduced: true,
		Mechanisms:      map[string]map[string]roleentry.RuntimeIsolationMechanism{"runtime": {"seccomp": {State: "applied"}}},
		Credentials:     map[string]roleentry.RuntimeIsolationCredentials{"runtime": {RealUID: 1000, SupplementaryGroups: []uint32{1000}}},
		CapabilitySets:  map[string]roleentry.RuntimeIsolationCapabilitySets{"runtime": {Bounding: []string{"CAP_NET_ADMIN"}}},
		ResourceLimits:  map[string][]roleentry.RuntimeIsolationResourceLimit{"runtime": {{Name: "tasks", Requested: "64", Effective: "64", Unit: "count", Scope: "role", Source: "configured", Enforcement: "applied"}}},
		Warnings:        []roleentry.RuntimeIsolationWarning{{Role: "runtime", Code: "not-delegated", Effect: "No role cgroup.", Feature: "A memory ceiling is unavailable."}},
		ObservationTime: "2026-10-15T12:00:00Z", SelectedPlanDigest: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"}
	admin, err := isolatedAdminAPI(&Config{}, nil, nil, status)
	if err != nil {
		t.Fatal(err)
	}
	got := admin.isolationStatus
	if got == nil || got.StatusSchemaVersion != 1 || got.RequestedPolicy != "auto" || got.ProfileID != "linux-user-v1" || got.ProfileRevision != 2 ||
		!got.Reduced || got.Mechanisms["runtime"]["seccomp"].State != "applied" || got.Credentials["runtime"].RealUID != 1000 ||
		got.CapabilitySets["runtime"].Bounding[0] != "CAP_NET_ADMIN" || got.ResourceLimits["runtime"][0].Effective != "64" ||
		got.Warnings[0].Code != "not-delegated" || got.SelectedPlanDigest != status.SelectedPlanDigest {
		t.Fatalf("mapped status = %#v", got)
	}
	status.Credentials["runtime"] = roleentry.RuntimeIsolationCredentials{}
	status.CapabilitySets["runtime"] = roleentry.RuntimeIsolationCapabilitySets{}
	status.ResourceLimits["runtime"][0].Effective = "changed"
	status.Warnings[0].Effect = "changed"
	if got.Credentials["runtime"].SupplementaryGroups[0] != 1000 || got.CapabilitySets["runtime"].Bounding[0] != "CAP_NET_ADMIN" ||
		got.ResourceLimits["runtime"][0].Effective != "64" || got.Warnings[0].Effect != "No role cgroup." {
		t.Fatal("mapped adaptive status aliases internal input")
	}
}
