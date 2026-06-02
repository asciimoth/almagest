package main

import (
	"testing"
	"time"
)

func TestConfigSaveIdleDefault(t *testing.T) {
	cfg, err := ParseConfig([]byte(`{}`), []byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.SaveIdle(); got != 500*time.Millisecond {
		t.Fatalf("SaveIdle() = %v, want 500ms", got)
	}
}

func TestConfigSaveIdleConfigured(t *testing.T) {
	cfg, err := ParseConfig([]byte(`{"Config":{"Save":{"IdleMilliseconds":1250}}}`), []byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.SaveIdle(); got != 1250*time.Millisecond {
		t.Fatalf("SaveIdle() = %v, want 1250ms", got)
	}
}

func TestConfigSaveIdleNonPositiveUsesDefault(t *testing.T) {
	cfg, err := ParseConfig([]byte(`{"Config":{"Save":{"IdleMilliseconds":0}}}`), []byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.SaveIdle(); got != 500*time.Millisecond {
		t.Fatalf("SaveIdle() = %v, want 500ms", got)
	}
}
