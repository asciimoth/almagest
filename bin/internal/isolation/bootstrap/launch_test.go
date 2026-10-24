package bootstrap

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/asciimoth/almagest/bin/internal/isolation/protocol"
)

func TestLaunchSpecUsesExactInputsAndSanitizedEnvironment(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("native Windows ACL launch validation runs in the VM case")
	}
	root, err := os.MkdirTemp(".", ".bootstrap-launch-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	root, _ = filepath.Abs(root)
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	executable := filepath.Join(root, "role")
	library := filepath.Join(root, "native.so")
	for path, data := range map[string][]byte{executable: []byte("role"), library: []byte("library")} {
		if err := os.WriteFile(path, data, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	spec := LaunchSpec{
		Role: protocol.RoleNetworkBroker, Executable: executable, WorkingDirectory: root,
		Environment: []string{"LANG=C", "TZ=UTC"}, LibraryPaths: []string{library},
	}
	if err := spec.Validate(&memoryLoader{}); err != nil {
		t.Fatal(err)
	}
	command := spec.Command("fixed")
	if command.Path != executable || command.Dir != root || len(command.Env) != 2 || command.Args[0] != executable {
		t.Fatalf("exact command = %#v", command)
	}
	spec.Environment[0] = "CHANGED=yes"
	if command.Env[0] != "LANG=C" {
		t.Fatal("command environment aliases launch specification")
	}
}

func TestLaunchSpecRejectsLookupAndInjectionInputs(t *testing.T) {
	base := LaunchSpec{Role: protocol.RoleNetworkBroker, Executable: "/relative/../role", WorkingDirectory: "/"}
	if err := base.Validate(&memoryLoader{}); err == nil {
		t.Fatal("unclean executable was accepted")
	}
	for _, environment := range [][]string{
		{"PATH=/untrusted"}, {"LD_PRELOAD=/tmp/x.so"}, {"DYLD_INSERT_LIBRARIES=x"},
		{"A=1", "A=2"}, {"BAD-NAME=x"},
	} {
		spec := LaunchSpec{Role: protocol.RoleNetworkBroker, Executable: "/missing", WorkingDirectory: "/", Environment: environment}
		err := spec.Validate(&memoryLoader{})
		if err == nil {
			t.Errorf("environment %#v was accepted", environment)
		}
	}
}

func FuzzLaunchEnvironmentValidation(f *testing.F) {
	f.Add("LANG=C")
	f.Add("LD_PRELOAD=/tmp/x")
	f.Add("BAD-NAME=x")
	f.Fuzz(func(t *testing.T, entry string) {
		if len(entry) > 4096 {
			t.Skip()
		}
		name, _, found := strings.Cut(entry, "=")
		if found && validEnvironmentName(name) && !unsafeLoaderEnvironment(name) {
			return
		}
	})
}
