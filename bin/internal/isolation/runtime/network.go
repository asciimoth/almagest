package runtime

import (
	"context"
	"net"
	"net/netip"
	"strconv"
	"time"

	"github.com/asciimoth/almagest/bin/internal/isolation/protocol"
	"github.com/asciimoth/gonnect"
	gdns "github.com/asciimoth/gonnect/dns"
)

// Network uses only broker sockets. Resolver must be supplied by R's DNS
// composition; nil makes name lookup unavailable, never host-native.
type Network struct {
	gonnect.Resolver
	client  *Client
	purpose protocol.NetworkPurpose
}

var _ gonnect.Network = (*Network)(nil)
var _ gonnect.TCPConn = (*Conn)(nil)
var _ gonnect.UDPConn = (*UDPConn)(nil)
var _ gonnect.TCPListener = (*Listener)(nil)

func (c *Client) Network(purpose protocol.NetworkPurpose, resolver gonnect.Resolver) gonnect.Network {
	if resolver == nil {
		resolver = gdns.NewResolver(c.emptyDNS)
	}
	base := &Network{Resolver: resolver, client: c, purpose: purpose}
	return gonnect.NewNetworkWithResolver(base, resolver)
}
func (*Network) IsNative() bool { return false }
func parseEndpoint(s, network string) (netip.AddrPort, error) {
	if s == "" {
		return netip.AddrPort{}, nil
	}
	host, port, err := net.SplitHostPort(s)
	if err != nil {
		return netip.AddrPort{}, err
	}
	n, err := strconv.ParseUint(port, 10, 16)
	if err != nil {
		return netip.AddrPort{}, err
	}
	if host == "" {
		host = "0.0.0.0"
		if network == "tcp6" || network == "udp6" {
			host = "::"
		}
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return netip.AddrPort{}, err
	}
	return netip.AddrPortFrom(addr, uint16(n)), nil
}
func (n *Network) open(ctx context.Context, kind protocol.SocketKind, network, local, remote string, multi protocol.MulticastOptions) (*Conn, error) {
	l, err := parseEndpoint(local, network)
	if err != nil {
		return nil, err
	}
	r, err := parseEndpoint(remote, network)
	if err != nil {
		return nil, err
	}
	req := protocol.OpenSocketRequest{Meta: n.client.meta(true), Options: protocol.SocketOptions{Purpose: n.purpose, Kind: kind, Network: network, Local: l, Remote: r, NoDelay: true, Multicast: multi}}
	result, err := n.client.openResource(ctx, protocol.OperationOpenSocket, req)
	if err != nil {
		return nil, err
	}
	if result.Outcome != protocol.OutcomeCommitted {
		if result.Code != "" {
			return nil, decodeError(result.Code)
		}
		return nil, protocol.ErrRecoveryRequired
	}
	if err = result.Resource.Validate(n.client.session, result.Resource.Type, n.client.epoch.Load(), 1); err != nil {
		return nil, err
	}
	packet := kind != protocol.SocketDialTCP && kind != protocol.SocketListenTCP
	c := &Conn{client: n.client, ref: result.Resource, packet: packet}
	if packet {
		c.local = net.UDPAddrFromAddrPort(result.State.Local)
		if result.State.Remote.IsValid() {
			c.remote = net.UDPAddrFromAddrPort(result.State.Remote)
		}
	} else {
		c.local = net.TCPAddrFromAddrPort(result.State.Local)
		if result.State.Remote.IsValid() {
			c.remote = net.TCPAddrFromAddrPort(result.State.Remote)
		}
	}
	return c, nil
}
func (n *Network) Dial(ctx context.Context, network, address string) (net.Conn, error) {
	if network == "udp" || network == "udp4" || network == "udp6" {
		return n.DialUDP(ctx, network, "", address)
	}
	return n.DialTCP(ctx, network, "", address)
}
func (n *Network) Listen(ctx context.Context, network, address string) (net.Listener, error) {
	return n.ListenTCP(ctx, network, address)
}
func (n *Network) DialTCP(ctx context.Context, network, laddr, raddr string) (gonnect.TCPConn, error) {
	c, e := n.open(ctx, protocol.SocketDialTCP, network, laddr, raddr, protocol.MulticastOptions{})
	if e != nil {
		return nil, e
	}
	return c, nil
}
func (n *Network) ListenTCP(ctx context.Context, network, address string) (gonnect.TCPListener, error) {
	c, e := n.open(ctx, protocol.SocketListenTCP, network, address, "", protocol.MulticastOptions{})
	if e != nil {
		return nil, e
	}
	return &Listener{Conn: c}, nil
}
func (n *Network) DialUDP(ctx context.Context, network, laddr, raddr string) (gonnect.UDPConn, error) {
	c, e := n.open(ctx, protocol.SocketDialUDP, network, laddr, raddr, protocol.MulticastOptions{})
	if e != nil {
		return nil, e
	}
	return &UDPConn{Conn: c}, nil
}
func (n *Network) ListenUDP(ctx context.Context, network, address string) (gonnect.UDPConn, error) {
	c, e := n.open(ctx, protocol.SocketListenUDP, network, address, "", protocol.MulticastOptions{})
	if e != nil {
		return nil, e
	}
	return &UDPConn{Conn: c}, nil
}
func (n *Network) PacketDial(ctx context.Context, network, address string) (gonnect.PacketConn, error) {
	return n.DialUDP(ctx, network, "", address)
}
func (n *Network) ListenPacket(ctx context.Context, network, address string) (gonnect.PacketConn, error) {
	return n.ListenUDP(ctx, network, address)
}
func (n *Network) ListenUDPConfig(ctx context.Context, lc *gonnect.ListenConfig, network, address string) (gonnect.UDPConn, error) {
	if lc != nil && lc.Control != nil {
		// The broker owns the native socket, so the isolated runtime cannot
		// receive syscall.RawConn access. Virtual gonnect networks can call the
		// control hook with nil to let callers apply only portable setup.
		if err := lc.Control(network, address, nil); err != nil {
			return nil, err
		}
	}
	return n.ListenUDP(ctx, network, address)
}
func (n *Network) ListenPacketConfig(ctx context.Context, lc *gonnect.ListenConfig, network, address string) (gonnect.PacketConn, error) {
	return n.ListenUDPConfig(ctx, lc, network, address)
}
func (n *Network) ListenMulticastUDP(ctx context.Context, network, address string, o gonnect.MulticastOptions) (gonnect.MulticastPacketConn, error) {
	c, e := n.open(ctx, protocol.SocketMulticastUDP, network, address, "", protocol.MulticastOptions{ReuseAddr: o.ReuseAddr, ReusePort: o.ReusePort, RecvAnyIf: o.RecvAnyIf, ControlFlags: uint32(o.ControlFlags)})
	if e != nil {
		return nil, e
	}
	return &UDPConn{Conn: c}, nil
}
func (n *Network) Interfaces() ([]gonnect.NetworkInterface, error) {
	var snapshot protocol.InterfaceSnapshot
	if err := n.client.peer.Call(context.Background(), protocol.OperationListInterfaces, protocol.InterfaceRequest{Meta: n.client.meta(false), Purpose: n.purpose}, &snapshot); err != nil {
		return nil, err
	}
	out := make([]gonnect.NetworkInterface, 0, len(snapshot.Interfaces))
	for _, v := range snapshot.Interfaces {
		iface := &gonnect.LiteralInterface{IDVal: v.ID, HardwareAddrVal: net.HardwareAddr(v.HardwareAddress), IndexVal: int(v.Index), NameVal: v.Name, MTUVal: int(v.MTU), FlagsVal: net.Flags(v.Flags)}
		for _, p := range v.Addresses {
			iface.AddrsVal = append(iface.AddrsVal, &net.IPNet{IP: net.IP(p.Addr().AsSlice()), Mask: net.CIDRMask(p.Bits(), p.Addr().BitLen())})
		}
		for _, a := range v.Multicast {
			iface.MulticastAddrsVal = append(iface.MulticastAddrsVal, &net.IPAddr{IP: net.IP(a.AsSlice()), Zone: a.Zone()})
		}
		out = append(out, iface)
	}
	return out, nil
}
func (n *Network) InterfaceAddrs() ([]net.Addr, error) {
	ifaces, e := n.Interfaces()
	if e != nil {
		return nil, e
	}
	var out []net.Addr
	for _, i := range ifaces {
		a, _ := i.Addrs()
		out = append(out, a...)
	}
	return out, nil
}
func (n *Network) InterfaceMulticastAddrs() ([]net.Addr, error) {
	ifaces, e := n.Interfaces()
	if e != nil {
		return nil, e
	}
	var out []net.Addr
	for _, i := range ifaces {
		a, _ := i.MulticastAddrs()
		out = append(out, a...)
	}
	return out, nil
}
func (n *Network) InterfacesByIndex(index int) ([]gonnect.NetworkInterface, error) {
	ifaces, e := n.Interfaces()
	if e != nil {
		return nil, e
	}
	var out []gonnect.NetworkInterface
	for _, i := range ifaces {
		if i.Index() == index {
			out = append(out, i)
		}
	}
	return out, nil
}
func (n *Network) InterfacesByName(name string) ([]gonnect.NetworkInterface, error) {
	ifaces, e := n.Interfaces()
	if e != nil {
		return nil, e
	}
	var out []gonnect.NetworkInterface
	for _, i := range ifaces {
		if i.Name() == name {
			out = append(out, i)
		}
	}
	return out, nil
}

