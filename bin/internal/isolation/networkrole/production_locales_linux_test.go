//go:build linux && amd64

package networkrole

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOpenOptionalLocalesDirectoryAllowsMissingRoot(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "not-installed")
	file, err := openOptionalLocalesDirectory(missing)
	if err != nil {
		t.Fatalf("missing locale root returned an error: %v", err)
	}
	if file != nil {
		_ = file.Close()
		t.Fatal("missing locale root returned a file")
	}

	regular := filepath.Join(t.TempDir(), "locales")
	if err := os.WriteFile(regular, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	if file, err = openOptionalLocalesDirectory(regular); err == nil {
		if file != nil {
			_ = file.Close()
		}
		t.Fatal("non-directory locale root was accepted")
	}
}
