//go:build windows

package platform

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWindowsWintunDependencyRejectsMissingAndWrongArchitectureDLL(t *testing.T) {
	base := t.TempDir()
	applicationPath := filepath.Join(base, "application", "wintun.dll")
	systemPath := filepath.Join(base, "system32", "wintun.dll")
	if _, err := validateWindowsWintun(applicationPath, systemPath); err == nil ||
		!strings.Contains(err.Error(), "architecture-matched wintun.dll 0.14.1 is absent") {
		t.Fatalf("missing dependency error = %v", err)
	}
	wrongArchitecture := `C:\winvm\dependencies\wintun-arm64.dll`
	if _, err := os.Stat(wrongArchitecture); err != nil {
		t.Fatalf("wrong-architecture Wintun fixture: %v", err)
	}
	if _, err := validateWindowsWintun(wrongArchitecture, systemPath); err == nil ||
		!strings.Contains(err.Error(), "load the architecture-matched wintun.dll 0.14.1") {
		t.Fatalf("wrong-architecture dependency error = %v", err)
	}
}

func TestWindowsWintunDependencyDoesNotSearchCurrentDirectory(t *testing.T) {
	base := t.TempDir()
	hostile := filepath.Join(base, "hostile")
	if err := os.MkdirAll(hostile, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(hostile, "wintun.dll"), []byte("hostile"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(hostile)
	_, err := validateWindowsWintun(
		filepath.Join(base, "application", "wintun.dll"),
		filepath.Join(base, "system32", "wintun.dll"),
	)
	if err == nil || !strings.Contains(err.Error(), "is absent from the daemon executable directory and System32") {
		t.Fatalf("hostile current-directory lookup error = %v", err)
	}
}

func TestWindowsRuntimeDependenciesRemainTrustedAfterChdir(t *testing.T) {
	before, err := ValidateRuntimeDependencies()
	if err != nil {
		t.Fatalf("ValidateRuntimeDependencies before chdir: %v", err)
	}
	if before.WintunVersion != windowsWintunVersion {
		t.Fatalf("Wintun version = %q, want %q", before.WintunVersion, windowsWintunVersion)
	}
	hostile := t.TempDir()
	if err := os.WriteFile(filepath.Join(hostile, "wintun.dll"), []byte("hostile current-directory DLL"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(hostile)
	after, err := ValidateRuntimeDependencies()
	if err != nil {
		t.Fatalf("ValidateRuntimeDependencies after chdir: %v", err)
	}
	equal, err := sameWindowsPath(before.WintunPath, after.WintunPath)
	if err != nil {
		t.Fatal(err)
	}
	if !equal || after.WintunVersion != before.WintunVersion {
		t.Fatalf("dependency changed after chdir: before=%+v after=%+v", before, after)
	}
}
