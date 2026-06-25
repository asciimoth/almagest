package almagest

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"

	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/gonnect"
	"github.com/asciimoth/gonnect/tun"
	"github.com/asciimoth/pasta/pasta"
	"github.com/asciimoth/pasta/pasta/std"
)

func init() {
	regClass(func(*engine) pasta.NodeClass {
		return debugLogNetworkClass{}
	}, true)
	regClass(func(*engine) pasta.NodeClass {
		return debugLogTunClass{}
	}, true)
}

const (
	NodeTypeDebugLogNetwork = "debug.almagest/LogNetwork"
	NodeTypeDebugLogTun     = "debug.almagest/LogTun"
)

type debugLogNetworkClass struct{}

func (debugLogNetworkClass) ClassName() string        { return NodeTypeDebugLogNetwork }
func (debugLogNetworkClass) ShortDescription() string { return "DEBUG network logger" }
func (debugLogNetworkClass) LongDescription() string {
	return "Passes almagest/network operations through unchanged while debug logging operations and payload bytes."
}
func (debugLogNetworkClass) DefaultNodeParams() pasta.NodeClassParams {
	return pasta.NodeClassParams{PrimaryType: TypeNetwork, InitialPorts: []pasta.Port{
		NetworkPort("left", "Network"),
		NetworkPort("right", "Network"),
	}}
}
func (debugLogNetworkClass) NewNode(_ configer.Config, _ ...*pasta.NodeClassState) (pasta.Node, error) {
	return &debugLogNetworkNode{sent: map[uint64]NetworkCloser{}}, nil
}

type debugLogNetworkNode struct {
	pasta.BasicNode

	w      *pasta.Workspace
	logger pasta.Logger
	id     uint64
	out    uint64
	in     uint64

	network NetworkCloser
	sent    map[uint64]NetworkCloser
}

func (n *debugLogNetworkNode) OnInit(w *pasta.Workspace, logger pasta.Logger, id uint64, _ string, restored *pasta.NodeInitData, _, _, _, _ bool) error {
	n.w = w
	n.logger = logger
	n.id = id
	if n.sent == nil {
		n.sent = map[uint64]NetworkCloser{}
	}
	if restored != nil {
		if len(restored.LeftPorts) > 0 {
			n.out = restored.LeftPorts[0]
		}
		if len(restored.RightPorts) > 0 {
			n.in = restored.RightPorts[0]
		}
	}
	if err := n.w.SetNodePrimary(n.id, TypeNetwork); err != nil {
		return err
	}
	_ = n.w.SetNodeLabel(n.id, "network log")
	return nil
}

func (n *debugLogNetworkNode) OnStop() {
	n.closeSentNetworks()
	if n.network != nil {
		_ = n.network.Close()
		n.network = nil
	}
}

func (n *debugLogNetworkNode) OnReady() error {
	n.requestInput()
	n.sendAll()
	return nil
}

func (n *debugLogNetworkNode) PreLinkAdd(port uint64, linkType, portDirection string) error {
	switch port {
	case n.out:
		if portDirection != "left" || linkType != TypeNetwork {
			return pasta.LinkTypeErr(linkType)
		}
	case n.in:
		if portDirection != "right" || linkType != TypeNetwork {
			return pasta.LinkTypeErr(linkType)
		}
		if n.portHasLinks(port) {
			return pasta.ErrLinkDup
		}
	default:
		return pasta.LinkTypeErr(linkType)
	}
	return nil
}

func (n *debugLogNetworkNode) OnLinkAdd(link, port uint64, linkType, portDirection string) error {
	if port == n.out && portDirection == "left" && linkType == TypeNetwork {
		n.sendNetworkToLink(link)
		return nil
	}
	if port == n.in && portDirection == "right" && linkType == TypeNetwork {
		std.Request(n.w, n.id, link)
	}
	return nil
}

func (n *debugLogNetworkNode) OnLinkRemoved(link, port uint64, linkType, portDirection string) error {
	if port == n.out && portDirection == "left" && linkType == TypeNetwork {
		n.closeSentNetwork(link)
		return nil
	}
	if port == n.in && portDirection == "right" && linkType == TypeNetwork {
		if n.network != nil {
			_ = n.network.Close()
		}
		n.network = nil
		n.closeSentNetworks()
	}
	return nil
}

