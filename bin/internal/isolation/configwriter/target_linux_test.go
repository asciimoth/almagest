//go:build linux

package configwriter

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/asciimoth/almagest/bin/internal/isolation/protocol"
)

func testConfigPath(t *testing.T, data []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.hujson")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLinuxTargetRejectsUnsafeLayouts(t *testing.T) {
	newPath := func(t *testing.T) (string, string) {
		t.Helper()
		directory := t.TempDir()
		path := filepath.Join(directory, "config")
		if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
			t.Fatal(err)
		}
		return directory, path
	}
	t.Run("target_symlink", func(t *testing.T) {
		directory := t.TempDir()
		outside := filepath.Join(t.TempDir(), "outside")
		if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, filepath.Join(directory, "config")); err != nil {
			t.Fatal(err)
		}
		if _, err := New(testSession(), filepath.Join(directory, "config")); err == nil {
			t.Fatal("accepted target symlink")
		}
	})
	t.Run("parent_symlink", func(t *testing.T) {
		directory, path := newPath(t)
		alias := filepath.Join(t.TempDir(), "alias")
		if err := os.Symlink(directory, alias); err != nil {
			t.Fatal(err)
		}
		if _, err := New(testSession(), filepath.Join(alias, filepath.Base(path))); err == nil {
			t.Fatal("accepted parent symlink")
		}
	})
	t.Run("hard_link", func(t *testing.T) {
		directory, path := newPath(t)
		if err := os.Link(path, filepath.Join(directory, "alias")); err != nil {
			t.Fatal(err)
		}
		if _, err := New(testSession(), path); err == nil {
			t.Fatal("accepted hard-linked target")
		}
	})
	t.Run("writable_parent", func(t *testing.T) {
		directory, path := newPath(t)
		if err := os.Chmod(directory, 0o777); err != nil {
			t.Fatal(err)
		}
		if _, err := New(testSession(), path); err == nil {
			t.Fatal("accepted writable parent")
		}
	})
	t.Run("non_canonical", func(t *testing.T) {
		_, path := newPath(t)
		if _, err := New(testSession(), filepath.Dir(path)+"/./config"); err == nil {
			t.Fatal("accepted non-canonical path")
		}
	})
}

func TestLinuxTargetCannotBeRedirectedAfterOpen(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "config")
	outside := filepath.Join(directory, "outside")
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	w, err := New(testSession(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close() //nolint:errcheck
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, path); err != nil {
		t.Fatal(err)
	}
	result, err := w.Replace(context.Background(), protocol.ReplaceConfigRequest{
		Meta: testMeta("redirect"), ExpectedRevision: protocol.ConfigRevision([]byte("old")), Data: []byte("new"),
	})
	if err == nil || result.Outcome != protocol.OutcomeRejected {
		t.Fatalf("redirect replace = %+v, %v", result, err)
	}
	data, readErr := os.ReadFile(outside)
	if readErr != nil || string(data) != "outside" {
		t.Fatalf("outside = %q, %v", data, readErr)
	}
}

func TestLinuxTargetDetectsExternalReplacementAndMetadataChange(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(string) error
	}{
		{"replacement", func(path string) error {
			temporary := path + ".external"
			if err := os.WriteFile(temporary, []byte("external"), 0o600); err != nil {
				return err
			}
			return os.Rename(temporary, path)
		}},
		{"mode", func(path string) error { return os.Chmod(path, 0o644) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config")
			if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
				t.Fatal(err)
			}
			w, err := New(testSession(), path)
			if err != nil {
				t.Fatal(err)
			}
			defer w.Close() //nolint:errcheck
			if err := tc.change(path); err != nil {
				t.Fatal(err)
			}
			_, err = w.Replace(context.Background(), protocol.ReplaceConfigRequest{
				Meta: testMeta("external"), ExpectedRevision: protocol.ConfigRevision([]byte("old")), Data: []byte("new"),
			})
			if err == nil || errors.Is(err, context.Canceled) {
				t.Fatalf("external change was not rejected: %v", err)
			}
		})
	}
}
