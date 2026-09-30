//go:build windows

package durablefile

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

func TestWindowsReplaceExistingFileAndRejectLockedDestination(t *testing.T) {
	directory := t.TempDir()
	destination := filepath.Join(directory, "config.hujson")
	if err := os.WriteFile(destination, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	replacement := filepath.Join(directory, "replacement.tmp")
	if err := os.WriteFile(replacement, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Replace(destination, replacement); err != nil {
		t.Fatalf("Replace existing file: %v", err)
	}
	if data, err := os.ReadFile(destination); err != nil || string(data) != "new" {
		t.Fatalf("replacement data = %q, err=%v", data, err)
	}

	name, err := windows.UTF16PtrFromString(destination)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := windows.CreateFile(name, windows.GENERIC_READ, windows.FILE_SHARE_READ, nil,
		windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(handle) //nolint:errcheck
	lockedReplacement := filepath.Join(directory, "locked.tmp")
	if err := os.WriteFile(lockedReplacement, []byte("must not publish"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Replace(destination, lockedReplacement); err == nil {
		t.Fatal("Replace succeeded while destination denied delete sharing")
	}
	if data, err := os.ReadFile(destination); err != nil || string(data) != "new" {
		t.Fatalf("destination after failed replacement = %q, err=%v", data, err)
	}
}

func TestWindowsReplaceRecoversInterruptedReplacement(t *testing.T) {
	directory := t.TempDir()
	destination := filepath.Join(directory, "config.hujson")
	replacement := filepath.Join(directory, "replacement.tmp")
	if err := os.WriteFile(destination, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(replacement, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	originalCall := callReplaceFile
	callReplaceFile = func(destinationName, _ *uint16, backupName *uint16) (uintptr, error) {
		if err := os.Rename(windows.UTF16PtrToString(destinationName), windows.UTF16PtrToString(backupName)); err != nil {
			return 0, err
		}
		return 0, windows.ERROR_UNABLE_TO_MOVE_REPLACEMENT_2
	}
	t.Cleanup(func() { callReplaceFile = originalCall })

	if err := Replace(destination, replacement); err == nil {
		t.Fatal("Replace succeeded after an injected partial move")
	}
	if data, err := os.ReadFile(destination); err != nil || string(data) != "old" {
		t.Fatalf("recovered destination = %q, err=%v", data, err)
	}
	if data, err := os.ReadFile(replacement); err != nil || string(data) != "new" {
		t.Fatalf("replacement after recovery = %q, err=%v", data, err)
	}
}