func (n *debugLogNetworkNode) OnEvent(event pasta.Event, linkType string, _ []string, receiverPortDirection string) error {
	if event.ReceiverPort == n.out && receiverPortDirection == "left" && linkType == TypeNetwork && std.IsRequest(event.Payload) {
		n.sendNetworkToLink(event.Link)
		return nil
	}
	if event.ReceiverPort != n.in || receiverPortDirection != "right" || linkType != TypeNetwork {
		return nil
	}
	payload, ok := event.Payload.(NetworkPayload)
	if !ok || payload.Network == nil {
		return nil
	}
	if SameNetworkInstance(n.network, payload.Network) {
		return nil
	}
	if n.network != nil {
		_ = n.network.Close()
	}
	n.closeSentNetworks()
	BindNetworkResource(n.w, n.id, event.Link, payload.Network)
	n.network = payload.Network
	n.sendAll()
	return nil
}

func (n *debugLogNetworkNode) requestInput() {
	port, ok := n.w.PortSnapshot(n.in)
	if !ok {
		return
	}
	for _, link := range port.Links {
		std.Request(n.w, n.id, link)
	}
}

func (n *debugLogNetworkNode) sendAll() {
	port, ok := n.w.PortSnapshot(n.out)
	if !ok {
		return
	}
	for _, link := range port.Links {
		n.sendNetworkToLink(link)
	}
}

func (n *debugLogNetworkNode) sendNetworkToLink(link uint64) {
	if n.network == nil {
		return
	}
	wrapper := n.sent[link]
	if wrapper == nil {
		wrapper = &debugLogNetwork{NetworkCloser: gonnect.DetachNetwork(n.network, nil), logger: n.logger}
		BindNetworkResource(n.w, n.id, link, wrapper)
		n.sent[link] = wrapper
	}
	n.w.EmitEvent(n.id, link, NetworkPayload{Network: wrapper})
}

func (n *debugLogNetworkNode) closeSentNetwork(link uint64) {
	if old := n.sent[link]; old != nil {
		_ = old.Close()
		delete(n.sent, link)
	}
}

func (n *debugLogNetworkNode) closeSentNetworks() {
	for link, old := range n.sent {
		if old != nil {
			_ = old.Close()
		}
		delete(n.sent, link)
	}
}

func (n *debugLogNetworkNode) portHasLinks(port uint64) bool {
	snapshot, ok := n.w.PortSnapshot(port)
	return ok && len(snapshot.Links) > 0
}

type debugLogTunClass struct{}

func (debugLogTunClass) ClassName() string        { return NodeTypeDebugLogTun }
func (debugLogTunClass) ShortDescription() string { return "DEBUG TUN logger" }
func (debugLogTunClass) LongDescription() string {
	return "Passes almagest/tun operations through unchanged while debug logging reads, writes, and packet payload bytes."
}
func (debugLogTunClass) DefaultNodeParams() pasta.NodeClassParams {
	return pasta.NodeClassParams{PrimaryType: TypeTun, InitialPorts: []pasta.Port{
		TunPort("left", "in"),
		TunPort("right", "Tun"),
	}}
}
func (debugLogTunClass) NewNode(_ configer.Config, _ ...*pasta.NodeClassState) (pasta.Node, error) {
	return &debugLogTunNode{sent: map[uint64]tun.Tun{}, outputCfgs: TunConfigByLink{}}, nil
}

type debugLogTunNode struct {
	pasta.BasicNode

	w      *pasta.Workspace
	logger pasta.Logger
	id     uint64
	in     uint64
	out    uint64

	tun  tun.Tun
	sent map[uint64]tun.Tun

	outputCfgs TunConfigByLink
}

