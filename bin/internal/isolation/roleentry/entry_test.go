package roleentry

import (
	"errors"
	"os"
	"runtime"
	"strconv"
	"testing"

	"github.com/asciimoth/almagest/bin/internal/isolation/protocol"
)

func TestRoleEntryRequiresInheritedChannelAndFixedRole(t *testing.T) {
	getenv := func(string) string { return "" }
	for _, role := range []protocol.Role{"", protocol.RoleRuntime, "forged"} {
		if _, err := OpenBootstrap(role, nil, getenv); !errors.Is(err, ErrStandaloneRole) {
			t.Errorf("role %q error = %v", role, err)
		}
	}
	if _, err := OpenBootstrap(protocol.RoleNetworkBroker, []string{"--role", "network"}, getenv); !errors.Is(err, ErrStandaloneRole) {
		t.Fatalf("role flag error = %v", err)
	}
	file, err := os.CreateTemp(t.TempDir(), "not-a-channel")
	if err != nil {
		t.Fatal(err)
	}
	_, openErr := OpenBootstrap(protocol.RoleConfigWriter, nil, func(string) string { return strconv.FormatUint(uint64(file.Fd()), 10) })
	// OpenBootstrap consumes the descriptor. Mark the original os.File as
	// closed now so that its finalizer cannot close a later descriptor reuse.
	_ = file.Close()
	if !errors.Is(openErr, ErrStandaloneRole) {
		t.Fatalf("regular file descriptor error = %v", openErr)
	}
}

func TestRoleEntryAcceptsInheritedPipeOnly(t *testing.T) {
	if runtime.GOOS == "linux" {
		t.Skip("Linux requires an inherited Unix socket")
	}
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close() //nolint:errcheck
	channel, err := OpenBootstrap(protocol.RoleNetworkBroker, nil, func(name string) string {
		if name != BootstrapFDEnvironment {
			t.Fatalf("environment lookup = %q", name)
		}
		return strconv.FormatUint(uint64(reader.Fd()), 10)
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := channel.Close(); err != nil {
		t.Fatal(err)
	}
}