type Listener struct{ *Conn }

func (l *Listener) Addr() net.Addr                { return l.local }
func (l *Listener) SetDeadline(t time.Time) error { return l.SetReadDeadline(t) }
func (l *Listener) Accept() (net.Conn, error)     { return l.AcceptTCP() }
func (l *Listener) AcceptTCP() (gonnect.TCPConn, error) {
	l.readMu.Lock()
	l.mu.Lock()
	deadline := l.readDeadline
	l.mu.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan ioResult, 1)
	accepted := make(chan struct{})
	go func() {
		defer l.readMu.Unlock()
		result, err := l.client.channel(ctx, protocol.ChannelRequest{Resource: l.ref, Kind: protocol.ChannelAccept, Remaining: remaining(deadline)})
		done <- ioResult{accepted: result, err: err}
		select {
		case <-accepted:
		case <-ctx.Done():
			if result.Resource.ID.Valid() {
				_, _ = l.client.channel(context.Background(), protocol.ChannelRequest{Resource: result.Resource, Kind: protocol.ChannelReject})
			}
		}
	}()
	value := l.awaitIO(done, false, cancel)
	if value.err != nil {
		return nil, value.err
	}
	close(accepted)
	result := value.accepted
	if result.Resource.Type != protocol.ResourceSocket {
		return nil, protocol.ErrResourceType
	}
	if _, err := l.client.channel(context.Background(), protocol.ChannelRequest{Resource: result.Resource, Kind: protocol.ChannelClaim}); err != nil {
		return nil, err
	}
	return &Conn{client: l.client, ref: result.Resource, local: net.TCPAddrFromAddrPort(result.State.Local), remote: net.TCPAddrFromAddrPort(result.State.Remote)}, nil
}

