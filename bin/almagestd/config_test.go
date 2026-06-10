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

func TestProtectedConfigWebFilters(t *testing.T) {
	cfg, err := ParseConfig([]byte(`{}`), []byte(`{
		"Admin": {
			"Web": 8080,
			"WebFilters": {
				"UIDs": [1000],
				"GIDs": [1001],
				"Usernames": ["alice"]
			}
		}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Protected.Admin.Web; got != 8080 {
		t.Fatalf("Admin.Web = %d, want 8080", got)
	}
	if got := cfg.Protected.Admin.WebFilters.UIDs; len(got) != 1 || got[0] != 1000 {
		t.Fatalf("Admin.WebFilters.UIDs = %v, want [1000]", got)
	}
	if got := cfg.Protected.Admin.WebFilters.GIDs; len(got) != 1 || got[0] != 1001 {
		t.Fatalf("Admin.WebFilters.GIDs = %v, want [1001]", got)
	}
	if got := cfg.Protected.Admin.WebFilters.Usernames; len(got) != 1 || got[0] != "alice" {
		t.Fatalf("Admin.WebFilters.Usernames = %v, want [alice]", got)
	}
	if !cfg.Protected.Admin.WebFilters.Enabled() {
		t.Fatal("Admin.WebFilters.Enabled() = false, want true")
	}
}

func TestProtectedConfigMergeWebFilterArgs(t *testing.T) {
	cfg, err := ParseConfig([]byte(`{}`), []byte(`{
		"Admin": {
			"Web": 8080,
			"WebFilters": {
				"UIDs": [1000],
				"GIDs": [1001],
				"Usernames": ["alice"]
			}
		}
	}`))
	if err != nil {
		t.Fatal(err)
	}

	web := uint16(9090)
	cfg.Protected.Merge(&Args{
		WebAdmin:                &web,
		WebAdminFilterUIDs:      []uint32{2000, 2001},
		WebAdminFilterUsernames: []string{"bob", "carol"},
	})

	if got := cfg.Protected.Admin.Web; got != 9090 {
		t.Fatalf("Admin.Web = %d, want 9090", got)
	}
	if got := cfg.Protected.Admin.WebFilters.UIDs; len(got) != 2 || got[0] != 2000 || got[1] != 2001 {
		t.Fatalf("Admin.WebFilters.UIDs = %v, want [2000 2001]", got)
	}
	if got := cfg.Protected.Admin.WebFilters.GIDs; len(got) != 1 || got[0] != 1001 {
		t.Fatalf("Admin.WebFilters.GIDs = %v, want config value [1001]", got)
	}
	if got := cfg.Protected.Admin.WebFilters.Usernames; len(got) != 2 || got[0] != "bob" || got[1] != "carol" {
		t.Fatalf("Admin.WebFilters.Usernames = %v, want [bob carol]", got)
	}
}
