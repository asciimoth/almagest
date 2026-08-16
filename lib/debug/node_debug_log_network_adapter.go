package debugnodes

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/netip"

	"github.com/asciimoth/gonnect"
	"github.com/asciimoth/pasta/pasta"
)

type debugLogNetwork struct {
	NetworkCloser
	logger pasta.Logger
}

func (n *debugLogNetwork) IsNative() bool {
	return n.NetworkCloser != nil && n.NetworkCloser.IsNative()
}
func (n *debugLogNetwork) Close() error {
	n.log("Close")
	if n.NetworkCloser == nil {
		return nil
	}
	return n.NetworkCloser.Close()
}

func (n *debugLogNetwork) Dial(ctx context.Context, network, address string) (net.Conn, error) {
	n.logf("Dial %s %s", network, address)
	conn, err := n.NetworkCloser.Dial(ctx, network, address)
	if err != nil {
		n.logf("Dial %s %s error=%v", network, address, err)
		return nil, err
	}
	return &debugLogConn{Conn: conn, logger: n.logger, name: "conn"}, nil
}

func (n *debugLogNetwork) Listen(ctx context.Context, network, address string) (net.Listener, error) {
	n.logf("Listen %s %s", network, address)
	listener, err := n.NetworkCloser.Listen(ctx, network, address)
	if err != nil {
		n.logf("Listen %s %s error=%v", network, address, err)
		return nil, err
	}
	return &debugLogListener{Listener: listener, logger: n.logger}, nil
}

func (n *debugLogNetwork) PacketDial(ctx context.Context, network, address string) (gonnect.PacketConn, error) {
	n.logf("PacketDial %s %s", network, address)
	conn, err := n.NetworkCloser.PacketDial(ctx, network, address)
	if err != nil {
		n.logf("PacketDial %s %s error=%v", network, address, err)
		return nil, err
	}
	return &debugLogPacketConn{PacketConn: conn, logger: n.logger, name: "packet"}, nil
}

func (n *debugLogNetwork) ListenPacket(ctx context.Context, network, address string) (gonnect.PacketConn, error) {
	n.logf("ListenPacket %s %s", network, address)
	conn, err := n.NetworkCloser.ListenPacket(ctx, network, address)
	if err != nil {
		n.logf("ListenPacket %s %s error=%v", network, address, err)
		return nil, err
	}
	return &debugLogPacketConn{PacketConn: conn, logger: n.logger, name: "packet"}, nil
}

func (n *debugLogNetwork) DialTCP(ctx context.Context, network, laddr, raddr string) (gonnect.TCPConn, error) {
	n.logf("DialTCP %s %s %s", network, laddr, raddr)
	conn, err := n.NetworkCloser.DialTCP(ctx, network, laddr, raddr)
	if err != nil {
		n.logf("DialTCP %s %s %s error=%v", network, laddr, raddr, err)
		return nil, err
	}
	return &debugLogTCPConn{TCPConn: conn, logger: n.logger}, nil
}

func (n *debugLogNetwork) ListenTCP(ctx context.Context, network, laddr string) (gonnect.TCPListener, error) {
	n.logf("ListenTCP %s %s", network, laddr)
	listener, err := n.NetworkCloser.ListenTCP(ctx, network, laddr)
	if err != nil {
		n.logf("ListenTCP %s %s error=%v", network, laddr, err)
		return nil, err
	}
	return &debugLogTCPListener{TCPListener: listener, logger: n.logger}, nil
}

func (n *debugLogNetwork) DialUDP(ctx context.Context, network, laddr, raddr string) (gonnect.UDPConn, error) {
	n.logf("DialUDP %s %s %s", network, laddr, raddr)
	conn, err := n.NetworkCloser.DialUDP(ctx, network, laddr, raddr)
	if err != nil {
		n.logf("DialUDP %s %s %s error=%v", network, laddr, raddr, err)
		return nil, err
	}
	return &debugLogUDPConn{UDPConn: conn, logger: n.logger}, nil
}

func (n *debugLogNetwork) ListenUDP(ctx context.Context, network, laddr string) (gonnect.UDPConn, error) {
	n.logf("ListenUDP %s %s", network, laddr)
	conn, err := n.NetworkCloser.ListenUDP(ctx, network, laddr)
	if err != nil {
		n.logf("ListenUDP %s %s error=%v", network, laddr, err)
		return nil, err
	}
	return &debugLogUDPConn{UDPConn: conn, logger: n.logger}, nil
}

