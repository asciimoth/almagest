package almagest

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/formular"
	"github.com/asciimoth/gonnect"
	netstackhelpers "github.com/asciimoth/gonnect-netstack/helpers"
	netstackspoofer "github.com/asciimoth/gonnect-netstack/spoofer"
	"github.com/asciimoth/gonnect/tun"
	"github.com/asciimoth/pasta/pasta"
	"github.com/asciimoth/pasta/pasta/std"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
)

func init() {
	regClass(func(e *engine) pasta.NodeClass {
		return spooferClass{engine: e}
	}, false)
}

const (
	// NodeTypeSpoofer is the node class name for a TUN-to-network adapter.
	NodeTypeSpoofer = "almagest/Spoofer"

	defaultSpooferMTU   = 1500
	defaultSpooferQueue = 1024
)

type spooferClass struct {
	engine *engine
}

func (spooferClass) ClassName() string        { return NodeTypeSpoofer }
func (spooferClass) ShortDescription() string { return "TUN network spoofer" }
func (spooferClass) LongDescription() string {
	return "Consumes an almagest/tun endpoint and forwards intercepted TCP and UDP flows through an almagest/network."
}
func (spooferClass) DefaultNodeParams() pasta.NodeClassParams {
	return pasta.NodeClassParams{PrimaryType: TypeTun, InitialPorts: []pasta.Port{
		TunPort("left", "Tun"),
		NetworkPort("right", "Network"),
	}}
}
func (c spooferClass) NewNode(cfg configer.Config, _ ...*pasta.NodeClassState) (pasta.Node, error) {
	config := defaultSpooferConfig()
	config.read(cfg)
	return &spooferNode{engine: c.engine, config: config}, nil
}

type spooferConfig struct {
	mtu                  int
	queueLength          int
	disableSACK          bool
	ttl                  int
	icmpBurst            int
	icmpLimit            float64
	tcpSendBufferSize    int
	tcpReceiveBufferSize int
	congestionControlAlg string
	disableNagle         bool
	disableTCPModRecBuff bool
	tcpKeepAlive         bool
	tcpKeepAliveIdle     int
	tcpKeepAliveInterval int
	tcpKeepAliveCount    int
	tcpForwardWnd        int
	tcpForwardAttempts   int
}

func defaultSpooferConfig() spooferConfig {
	return spooferConfig{
		mtu:         defaultSpooferMTU,
		queueLength: defaultSpooferQueue,
	}
}

func (c *spooferConfig) read(cfg configer.Config) {
	if cfg == nil {
		return
	}
	c.mtu = readConfigInt(cfg, "mtu", c.mtu)
	c.queueLength = readConfigInt(cfg, "queueLength", c.queueLength)
	c.disableSACK = readConfigBool(cfg, "disableSACK", c.disableSACK)
	c.ttl = readConfigInt(cfg, "ttl", c.ttl)
	c.icmpBurst = readConfigInt(cfg, "icmpBurst", c.icmpBurst)
	c.icmpLimit = readConfigFloat(cfg, "icmpLimit", c.icmpLimit)
	c.tcpSendBufferSize = readConfigInt(cfg, "tcpSendBufferSize", c.tcpSendBufferSize)
	c.tcpReceiveBufferSize = readConfigInt(cfg, "tcpReceiveBufferSize", c.tcpReceiveBufferSize)
	c.congestionControlAlg = readConfigString(cfg, "congestionControlAlg", c.congestionControlAlg)
	c.disableNagle = readConfigBool(cfg, "disableNagle", c.disableNagle)
	c.disableTCPModRecBuff = readConfigBool(cfg, "disableTCPModRecBuff", c.disableTCPModRecBuff)
	c.tcpKeepAlive = readConfigBool(cfg, "tcpKeepAlive", c.tcpKeepAlive)
	c.tcpKeepAliveIdle = readConfigInt(cfg, "tcpKeepAliveIdle", c.tcpKeepAliveIdle)
	c.tcpKeepAliveInterval = readConfigInt(cfg, "tcpKeepAliveInterval", c.tcpKeepAliveInterval)
	c.tcpKeepAliveCount = readConfigInt(cfg, "tcpKeepAliveCount", c.tcpKeepAliveCount)
	c.tcpForwardWnd = readConfigInt(cfg, "tcpForwardWnd", c.tcpForwardWnd)
	c.tcpForwardAttempts = readConfigInt(cfg, "tcpForwardAttempts", c.tcpForwardAttempts)
	c.normalize()
}

