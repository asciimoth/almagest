//go:build windows

package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"testing"
)

func TestWindowsSnapshotRejectsSymlinkEntryWithoutPrivilege(t *testing.T) {
	service, _, _, _, _ := newSnapshotTestService(t, false)
	created, err := service.Create(context.Background(), "windows symlink")
	if err != nil {
		t.Fatal(err)
	}
	exported, err := service.Export(created.UUID)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Delete(created.UUID); err != nil {
		t.Fatal(err)
	}
	archive := appendSnapshotArchiveEntry(t, exported.Archive,
		snapshotFilesPrefix+"link", []byte("kv.db"), os.ModeSymlink|0o777)
	if _, err := service.Import(context.Background(), bytes.NewReader(archive), nil); !errors.Is(err, errSnapshotInvalid) {
		t.Fatalf("Import symlink error = %v, want invalid snapshot", err)
	}
}
