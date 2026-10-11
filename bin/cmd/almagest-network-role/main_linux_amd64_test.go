//go:build linux && amd64

package main

import (
	"errors"
	"os/user"
	"path/filepath"
	"testing"

	"github.com/asciimoth/almagest/bin/internal/daemonargs"
)

func TestLinuxProductionSelectsFixedSiblingRolesAndSeparatedStorage(t *testing.T) {
	ids := map[string]*user.User{
		"almagest-runtime": {Uid: "101", Gid: "201"},
		"almagest-writer":  {Uid: "102", Gid: "202"},
		"almagest-command": {Uid: "103", Gid: "203"},
	}
	production, err := linuxProduction(daemonargs.Args{Config: "/srv/config/alm.hujson", ProtectedConfig: "/etc/almagest/protected.hujson", FileStorage: "/srv/runtime"},
		func() (string, error) { return "/opt/almagest/almagest-network-role", nil }, func(name string) (*user.User, error) { return ids[name], nil }, "")
	if err != nil {
		t.Fatal(err)
	}
	if production.Paths.RuntimeExecutable != "/opt/almagest/almagestd" ||
		production.Paths.Launcher != "/opt/almagest/almagest-linux-role-launcher" ||
		production.Paths.WriterDirectory != "/srv/config" || production.Paths.RuntimeDirectory != "/srv/runtime" ||
		production.Paths.BrokerDirectory == production.Paths.RuntimeDirectory || production.Sources.ProtectedPath != "/etc/almagest/protected.hujson" ||
		production.Identities.Runtime.UID != 101 || production.Identities.Writer.GID != 202 || production.Identities.Helper.UID != 103 {
		t.Fatalf("production launch = %#v", production)
	}
}

func TestLinuxProductionRejectsMissingOrRootAccounts(t *testing.T) {
	for name, lookup := range map[string]func(string) (*user.User, error){
		"missing": func(string) (*user.User, error) { return nil, errors.New("missing") },
		"root":    func(string) (*user.User, error) { return &user.User{Uid: "0", Gid: "1"}, nil },
		"invalid": func(string) (*user.User, error) { return &user.User{Uid: "x", Gid: "1"}, nil },
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := linuxProduction(daemonargs.Args{}, func() (string, error) { return "/opt/network", nil }, lookup, ""); err == nil {
				t.Fatal("invalid account accepted")
			}
		})
	}
}

func TestLinuxProductionUsesSeparatedDevelopmentState(t *testing.T) {
	ids := map[string]*user.User{
		"almagest-runtime": {Uid: "101", Gid: "201"},
		"almagest-writer":  {Uid: "102", Gid: "202"},
		"almagest-command": {Uid: "103", Gid: "203"},
	}
	root := "/var/lib/almagest/dev-1000"
	production, err := linuxProduction(daemonargs.Args{},
		func() (string, error) { return "/run/almagest-dev/almagest-network-role", nil },
		func(name string) (*user.User, error) { return ids[name], nil }, root)
	if err != nil {
		t.Fatal(err)
	}
	if production.Sources.RegularPath != filepath.Join(root, "config", "alm.hujson") ||
		production.Sources.ProtectedPath != filepath.Join(root, "protected", "protected_alm.hujson") ||
		production.Paths.RuntimeDirectory != filepath.Join(root, "runtime") ||
		production.Paths.HelperDirectory != filepath.Join(root, "helper") ||
		production.Paths.BrokerDirectory != filepath.Join(root, "broker") ||
		production.Paths.Layout.Legacy != filepath.Join(root, "legacy-store") {
		t.Fatalf("development launch = %#v", production)
	}
}

func TestLinuxProductionRejectsInvalidDevelopmentRoot(t *testing.T) {
	lookup := func(string) (*user.User, error) { return &user.User{Uid: "101", Gid: "201"}, nil }
	for _, root := range []string{"relative", "/var/lib/../tmp/dev"} {
		if _, err := linuxProduction(daemonargs.Args{}, func() (string, error) { return "/opt/network", nil }, lookup, root); err == nil {
			t.Fatalf("development root %q accepted", root)
		}
	}
	if _, err := linuxProduction(daemonargs.Args{Config: "/tmp/config"}, func() (string, error) { return "/opt/network", nil }, lookup, "/var/lib/almagest/dev-1"); err == nil {
		t.Fatal("development root accepted an explicit config path")
	}
}

func TestAbsolutePath(t *testing.T) {
	path, err := absolutePath("relative/config", "unused")
	if err != nil || !filepath.IsAbs(path) || filepath.Base(path) != "config" {
		t.Fatalf("path = %q, %v", path, err)
	}
}
