//go:build linux

package discovery

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asciimoth/almagest/bin/internal/subprogram/direct"
	"github.com/asciimoth/almagest/bin/internal/subprogram/policy"
	sandbox "github.com/asciimoth/gobox"
)

func TestPrepareStateRootAllowsUnprivilegedTraversalWithoutListing(t *testing.T) {
	private := filepath.Join(t.TempDir(), "broker")
	root := DefaultStateRoot(private)
	if filepath.Dir(root) != filepath.Dir(private) || strings.HasPrefix(root, private+string(filepath.Separator)) {
		t.Fatalf("state root %q is not beside private root %q", root, private)
	}
	if err := prepareStateRoot(root); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(root)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o711 {
		t.Fatalf("state root mode = %04o, want 0711", got)
	}
}

func TestDiscoverRuntimeInputsIncludesELFClosureAndNixStorePaths(t *testing.T) {
	image, err := exec.LookPath("tor")
	if err != nil {
		t.Skip("Tor is not installed in the test PATH")
	}
	image, err = filepath.EvalSymlinks(image)
	if err != nil {
		t.Fatal(err)
	}
	inputs, err := discoverRuntimeInputs(image)
	if err != nil {
		t.Fatal(err)
	}
	if len(inputs) == 0 {
		t.Fatal("Tor ELF dependency closure is empty")
	}
	if strings.HasPrefix(image, "/nix/store/") {
		found := false
		for _, input := range inputs {
			found = found || strings.HasPrefix(input.Target, "/nix/store/")
		}
		if !found {
			t.Fatalf("Nix Tor dependency closure has no /nix/store target: %+v", inputs)
		}
	}
}

func TestDiscoveredNixTorPolicyPassesBackendIdentityChecks(t *testing.T) {
	image, err := exec.LookPath("tor")
	if err != nil {
		t.Skip("Tor is not installed in the test PATH")
	}
	root := t.TempDir()
	configured, _, err := Configure(policy.Config{Mode: sandbox.Off, DiscoverSystemPrograms: true}, Options{
		StateRoot: filepath.Join(root, "sandbox"), GOOS: "linux",
		LookPath: func(name string) (string, error) {
			if name == "tor" {
				return image, nil
			}
			return "", exec.ErrNotFound
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	configured, err = policy.Normalize(configured)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = direct.NewBackend(configured, policy.Probe{Platform: "linux", EffectiveIdentity: "test",
		Available: map[sandbox.Feature]bool{sandbox.Environment: true, sandbox.HandleInheritance: true}}); err != nil {
		t.Fatalf("discovered Tor policy backend: %v", err)
	}
}
