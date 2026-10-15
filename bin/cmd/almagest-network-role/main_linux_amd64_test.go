//go:build linux && amd64

package main

import (
	"errors"
	"os"
	"os/user"
	"path/filepath"
	"testing"

	"github.com/asciimoth/almagest/bin/internal/daemonargs"
	"github.com/asciimoth/almagest/bin/internal/isolation/linuxprofile"
	"golang.org/x/sys/unix"
)

func TestLinuxUserProductionRejectsWritableOverlap(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	runtimeDirectory := filepath.Join(root, "runtime")
	config := filepath.Join(runtimeDirectory, "alm.hujson")
	_, err := linuxUserProduction(daemonargs.Args{Isolation: "auto", IsolationStateDir: root, Config: config}, "/usr/local/bin")
	if err == nil {
		t.Fatal("overlapping runtime and config directories accepted")
	}
}

func TestLinuxUserProductionRejectsAdminDirectoryOverlap(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_RUNTIME_DIR", "")
	_, err := linuxUserProduction(daemonargs.Args{Isolation: "auto", IsolationStateDir: root,
		FileStorage: filepath.Join(root, "run")}, "/usr/local/bin")
	if err == nil {
		t.Fatal("admin and runtime directory overlap accepted")
	}
}

func TestLinuxUserProductionRequiresDedicatedConfigDirectory(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	configDirectory := filepath.Join(root, "external-config")
	if err := os.Mkdir(configDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDirectory, "alm.hujson"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDirectory, "unrelated"), []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := linuxUserProduction(daemonargs.Args{Isolation: "auto", IsolationStateDir: filepath.Join(root, "state"),
		Config: filepath.Join(configDirectory, "alm.hujson")}, "/usr/local/bin")
	if err == nil {
		t.Fatal("non-dedicated config directory accepted")
	}
}

func TestLinuxUserProductionAllowsFixedWriterTemporaryDirectory(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	configDirectory := filepath.Join(root, "external-config")
	if err := os.Mkdir(configDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDirectory, "alm.hujson"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(configDirectory, ".tmp"), 0o700); err != nil {
		t.Fatal(err)
	}
	binDirectory := filepath.Join(root, "tools")
	if err := os.Mkdir(binDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_RUNTIME_DIR", "")
	if _, err := linuxUserProduction(daemonargs.Args{Isolation: "auto", IsolationStateDir: filepath.Join(root, "state"),
		Config: filepath.Join(configDirectory, "alm.hujson")}, binDirectory); err != nil {
		t.Fatal(err)
	}
}

func TestLinuxUserProductionRejectsCrossRootBindAlias(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	configDirectory := filepath.Join(root, "external-config")
	runtimeDirectory := filepath.Join(root, "external-runtime")
	for _, path := range []string{configDirectory, runtimeDirectory} {
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	configPath := filepath.Join(configDirectory, "alm.hujson")
	if err := os.WriteFile(configPath, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mount(configDirectory, runtimeDirectory, "", unix.MS_BIND, ""); err != nil {
		if errors.Is(err, unix.EPERM) {
			t.Skip("bind mounts require mount authority")
		}
		t.Fatal(err)
	}
	defer unix.Unmount(runtimeDirectory, unix.MNT_DETACH) //nolint:errcheck
	t.Setenv("XDG_RUNTIME_DIR", "")
	_, err := linuxUserProduction(daemonargs.Args{Isolation: "auto", IsolationStateDir: filepath.Join(root, "state"),
		Config: configPath, FileStorage: runtimeDirectory}, "/usr/local/bin")
	if err == nil {
		t.Fatal("cross-root bind alias accepted")
	}
}

func TestLinuxProductionSelectsFixedSiblingRolesAndSeparatedStorage(t *testing.T) {
	ids := map[string]*user.User{
		"almagest-runtime": {Uid: "101", Gid: "201"},
		"almagest-writer":  {Uid: "102", Gid: "202"},
		"almagest-command": {Uid: "103", Gid: "203"},
	}
	production, err := linuxProduction(daemonargs.Args{Config: "/srv/config/alm.hujson", ProtectedConfig: "/etc/almagest/protected.hujson", FileStorage: "/srv/runtime"}, linuxprofile.ProfileFull,
		func() (string, error) { return "/opt/almagest/almagest-network-role", nil }, func(name string) (*user.User, error) { return ids[name], nil }, "", "")
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
			if _, err := linuxProduction(daemonargs.Args{}, linuxprofile.ProfileFull, func() (string, error) { return "/opt/network", nil }, lookup, "", ""); err == nil {
				t.Fatal("invalid account accepted")
			}
		})
	}
}

func TestLinuxProductionUsesSeparatedDevelopmentState(t *testing.T) {
	root := "/var/lib/almagest/dev-1000"
	production, err := linuxProduction(daemonargs.Args{}, linuxprofile.ProfileFull,
		func() (string, error) { return "/run/almagest-dev/almagest-network-role", nil },
		nil, root, "100000:200000,100001:200001,100002:200002")
	if err != nil {
		t.Fatal(err)
	}
	if production.Sources.RegularPath != filepath.Join(root, "config", "alm.hujson") ||
		production.Sources.ProtectedPath != filepath.Join(root, "protected", "protected_alm.hujson") ||
		production.Paths.RuntimeDirectory != filepath.Join(root, "runtime") ||
		production.Paths.HelperDirectory != filepath.Join(root, "helper") ||
		production.Paths.BrokerDirectory != filepath.Join(root, "broker") ||
		production.Paths.Layout.Legacy != filepath.Join(root, "legacy-store") ||
		production.Identities.Runtime.UID != 100000 || production.Identities.Writer.GID != 200001 ||
		production.Identities.Helper.UID != 100002 {
		t.Fatalf("development launch = %#v", production)
	}
}

func TestLinuxProductionRejectsInvalidDevelopmentRoot(t *testing.T) {
	lookup := func(string) (*user.User, error) { return &user.User{Uid: "101", Gid: "201"}, nil }
	for _, root := range []string{"relative", "/var/lib/../tmp/dev"} {
		if _, err := linuxProduction(daemonargs.Args{}, linuxprofile.ProfileFull, func() (string, error) { return "/opt/network", nil }, lookup, root, "100:100,101:101,102:102"); err == nil {
			t.Fatalf("development root %q accepted", root)
		}
	}
	if _, err := linuxProduction(daemonargs.Args{Config: "/tmp/config"}, linuxprofile.ProfileFull, func() (string, error) { return "/opt/network", nil }, lookup, "/var/lib/almagest/dev-1", "100:100,101:101,102:102"); err == nil {
		t.Fatal("development root accepted an explicit config path")
	}
}

func TestLinuxProductionRejectsInvalidDevelopmentIdentities(t *testing.T) {
	for _, identities := range []string{"", "1:1,2:2", "0:1,2:2,3:3", "1:1,1:2,3:3", "1:1,2:1,3:3", "x:1,2:2,3:3"} {
		if _, err := linuxProduction(daemonargs.Args{}, linuxprofile.ProfileFull, func() (string, error) { return "/opt/network", nil }, nil,
			"/var/lib/almagest/dev-1", identities); err == nil {
			t.Fatalf("development identities %q accepted", identities)
		}
	}
}

func TestAbsolutePath(t *testing.T) {
	path, err := absolutePath("relative/config", "unused")
	if err != nil || !filepath.IsAbs(path) || filepath.Base(path) != "config" {
		t.Fatalf("path = %q, %v", path, err)
	}
}
