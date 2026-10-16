package sandbox

import (
	"context"
	"errors"
	"net"
	"testing"
)

type networkSandbox struct {
	Sandbox
	dialed   bool
	listened bool
}

func (s *networkSandbox) DialContext(context.Context, string, string) (net.Conn, error) {
	s.dialed = true
	return nil, ErrUnavailable
}

func (s *networkSandbox) Listen(context.Context, string, string) (net.Listener, error) {
	s.listened = true
	return nil, ErrUnavailable
}

func TestNetworkAdapterUsesOnlySandboxCapabilities(t *testing.T) {
	sand := &networkSandbox{}
	network := Network(sand)
	if network.IsNative() {
		t.Fatal("sandbox adapter reported native access")
	}
	if _, err := network.Dial(context.Background(), "tcp", "127.0.0.1:1"); !errors.Is(err, ErrUnavailable) || !sand.dialed {
		t.Fatalf("dial error = %v", err)
	}
	if _, err := network.Listen(context.Background(), "tcp", "127.0.0.1:0"); !errors.Is(err, ErrUnavailable) || !sand.listened {
		t.Fatalf("listen error = %v", err)
	}
	if _, err := network.LookupHost(context.Background(), "localhost"); err == nil {
		t.Fatal("adapter unexpectedly provided DNS")
	}
}
