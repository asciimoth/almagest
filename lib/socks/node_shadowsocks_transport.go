package socksnodes

import (
	"context"
	"errors"
	"io"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"github.com/asciimoth/gonnect"
	"golang.getoutline.org/sdk/transport"
	ss "golang.getoutline.org/sdk/transport/shadowsocks"
)

// shadowsocksNetwork never selects a host network. Every socket to the proxy
// is opened by upstream; all incoming listener operations are rejected.
type shadowsocksNetwork struct {
	*gonnect.RejectNetwork
	upstream gonnect.Network
	address  string
	key      *ss.EncryptionKey
	tcp, udp bool
	stream   *ss.StreamDialer
	packet   ss.PacketListener
}

func newShadowsocksNetwork(upstream gonnect.Network, address, method, password string, tcp, udp bool) (*shadowsocksNetwork, error) {
	if upstream == nil {
		return nil, errors.New("upstream network is not connected")
	}
	if password == "" {
		return nil, errors.New("shadowsocks password is unavailable")
	}
	if _, err := validateShadowsocksAddress(address); err != nil {
		return nil, err
	}
	key, err := ss.NewEncryptionKey(method, password)
	if err != nil {
		return nil, err
	}
	n := &shadowsocksNetwork{RejectNetwork: &gonnect.RejectNetwork{}, upstream: upstream, address: address, key: key, tcp: tcp, udp: udp}
	endpoint := transport.FuncStreamEndpoint(func(ctx context.Context) (transport.StreamConn, error) {
		conn, err := upstream.Dial(ctx, "tcp", address)
		if err != nil {
			return nil, err
		}
		if stream, ok := conn.(transport.StreamConn); ok {
			return stream, nil
		}
		return &halfCloseConn{Conn: conn}, nil
	})
	n.stream, err = ss.NewStreamDialer(endpoint, key)
	if err != nil {
		return nil, err
	}
	n.packet, err = ss.NewPacketListener(transport.FuncPacketEndpoint(func(ctx context.Context) (net.Conn, error) {
		return upstream.PacketDial(ctx, "udp", address)
	}), key)
	return n, err
}

func validateShadowsocksAddress(address string) (string, error) {
	host, port, err := net.SplitHostPort(strings.TrimSpace(address))
	if err != nil || host == "" || port == "" {
		return "", errors.New("server address must be host:port")
	}
	number, err := strconv.Atoi(port)
	if err != nil || number < 1 || number > 65535 {
		return "", errors.New("port must be between 1 and 65535")
	}
	return host, nil
}

