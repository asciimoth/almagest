package almagest

import (
	"context"
	"errors"
	"net"
	"syscall"
	"testing"
)

func TestMockLoopbackUDPNoRouteWriteIsHostUnreachable(t *testing.T) {
	// Upstream SOCKS UDP handling keys transient no-route writes off syscall
	// sentinels, so MockNetwork's textual loopback error must expose one.
	network := newMockLoopbackNetwork()
	conn, err := network.ListenPacket(context.Background(), "udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("ListenPacket error = %v, want nil", err)
	}
	defer func() { _ = conn.Close() }()

	_, err = conn.WriteTo([]byte("ping"), &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 9})
	if !errors.Is(err, syscall.EHOSTUNREACH) {
		t.Fatalf("WriteTo error = %v, want errors.Is EHOSTUNREACH", err)
	}
}
