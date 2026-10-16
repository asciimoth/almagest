package sandbox

import (
	"context"
	"net"
	"time"

	"github.com/asciimoth/gonnect"
)

// Network adapts a sandbox's supported loopback operations to gonnect. DNS,
// raw sockets, multicast, interface discovery, and typed native operations use
// RejectNetwork and fail without bypassing the sandbox.
func Network(value Sandbox) gonnect.Network {
	return &sandboxNetwork{RejectNetwork: &gonnect.RejectNetwork{}, sandbox: value}
}

type sandboxNetwork struct {
	*gonnect.RejectNetwork
	sandbox Sandbox
}

var _ gonnect.Network = (*sandboxNetwork)(nil)

func (n *sandboxNetwork) Dial(ctx context.Context, network, address string) (net.Conn, error) {
	return n.sandbox.DialContext(ctx, network, address)
}

func (n *sandboxNetwork) Listen(ctx context.Context, network, address string) (net.Listener, error) {
	return n.sandbox.Listen(ctx, network, address)
}

func (n *sandboxNetwork) ListenPacket(ctx context.Context, network, address string) (gonnect.PacketConn, error) {
	packet, err := n.sandbox.ListenPacket(ctx, network, address)
	if err != nil {
		return nil, err
	}
	connection, ok := packet.(net.Conn)
	if !ok {
		_ = packet.Close()
		return nil, ErrUnsupported
	}
	return &packetConn{PacketConn: packet, connection: connection}, nil
}

type packetConn struct {
	net.PacketConn
	connection net.Conn
}

func (c *packetConn) Read(data []byte) (int, error)     { return c.connection.Read(data) }
func (c *packetConn) Write(data []byte) (int, error)    { return c.connection.Write(data) }
func (c *packetConn) RemoteAddr() net.Addr              { return c.connection.RemoteAddr() }
func (c *packetConn) SetDeadline(value time.Time) error { return c.PacketConn.SetDeadline(value) }
func (c *packetConn) SetReadDeadline(value time.Time) error {
	return c.PacketConn.SetReadDeadline(value)
}
func (c *packetConn) SetWriteDeadline(value time.Time) error {
	return c.PacketConn.SetWriteDeadline(value)
}
