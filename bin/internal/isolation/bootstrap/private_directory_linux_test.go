//go:build linux

package bootstrap

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestOpenPrivateDirectoryHoldsValidatedObject(t *testing.T) {
	parent, err := os.MkdirTemp(".", "private-directory-test-")
	if err != nil {
		t.Fatal(err)
	}
	parent, err = filepath.Abs(parent)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(parent) })
	path := filepath.Join(parent, "role")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	file, err := OpenPrivateDirectory(path, uint32(os.Geteuid()), uint32(os.Getegid()))
	if err != nil {
		t.Fatal(err)
	}
	parent, err = filepath.Abs(parent)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close() //nolint:errcheck
	if err := os.Rename(path, path+".moved"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if file.Name() != path {
		t.Fatalf("held name = %q", file.Name())
	}
	info, err := file.Stat()
	if err != nil || !info.IsDir() {
		t.Fatalf("held object changed: %v %v", info, err)
	}
}

func TestOpenPrivateDirectoryRejectsUnsafePaths(t *testing.T) {
	parent, err := os.MkdirTemp(".", "private-directory-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(parent) })
	good := filepath.Join(parent, "good")
	if err := os.Mkdir(good, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(parent, "link")
	if err := os.Symlink(good, link); err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func() string{
		"relative": func() string { return "relative" },
		"link":     func() string { return link },
		"mode": func() string {
			_ = os.Chmod(good, 0o755)
			return good
		},
	} {
		t.Run(name, func(t *testing.T) {
			if file, err := OpenPrivateDirectory(change(), uint32(os.Geteuid()), uint32(os.Getegid())); err == nil {
				_ = file.Close()
				t.Fatal("unsafe directory accepted")
			} else if !errors.Is(err, ErrUnsafeInput) {
				t.Fatalf("error = %v", err)
			}
		})
		_ = os.Chmod(good, 0o700)
	}
}

func TestNativeLoaderUntrustedReadRejectsLinkTraversal(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	if err := os.WriteFile(target, []byte("regular"), 0o600); err != nil {
		t.Fatal(err)
	}
	loader := NativeLoader{}
	data, err := loader.ReadUntrusted(target, 64)
	if err != nil || string(data) != "regular" {
		t.Fatalf("regular untrusted read = %q, %v", data, err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := loader.ReadUntrusted(link, 64); err == nil {
		t.Fatal("untrusted final symlink followed")
	}
	parentLink := filepath.Join(root, "parent-link")
	if err := os.Symlink(root, parentLink); err != nil {
		t.Fatal(err)
	}
	if _, err := loader.ReadUntrusted(filepath.Join(parentLink, "target"), 64); err == nil {
		t.Fatal("untrusted parent symlink followed")
	}
}
