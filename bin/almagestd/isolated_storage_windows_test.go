//go:build windows

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnterIsolatedRuntimeStorageWindowsUsesSelectedDirectory(t *testing.T) {
	original, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	storage, err := filepath.Abs(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// Register this after TempDir so the working directory is restored before
	// the Windows test harness tries to remove the directory.
	t.Cleanup(func() { _ = os.Chdir(original) })
	if err = enterIsolatedRuntimeStorage(storage, storage); err != nil {
		t.Fatal(err)
	}
	current, err := os.Getwd()
	if err != nil || !filepath.IsAbs(current) || !equalWindowsPath(current, storage) {
		t.Fatalf("current directory = %q, %v; want %q", current, err, storage)
	}
	if err = enterIsolatedRuntimeStorage(storage, filepath.Join(storage, "other")); err == nil {
		t.Fatal("accepted a different runtime storage path")
	}
	if err = enterIsolatedRuntimeStorage("relative", "relative"); err == nil {
		t.Fatal("accepted a relative runtime storage path")
	}
}

func equalWindowsPath(left, right string) bool {
	left, _ = filepath.Abs(left)
	right, _ = filepath.Abs(right)
	return strings.EqualFold(filepath.Clean(left), filepath.Clean(right))
}
