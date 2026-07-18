package platform

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTmpAdminUnixSocketPathInReturnsUnusedPrefixedPath(t *testing.T) {
	parent := t.TempDir()

	socketPath, err := tmpAdminUnixSocketPathIn(parent)
	if err != nil {
		t.Fatalf("tmpAdminUnixSocketPathIn: %v", err)
	}

	if filepath.Dir(socketPath) != parent {
		t.Fatalf("socket parent = %q, want %q", filepath.Dir(socketPath), parent)
	}
	if !strings.HasPrefix(filepath.Base(socketPath), tmpAdminUnixSocketPrefix) {
		t.Fatalf("socket base = %q, want %s prefix", filepath.Base(socketPath), tmpAdminUnixSocketPrefix)
	}
	if _, err := os.Lstat(socketPath); !os.IsNotExist(err) {
		t.Fatalf("socket path exists before listen: %v", err)
	}
}

func TestTmpAdminUnixSocketPathInRejectsMissingParent(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "missing")

	if _, err := tmpAdminUnixSocketPathIn(parent); err == nil {
		t.Fatal("tmpAdminUnixSocketPathIn succeeded with missing parent")
	}
}