func (c *spooferConfig) normalize() {
	if c.mtu <= 0 {
		c.mtu = defaultSpooferMTU
	}
	if c.queueLength <= 0 {
		c.queueLength = defaultSpooferQueue
	}
	c.congestionControlAlg = strings.TrimSpace(c.congestionControlAlg)
}

// spooferNode owns a running netstack Stack while both input links are
// present. Any change to options, the source TUN, or the target network
// replaces the Stack so callbacks always close over a coherent pair.
type spooferNode struct {
	pasta.BasicNode

	engine *engine
	logger pasta.Logger

	w       *pasta.Workspace
	id      uint64
	tunp    uint64
	network uint64

	config spooferConfig

	tun          tun.Tun
	networkValue NetworkCloser
	runtime      *spooferRuntime
	formError    string
}

type spooferRuntime struct {
	ctx      context.Context
	cancel   context.CancelFunc
	stack    *stack.Stack
	endpoint stack.LinkEndpoint
	tun      tun.Tun
}

func (n *spooferNode) OnInit(w *pasta.Workspace, logger pasta.Logger, id uint64, _ string, restored *pasta.NodeInitData, _, _, _, _ bool) error {
	n.w = w
	n.id = id
	n.logger = logger
	n.config.normalize()
	if restored != nil {
		if len(restored.LeftPorts) > 0 {
			n.tunp = restored.LeftPorts[0]
		}
		if len(restored.RightPorts) > 0 {
			n.network = restored.RightPorts[0]
		}
	}
	if err := n.w.SetNodePrimary(n.id, TypeTun); err != nil {
		return err
	}
	_ = n.updateLabel()
	n.sendMenuSnapshot()
	return nil
}

func (n *spooferNode) OnReady() error {
	n.requestPort(n.tunp)
	n.requestPort(n.network)
	n.rebuildStack()
	return nil
}

func (n *spooferNode) OnStop() {
	n.stopStack()
}

func (n *spooferNode) PreLinkAdd(port uint64, linkType, portDirection string) error {
	switch port {
	case n.tunp:
		if portDirection != "left" {
			return pasta.LinkTypeErr(linkType)
		}
		return TunPreLinkAdd(n.w, port, linkType)
	case n.network:
		if portDirection != "right" || linkType != TypeNetwork {
			return pasta.LinkTypeErr(linkType)
		}
		if portHasLinks(n.w, port) {
			return pasta.ErrLinkDup
		}
	default:
		return pasta.LinkTypeErr(linkType)
	}
	return nil
}

func (n *spooferNode) OnLinkAdd(link, port uint64, _, _ string) error {
	switch port {
	case n.tunp, n.network:
		std.Request(n.w, n.id, link)
	}
	return nil
}

func (n *spooferNode) OnLinkRemoved(_ uint64, port uint64, _, _ string) error {
	switch port {
	case n.tunp:
		n.tun = nil
		n.rebuildStack()
	case n.network:
		n.networkValue = nil
		n.rebuildStack()
	}
	return nil
}

