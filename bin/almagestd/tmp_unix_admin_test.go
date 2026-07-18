package main

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/asciimoth/almagest/bin/almagestd/args"
)

func TestResolveTmpUnixAdminArgSetsUnixOverride(t *testing.T) {
	a := args.Args{TmpUnixAdmin: true}

	if err := resolveTmpUnixAdminArg(&a); err != nil {
		t.Fatalf("resolveTmpUnixAdminArg: %v", err)
	}

	if a.UnixAdmin == nil {
		t.Fatal("UnixAdmin is nil, want generated temporary socket path")
	}
	if !filepath.IsAbs(*a.UnixAdmin) {
		t.Fatalf("UnixAdmin = %q, want absolute path", *a.UnixAdmin)
	}
	if !strings.HasPrefix(filepath.Base(*a.UnixAdmin), "alm-sock-") {
		t.Fatalf("UnixAdmin base = %q, want alm-sock- prefix", filepath.Base(*a.UnixAdmin))
	}
}

func TestResolveTmpUnixAdminArgRejectsExplicitUnix(t *testing.T) {
	unixSocket := "/tmp/admin.sock"
	a := args.Args{
		UnixAdmin:    &unixSocket,
		TmpUnixAdmin: true,
	}

	if err := resolveTmpUnixAdminArg(&a); err == nil {
		t.Fatal("resolveTmpUnixAdminArg succeeded with both --unix and --tmp-unix-admin")
	}
}
