package tailscalenodes

import (
	"context"
	"net"
	"net/netip"

	"github.com/asciimoth/gonnect"
)

// tailscaleNetwork normalizes IPv4-mapped IPv6 source addresses before they
// reach wgo-tailscale. A dual-stack UDP socket can report an IPv4 sender as
// ::ffff:a.b.c.d. If that value becomes a WireGuard peer endpoint, wgo selects
// the IPv6 UDP socket and some systems return ENETUNREACH. The network keeps
// all other behavior, including IsNative, from the attached Network port.
type tailscaleNetwork struct {
	gonnect.Network
}

func newTailscaleNetwork(network gonnect.Network) gonnect.Network {
	return &tailscaleNetwork{Network: network}
}

func (n *tailscaleNetwork) DialUDP(
	ctx context.Context,
	network, laddr, raddr string,
) (gonnect.UDPConn, error) {
	conn, err := n.Network.DialUDP(ctx, network, laddr, raddr)
	return wrapTailscaleUDPConn(conn, err)
}

func (n *tailscaleNetwork) ListenUDP(
	ctx context.Context,
	network, laddr string,
) (gonnect.UDPConn, error) {
	conn, err := n.Network.ListenUDP(ctx, network, laddr)
	return wrapTailscaleUDPConn(conn, err)
}

func (n *tailscaleNetwork) ListenUDPConfig(
	ctx context.Context,
	lc *gonnect.ListenConfig,
	network, laddr string,
) (gonnect.UDPConn, error) {
	conn, err := n.Network.ListenUDPConfig(ctx, lc, network, laddr)
	return wrapTailscaleUDPConn(conn, err)
}

func wrapTailscaleUDPConn(conn gonnect.UDPConn, err error) (gonnect.UDPConn, error) {
	if err != nil || conn == nil {
		return conn, err
	}
	return &tailscaleUDPConn{UDPConn: conn}, nil
}

// tailscaleUDPConn preserves the original connection and normalizes only
// source addresses returned by receive operations.
type tailscaleUDPConn struct {
	gonnect.UDPConn
}

func (c *tailscaleUDPConn) ReadFrom(p []byte) (int, net.Addr, error) {
	n, addr, err := c.UDPConn.ReadFrom(p)
	udpAddr, ok := addr.(*net.UDPAddr)
	if !ok {
		return n, addr, err
	}
	return n, unmapUDPAddr(udpAddr), err
}

func (c *tailscaleUDPConn) ReadFromUDP(p []byte) (int, *net.UDPAddr, error) {
	n, addr, err := c.UDPConn.ReadFromUDP(p)
	return n, unmapUDPAddr(addr), err
}

func (c *tailscaleUDPConn) ReadFromUDPAddrPort(p []byte) (int, netip.AddrPort, error) {
	n, addr, err := c.UDPConn.ReadFromUDPAddrPort(p)
	return n, unmapAddrPort(addr), err
}

func (c *tailscaleUDPConn) ReadMsgUDP(
	b, oob []byte,
) (int, int, int, *net.UDPAddr, error) {
	n, oobn, flags, addr, err := c.UDPConn.ReadMsgUDP(b, oob)
	return n, oobn, flags, unmapUDPAddr(addr), err
}

func (c *tailscaleUDPConn) ReadMsgUDPAddrPort(
	b, oob []byte,
) (int, int, int, netip.AddrPort, error) {
	n, oobn, flags, addr, err := c.UDPConn.ReadMsgUDPAddrPort(b, oob)
	return n, oobn, flags, unmapAddrPort(addr), err
}

func unmapAddrPort(addr netip.AddrPort) netip.AddrPort {
	if !addr.IsValid() {
		return addr
	}
	return netip.AddrPortFrom(addr.Addr().Unmap(), addr.Port())
}

func unmapUDPAddr(addr *net.UDPAddr) *net.UDPAddr {
	if addr == nil || addr.IP.To4() == nil {
		return addr
	}
	unmapped := *addr
	unmapped.IP = append(net.IP(nil), addr.IP.To4()...)
	return &unmapped
}