func (n *spooferNode) OnEvent(event pasta.Event, linkType string, _ []string, receiverPortDirection string) error {
	if event.ReceiverPort == n.tunp && receiverPortDirection == "left" && linkType == TypeTun {
		if std.IsRequest(event.Payload) {
			return nil
		}
		payload, ok := event.Payload.(TunPayload)
		if !ok || payload.Tun == nil {
			return nil
		}
		if SameTunSource(n.tun, payload.Tun) {
			if !SameTunInstance(n.tun, payload.Tun) {
				_ = payload.Tun.Close()
			}
			return nil
		}
		BindTunResource(n.w, n.id, event.Link, payload.Tun)
		n.tun = payload.Tun
		n.rebuildStack()
		return nil
	}

	if event.ReceiverPort == n.network && receiverPortDirection == "right" && linkType == TypeNetwork {
		payload, ok := event.Payload.(NetworkPayload)
		if !ok || payload.Network == nil {
			return nil
		}
		if NetworkIsReady(n.networkValue) && SameNetworkSource(n.networkValue, payload.Network) {
			if !SameNetworkInstance(n.networkValue, payload.Network) {
				_ = payload.Network.Close()
			}
			return nil
		}
		BindNetworkResource(n.w, n.id, event.Link, payload.Network)
		n.networkValue = payload.Network
		n.rebuildStack()
	}
	return nil
}

func (n *spooferNode) OnFormularMsg(message any) error {
	msg, ok := message.(formular.FormApplyMessage)
	if !ok || msg.MenuID != pasta.NodeMenuID(n.id) || msg.BlockID != "config" {
		return nil
	}
	return n.applyConfigForm(msg.Values)
}

func (n *spooferNode) OnSave(cfg configer.Config) error {
	if err := pasta.DeleteNodeOwnedConfigKeys(cfg); err != nil {
		return err
	}
	values := map[string]any{
		"mtu":                  n.config.mtu,
		"queueLength":          n.config.queueLength,
		"disableSACK":          n.config.disableSACK,
		"ttl":                  n.config.ttl,
		"icmpBurst":            n.config.icmpBurst,
		"icmpLimit":            n.config.icmpLimit,
		"tcpSendBufferSize":    n.config.tcpSendBufferSize,
		"tcpReceiveBufferSize": n.config.tcpReceiveBufferSize,
		"congestionControlAlg": n.config.congestionControlAlg,
		"disableNagle":         n.config.disableNagle,
		"disableTCPModRecBuff": n.config.disableTCPModRecBuff,
		"tcpKeepAlive":         n.config.tcpKeepAlive,
		"tcpKeepAliveIdle":     n.config.tcpKeepAliveIdle,
		"tcpKeepAliveInterval": n.config.tcpKeepAliveInterval,
		"tcpKeepAliveCount":    n.config.tcpKeepAliveCount,
		"tcpForwardWnd":        n.config.tcpForwardWnd,
		"tcpForwardAttempts":   n.config.tcpForwardAttempts,
	}
	for key, value := range values {
		if err := cfg.Set(configer.Path{key}, value); err != nil {
			return err
		}
	}
	return nil
}

func (n *spooferNode) applyConfigForm(values map[string]any) error {
	next := n.config
	if value, ok := intFromAny(values["mtu"]); ok {
		next.mtu = value
	}
	if value, ok := intFromAny(values["queueLength"]); ok {
		next.queueLength = value
	}
	if value, ok := boolFromAny(values["disableSACK"]); ok {
		next.disableSACK = value
	}
	if value, ok := intFromAny(values["ttl"]); ok {
		next.ttl = value
	}
	if value, ok := intFromAny(values["icmpBurst"]); ok {
		next.icmpBurst = value
	}
	if value, ok := floatFromAny(values["icmpLimit"]); ok {
		next.icmpLimit = value
	}
	if value, ok := intFromAny(values["tcpSendBufferSize"]); ok {
		next.tcpSendBufferSize = value
	}
	if value, ok := intFromAny(values["tcpReceiveBufferSize"]); ok {
		next.tcpReceiveBufferSize = value
	}
	if value, ok := stringFromAny(values["congestionControlAlg"]); ok {
		next.congestionControlAlg = value
	}
	if value, ok := boolFromAny(values["disableNagle"]); ok {
		next.disableNagle = value
	}
	if value, ok := boolFromAny(values["disableTCPModRecBuff"]); ok {
		next.disableTCPModRecBuff = value
	}
	if value, ok := boolFromAny(values["tcpKeepAlive"]); ok {
		next.tcpKeepAlive = value
	}
	if value, ok := intFromAny(values["tcpKeepAliveIdle"]); ok {
		next.tcpKeepAliveIdle = value
	}
	if value, ok := intFromAny(values["tcpKeepAliveInterval"]); ok {
		next.tcpKeepAliveInterval = value
	}
	if value, ok := intFromAny(values["tcpKeepAliveCount"]); ok {
		next.tcpKeepAliveCount = value
	}
	if value, ok := intFromAny(values["tcpForwardWnd"]); ok {
		next.tcpForwardWnd = value
	}
	if value, ok := intFromAny(values["tcpForwardAttempts"]); ok {
		next.tcpForwardAttempts = value
	}
	next.normalize()
	if err := validateSpooferConfig(next); err != nil {
		n.formError = err.Error()
		n.sendForcedMenuSnapshot()
		_ = n.updateLabel()
		return nil
	}
	n.formError = ""
	n.config = next
	n.sendForcedMenuSnapshot()
	n.rebuildStack()
	return nil
}

