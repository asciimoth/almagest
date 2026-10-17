package nodekit

import (
	"context"
	"net"
	"time"

	sandbox "github.com/asciimoth/gobox"
	"github.com/asciimoth/gonnect"
)

// SandboxNetwork adapts supported sandbox loopback operations to gonnect.
// DNS, raw sockets, multicast, interface discovery, and typed native operations
// use RejectNetwork and fail without bypassing the sandbox.
func SandboxNetwork(value sandbox.Sandbox) gonnect.Network {
	return &sandboxNetwork{RejectNetwork: &gonnect.RejectNetwork{}, sandbox: value}
}

type sandboxNetwork struct {
	*gonnect.RejectNetwork
	sandbox sandbox.Sandbox
}

var _ gonnect.Network = (*sandboxNetwork)(nil)

func (network *sandboxNetwork) Dial(ctx context.Context, protocol, address string) (net.Conn, error) {
	return network.sandbox.DialContext(ctx, protocol, address)
}

func (network *sandboxNetwork) Listen(ctx context.Context, protocol, address string) (net.Listener, error) {
	return network.sandbox.Listen(ctx, protocol, address)
}

func (network *sandboxNetwork) ListenPacket(ctx context.Context, protocol, address string) (gonnect.PacketConn, error) {
	packet, err := network.sandbox.ListenPacket(ctx, protocol, address)
	if err != nil {
		return nil, err
	}
	connection, ok := packet.(net.Conn)
	if !ok {
		_ = packet.Close()
		return nil, sandbox.ErrUnsupported
	}
	return &sandboxPacketConn{PacketConn: packet, connection: connection}, nil
}

type sandboxPacketConn struct {
	net.PacketConn
	connection net.Conn
}

func (connection *sandboxPacketConn) Read(data []byte) (int, error) {
	return connection.connection.Read(data)
}

func (connection *sandboxPacketConn) Write(data []byte) (int, error) {
	return connection.connection.Write(data)
}

func (connection *sandboxPacketConn) RemoteAddr() net.Addr {
	return connection.connection.RemoteAddr()
}

func (connection *sandboxPacketConn) SetDeadline(value time.Time) error {
	return connection.PacketConn.SetDeadline(value)
}

func (connection *sandboxPacketConn) SetReadDeadline(value time.Time) error {
	return connection.PacketConn.SetReadDeadline(value)
}

func (connection *sandboxPacketConn) SetWriteDeadline(value time.Time) error {
	return connection.PacketConn.SetWriteDeadline(value)
}