func (n *debugLogNetwork) ListenPacketConfig(ctx context.Context, lc *gonnect.ListenConfig, network, address string) (gonnect.PacketConn, error) {
	n.logf("ListenPacketConfig %s %s", network, address)
	conn, err := n.NetworkCloser.ListenPacketConfig(ctx, lc, network, address)
	if err != nil {
		n.logf("ListenPacketConfig %s %s error=%v", network, address, err)
		return nil, err
	}
	return &debugLogPacketConn{PacketConn: conn, logger: n.logger, name: "packet"}, nil
}

func (n *debugLogNetwork) ListenUDPConfig(ctx context.Context, lc *gonnect.ListenConfig, network, laddr string) (gonnect.UDPConn, error) {
	n.logf("ListenUDPConfig %s %s", network, laddr)
	conn, err := n.NetworkCloser.ListenUDPConfig(ctx, lc, network, laddr)
	if err != nil {
		n.logf("ListenUDPConfig %s %s error=%v", network, laddr, err)
		return nil, err
	}
	return &debugLogUDPConn{UDPConn: conn, logger: n.logger}, nil
}

func (n *debugLogNetwork) ListenMulticastUDP(ctx context.Context, network, address string, opts gonnect.MulticastOptions) (gonnect.MulticastPacketConn, error) {
	n.logf("ListenMulticastUDP %s %s", network, address)
	conn, err := n.NetworkCloser.ListenMulticastUDP(ctx, network, address, opts)
	if err != nil {
		n.logf("ListenMulticastUDP %s %s error=%v", network, address, err)
		return nil, err
	}
	return &debugLogMulticastPacketConn{MulticastPacketConn: conn, logger: n.logger}, nil
}

func (n *debugLogNetwork) log(message string) {
	if n.logger != nil {
		n.logger.Debug(message)
	}
}

func (n *debugLogNetwork) logf(format string, args ...any) {
	if n.logger != nil {
		n.logger.Debugf(format, args...)
	}
}

type debugLogConn struct {
	net.Conn
	logger pasta.Logger
	name   string
}

func (c *debugLogConn) Read(b []byte) (int, error) {
	n, err := c.Conn.Read(b)
	logPayload(c.logger, c.name+".Read", b[:n], err)
	return n, err
}

func (c *debugLogConn) Write(b []byte) (int, error) {
	n, err := c.Conn.Write(b)
	logPayload(c.logger, c.name+".Write", b[:n], err)
	return n, err
}

func (c *debugLogConn) Close() error {
	err := c.Conn.Close()
	logResult(c.logger, c.name+".Close", err)
	return err
}

type debugLogListener struct {
	net.Listener
	logger pasta.Logger
}

func (l *debugLogListener) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	logResult(l.logger, "listener.Accept", err)
	if err != nil {
		return nil, err
	}
	return &debugLogConn{Conn: conn, logger: l.logger, name: "accepted"}, nil
}

type debugLogTCPListener struct {
	gonnect.TCPListener
	logger pasta.Logger
}

func (l *debugLogTCPListener) Accept() (net.Conn, error) {
	conn, err := l.TCPListener.Accept()
	logResult(l.logger, "tcpListener.Accept", err)
	if err != nil {
		return nil, err
	}
	return &debugLogConn{Conn: conn, logger: l.logger, name: "acceptedTCP"}, nil
}

func (l *debugLogTCPListener) AcceptTCP() (gonnect.TCPConn, error) {
	conn, err := l.TCPListener.AcceptTCP()
	logResult(l.logger, "tcpListener.AcceptTCP", err)
	if err != nil {
		return nil, err
	}
	return &debugLogTCPConn{TCPConn: conn, logger: l.logger}, nil
}

type debugLogTCPConn struct {
	gonnect.TCPConn
	logger pasta.Logger
}

func (c *debugLogTCPConn) Read(b []byte) (int, error) {
	n, err := c.TCPConn.Read(b)
	logPayload(c.logger, "tcp.Read", b[:n], err)
	return n, err
}

func (c *debugLogTCPConn) Write(b []byte) (int, error) {
	n, err := c.TCPConn.Write(b)
	logPayload(c.logger, "tcp.Write", b[:n], err)
	return n, err
}

func (c *debugLogTCPConn) Close() error {
	err := c.TCPConn.Close()
	logResult(c.logger, "tcp.Close", err)
	return err
}