func validateSpooferConfig(config spooferConfig) error {
	if config.mtu <= 0 {
		return errors.New("MTU must be positive")
	}
	if config.queueLength <= 0 {
		return errors.New("queue length must be positive")
	}
	if config.ttl < 0 || config.ttl > 255 {
		return errors.New("TTL must be between 0 and 255")
	}
	if config.icmpBurst < 0 {
		return errors.New("ICMP burst must not be negative")
	}
	if config.icmpLimit < 0 {
		return errors.New("ICMP limit must not be negative")
	}
	if config.tcpKeepAliveIdle < 0 || config.tcpKeepAliveInterval < 0 || config.tcpKeepAliveCount < 0 {
		return errors.New("TCP keepalive values must not be negative")
	}
	return nil
}

func (n *spooferNode) rebuildStack() {
	n.stopStack()
	if n.tun == nil || n.networkValue == nil {
		_ = n.updateLabel()
		return
	}
	ctx := context.Background()
	if n.engine != nil && n.engine.ctx != nil {
		ctx = n.engine.ctx
	}
	ctx, cancel := context.WithCancel(ctx)
	stackTun := tun.Detach(n.tun)
	BindTunResource(n.w, n.id, 0, stackTun)
	network := n.networkValue

	opts := &netstackspoofer.Opts{
		OnTCPConn: func(c net.Conn, ep stack.TransportEndpointID) {
			forwardSpooferTCP(ctx, network, c, ep)
		},
		OnUDPConn: func(c gonnect.PacketConn, ep stack.TransportEndpointID) {
			forwardSpooferUDP(ctx, network, c, ep)
		},
		TCPSendBufferSize:    n.config.tcpSendBufferSize,
		TCPReceiveBufferSize: n.config.tcpReceiveBufferSize,
		TTL:                  n.config.ttl,
		ICMPBurst:            n.config.icmpBurst,
		ICMPLimit:            n.config.icmpLimit,
		CongestionControlAlg: n.config.congestionControlAlg,
		DisableNagle:         n.config.disableNagle,
		DisableTCPModRecBuff: n.config.disableTCPModRecBuff,
		TCPKeepAlive:         n.config.tcpKeepAlive,
		TCPKeepAliveIdle:     time.Duration(n.config.tcpKeepAliveIdle) * time.Second,
		TCPKeepaliveInterval: time.Duration(n.config.tcpKeepAliveInterval) * time.Second,
		TCPKeepaliveCount:    n.config.tcpKeepAliveCount,
		TCPForwardWnd:        n.config.tcpForwardWnd,
		TCPForwardAttempts:   n.config.tcpForwardAttempts,
		NetStackOpts: &netstackhelpers.Opts{
			MTU:         n.config.mtu,
			DisableSACK: n.config.disableSACK,
		},
	}
	opts.WithTunEndpoint(stackTun, n.config.queueLength)
	st, err := opts.Launch()
	if err != nil {
		cancel()
		if opts.Endpoint != nil {
			closeLinkEndpoint(opts.Endpoint)
			waitLinkEndpoint(opts.Endpoint)
		} else {
			_ = stackTun.Close()
		}
		n.formError = err.Error()
		n.sendForcedMenuSnapshot()
		_ = n.updateLabel()
		return
	}
	n.formError = ""
	n.runtime = &spooferRuntime{
		ctx:      ctx,
		cancel:   cancel,
		stack:    st,
		endpoint: opts.Endpoint,
		tun:      stackTun,
	}
	_ = n.updateLabel()
}