func (n *debugLogTunNode) OnInit(w *pasta.Workspace, logger pasta.Logger, id uint64, _ string, restored *pasta.NodeInitData, _, _, _, _ bool) error {
	n.w = w
	n.logger = logger
	n.id = id
	if n.sent == nil {
		n.sent = map[uint64]tun.Tun{}
	}
	if n.outputCfgs == nil {
		n.outputCfgs = TunConfigByLink{}
	}
	if restored != nil {
		if len(restored.LeftPorts) > 0 {
			n.in = restored.LeftPorts[0]
		}
		if len(restored.RightPorts) > 0 {
			n.out = restored.RightPorts[0]
		}
	}
	if err := n.w.SetNodePrimary(n.id, TypeTun); err != nil {
		return err
	}
	_ = n.w.SetNodeLabel(n.id, "tun log")
	return nil
}

func (n *debugLogTunNode) OnStop() {
	n.closeSentTuns()
	if n.tun != nil {
		_ = n.tun.Close()
		n.tun = nil
	}
	n.outputCfgs = TunConfigByLink{}
}

func (n *debugLogTunNode) OnReady() error {
	n.requestInput()
	n.sendAll()
	n.sendMergedTunConfig()
	return nil
}

func (n *debugLogTunNode) PreLinkAdd(port uint64, linkType, portDirection string) error {
	if port == n.in && portDirection == "left" {
		return TunPreLinkAdd(n.w, port, linkType)
	}
	if port == n.out && portDirection == "right" {
		return TunPreLinkAdd(n.w, port, linkType)
	}
	return pasta.LinkTypeErr(linkType)
}

func (n *debugLogTunNode) OnLinkAdd(link, port uint64, linkType, portDirection string) error {
	if port == n.in && portDirection == "left" && linkType == TypeTun {
		std.Request(n.w, n.id, link)
		n.sendMergedTunConfigToLink(link)
		return nil
	}
	if port == n.out && portDirection == "right" && linkType == TypeTun {
		n.sendTunToLink(link)
	}
	return nil
}

func (n *debugLogTunNode) OnLinkRemoved(link uint64, port uint64, linkType, portDirection string) error {
	if port == n.out && portDirection == "right" && linkType == TypeTun {
		n.closeSentTun(link)
		n.outputCfgs.Delete(link)
		n.sendMergedTunConfig()
		return nil
	}
	if port == n.in && portDirection == "left" && linkType == TypeTun {
		if n.tun != nil {
			_ = n.tun.Close()
		}
		n.tun = nil
		n.closeSentTuns()
	}
	return nil
}

func (n *debugLogTunNode) OnEvent(event pasta.Event, linkType string, _ []string, receiverPortDirection string) error {
	if linkType != TypeTun {
		return nil
	}
	if event.ReceiverPort == n.out && receiverPortDirection == "right" {
		if std.IsRequest(event.Payload) {
			n.sendTunToLink(event.Link)
			return nil
		}
		if cfg, ok := event.Payload.(TunConfig); ok {
			n.outputCfgs.Set(event.Link, cfg)
			n.sendMergedTunConfig()
		}
		return nil
	}
	if event.ReceiverPort == n.in && receiverPortDirection == "left" && std.IsRequest(event.Payload) {
		n.sendMergedTunConfigToLink(event.Link)
		return nil
	}
	if event.ReceiverPort != n.in || receiverPortDirection != "left" {
		return nil
	}
	payload, ok := event.Payload.(TunPayload)
	if !ok || payload.Tun == nil {
		return nil
	}
	if SameTunInstance(n.tun, payload.Tun) {
		return nil
	}
	if n.tun != nil {
		_ = n.tun.Close()
	}
	n.closeSentTuns()
	BindTunResource(n.w, n.id, event.Link, payload.Tun)
	n.tun = payload.Tun
	n.sendAll()
	return nil
}

func (n *debugLogTunNode) requestInput() {
	port, ok := n.w.PortSnapshot(n.in)
	if !ok {
		return
	}
	for _, link := range port.Links {
		std.Request(n.w, n.id, link)
	}
}

func (n *debugLogTunNode) sendAll() {
	port, ok := n.w.PortSnapshot(n.out)
	if !ok {
		return
	}
	for _, link := range port.Links {
		n.sendTunToLink(link)
	}
}