func (c *debugLogTCPConn) CloseRead() error {
	err := c.TCPConn.CloseRead()
	logResult(c.logger, "tcp.CloseRead", err)
	return err
}

func (c *debugLogTCPConn) CloseWrite() error {
	err := c.TCPConn.CloseWrite()
	logResult(c.logger, "tcp.CloseWrite", err)
	return err
}

func (c *debugLogTCPConn) ReadFrom(r io.Reader) (int64, error) {
	n, err := c.TCPConn.ReadFrom(r)
	logResult(c.logger, fmt.Sprintf("tcp.ReadFrom bytes=%d", n), err)
	return n, err
}

func (c *debugLogTCPConn) WriteTo(w io.Writer) (int64, error) {
	n, err := c.TCPConn.WriteTo(w)
	logResult(c.logger, fmt.Sprintf("tcp.WriteTo bytes=%d", n), err)
	return n, err
}

type debugLogPacketConn struct {
	gonnect.PacketConn
	logger pasta.Logger
	name   string
}

func (c *debugLogPacketConn) Read(b []byte) (int, error) {
	n, err := c.PacketConn.Read(b)
	logPayload(c.logger, c.name+".Read", b[:n], err)
	return n, err
}

func (c *debugLogPacketConn) Write(b []byte) (int, error) {
	n, err := c.PacketConn.Write(b)
	logPayload(c.logger, c.name+".Write", b[:n], err)
	return n, err
}

func (c *debugLogPacketConn) ReadFrom(b []byte) (int, net.Addr, error) {
	n, addr, err := c.PacketConn.ReadFrom(b)
	logPayload(c.logger, fmt.Sprintf("%s.ReadFrom addr=%v", c.name, addr), b[:n], err)
	return n, addr, err
}

func (c *debugLogPacketConn) WriteTo(b []byte, addr net.Addr) (int, error) {
	n, err := c.PacketConn.WriteTo(b, addr)
	logPayload(c.logger, fmt.Sprintf("%s.WriteTo addr=%v", c.name, addr), b[:n], err)
	return n, err
}

func (c *debugLogPacketConn) Close() error {
	err := c.PacketConn.Close()
	logResult(c.logger, c.name+".Close", err)
	return err
}

type debugLogUDPConn struct {
	gonnect.UDPConn
	logger pasta.Logger
}

func (c *debugLogUDPConn) Read(b []byte) (int, error) {
	n, err := c.UDPConn.Read(b)
	logPayload(c.logger, "udp.Read", b[:n], err)
	return n, err
}

func (c *debugLogUDPConn) Write(b []byte) (int, error) {
	n, err := c.UDPConn.Write(b)
	logPayload(c.logger, "udp.Write", b[:n], err)
	return n, err
}

func (c *debugLogUDPConn) ReadFrom(b []byte) (int, net.Addr, error) {
	n, addr, err := c.UDPConn.ReadFrom(b)
	logPayload(c.logger, fmt.Sprintf("udp.ReadFrom addr=%v", addr), b[:n], err)
	return n, addr, err
}

func (c *debugLogUDPConn) WriteTo(b []byte, addr net.Addr) (int, error) {
	n, err := c.UDPConn.WriteTo(b, addr)
	logPayload(c.logger, fmt.Sprintf("udp.WriteTo addr=%v", addr), b[:n], err)
	return n, err
}

func (c *debugLogUDPConn) ReadFromUDP(b []byte) (int, *net.UDPAddr, error) {
	n, addr, err := c.UDPConn.ReadFromUDP(b)
	logPayload(c.logger, fmt.Sprintf("udp.ReadFromUDP addr=%v", addr), b[:n], err)
	return n, addr, err
}

func (c *debugLogUDPConn) ReadFromUDPAddrPort(b []byte) (int, netip.AddrPort, error) {
	n, addr, err := c.UDPConn.ReadFromUDPAddrPort(b)
	logPayload(c.logger, fmt.Sprintf("udp.ReadFromUDPAddrPort addr=%v", addr), b[:n], err)
	return n, addr, err
}

func (c *debugLogUDPConn) WriteToUDP(b []byte, addr *net.UDPAddr) (int, error) {
	n, err := c.UDPConn.WriteToUDP(b, addr)
	logPayload(c.logger, fmt.Sprintf("udp.WriteToUDP addr=%v", addr), b[:n], err)
	return n, err
}