func (n *shadowsocksNetwork) IsNative() bool { return false }
func (n *shadowsocksNetwork) Dial(ctx context.Context, network, address string) (net.Conn, error) {
	switch network {
	case "tcp", "tcp4", "tcp6":
		if !n.tcp {
			return nil, gonnect.ErrUnsupported
		}
		return n.stream.DialStream(ctx, address)
	case "udp", "udp4", "udp6":
		return n.PacketDial(ctx, network, address)
	default:
		return nil, gonnect.ErrUnsupported
	}
}
func (n *shadowsocksNetwork) DialTCP(ctx context.Context, network, laddr, raddr string) (gonnect.TCPConn, error) {
	if laddr != "" || (network != "tcp" && network != "tcp4" && network != "tcp6") {
		return nil, gonnect.ErrUnsupported
	}
	conn, err := n.Dial(ctx, network, raddr)
	if err != nil {
		return nil, err
	}
	return &shadowsocksTCPConn{StreamConn: conn.(transport.StreamConn)}, nil
}
func (n *shadowsocksNetwork) Listen(context.Context, string, string) (net.Listener, error) {
	return nil, gonnect.ErrUnsupported
}
func (n *shadowsocksNetwork) ListenTCP(context.Context, string, string) (gonnect.TCPListener, error) {
	return nil, gonnect.ErrUnsupported
}
func (n *shadowsocksNetwork) ListenUDP(ctx context.Context, network, address string) (gonnect.UDPConn, error) {
	conn, err := n.ListenPacket(ctx, network, address)
	if err != nil {
		return nil, err
	}
	return &shadowsocksUDPConn{PacketConn: conn}, nil
}
func (n *shadowsocksNetwork) DialUDP(ctx context.Context, network, laddr, raddr string) (gonnect.UDPConn, error) {
	if laddr != "" && laddr != ":0" && laddr != "0.0.0.0:0" && laddr != "[::]:0" {
		return nil, gonnect.ErrUnsupported
	}
	conn, err := n.PacketDial(ctx, network, raddr)
	if err != nil {
		return nil, err
	}
	return &shadowsocksUDPConn{PacketConn: conn}, nil
}
func (n *shadowsocksNetwork) ListenPacketConfig(ctx context.Context, lc *gonnect.ListenConfig, network, address string) (gonnect.PacketConn, error) {
	if lc != nil && lc.Control != nil {
		return nil, gonnect.ErrUnsupported
	}
	return n.ListenPacket(ctx, network, address)
}
func (n *shadowsocksNetwork) ListenUDPConfig(ctx context.Context, lc *gonnect.ListenConfig, network, address string) (gonnect.UDPConn, error) {
	if lc != nil && lc.Control != nil {
		return nil, gonnect.ErrUnsupported
	}
	return n.ListenUDP(ctx, network, address)
}
func (n *shadowsocksNetwork) ListenMulticastUDP(context.Context, string, string, gonnect.MulticastOptions) (gonnect.MulticastPacketConn, error) {
	return nil, gonnect.ErrUnsupported
}
func (n *shadowsocksNetwork) ListenPacket(ctx context.Context, network, address string) (gonnect.PacketConn, error) {
	if !n.udp || (network != "udp" && network != "udp4" && network != "udp6") || (address != "" && address != ":0" && address != "0.0.0.0:0" && address != "[::]:0") {
		return nil, gonnect.ErrUnsupported
	}
	conn, err := n.packet.ListenPacket(ctx)
	if err != nil {
		return nil, err
	}
	return &shadowsocksUnboundPacket{PacketConn: conn.(gonnect.PacketConn)}, nil
}
func (n *shadowsocksNetwork) PacketDial(ctx context.Context, network, address string) (gonnect.PacketConn, error) {
	conn, err := n.ListenPacket(ctx, network, "")
	if err != nil {
		return nil, err
	}
	target, err := transport.MakeNetAddr("udp", address)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	return &shadowsocksBoundPacket{PacketConn: conn, remote: target}, nil
}

type halfCloseConn struct{ net.Conn }

func (c *halfCloseConn) CloseRead() error {
	if h, ok := c.Conn.(interface{ CloseRead() error }); ok {
		return h.CloseRead()
	}
	return c.Close()
}
func (c *halfCloseConn) CloseWrite() error {
	if h, ok := c.Conn.(interface{ CloseWrite() error }); ok {
		return h.CloseWrite()
	}
	return c.Close()
}

type shadowsocksTCPConn struct{ transport.StreamConn }

func (c *shadowsocksTCPConn) ReadFrom(r io.Reader) (int64, error) { return io.Copy(c.StreamConn, r) }
func (c *shadowsocksTCPConn) WriteTo(w io.Writer) (int64, error)  { return io.Copy(w, c.StreamConn) }
func (c *shadowsocksTCPConn) SetKeepAlive(bool) error             { return gonnect.ErrUnsupported }
func (c *shadowsocksTCPConn) SetKeepAliveConfig(net.KeepAliveConfig) error {
	return gonnect.ErrUnsupported
}
func (c *shadowsocksTCPConn) SetKeepAlivePeriod(time.Duration) error { return gonnect.ErrUnsupported }
func (c *shadowsocksTCPConn) SetLinger(int) error                    { return gonnect.ErrUnsupported }
func (c *shadowsocksTCPConn) SetNoDelay(bool) error                  { return gonnect.ErrUnsupported }