func (n *spooferNode) stopStack() {
	rt := n.runtime
	if rt == nil {
		return
	}
	n.runtime = nil
	rt.cancel()
	if rt.stack != nil {
		rt.stack.Close()
	}
	if rt.endpoint != nil {
		closeLinkEndpoint(rt.endpoint)
		waitLinkEndpoint(rt.endpoint)
	} else if rt.tun != nil {
		_ = rt.tun.Close()
	}
}

func closeLinkEndpoint(endpoint stack.LinkEndpoint) {
	if closer, ok := endpoint.(interface{ Close() }); ok {
		closer.Close()
	}
}

func waitLinkEndpoint(endpoint stack.LinkEndpoint) {
	if waiter, ok := endpoint.(interface{ Wait() }); ok {
		waiter.Wait()
	}
}

func forwardSpooferTCP(ctx context.Context, network NetworkCloser, c net.Conn, ep stack.TransportEndpointID) {
	target, ok := spooferEndpointAddress(ep)
	if !ok || network == nil {
		_ = c.Close()
		return
	}
	upstream, err := network.Dial(ctx, "tcp", target)
	if err != nil {
		_ = c.Close()
		return
	}
	closeBoth := sync.OnceFunc(func() {
		_ = c.Close()
		_ = upstream.Close()
	})
	go func() {
		_, _ = io.Copy(upstream, c)
		closeBoth()
	}()
	go func() {
		_, _ = io.Copy(c, upstream)
		closeBoth()
	}()
}

func forwardSpooferUDP(ctx context.Context, network NetworkCloser, c gonnect.PacketConn, ep stack.TransportEndpointID) {
	target, ok := spooferEndpointUDPAddr(ep)
	if !ok || network == nil {
		_ = c.Close()
		return
	}
	upstream, err := network.ListenPacket(ctx, "udp", "")
	if err != nil {
		_ = c.Close()
		return
	}
	closeBoth := sync.OnceFunc(func() {
		_ = c.Close()
		_ = upstream.Close()
	})
	go func() {
		defer closeBoth()
		buf := make([]byte, 64*1024)
		for {
			n, _, err := c.ReadFrom(buf)
			if err != nil {
				return
			}
			if _, err := upstream.WriteTo(buf[:n], target); err != nil {
				return
			}
		}
	}()
	go func() {
		defer closeBoth()
		buf := make([]byte, 64*1024)
		for {
			n, _, err := upstream.ReadFrom(buf)
			if err != nil {
				return
			}
			if _, err := c.Write(buf[:n]); err != nil {
				return
			}
		}
	}()
}

func spooferEndpointAddress(ep stack.TransportEndpointID) (string, bool) {
	addr, ok := spooferEndpointAddr(ep)
	if !ok {
		return "", false
	}
	return net.JoinHostPort(addr.String(), strconv.Itoa(int(ep.LocalPort))), true
}

func spooferEndpointUDPAddr(ep stack.TransportEndpointID) (*net.UDPAddr, bool) {
	addr, ok := spooferEndpointAddr(ep)
	if !ok {
		return nil, false
	}
	return &net.UDPAddr{IP: net.IP(addr.AsSlice()), Port: int(ep.LocalPort)}, true
}

func spooferEndpointAddr(ep stack.TransportEndpointID) (netip.Addr, bool) {
	addr, ok := netip.AddrFromSlice(ep.LocalAddress.AsSlice())
	if !ok || !addr.IsValid() || ep.LocalPort == 0 {
		return netip.Addr{}, false
	}
	return addr, true
}

