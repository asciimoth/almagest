package nodekit

import (
	"context"
	"errors"
	"net"
	"testing"

	sandbox "github.com/asciimoth/gobox"
)

type networkSandbox struct {
	sandbox.Sandbox
	dialed   bool
	listened bool
}

func (value *networkSandbox) DialContext(context.Context, string, string) (net.Conn, error) {
	value.dialed = true
	return nil, sandbox.ErrUnavailable
}

func (value *networkSandbox) Listen(context.Context, string, string) (net.Listener, error) {
	value.listened = true
	return nil, sandbox.ErrUnavailable
}

func TestSandboxNetworkUsesOnlySandboxCapabilities(t *testing.T) {
	value := &networkSandbox{}
	network := SandboxNetwork(value)
	if network.IsNative() {
		t.Fatal("sandbox adapter reported native access")
	}
	if _, err := network.Dial(context.Background(), "tcp", "127.0.0.1:1"); !errors.Is(err, sandbox.ErrUnavailable) || !value.dialed {
		t.Fatalf("dial error = %v", err)
	}
	if _, err := network.Listen(context.Background(), "tcp", "127.0.0.1:0"); !errors.Is(err, sandbox.ErrUnavailable) || !value.listened {
		t.Fatalf("listen error = %v", err)
	}
	if _, err := network.LookupHost(context.Background(), "localhost"); err == nil {
		t.Fatal("adapter unexpectedly provided DNS")
	}
}
