//go:build windows

package bootstrap

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/asciimoth/almagest/bin/internal/isolation/protocol"
	"github.com/asciimoth/almagest/bin/internal/isolation/roleentry"
)

func TestPrepareRuntimeStorageWindowsFixedInputs(t *testing.T) {
	root := t.TempDir()
	directory, err := os.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer directory.Close() //nolint:errcheck
	protected := RuntimeProtected{Secrets: map[string]Secret{"token": {Value: "resolved", Source: "protected"}}}
	protectedDocument, err := protected.RuntimeDocument()
	if err != nil {
		t.Fatal(err)
	}
	first := RuntimeStorageInput{Regular: []byte(`{"nodes":{}}`), Protected: protected, Snapshot: bytes.NewReader([]byte("snapshot"))}
	if err = PrepareRuntimeStorage(directory, 0, 0, first); err != nil {
		t.Fatal(err)
	}
	assertFile := func(name, want string) {
		t.Helper()
		data, readErr := os.ReadFile(filepath.Join(root, name))
		if readErr != nil || string(data) != want {
			t.Fatalf("%s = %q, %v", name, data, readErr)
		}
	}
	assertFile(roleentry.RuntimeRegularConfigFile, `{"nodes":{}}`)
	assertFile(roleentry.RuntimeProtectedConfigFile, string(protectedDocument))
	assertFile(roleentry.RuntimeSnapshotFile, "snapshot")

	if err = PrepareRuntimeStorage(directory, 0, 0, RuntimeStorageInput{Regular: []byte(`{}`)}); err != nil {
		t.Fatal(err)
	}
	assertFile(roleentry.RuntimeRegularConfigFile, `{}`)
	if _, err = os.Stat(filepath.Join(root, roleentry.RuntimeSnapshotFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale snapshot remains: %v", err)
	}
	for _, name := range []string{roleentry.RuntimeRegularConfigFile, roleentry.RuntimeProtectedConfigFile, roleentry.RuntimeSnapshotFile} {
		if _, err = os.Stat(filepath.Join(root, name+".bootstrap-new")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("staging file remains for %s: %v", name, err)
		}
	}
}

func TestPrepareRuntimeStorageWindowsRejectsInvalidInputs(t *testing.T) {
	root := t.TempDir()
	directory, err := os.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer directory.Close() //nolint:errcheck
	valid := RuntimeStorageInput{Regular: []byte(`{}`)}
	if err = PrepareRuntimeStorage(directory, 1, 0, valid); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("non-Windows identity = %v", err)
	}
	large := valid
	large.Regular = make([]byte, protocol.MaxConfigBytes+1)
	if err = PrepareRuntimeStorage(directory, 0, 0, large); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("large regular config = %v", err)
	}
	reserved := filepath.Join(root, roleentry.RuntimeRegularConfigFile+".bootstrap-new")
	if err = os.Mkdir(reserved, 0o700); err != nil {
		t.Fatal(err)
	}
	if err = PrepareRuntimeStorage(directory, 0, 0, valid); !errors.Is(err, ErrUnsafeInput) {
		t.Fatalf("reserved directory = %v", err)
	}
}