func (n *debugLogTunNode) sendTunToLink(link uint64) {
	if n.tun == nil {
		return
	}
	wrapper := n.sent[link]
	if wrapper == nil {
		wrapper = &debugLogTun{Tun: tun.Detach(n.tun), logger: n.logger}
		BindTunResource(n.w, n.id, link, wrapper)
		n.sent[link] = wrapper
	}
	n.w.EmitEvent(n.id, link, TunPayload{Tun: wrapper})
}

func (n *debugLogTunNode) sendMergedTunConfig() {
	port, ok := n.w.PortSnapshot(n.in)
	if !ok {
		return
	}
	for _, link := range port.Links {
		n.sendMergedTunConfigToLink(link)
	}
}

func (n *debugLogTunNode) sendMergedTunConfigToLink(link uint64) {
	if link == 0 {
		return
	}
	n.w.EmitEvent(n.id, link, n.outputCfgs.Merge())
}

func (n *debugLogTunNode) closeSentTun(link uint64) {
	if old := n.sent[link]; old != nil {
		_ = old.Close()
		delete(n.sent, link)
	}
}

func (n *debugLogTunNode) closeSentTuns() {
	for link, old := range n.sent {
		if old != nil {
			_ = old.Close()
		}
		delete(n.sent, link)
	}
}

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

type debugLogTun struct {
	tun.Tun
	logger pasta.Logger
}

func (t *debugLogTun) IsNative() bool { return t.Tun != nil && t.Tun.IsNative() }
func (t *debugLogTun) Close() error {
	if t.logger != nil {
		t.logger.Debug("tun.Close")
	}
	if t.Tun == nil {
		return nil
	}
	return t.Tun.Close()
}

func (t *debugLogTun) Read(bufs [][]byte, sizes []int, offset int) (int, error) {
	n, err := t.Tun.Read(bufs, sizes, offset)
	for i := 0; i < n && i < len(bufs) && i < len(sizes); i++ {
		start := min(offset, len(bufs[i]))
		end := min(start+sizes[i], len(bufs[i]))
		logPayload(t.logger, fmt.Sprintf("tun.Read packet=%d", i), bufs[i][start:end], err)
	}
	if n == 0 || err != nil {
		logResult(t.logger, "tun.Read", err)
	}
	return n, err
}

func (t *debugLogTun) Write(bufs [][]byte, offset int) (int, error) {
	n, err := t.Tun.Write(bufs, offset)
	for i := 0; i < n && i < len(bufs); i++ {
		start := min(offset, len(bufs[i]))
		logPayload(t.logger, fmt.Sprintf("tun.Write packet=%d", i), bufs[i][start:], err)
	}
	if n == 0 || err != nil {
		logResult(t.logger, "tun.Write", err)
	}
	return n, err
}

func logPayload(logger pasta.Logger, op string, payload []byte, err error) {
	if logger == nil {
		return
	}
	if err != nil {
		logger.Debugf("%s bytes=%d payload=%q error=%v", op, len(payload), payload, err)
		return
	}
	logger.Debugf("%s bytes=%d payload=%q", op, len(payload), payload)
}

func logResult(logger pasta.Logger, op string, err error) {
	if logger == nil {
		return
	}
	if err != nil {
		logger.Debugf("%s error=%v", op, err)
		return
	}
	logger.Debug(op)
}

var (
	_ NetworkCloser                = (*debugLogNetwork)(nil)
	_ net.Conn                     = (*debugLogConn)(nil)
	_ net.Listener                 = (*debugLogListener)(nil)
	_ gonnect.TCPListener          = (*debugLogTCPListener)(nil)
	_ gonnect.TCPConn              = (*debugLogTCPConn)(nil)
	_ gonnect.PacketConn           = (*debugLogPacketConn)(nil)
	_ gonnect.UDPConn              = (*debugLogUDPConn)(nil)
	_ gonnect.MulticastPacketConn  = (*debugLogMulticastPacketConn)(nil)
	_ tun.Tun                      = (*debugLogTun)(nil)
	_ interface{ File() *os.File } = (*debugLogTun)(nil)
)
