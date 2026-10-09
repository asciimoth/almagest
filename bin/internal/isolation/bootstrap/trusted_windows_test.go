//go:build windows

package bootstrap

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

func TestWindowsTrustedInputsRejectReplacementAliasesAndUnsafeNames(t *testing.T) {
	directory := t.TempDir()
	hardenTestStorageRoot(t, directory)
	path := filepath.Join(directory, "protected.hujson")
	if err := os.WriteFile(path, []byte("protected"), 0o600); err != nil {
		t.Fatal(err)
	}
	data, err := (NativeLoader{}).ReadTrusted(path, 1024)
	if err != nil || string(data) != "protected" {
		t.Fatalf("trusted read = %q, %v", data, err)
	}
	alias := filepath.Join(directory, "alias.hujson")
	if err := os.Link(path, alias); err != nil {
		t.Fatal(err)
	}
	if _, err := (NativeLoader{}).ReadTrusted(path, 1024); !errors.Is(err, ErrUnsafeInput) {
		t.Fatalf("hard-linked trusted input error = %v", err)
	}
	for _, unsafe := range []string{path + ":stream", `\\server\share\protected`, `\\?\C:\protected`, filepath.Join(directory, "x", "..", "protected.hujson")} {
		if _, err := openTrustedFile(unsafe); !errors.Is(err, ErrUnsafeInput) {
			t.Errorf("openTrustedFile(%q) error = %v", unsafe, err)
		}
	}

	writable := filepath.Join(directory, "replaceable")
	if err := os.Mkdir(writable, 0o700); err != nil {
		t.Fatal(err)
	}
	setWindowsBootstrapDACL(t, writable, "D:P(A;;GA;;;WD)")
	if err := os.WriteFile(filepath.Join(writable, "protected.hujson"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := (NativeLoader{}).ReadTrusted(filepath.Join(writable, "protected.hujson"), 1024); !errors.Is(err, ErrUnsafeInput) {
		t.Fatalf("replaceable-parent error = %v", err)
	}
}

func TestWindowsStorageLayoutRejectsReparseParent(t *testing.T) {
	directory := t.TempDir()
	hardenTestStorageRoot(t, directory)
	target := filepath.Join(directory, "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(directory, "link")
	if output, err := exec.Command("cmd.exe", "/c", "mklink", "/J", link, target).CombinedOutput(); err != nil { //nolint:gosec // Fixed native test command and temporary paths.
		t.Fatalf("create test junction: %v: %s", err, output)
	}
	layout := StorageLayout{
		Legacy: filepath.Join(link, "legacy"), Runtime: filepath.Join(link, "runtime"),
		Broker: filepath.Join(link, "broker"), Backup: filepath.Join(link, "backup"), State: filepath.Join(link, "state"),
	}
	if err := layout.Validate(); !errors.Is(err, ErrUnsafeLayout) {
		t.Fatalf("reparse-parent layout error = %v", err)
	}
}

func setWindowsBootstrapDACL(t *testing.T, path, sddl string) {
	t.Helper()
	descriptor, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		t.Fatal(err)
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		t.Fatal(err)
	}
	if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil, nil, dacl, nil); err != nil {
		t.Fatalf("set DACL for %s: %v", fmt.Sprint(path), err)
	}
}
