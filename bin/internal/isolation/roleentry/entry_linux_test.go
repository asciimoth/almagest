//go:build linux

package roleentry

import (
	"strconv"
	"testing"

	"github.com/asciimoth/almagest/bin/internal/isolation/protocol"
	"github.com/asciimoth/almagest/bin/internal/isolation/transport"
)

func TestRoleEntryAcceptsInheritedUnixSocket(t *testing.T) {
	reader, writer, err := transport.SocketPair()
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close() //nolint:errcheck
	channel, err := OpenBootstrap(protocol.RoleConfigWriter, nil, func(string) string {
		return strconv.FormatUint(uint64(reader.Fd()), 10)
	})
	// OpenBootstrap consumes the descriptor. Mark the original os.File as
	// closed now so that its finalizer cannot close a later descriptor reuse.
	_ = reader.Close()
	if err != nil {
		t.Fatal(err)
	}
	if err := channel.Close(); err != nil {
		t.Fatal(err)
	}
	// OpenBootstrap consumes and closes the original descriptor after net.FileConn.
	if _, err := reader.Stat(); err == nil {
		t.Fatalf("original inherited descriptor remains open: %v", err)
	}
}
