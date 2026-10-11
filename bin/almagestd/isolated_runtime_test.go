package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/asciimoth/almagest/bin/internal/isolation/roleentry"
)

func TestReadRuntimeConfigConsumesFixedPrivateInputs(t *testing.T) {
	storage := t.TempDir()
	regular := []byte("{\"nodes\":{}}")
	protected := []byte("{}")
	for name, data := range map[string][]byte{
		roleentry.RuntimeRegularConfigFile: regular, roleentry.RuntimeProtectedConfigFile: protected,
	} {
		if err := os.WriteFile(filepath.Join(storage, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	gotRegular, gotProtected, err := readRuntimeConfig(storage)
	if err != nil || string(gotRegular) != string(regular) || string(gotProtected) != string(protected) {
		t.Fatalf("readRuntimeConfig() = %q, %q, %v", gotRegular, gotProtected, err)
	}
	for _, name := range []string{roleentry.RuntimeRegularConfigFile, roleentry.RuntimeProtectedConfigFile} {
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
	if _, _, err := readRuntimeConfig(storage); err == nil {
		t.Fatal("readRuntimeConfig accepted missing protected input")
	}
	if _, err := os.Stat(filepath.Join(storage, roleentry.RuntimeRegularConfigFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("consumed regular input remains: %v", err)
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
