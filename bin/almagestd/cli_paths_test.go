package main

import (
	"path/filepath"
	"testing"

	"github.com/asciimoth/almagest/bin/almagestd/args"
)

func TestResolveCLIPathArgs(t *testing.T) {
	base := t.TempDir()
	t.Chdir(base)

	unixSocket := "run/admin.sock"
	a := args.Args{
		Config:          "conf/alm.hujson",
		ProtectedConfig: "conf/protected.hujson",
		FileStorage:     "store",
		Snapshot:        "backups/state.zip",
		UnixAdmin:       &unixSocket,
	}

	if err := resolveCLIPathArgs(&a); err != nil {
		t.Fatalf("resolveCLIPathArgs: %v", err)
	}

	if got, want := a.Config, filepath.Join(base, "conf", "alm.hujson"); got != want {
		t.Fatalf("Config = %q, want %q", got, want)
	}
	if got, want := a.ProtectedConfig, filepath.Join(base, "conf", "protected.hujson"); got != want {
		t.Fatalf("ProtectedConfig = %q, want %q", got, want)
	}
	if got, want := a.FileStorage, filepath.Join(base, "store"); got != want {
		t.Fatalf("FileStorage = %q, want %q", got, want)
	}
	if got, want := a.Snapshot, filepath.Join(base, "backups", "state.zip"); got != want {
		t.Fatalf("Snapshot = %q, want %q", got, want)
	}
	if got, want := *a.UnixAdmin, filepath.Join(base, "run", "admin.sock"); got != want {
		t.Fatalf("UnixAdmin = %q, want %q", got, want)
	}
}

func TestResolveCLIPathArgsKeepsSnapshotStdin(t *testing.T) {
	a := args.Args{Snapshot: "-"}
	if err := resolveCLIPathArgs(&a); err != nil {
		t.Fatalf("resolveCLIPathArgs: %v", err)
	}
	if a.Snapshot != "-" {
		t.Fatalf("Snapshot = %q, want stdin marker", a.Snapshot)
	}
}

func TestResolveCLIPathArgsKeepsEmptyUnixAdmin(t *testing.T) {
	unixSocket := ""
	a := args.Args{UnixAdmin: &unixSocket}

	if err := resolveCLIPathArgs(&a); err != nil {
		t.Fatalf("resolveCLIPathArgs: %v", err)
	}

	if a.UnixAdmin == nil || *a.UnixAdmin != "" {
		t.Fatalf("UnixAdmin = %v, want explicit empty disable", a.UnixAdmin)
	}
}
