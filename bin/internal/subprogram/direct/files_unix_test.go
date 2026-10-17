//go:build unix

package direct

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	sandbox "github.com/asciimoth/gobox"
	"golang.org/x/sys/unix"
)

func TestDirectFileBrokerRejectsFIFOWithoutBlocking(t *testing.T) {
	sand := prepareInstance(t, testBackend(t, sandbox.Off))
	files, err := sand.FS(sandbox.Temporary)
	if err != nil {
		t.Fatal(err)
	}
	if err = unix.Mkfifo(filepath.Join(sand.(*instance).temporaryPath, "pipe"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err = files.Open(context.Background(), "pipe", sandbox.OpenOptions{Read: true}); !errors.Is(err, sandbox.ErrDenied) {
		t.Fatalf("FIFO open = %v", err)
	}
}