func (c *debugLogUDPConn) WriteToUDPAddrPort(b []byte, addr netip.AddrPort) (int, error) {
	n, err := c.UDPConn.WriteToUDPAddrPort(b, addr)
	logPayload(c.logger, fmt.Sprintf("udp.WriteToUDPAddrPort addr=%v", addr), b[:n], err)
	return n, err
}

func (c *debugLogUDPConn) ReadMsgUDP(b, oob []byte) (int, int, int, *net.UDPAddr, error) {
	n, oobn, flags, addr, err := c.UDPConn.ReadMsgUDP(b, oob)
	logPayload(c.logger, fmt.Sprintf("udp.ReadMsgUDP addr=%v oob=%d flags=%d", addr, oobn, flags), b[:n], err)
	return n, oobn, flags, addr, err
}

func (c *debugLogUDPConn) ReadMsgUDPAddrPort(b, oob []byte) (int, int, int, netip.AddrPort, error) {
	n, oobn, flags, addr, err := c.UDPConn.ReadMsgUDPAddrPort(b, oob)
	logPayload(c.logger, fmt.Sprintf("udp.ReadMsgUDPAddrPort addr=%v oob=%d flags=%d", addr, oobn, flags), b[:n], err)
	return n, oobn, flags, addr, err
}

func (c *debugLogUDPConn) WriteMsgUDP(b, oob []byte, addr *net.UDPAddr) (int, int, error) {
	n, oobn, err := c.UDPConn.WriteMsgUDP(b, oob, addr)
	logPayload(c.logger, fmt.Sprintf("udp.WriteMsgUDP addr=%v oob=%d", addr, oobn), b[:n], err)
	return n, oobn, err
}

func (c *debugLogUDPConn) WriteMsgUDPAddrPort(b, oob []byte, addr netip.AddrPort) (int, int, error) {
	n, oobn, err := c.UDPConn.WriteMsgUDPAddrPort(b, oob, addr)
	logPayload(c.logger, fmt.Sprintf("udp.WriteMsgUDPAddrPort addr=%v oob=%d", addr, oobn), b[:n], err)
	return n, oobn, err
}

func (c *debugLogUDPConn) Close() error {
	err := c.UDPConn.Close()
	logResult(c.logger, "udp.Close", err)
	return err
}

type debugLogMulticastPacketConn struct {
	gonnect.MulticastPacketConn
	logger pasta.Logger
}

func (c *debugLogMulticastPacketConn) ReadFrom(b []byte) (int, net.Addr, error) {
	n, addr, err := c.MulticastPacketConn.ReadFrom(b)
	logPayload(c.logger, fmt.Sprintf("multicast.ReadFrom addr=%v", addr), b[:n], err)
	return n, addr, err
}

func (c *debugLogMulticastPacketConn) WriteTo(b []byte, addr net.Addr) (int, error) {
	n, err := c.MulticastPacketConn.WriteTo(b, addr)
	logPayload(c.logger, fmt.Sprintf("multicast.WriteTo addr=%v", addr), b[:n], err)
	return n, err
}

func (c *debugLogMulticastPacketConn) ReadFromControl(b []byte) (int, gonnect.ControlMessage, net.Addr, error) {
	n, cm, addr, err := c.MulticastPacketConn.ReadFromControl(b)
	logPayload(c.logger, fmt.Sprintf("multicast.ReadFromControl addr=%v control=%+v", addr, cm), b[:n], err)
	return n, cm, addr, err
}

func (c *debugLogMulticastPacketConn) WriteToControl(b []byte, cm gonnect.ControlMessage, addr net.Addr) (int, error) {
	n, err := c.MulticastPacketConn.WriteToControl(b, cm, addr)
	logPayload(c.logger, fmt.Sprintf("multicast.WriteToControl addr=%v control=%+v", addr, cm), b[:n], err)
	return n, err
}

var (
	_ NetworkCloser               = (*debugLogNetwork)(nil)
	_ net.Conn                    = (*debugLogConn)(nil)
	_ net.Listener                = (*debugLogListener)(nil)
	_ gonnect.TCPListener         = (*debugLogTCPListener)(nil)
	_ gonnect.TCPConn             = (*debugLogTCPConn)(nil)
	_ gonnect.PacketConn          = (*debugLogPacketConn)(nil)
	_ gonnect.UDPConn             = (*debugLogUDPConn)(nil)
	_ gonnect.MulticastPacketConn = (*debugLogMulticastPacketConn)(nil)
)