func (n *spooferNode) requestPort(port uint64) {
	snapshot, ok := n.w.PortSnapshot(port)
	if !ok {
		return
	}
	for _, link := range snapshot.Links {
		std.Request(n.w, n.id, link)
	}
}

func (n *spooferNode) updateLabel() error {
	if n.formError != "" {
		return n.w.SetNodeLabel(n.id, "invalid config")
	}
	if n.tun == nil {
		return n.w.SetNodeLabel(n.id, "waiting for tun")
	}
	if n.networkValue == nil {
		return n.w.SetNodeLabel(n.id, "waiting for network")
	}
	return n.w.SetNodeLabel(n.id, "forwarding")
}

func (n *spooferNode) sendMenuSnapshot() {
	n.w.SendNodeMenuMsg(n.id, formular.MenuSnapshotMessage{
		MessageBase: formular.MessageBase{Type: formular.MessageMenuSnapshot, MenuID: pasta.NodeMenuID(n.id), MenuGeneration: 1},
		Blocks:      []formular.Block{n.configBlock()},
	})
}

func (n *spooferNode) sendForcedMenuSnapshot() {
	if n.w == nil || n.id == 0 {
		return
	}
	n.w.SendNodeMenuMsg(n.id, formular.MenuSnapshotMessage{
		MessageBase: formular.MessageBase{Type: formular.MessageMenuSnapshot, MenuID: pasta.NodeMenuID(n.id), MenuGeneration: 1},
		Force:       true,
		Blocks:      []formular.Block{n.configBlock()},
	})
}

func (n *spooferNode) configBlock() formular.Block {
	statusErr := n.formError != ""
	return formular.Block{ID: "config", Order: 10, Generation: 1, Form: true, Items: []formular.Item{
		configIntField("mtu", "MTU", n.config.mtu, statusErr, n.formError),
		configIntField("queueLength", "Queue length", n.config.queueLength, statusErr, n.formError),
		configCheckbox("disableSACK", "Disable TCP SACK", n.config.disableSACK),
		configIntField("ttl", "TTL", n.config.ttl, statusErr, n.formError),
		configIntField("icmpBurst", "ICMP burst", n.config.icmpBurst, statusErr, n.formError),
		configFloatField("icmpLimit", "ICMP limit", n.config.icmpLimit, statusErr, n.formError),
		configIntField("tcpSendBufferSize", "TCP send buffer", n.config.tcpSendBufferSize, statusErr, n.formError),
		configIntField("tcpReceiveBufferSize", "TCP receive buffer", n.config.tcpReceiveBufferSize, statusErr, n.formError),
		configTextField("congestionControlAlg", "TCP congestion", n.config.congestionControlAlg, statusErr, n.formError, false),
		configCheckbox("disableNagle", "Disable Nagle", n.config.disableNagle),
		configCheckbox("disableTCPModRecBuff", "Disable TCP receive autotune", n.config.disableTCPModRecBuff),
		configCheckbox("tcpKeepAlive", "TCP keepalive", n.config.tcpKeepAlive),
		configIntField("tcpKeepAliveIdle", "TCP keepalive idle", n.config.tcpKeepAliveIdle, statusErr, n.formError),
		configIntField("tcpKeepAliveInterval", "TCP keepalive interval", n.config.tcpKeepAliveInterval, statusErr, n.formError),
		configIntField("tcpKeepAliveCount", "TCP keepalive count", n.config.tcpKeepAliveCount, statusErr, n.formError),
		configIntField("tcpForwardWnd", "TCP forward window", n.config.tcpForwardWnd, statusErr, n.formError),
		configIntField("tcpForwardAttempts", "TCP forward attempts", n.config.tcpForwardAttempts, statusErr, n.formError),
	}}
}

func (n *spooferNode) String() string {
	return fmt.Sprintf("spooferNode(%d)", n.id)
}
