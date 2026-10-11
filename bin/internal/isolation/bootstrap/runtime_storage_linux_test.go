//go:build linux

package bootstrap

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asciimoth/almagest/bin/internal/isolation/protocol"
	"github.com/asciimoth/almagest/bin/internal/isolation/roleentry"
	"github.com/asciimoth/almagest/bin/internal/snapshotlimits"
	"golang.org/x/sys/unix"
)

func runtimeDirectory(t *testing.T) (*os.File, uint32, uint32) {
	t.Helper()
	path := t.TempDir()
	if err := os.Chmod(path, 0o700); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = file.Close() })
	return file, uint32(os.Geteuid()), uint32(os.Getegid())
}

func TestPrepareRuntimeStorageInstallsFixedInputsAndPreservesData(t *testing.T) {
	directory, uid, gid := runtimeDirectory(t)
	keep := filepath.Join(directory.Name(), "kv.db")
	if err := os.WriteFile(keep, []byte("persistent"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{roleentry.RuntimeRegularConfigFile, roleentry.RuntimeProtectedConfigFile, roleentry.RuntimeSnapshotFile} {
		if err := os.WriteFile(filepath.Join(directory.Name(), name), []byte("attacker-old"), 0o777); err != nil {
			t.Fatal(err)
		}
	}
	input := RuntimeStorageInput{Regular: []byte(`{"nodes":{}}`), Protected: RuntimeProtected{
		Secrets:      map[string]Secret{"token": {Value: "resolved", Nodes: []string{"node"}, Source: "environment"}},
		Certificates: map[string]string{"ca": "certificate"}, Policies: map[string]json.RawMessage{"policy": []byte(`{"Mode":"strict"}`)}, BuiltinCA: "keypair",
	}, Snapshot: strings.NewReader("opaque archive")}
	if err := PrepareRuntimeStorage(directory, uid, gid, input); err != nil {
		t.Fatal(err)
	}
	checks := map[string]string{roleentry.RuntimeRegularConfigFile: `{"nodes":{}}`, roleentry.RuntimeSnapshotFile: "opaque archive", "kv.db": "persistent"}
	for name, want := range checks {
		data, err := os.ReadFile(filepath.Join(directory.Name(), name))
		if err != nil || string(data) != want {
			t.Fatalf("%s=%q err=%v", name, data, err)
		}
	}
	protected, err := os.ReadFile(filepath.Join(directory.Name(), roleentry.RuntimeProtectedConfigFile))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"resolved", "certificate", "strict", "keypair"} {
		if !bytes.Contains(protected, []byte(want)) {
			t.Fatalf("protected input lost %q: %s", want, protected)
		}
	}
	for _, forbidden := range []string{"Env", "File", "TOKEN", "/trusted/"} {
		if bytes.Contains(protected, []byte(forbidden)) {
			t.Fatalf("protected input exposes %q: %s", forbidden, protected)
		}
	}
	for _, name := range []string{roleentry.RuntimeRegularConfigFile, roleentry.RuntimeProtectedConfigFile, roleentry.RuntimeSnapshotFile} {
		var stat unix.Stat_t
		if err := unix.Stat(filepath.Join(directory.Name(), name), &stat); err != nil {
			t.Fatal(err)
		}
		if stat.Mode&0o7777 != 0o600 || stat.Uid != uid || stat.Gid != gid || stat.Nlink != 1 {
			t.Fatalf("unsafe %s metadata: mode=%#o uid=%d gid=%d links=%d", name, stat.Mode, stat.Uid, stat.Gid, stat.Nlink)
		}
		if _, err := os.Stat(filepath.Join(directory.Name(), name+".bootstrap-new")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("staging remains for %s: %v", name, err)
		}
	}
}

func TestPrepareRuntimeStorageRemovesStaleSnapshot(t *testing.T) {
	directory, uid, gid := runtimeDirectory(t)
	path := filepath.Join(directory.Name(), roleentry.RuntimeSnapshotFile)
	if err := os.WriteFile(path, []byte("stale"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := PrepareRuntimeStorage(directory, uid, gid, RuntimeStorageInput{Regular: []byte(`{}`)}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale snapshot remains: %v", err)
	}
}

func TestPrepareRuntimeStorageRejectsUnsafeDirectoryAndInputs(t *testing.T) {
	directory, uid, gid := runtimeDirectory(t)
	for name, change := range map[string]func(*RuntimeStorageInput, *uint32, *uint32){
		"zero uid":          func(_ *RuntimeStorageInput, uid, _ *uint32) { *uid = 0 },
		"zero gid":          func(_ *RuntimeStorageInput, _, gid *uint32) { *gid = 0 },
		"regular too large": func(in *RuntimeStorageInput, _, _ *uint32) { in.Regular = make([]byte, protocol.MaxConfigBytes+1) },
		"snapshot too large": func(in *RuntimeStorageInput, _, _ *uint32) {
			in.Snapshot = io.LimitReader(zeroReader{}, snapshotlimits.ArchiveBytes+1)
		},
	} {
		t.Run(name, func(t *testing.T) {
			in, testUID, testGID := RuntimeStorageInput{Regular: []byte(`{}`)}, uid, gid
			change(&in, &testUID, &testGID)
			if err := PrepareRuntimeStorage(directory, testUID, testGID, in); err == nil {
				t.Fatal("unsafe input accepted")
			}
		})
	}
	if err := os.Chmod(directory.Name(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := PrepareRuntimeStorage(directory, uid, gid, RuntimeStorageInput{Regular: []byte(`{}`)}); !errors.Is(err, ErrUnsafeInput) {
		t.Fatalf("loose directory: %v", err)
	}
}

func TestPrepareRuntimeStorageDoesNotReplaceDirectoryAtReservedName(t *testing.T) {
	directory, uid, gid := runtimeDirectory(t)
	reserved := filepath.Join(directory.Name(), roleentry.RuntimeRegularConfigFile+".bootstrap-new")
	if err := os.Mkdir(reserved, 0o700); err != nil {
		t.Fatal(err)
	}
	err := PrepareRuntimeStorage(directory, uid, gid, RuntimeStorageInput{Regular: []byte(`new`)})
	if err == nil {
		t.Fatal("reserved directory replaced")
	}
	info, statErr := os.Stat(reserved)
	if statErr != nil || !info.IsDir() {
		t.Fatalf("reserved directory changed: %v %v", info, statErr)
	}
}