type shadowsocksBoundPacket struct {
	gonnect.PacketConn
	remote net.Addr
}
type shadowsocksUnboundPacket struct{ gonnect.PacketConn }

func (c *shadowsocksUnboundPacket) Read([]byte) (int, error)  { return 0, gonnect.ErrUnsupported }
func (c *shadowsocksUnboundPacket) Write([]byte) (int, error) { return 0, gonnect.ErrUnsupported }

type shadowsocksUDPConn struct{ gonnect.PacketConn }

func (c *shadowsocksUDPConn) ReadFromUDP(b []byte) (int, *net.UDPAddr, error) {
	n, addr, err := c.ReadFrom(b)
	if err != nil {
		return n, nil, err
	}
	udp, ok := addr.(*net.UDPAddr)
	if !ok {
		return n, nil, gonnect.ErrUnsupported
	}
	return n, udp, nil
}
func (c *shadowsocksUDPConn) ReadFromUDPAddrPort(b []byte) (int, netip.AddrPort, error) {
	n, addr, err := c.ReadFromUDP(b)
	if err != nil {
		return n, netip.AddrPort{}, err
	}
	return n, addr.AddrPort(), nil
}
func (c *shadowsocksUDPConn) WriteToUDP(b []byte, addr *net.UDPAddr) (int, error) {
	return c.WriteTo(b, addr)
}
func (c *shadowsocksUDPConn) WriteToUDPAddrPort(b []byte, addr netip.AddrPort) (int, error) {
	return c.WriteTo(b, net.UDPAddrFromAddrPort(addr))
}
func (c *shadowsocksUDPConn) ReadMsgUDP(b, oob []byte) (int, int, int, *net.UDPAddr, error) {
	return 0, 0, 0, nil, gonnect.ErrUnsupported
}
func (c *shadowsocksUDPConn) ReadMsgUDPAddrPort(b, oob []byte) (int, int, int, netip.AddrPort, error) {
	return 0, 0, 0, netip.AddrPort{}, gonnect.ErrUnsupported
}
func (c *shadowsocksUDPConn) WriteMsgUDP(b, oob []byte, addr *net.UDPAddr) (int, int, error) {
	if len(oob) != 0 {
		return 0, 0, gonnect.ErrUnsupported
	}
	n, err := c.WriteTo(b, addr)
	return n, 0, err
}
func (c *shadowsocksUDPConn) WriteMsgUDPAddrPort(b, oob []byte, addr netip.AddrPort) (int, int, error) {
	return c.WriteMsgUDP(b, oob, net.UDPAddrFromAddrPort(addr))
}

func (c *shadowsocksBoundPacket) RemoteAddr() net.Addr { return c.remote }
func (c *shadowsocksBoundPacket) Read(b []byte) (int, error) {
	host, port, err := net.SplitHostPort(c.remote.String())
	if err != nil {
		return 0, err
	}
	// The server can return the resolved IP for a domain target. The client
	// cannot resolve that domain without bypassing the supplied network. Keep
	// the destination port check that a connected UDP socket would apply.
	matchIP := net.ParseIP(host) != nil
	for {
		n, addr, err := c.ReadFrom(b)
		if err != nil {
			return n, err
		}
		if addr == nil {
			continue
		}
		if matchIP && addr.String() == c.remote.String() {
			return n, nil
		}
		if !matchIP {
			_, sourcePort, err := net.SplitHostPort(addr.String())
			if err == nil && sourcePort == port {
				return n, nil
			}
		}
	}
}
func (c *shadowsocksBoundPacket) Write(b []byte) (int, error) { return c.WriteTo(b, c.remote) }

var _ gonnect.Network = (*shadowsocksNetwork)(nil)
var _ gonnect.TCPConn = (*shadowsocksTCPConn)(nil)
var _ gonnect.PacketConn = (*shadowsocksBoundPacket)(nil)
var _ gonnect.PacketConn = (*shadowsocksUnboundPacket)(nil)
var _ gonnect.UDPConn = (*shadowsocksUDPConn)(nil)
