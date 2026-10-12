//go:build linux

package bootstrap

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func TestOpenTrustedFileFollowsTrustedSymlinkChain(t *testing.T) {
	root, err := os.MkdirTemp(".", ".trusted-file-test-")
	if err != nil {
		t.Fatal(err)
	}
	root, err = filepath.Abs(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	target := filepath.Join(root, "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "tool"), []byte("trusted"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("target", filepath.Join(root, "directory-link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("directory-link/tool", filepath.Join(root, "tool-link")); err != nil {
		t.Fatal(err)
	}

	file, err := OpenTrustedFile(filepath.Join(root, "tool-link"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := file.Close(); err != nil {
			t.Errorf("close trusted file: %v", err)
		}
	}()
	contents, err := io.ReadAll(file)
	if err != nil || string(contents) != "trusted" {
		t.Fatalf("resolved trusted file = %q, %v", contents, err)
	}
}

func TestOpenTrustedFileRejectsSymlinkBelowWritableDirectory(t *testing.T) {
	root, err := os.MkdirTemp(".", ".trusted-file-test-")
	if err != nil {
		t.Fatal(err)
	}
	root, err = filepath.Abs(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	target := filepath.Join(root, "target")
	if err := os.WriteFile(target, []byte("trusted"), 0o600); err != nil {
		t.Fatal(err)
	}
	writable := filepath.Join(root, "writable")
	if err := os.Mkdir(writable, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(writable, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(writable, "tool")); err != nil {
		t.Fatal(err)
	}

	if _, err := OpenTrustedFile(filepath.Join(writable, "tool")); !errors.Is(err, ErrUnsafeInput) {
		t.Fatalf("writable-parent symlink error = %v", err)
	}
}

func TestOpenTrustedFileAllowsTrustedStickyDirectory(t *testing.T) {
	root, err := os.MkdirTemp(".", ".trusted-file-test-")
	if err != nil {
		t.Fatal(err)
	}
	root, err = filepath.Abs(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	sticky := filepath.Join(root, "sticky")
	if err := os.Mkdir(sticky, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(sticky, os.ModeSticky|0o777); err != nil {
		t.Fatal(err)
	}
	var stat unix.Stat_t
	if err := unix.Stat(sticky, &stat); err != nil {
		t.Fatal(err)
	}
	if !trustedMetadata(&stat, unix.S_IFDIR) {
		t.Fatalf("sticky directory mode=%#o uid=%d euid=%d", stat.Mode, stat.Uid, os.Geteuid())
	}
	target := filepath.Join(sticky, "tool")
	if err := os.WriteFile(target, []byte("trusted"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "tool-link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}

	file, err := OpenTrustedFile(link)
	if err != nil {
		t.Fatal(err)
	}
	_ = file.Close()
}

func TestOpenTrustedFileAcceptsSystemEnv(t *testing.T) {
	if _, err := os.Lstat("/usr/bin/env"); errors.Is(err, os.ErrNotExist) {
		t.Skip("/usr/bin/env is unavailable")
	} else if err != nil {
		t.Fatal(err)
	}
	file, err := OpenTrustedFile("/usr/bin/env")
	if err != nil {
		t.Fatal(err)
	}
	_ = file.Close()
}