// ResolverSnapshot is data from the broker's configured DNS provider, not an
// instruction to read the runtime host's resolver files.
func (c *Client) ResolverSnapshot(ctx context.Context) (protocol.ResolverSnapshot, error) {
	var snapshot protocol.ResolverSnapshot
	err := c.peer.Call(ctx, protocol.OperationResolverSnapshot, protocol.ReadRequest{Meta: c.meta(false)}, &snapshot)
	return snapshot, err
}

// SnapshotDNS retains the explicit numeric-upstream adapter for callers that
// need a snapshot. OutDNS uses the native provider instead.
func (c *Client) SnapshotDNS(ctx context.Context) (gdns.Interface, error) {
	snapshot, err := c.ResolverSnapshot(ctx)
	if err != nil {
		return nil, err
	}
	if len(snapshot.Servers) == 0 {
		return nil, protocol.ErrCapabilityUnavailable
	}
	urls := make([]string, 0, len(snapshot.Servers))
	for _, server := range snapshot.Servers {
		if !server.IsValid() || server.Port() == 0 {
			return nil, protocol.ErrInvalidContract
		}
		urls = append(urls, "udp://"+server.String())
	}
	network := &Network{client: c, purpose: protocol.NetworkOutbound, Resolver: gdns.NewResolver(c.emptyDNS)}
	return gdns.NewClient(network.Dial, nil, nil, urls...), nil
}
