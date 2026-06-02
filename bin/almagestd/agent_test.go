package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/asciimoth/configer/configer"
	confighujson "github.com/asciimoth/configer/hujson"
	tailscalehujson "github.com/tailscale/hujson"
)

func TestAgentWriteConfigSavesFormattedHuJSON(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "alm.hujson")
	if err := os.WriteFile(configPath, []byte(`{"nodes":{}}`), 0o640); err != nil {
		t.Fatal(err)
	}

	cfg, err := confighujson.Parse([]byte(`{"nodes":{"node":{"enabled":true}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.Set(configer.Path{"nodes", "node", "name"}, "test"); err != nil {
		t.Fatal(err)
	}

	agent := &Agent{configPath: configPath}
	if err := agent.WriteConfig(cfg); err != nil {
		t.Fatalf("WriteConfig: %v", err)
	}

	got, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	want, err := tailscalehujson.Format(cfg.Pack())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("written config mismatch\ngot:\n%s\nwant:\n%s", got, want)
	}
}

func TestAgentWriteConfigIgnoresReadOnlyAgent(t *testing.T) {
	cfg, err := confighujson.Parse([]byte(`{"nodes":{}}`))
	if err != nil {
		t.Fatal(err)
	}

	agent := &Agent{configReadOnly: true, configPath: filepath.Join(t.TempDir(), "missing.hujson")}
	if err := agent.WriteConfig(cfg); err != nil {
		t.Fatalf("WriteConfig: %v", err)
	}
}

func TestAgentWriteConfigReturnsOverwriteError(t *testing.T) {
	configPath := t.TempDir()

	cfg, err := confighujson.Parse([]byte(`{"nodes":{"node":{"enabled":true}}}`))
	if err != nil {
		t.Fatal(err)
	}

	agent := &Agent{configPath: configPath}
	if err := agent.WriteConfig(cfg); err == nil {
		t.Fatal("WriteConfig succeeded for read-only file")
	}
}
