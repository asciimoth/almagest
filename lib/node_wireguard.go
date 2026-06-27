package almagest

import (
	"crypto/rand"
	_ "embed"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"time"

	conn "github.com/asciimoth/batchudp"
	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/formular"
	"github.com/asciimoth/gonnect/tun"
	"github.com/asciimoth/pasta/pasta"
	"github.com/asciimoth/pasta/pasta/std"
	wgdevice "github.com/asciimoth/wgo/device"
	"golang.org/x/crypto/curve25519"
)

func init() {
	regClass(func(e *engine) pasta.NodeClass {
		return wireGuardClass{engine: e}
	}, false)
}

const (
	// NodeTypeWireGuard is the node class name for the embedded WireGuard
	// device wrapper.
	NodeTypeWireGuard = "almagest/WireGuard"
)

type wireGuardClass struct {
	engine *engine
}

var (
	//go:embed node_wireguard.md
	wireGuardClassLongDescription string
)

func (wireGuardClass) ClassName() string        { return NodeTypeWireGuard }
func (wireGuardClass) ShortDescription() string { return "WireGuard" }
func (wireGuardClass) LongDescription() string  { return wireGuardClassLongDescription }
func (wireGuardClass) DefaultNodeParams() pasta.NodeClassParams {
	return pasta.NodeClassParams{PrimaryType: TypeTun, InitialPorts: []pasta.Port{
		TunPort("left", "Tun"),
		NetworkPort("right", "Network"),
	}}
}
func (c wireGuardClass) NewNode(cfg configer.Config, _ ...*pasta.NodeClassState) (pasta.Node, error) {
	config := defaultWireGuardConfig()
	config.read(cfg)
	if err := config.ensurePrivateKey(); err != nil {
		return nil, err
	}
	return &wireGuardNode{engine: c.engine, config: config}, nil
}

type wireGuardConfig struct {
	privateKey string
	listenPort int
	ownedIPs   string
	peers      []wireGuardPeerConfig
}

type wireGuardPeerConfig struct {
	id                          string
	publicKey                   string
	presharedKey                string
	endpoint                    string
	allowedIPs                  string
	persistentKeepaliveInterval int
}

func defaultWireGuardConfig() wireGuardConfig {
	return wireGuardConfig{}
}

func (c *wireGuardConfig) read(cfg configer.Config) {
	if cfg == nil {
		return
	}
	c.privateKey = readConfigString(cfg, "privateKey", c.privateKey)
	c.listenPort = readConfigInt(cfg, "listenPort", c.listenPort)
	c.ownedIPs = readConfigString(cfg, "ownedIPs", c.ownedIPs)
	if peers, ok := readWireGuardPeers(cfg); ok {
		c.peers = peers
	}
	c.normalize()
}

func (c *wireGuardConfig) normalize() {
	c.privateKey = normalizeHexKey(c.privateKey)
	if c.listenPort < 0 || c.listenPort > 65535 {
		c.listenPort = 0
	}
	c.ownedIPs = normalizeListText(c.ownedIPs)
	for i := range c.peers {
		c.peers[i].normalize(i)
	}
	c.peers = compactWireGuardPeers(c.peers)
}

func (c *wireGuardConfig) ensurePrivateKey() error {
	if c.privateKey != "" {
		return nil
	}
	key, err := generateWireGuardPrivateKeyHex()
	if err != nil {
		return fmt.Errorf("generate private key: %w", err)
	}
	c.privateKey = key
	return nil
}

func (p *wireGuardPeerConfig) normalize(index int) {
	p.publicKey = normalizeHexKey(p.publicKey)
	p.presharedKey = normalizeHexKey(p.presharedKey)
	p.endpoint = strings.TrimSpace(p.endpoint)
	p.allowedIPs = normalizeListText(p.allowedIPs)
	if p.persistentKeepaliveInterval < 0 || p.persistentKeepaliveInterval > 65535 {
		p.persistentKeepaliveInterval = 0
	}
	p.id = strings.TrimSpace(p.id)
	if p.id == "" {
		if p.publicKey != "" {
			p.id = "peer-" + shortWireGuardKey(p.publicKey)
		} else {
			p.id = fmt.Sprintf("peer-%d", index+1)
		}
	}
}

// wireGuardNode owns exactly one wgo Device. Link attach/detach operations
// replace only the live TUN or UDP bind attachments, preserving configured
// peers and device identity across graph rewiring.
type wireGuardNode struct {
	pasta.BasicNode

	engine *engine
	logger pasta.Logger

	w    *pasta.Workspace
	id   uint64
	tunp uint64
	netp uint64

	tunLink uint64
	netLink uint64

	currentTun     tun.Tun
	currentNetwork NetworkCloser
	currentBind    conn.Bind

	config wireGuardConfig
	dev    *wgdevice.Device

	formError    string
	runtimeError string

	retryMu         sync.Mutex
	retryGeneration uint64
	retryDelay      time.Duration
	retryCancel     chan struct{}
}

// wireGuardPortBind intentionally hides batchudp's concrete StdNetBind type
// from wgo. The underlying bind still uses the Network received via this node's
// almagest/network port, while wgo's Linux sticky-socket netlink path only
// activates for the concrete *batchudp.StdNetBind type.
type wireGuardPortBind struct {
	conn.Bind

	mu             sync.Mutex
	generation     uint64
	onReceiveError func(bind *wireGuardPortBind, generation uint64, err error)
}

type wireGuardRetryMessage struct {
	generation uint64
}

type wireGuardBindReceiveErrorMessage struct {
	bind       *wireGuardPortBind
	generation uint64
	err        error
}

const (
	wireGuardConfigRetryInitialDelay = 100 * time.Millisecond
	wireGuardConfigRetryMaxDelay     = 10 * time.Second
)

func (n *wireGuardNode) OnInit(w *pasta.Workspace, logger pasta.Logger, id uint64, _ string, restored *pasta.NodeInitData, _, _, _, _ bool) error {
	n.w = w
	n.id = id
	n.logger = logger
	n.config.normalize()
	if restored != nil {
		if len(restored.LeftPorts) > 0 {
			n.tunp = restored.LeftPorts[0]
		}
		if len(restored.RightPorts) > 0 {
			n.netp = restored.RightPorts[0]
		}
	}
	if err := n.w.SetNodePrimary(n.id, TypeTun); err != nil {
		return err
	}
	n.ensureDevice()
	if err := n.applyConfigToDevice(true); err != nil {
		n.setRuntimeError(err)
	}
	_ = n.updateLabel()
	n.sendMenuSnapshot()
	return nil
}

func (n *wireGuardNode) OnReady() error {
	n.sendTunConfig()
	n.requestPort(n.tunp)
	n.requestPort(n.netp)
	return nil
}

func (n *wireGuardNode) OnStop() {
	n.cancelConfigRetry()
	if n.dev != nil {
		n.dev.Close()
		n.dev = nil
	}
	n.currentTun = nil
	n.currentNetwork = nil
	n.currentBind = nil
}

func (n *wireGuardNode) PreLinkAdd(port uint64, linkType, portDirection string) error {
	switch port {
	case n.tunp:
		if portDirection != "left" {
			return pasta.LinkTypeErr(linkType)
		}
		return TunPreLinkAdd(n.w, port, linkType)
	case n.netp:
		if portDirection != "right" || linkType != TypeNetwork {
			return pasta.LinkTypeErr(linkType)
		}
		if portHasLinks(n.w, port) {
			return pasta.ErrLinkDup
		}
		return nil
	default:
		return pasta.LinkTypeErr(linkType)
	}
}

func (n *wireGuardNode) OnLinkAdd(link, port uint64, linkType, portDirection string) error {
	switch {
	case port == n.tunp && portDirection == "left" && linkType == TypeTun:
		n.tunLink = link
		n.sendTunConfigToLink(link)
		std.Request(n.w, n.id, link)
	case port == n.netp && portDirection == "right" && linkType == TypeNetwork:
		n.netLink = link
		std.Request(n.w, n.id, link)
	}
	_ = n.updateLabel()
	n.sendStatusBlock()
	return nil
}

func (n *wireGuardNode) OnLinkRemoved(link uint64, port uint64, linkType, portDirection string) error {
	switch {
	case port == n.tunp && portDirection == "left" && linkType == TypeTun && link == n.tunLink:
		n.cancelConfigRetry()
		n.tunLink = 0
		n.currentTun = nil
		n.detachDevicePart("TUN", func() error { return n.dev.DetachTUN() })
	case port == n.netp && portDirection == "right" && linkType == TypeNetwork && link == n.netLink:
		n.cancelConfigRetry()
		n.netLink = 0
		n.currentNetwork = nil
		n.currentBind = nil
		n.detachDevicePart("network bind", func() error { return n.dev.DetachBind() })
	}
	_ = n.updateLabel()
	n.sendStatusBlock()
	return nil
}

func (n *wireGuardNode) detachDevicePart(name string, detach func() error) {
	if n.dev == nil {
		return
	}
	var detachErr error
	if err := n.dev.Down(); err != nil {
		detachErr = fmt.Errorf("bring device down before detaching %s: %w", name, err)
		n.setRuntimeError(detachErr)
	}
	if err := detach(); err != nil {
		detachErr = fmt.Errorf("detach %s: %w", name, err)
		n.setRuntimeError(detachErr)
	}
	if detachErr == nil {
		n.clearRuntimeError()
	}
}

func (n *wireGuardNode) OnEvent(event pasta.Event, linkType string, _ []string, receiverPortDirection string) error {
	if event.ReceiverPort == n.tunp && receiverPortDirection == "left" && linkType == TypeTun {
		if std.IsRequest(event.Payload) {
			n.sendTunConfigToLink(event.Link)
			return nil
		}
		payload, ok := event.Payload.(TunPayload)
		if !ok || payload.Tun == nil || event.Link != n.tunLink {
			return nil
		}
		if !TunIsReady(payload.Tun) {
			_ = payload.Tun.Close()
			return nil
		}
		BindTunResource(n.w, n.id, event.Link, payload.Tun)
		n.attachTun(payload.Tun)
		return nil
	}
	if event.ReceiverPort == n.netp && receiverPortDirection == "right" && linkType == TypeNetwork {
		if std.IsRequest(event.Payload) {
			return nil
		}
		payload, ok := event.Payload.(NetworkPayload)
		if !ok || payload.Network == nil || event.Link != n.netLink {
			return nil
		}
		if !NetworkIsReady(payload.Network) {
			_ = payload.Network.Close()
			return nil
		}
		if NetworkIsReady(n.currentNetwork) && SameNetworkSource(n.currentNetwork, payload.Network) {
			if !SameNetworkInstance(n.currentNetwork, payload.Network) {
				_ = payload.Network.Close()
			}
			return nil
		}
		n.attachNetwork(event.Link, payload.Network)
		return nil
	}
	return nil
}

func (n *wireGuardNode) OnFormularMsg(message any) error {
	switch msg := message.(type) {
	case wireGuardRetryMessage:
		n.retryConfigToDevice(msg.generation)
		return nil
	case wireGuardBindReceiveErrorMessage:
		n.handleBindReceiveError(msg)
		return nil
	case formular.FormApplyMessage:
		if msg.MenuID != pasta.NodeMenuID(n.id) {
			return nil
		}
		switch msg.BlockID {
		case "config":
			return n.applyConfigForm(msg.Values)
		case "peers":
			return n.applyPeersForm(msg.Values)
		}
	case formular.FieldValidateMessage:
		if msg.MenuID != pasta.NodeMenuID(n.id) {
			return nil
		}
		switch msg.Field.BlockID {
		case "config":
			n.sendConfigFieldStatus(msg)
		case "peers":
			n.sendPeersFieldStatus(msg)
		}
	}
	return nil
}

func (n *wireGuardNode) OnSave(cfg configer.Config) error {
	if err := pasta.DeleteNodeOwnedConfigKeys(cfg); err != nil {
		return err
	}
	values := map[string]any{
		"privateKey": n.config.privateKey,
		"listenPort": n.config.listenPort,
		"ownedIPs":   n.config.ownedIPs,
		"peers":      wireGuardPeersConfig(n.config.peers),
	}
	for key, value := range values {
		if err := cfg.Set(configer.Path{key}, value); err != nil {
			return err
		}
	}
	return nil
}

func (n *wireGuardNode) ensureDevice() {
	if n.dev != nil {
		return
	}
	var logger wgdevice.Logger = wgdevice.NopLogger{}
	if n.logger != nil {
		logger = n.logger
	}
	n.dev = wgdevice.NewDevice(nil, nil, logger)
}

func (n *wireGuardNode) applyConfigForm(values map[string]any) error {
	next := n.config
	if value, ok := stringFromAny(values["privateKey"]); ok {
		next.privateKey = value
	}
	if value, ok := intFromAny(values["listenPort"]); ok {
		next.listenPort = value
	}
	if value, ok := stringFromAny(values["ownedIPs"]); ok {
		next.ownedIPs = value
	}
	return n.commitConfigForm(next, true, true)
}

func (n *wireGuardNode) applyPeersForm(values map[string]any) error {
	next := n.config
	if peers, ok := parseWireGuardPeers(values["peers"]); ok {
		next.peers = peers
	}
	return n.commitConfigForm(next, true, false)
}

func (n *wireGuardNode) commitConfigForm(next wireGuardConfig, rebuildPeers, emitTunConfig bool) error {
	next.normalize()
	if err := next.ensurePrivateKey(); err != nil {
		n.rejectConfigForm(err)
		return nil
	}
	if err := validateWireGuardConfig(next); err != nil {
		n.rejectConfigForm(err)
		return nil
	}
	n.config = next
	n.formError = ""
	n.cancelConfigRetry()
	_ = n.applyConfigToDeviceWithRetry(rebuildPeers)
	if emitTunConfig {
		n.sendTunConfig()
	}
	n.sendForcedMenuSnapshot()
	_ = n.updateLabel()
	return nil
}

func (n *wireGuardNode) rejectConfigForm(err error) {
	n.formError = err.Error()
	n.sendForcedMenuSnapshot()
	_ = n.updateLabel()
}

func (n *wireGuardNode) applyConfigToDeviceWithRetry(rebuildPeers bool) error {
	if err := n.applyConfigToDevice(rebuildPeers); err != nil {
		n.failRuntime(err, n.hasActiveAttachments())
		return err
	}
	n.cancelConfigRetry()
	n.clearRuntimeError()
	return nil
}

func (n *wireGuardNode) applyConfigToDevice(rebuildPeers bool) error {
	n.ensureDevice()
	if err := validateWireGuardConfig(n.config); err != nil {
		n.formError = err.Error()
		return err
	}
	active := n.hasActiveAttachments()
	if !active {
		if err := n.dev.Down(); err != nil {
			return err
		}
	}
	privateKey, err := parseWireGuardPrivateKey(n.config.privateKey)
	if err != nil {
		return fmt.Errorf("private key: %w", err)
	}
	if err := n.dev.SetPrivateKey(privateKey); err != nil {
		return fmt.Errorf("set private key: %w", err)
	}
	if rebuildPeers {
		if err := n.rebuildPeers(); err != nil {
			return err
		}
	}
	if !active {
		return nil
	}
	if err := n.dev.SetListenPort(uint16(n.config.listenPort)); err != nil {
		return fmt.Errorf("set listen port: %w", err)
	}
	if err := n.applyPeerEndpoints(); err != nil {
		return err
	}
	if err := n.dev.Up(); err != nil {
		return fmt.Errorf("bring device up: %w", err)
	}
	if err := n.activatePeers(); err != nil {
		return err
	}
	return nil
}

func (n *wireGuardNode) hasActiveAttachments() bool {
	return n.currentTun != nil && n.currentBind != nil
}

func (n *wireGuardNode) activatePeers() error {
	for _, peer := range n.config.peers {
		publicKey, err := parseWireGuardPublicKey(peer.publicKey)
		if err != nil {
			return fmt.Errorf("peer %s public key: %w", peerLabel(peer), err)
		}
		if err := n.dev.ActivatePeer(publicKey); err != nil {
			return fmt.Errorf("activate peer %s: %w", peerLabel(peer), err)
		}
	}
	return nil
}

func (n *wireGuardNode) rebuildPeers() error {
	n.dev.RemoveAllPeers()
	for _, peer := range n.config.peers {
		if peer.publicKey == "" {
			continue
		}
		publicKey, err := parseWireGuardPublicKey(peer.publicKey)
		if err != nil {
			return fmt.Errorf("peer %s public key: %w", peerLabel(peer), err)
		}
		if _, err := n.dev.NewPeer(publicKey); err != nil {
			return fmt.Errorf("create peer %s: %w", peerLabel(peer), err)
		}
		if err := n.dev.SetPeerProtocolVersion(publicKey, 1); err != nil {
			return fmt.Errorf("set peer %s protocol: %w", peerLabel(peer), err)
		}
		presharedKey, err := parseWireGuardPresharedKey(peer.presharedKey)
		if err != nil {
			return fmt.Errorf("peer %s preshared key: %w", peerLabel(peer), err)
		}
		if err := n.dev.SetPeerPresharedKey(publicKey, presharedKey); err != nil {
			return fmt.Errorf("set peer %s preshared key: %w", peerLabel(peer), err)
		}
		allowedIPs, err := parseWireGuardAllowedIPs(peer.allowedIPs)
		if err != nil {
			return fmt.Errorf("peer %s allowed IPs: %w", peerLabel(peer), err)
		}
		if err := n.dev.ReplacePeerAllowedIPs(publicKey, allowedIPs); err != nil {
			return fmt.Errorf("set peer %s allowed IPs: %w", peerLabel(peer), err)
		}
		if err := n.dev.SetPeerPersistentKeepaliveInterval(publicKey, uint16(peer.persistentKeepaliveInterval)); err != nil {
			return fmt.Errorf("set peer %s keepalive: %w", peerLabel(peer), err)
		}
	}
	return nil
}

func (n *wireGuardNode) applyPeerEndpoints() error {
	for _, peer := range n.config.peers {
		if peer.publicKey == "" || peer.endpoint == "" {
			continue
		}
		publicKey, err := parseWireGuardPublicKey(peer.publicKey)
		if err != nil {
			return fmt.Errorf("peer %s public key: %w", peerLabel(peer), err)
		}
		if err := n.dev.SetPeerEndpoint(publicKey, peer.endpoint); err != nil {
			return fmt.Errorf("set peer %s endpoint: %w", peerLabel(peer), err)
		}
	}
	return nil
}

func (n *wireGuardNode) attachTun(t tun.Tun) {
	n.ensureDevice()
	if SameComparableValue(n.currentTun, t) {
		return
	}
	n.cancelConfigRetry()
	var err error
	if n.currentTun == nil {
		err = n.dev.AttachTUN(t)
	} else {
		err = n.dev.ReplaceTUN(t)
	}
	if err != nil {
		_ = t.Close()
		n.setRuntimeError(fmt.Errorf("attach TUN: %w", err))
		return
	}
	n.currentTun = t
	if n.currentBind != nil {
		if err := n.applyConfigToDeviceWithRetry(false); err != nil {
			return
		}
	}
	n.clearRuntimeError()
	_ = n.updateLabel()
	n.sendStatusBlock()
}

func (n *wireGuardNode) attachNetwork(link uint64, network NetworkCloser) {
	n.ensureDevice()
	n.cancelConfigRetry()
	n.currentNetwork = network
	BindNetworkResource(n.w, n.id, link, network)
	_ = n.attachCurrentNetworkBindWithRetry()
	_ = n.updateLabel()
	n.sendStatusBlock()
}

func (n *wireGuardNode) attachCurrentNetworkBindWithRetry() error {
	if err := n.attachCurrentNetworkBind(); err != nil {
		n.failRuntime(err, n.currentNetwork != nil)
		return err
	}
	return nil
}

func (n *wireGuardNode) attachCurrentNetworkBind() error {
	if n.currentNetwork == nil {
		return errors.New("network is detached")
	}
	networkBind := conn.NewDefaultBind(n.currentNetwork)
	if !n.currentNetwork.IsNative() {
		networkBind = conn.NewDefaultBindWithOptions(n.currentNetwork, conn.StdNetBindOptions{
			AllowSingleFamily: true,
		})
	}
	bind := &wireGuardPortBind{Bind: networkBind}
	bind.onReceiveError = n.notifyBindReceiveError
	var err error
	if n.currentBind == nil {
		err = n.dev.AttachBind(bind)
	} else {
		err = n.dev.ReplaceBind(bind)
	}
	if err != nil {
		_ = n.dev.DetachBind()
		_ = bind.Close()
		n.currentBind = nil
		return fmt.Errorf("attach network: %w", err)
	}
	n.currentBind = bind
	return n.applyConfigToDeviceWithRetry(false)
}

func (n *wireGuardNode) notifyBindReceiveError(bind *wireGuardPortBind, generation uint64, err error) {
	if n.w == nil || n.id == 0 {
		return
	}
	go n.w.SendNodeFormularMsg(n.id, wireGuardBindReceiveErrorMessage{
		bind:       bind,
		generation: generation,
		err:        err,
	})
}

func (n *wireGuardNode) handleBindReceiveError(msg wireGuardBindReceiveErrorMessage) {
	if msg.bind == nil || msg.err == nil || n.currentBind != msg.bind || !msg.bind.isCurrentGeneration(msg.generation) {
		return
	}
	n.failRuntime(fmt.Errorf("receive from bind: %w", msg.err), n.hasActiveAttachments())
	_ = n.updateLabel()
	n.sendStatusBlock()
}

func (n *wireGuardNode) retryConfigToDevice(generation uint64) {
	if !n.isCurrentConfigRetry(generation) {
		return
	}
	if n.currentBind == nil && n.currentNetwork != nil {
		if err := n.attachCurrentNetworkBind(); err != nil {
			if n.isCurrentConfigRetry(generation) && n.currentNetwork != nil {
				n.failRuntime(err, true)
			} else {
				n.setRuntimeError(err)
			}
			return
		}
		return
	}
	if err := n.applyConfigToDevice(false); err != nil {
		if n.isCurrentConfigRetry(generation) && n.hasActiveAttachments() {
			n.failRuntime(err, true)
		} else {
			n.setRuntimeError(err)
		}
		return
	}
	n.cancelConfigRetry()
	n.clearRuntimeError()
}

func (n *wireGuardNode) failRuntime(err error, retry bool) {
	n.setRuntimeError(err)
	if retry {
		n.scheduleConfigRetry()
	}
}

func (n *wireGuardNode) scheduleConfigRetry() {
	if n.w == nil || n.id == 0 {
		return
	}

	n.retryMu.Lock()
	if n.retryCancel != nil {
		n.retryMu.Unlock()
		return
	}
	delay := n.retryDelay
	if delay <= 0 {
		delay = wireGuardConfigRetryInitialDelay
	}
	nextDelay := delay * 2
	if nextDelay > wireGuardConfigRetryMaxDelay {
		nextDelay = wireGuardConfigRetryMaxDelay
	}
	n.retryDelay = nextDelay
	generation := n.retryGeneration
	cancel := make(chan struct{})
	n.retryCancel = cancel
	n.retryMu.Unlock()

	go func() {
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-cancel:
			return
		}

		n.retryMu.Lock()
		if n.retryCancel != cancel {
			n.retryMu.Unlock()
			return
		}
		n.retryCancel = nil
		n.retryMu.Unlock()

		n.w.SendNodeFormularMsg(n.id, wireGuardRetryMessage{generation: generation})
	}()
}

func (n *wireGuardNode) cancelConfigRetry() {
	n.retryMu.Lock()
	n.retryGeneration++
	if n.retryCancel != nil {
		close(n.retryCancel)
		n.retryCancel = nil
	}
	n.retryDelay = wireGuardConfigRetryInitialDelay
	n.retryMu.Unlock()
}

func (n *wireGuardNode) isCurrentConfigRetry(generation uint64) bool {
	n.retryMu.Lock()
	defer n.retryMu.Unlock()
	return n.retryGeneration == generation
}

func (b *wireGuardPortBind) Open(port uint16) ([]conn.ReceiveFunc, uint16, error) {
	fns, actualPort, err := b.Bind.Open(port)
	if err != nil {
		return nil, 0, err
	}

	b.mu.Lock()
	b.generation++
	generation := b.generation
	b.mu.Unlock()

	if b.onReceiveError == nil {
		return fns, actualPort, nil
	}

	wrapped := make([]conn.ReceiveFunc, len(fns))
	for i, fn := range fns {
		wrapped[i] = b.wrapReceiveFunc(generation, fn)
	}
	return wrapped, actualPort, nil
}

func (b *wireGuardPortBind) wrapReceiveFunc(generation uint64, fn conn.ReceiveFunc) conn.ReceiveFunc {
	return func(packets [][]byte, sizes []int, eps []conn.Endpoint) (int, error) {
		n, err := fn(packets, sizes, eps)
		if wireGuardReceiveErrorShouldRetry(err) {
			b.onReceiveError(b, generation, err)
		}
		return n, err
	}
}

func (b *wireGuardPortBind) isCurrentGeneration(generation uint64) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.generation == generation
}

func wireGuardReceiveErrorShouldRetry(err error) bool {
	if err == nil || errors.Is(err, net.ErrClosed) {
		return false
	}
	if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
		return false
	}
	return true
}

func (n *wireGuardNode) requestPort(port uint64) {
	snapshot, ok := n.w.PortSnapshot(port)
	if !ok {
		return
	}
	for _, link := range snapshot.Links {
		std.Request(n.w, n.id, link)
	}
}

// sendTunConfig publishes this WireGuard interface's owned IP prefixes to the
// attached TUN provider. TypeTun defines TunConfig as flowing opposite the TUN
// payload, so this left-directed consumer sends suggestions upstream to the
// right-directed provider whenever the link or local WireGuard address set
// changes.
func (n *wireGuardNode) sendTunConfig() {
	if n.tunLink == 0 {
		return
	}
	n.sendTunConfigToLink(n.tunLink)
}

func (n *wireGuardNode) sendTunConfigToLink(link uint64) {
	if n.w == nil || n.id == 0 || link == 0 || link != n.tunLink {
		return
	}
	n.w.EmitEvent(n.id, link, n.tunConfig())
}

func (n *wireGuardNode) tunConfig() TunConfig {
	return TunConfig{Addrs: wireGuardOwnedIPAddrs(n.config.ownedIPs)}
}

func (n *wireGuardNode) updateLabel() error {
	return n.w.SetNodeLabel(n.id, n.stateText())
}

func (n *wireGuardNode) stateText() string {
	switch {
	case n.formError != "":
		return "invalid config"
	case n.runtimeError != "":
		return "error"
	case n.config.privateKey == "":
		return "missing private key"
	case n.currentTun == nil && n.currentNetwork == nil:
		return "waiting for TUN and network"
	case n.currentTun == nil:
		return "waiting for TUN"
	case n.currentNetwork == nil:
		return "waiting for network"
	default:
		return "up"
	}
}

func (n *wireGuardNode) setRuntimeError(err error) {
	if err == nil {
		n.clearRuntimeError()
		return
	}
	text := err.Error()
	if n.runtimeError == text {
		return
	}
	n.runtimeError = text
	if n.logger != nil {
		n.logger.Errf("WireGuard node error: %v", err)
	}
	_ = n.updateLabel()
	n.sendStatusBlock()
}

func (n *wireGuardNode) clearRuntimeError() {
	if n.runtimeError == "" {
		return
	}
	n.runtimeError = ""
	_ = n.updateLabel()
	n.sendStatusBlock()
}

func (n *wireGuardNode) sendMenuSnapshot() {
	n.w.SendNodeMenuMsg(n.id, formular.MenuSnapshotMessage{
		MessageBase: formular.MessageBase{Type: formular.MessageMenuSnapshot, MenuID: pasta.NodeMenuID(n.id), MenuGeneration: 1},
		Blocks:      n.menuBlocks(),
	})
}

func (n *wireGuardNode) sendForcedMenuSnapshot() {
	if n.w == nil || n.id == 0 {
		return
	}
	n.w.SendNodeMenuMsg(n.id, formular.MenuSnapshotMessage{
		MessageBase: formular.MessageBase{Type: formular.MessageMenuSnapshot, MenuID: pasta.NodeMenuID(n.id), MenuGeneration: 1},
		Force:       true,
		Blocks:      n.menuBlocks(),
	})
}

func (n *wireGuardNode) menuBlocks() []formular.Block {
	return []formular.Block{n.statusBlock(), n.configBlock(), n.peersBlock()}
}

func (n *wireGuardNode) sendStatusBlock() {
	if n.w == nil || n.id == 0 {
		return
	}
	n.w.SendNodeMenuMsg(n.id, formular.BlockSnapshotMessage{
		MessageBase: formular.MessageBase{Type: formular.MessageBlockSnapshot, MenuID: pasta.NodeMenuID(n.id), MenuGeneration: 1, BlockGeneration: 1},
		Block:       n.statusBlock(),
	})
}

func (n *wireGuardNode) statusBlock() formular.Block {
	publicKey := ""
	if n.config.privateKey != "" {
		if key, err := parseWireGuardPrivateKey(n.config.privateKey); err == nil && !key.IsZero() {
			publicKey = wireGuardPublicKeyHex(key)
		}
	}
	listenPort := n.config.listenPort
	if n.dev != nil && n.hasActiveAttachments() {
		listenPort = int(n.dev.ListenPort())
	}
	statusErr := n.formError
	if statusErr == "" {
		statusErr = n.runtimeError
	}
	return formular.Block{ID: "status", Order: 10, Generation: 1, Items: []formular.Item{
		readonlyTextField("state", "State", n.stateText()),
		readonlyTextField("publicKey", "Public key", publicKey),
		readonlyIntField("listenPort", "Listen port", listenPort),
		readonlyTextField("tun", "TUN", attachedText(n.currentTun != nil)),
		readonlyTextField("network", "Network", attachedText(n.currentNetwork != nil)),
		readonlyIntField("peers", "Peers", len(n.config.peers)),
		readonlyTextField("error", "Error", statusErr),
	}}
}

func (n *wireGuardNode) configBlock() formular.Block {
	statusErr := n.formError != ""
	return formular.Block{ID: "config", Order: 20, Generation: 1, Form: true, Items: []formular.Item{
		secretTextField("privateKey", "Private key", n.config.privateKey, statusErr, n.formError, false),
		configIntField("listenPort", "Listen port", n.config.listenPort, statusErr, n.formError),
		configTextField("ownedIPs", "Owned IPs", n.config.ownedIPs, statusErr, n.formError, true),
	}}
}

func (n *wireGuardNode) peersBlock() formular.Block {
	statusErr := n.formError != ""
	field := &formular.Field{
		Kind:      formular.FieldArray,
		Templates: []formular.ArrayTemplate{wireGuardPeerTemplate()},
		Elements:  wireGuardPeerElements(n.config.peers),
	}
	if statusErr {
		field.Status = formular.StatusError
		field.StatusText = n.formError
	}
	return formular.Block{ID: "peers", Order: 30, Generation: 1, Form: true, Items: []formular.Item{
		{Type: formular.ItemField, ID: "peers", Label: "Peers", Field: field},
	}}
}

func (n *wireGuardNode) sendConfigFieldStatus(msg formular.FieldValidateMessage) {
	next := n.config
	switch msg.Field.FieldID {
	case "privateKey":
		if value, ok := stringFromAny(msg.Value); ok {
			next.privateKey = value
		}
	case "listenPort":
		if value, ok := intFromAny(msg.Value); ok {
			next.listenPort = value
		}
	case "ownedIPs":
		if value, ok := stringFromAny(msg.Value); ok {
			next.ownedIPs = value
		}
	default:
		return
	}
	next.normalize()
	n.sendFieldStatus(msg, validateWireGuardConfig(next))
}

func (n *wireGuardNode) sendPeersFieldStatus(msg formular.FieldValidateMessage) {
	if len(msg.Field.ElementPath) == 0 {
		return
	}
	next := n.config
	peers := slices.Clone(next.peers)
	element := msg.Field.ElementPath[len(msg.Field.ElementPath)-1]
	index := -1
	for i, peer := range peers {
		if peer.id == element.ElementID {
			index = i
			break
		}
	}
	if index < 0 {
		return
	}
	switch msg.Field.FieldID {
	case "publicKey":
		if value, ok := stringFromAny(msg.Value); ok {
			peers[index].publicKey = value
		}
	case "presharedKey":
		if value, ok := stringFromAny(msg.Value); ok {
			peers[index].presharedKey = value
		}
	case "endpoint":
		if value, ok := stringFromAny(msg.Value); ok {
			peers[index].endpoint = value
		}
	case "allowedIPs":
		if value, ok := stringFromAny(msg.Value); ok {
			peers[index].allowedIPs = value
		}
	case "persistentKeepaliveInterval":
		if value, ok := intFromAny(msg.Value); ok {
			peers[index].persistentKeepaliveInterval = value
		}
	default:
		return
	}
	next.peers = peers
	next.normalize()
	n.sendFieldStatus(msg, validateWireGuardConfig(next))
}

func (n *wireGuardNode) sendFieldStatus(msg formular.FieldValidateMessage, err error) {
	status := formular.StatusOK
	statusText := ""
	if err != nil {
		status = formular.StatusError
		statusText = err.Error()
	}
	n.w.SendNodeMenuMsg(n.id, formular.FieldStatusMessage{
		MessageBase: formular.MessageBase{
			Type:            formular.MessageFieldStatus,
			MenuID:          pasta.NodeMenuID(n.id),
			MenuGeneration:  1,
			BlockGeneration: 1,
		},
		Field:      msg.Field,
		Status:     status,
		StatusText: statusText,
	})
}

func validateWireGuardConfig(config wireGuardConfig) error {
	if config.privateKey != "" {
		if _, err := parseWireGuardPrivateKey(config.privateKey); err != nil {
			return fmt.Errorf("private key: %w", err)
		}
	}
	if config.listenPort < 0 || config.listenPort > 65535 {
		return errors.New("listen port must be between 0 and 65535")
	}
	if _, err := parseWireGuardAllowedIPs(config.ownedIPs); err != nil {
		return fmt.Errorf("owned IPs: %w", err)
	}
	for _, peer := range config.peers {
		if peer.publicKey == "" {
			return errors.New("peer public key is required")
		}
		if _, err := parseWireGuardPublicKey(peer.publicKey); err != nil {
			return fmt.Errorf("peer %s public key: %w", peerLabel(peer), err)
		}
		if _, err := parseWireGuardPresharedKey(peer.presharedKey); err != nil {
			return fmt.Errorf("peer %s preshared key: %w", peerLabel(peer), err)
		}
		if peer.endpoint != "" {
			if _, err := netip.ParseAddrPort(peer.endpoint); err != nil {
				return fmt.Errorf("peer %s endpoint: %w", peerLabel(peer), err)
			}
		}
		if _, err := parseWireGuardAllowedIPs(peer.allowedIPs); err != nil {
			return fmt.Errorf("peer %s allowed IPs: %w", peerLabel(peer), err)
		}
		if peer.persistentKeepaliveInterval < 0 || peer.persistentKeepaliveInterval > 65535 {
			return fmt.Errorf("peer %s persistent keepalive must be between 0 and 65535", peerLabel(peer))
		}
	}
	return nil
}

func parseWireGuardPrivateKey(value string) (wgdevice.NoisePrivateKey, error) {
	var key wgdevice.NoisePrivateKey
	value = normalizeHexKey(value)
	if value == "" {
		return key, nil
	}
	return key, key.FromHex(value)
}

func generateWireGuardPrivateKeyHex() (string, error) {
	var key wgdevice.NoisePrivateKey
	if _, err := rand.Read(key[:]); err != nil {
		return "", err
	}
	key[0] &= 248
	key[31] = (key[31] & 127) | 64
	return hex.EncodeToString(key[:]), nil
}

func parseWireGuardPublicKey(value string) (wgdevice.NoisePublicKey, error) {
	var key wgdevice.NoisePublicKey
	value = normalizeHexKey(value)
	if value == "" {
		return key, errors.New("empty key")
	}
	return key, key.FromHex(value)
}

func parseWireGuardPresharedKey(value string) (wgdevice.NoisePresharedKey, error) {
	var key wgdevice.NoisePresharedKey
	value = normalizeHexKey(value)
	if value == "" {
		return key, nil
	}
	return key, key.FromHex(value)
}

func wireGuardPublicKeyHex(privateKey wgdevice.NoisePrivateKey) string {
	if privateKey.IsZero() {
		return ""
	}
	publicBytes, err := curve25519.X25519(privateKey[:], curve25519.Basepoint)
	if err != nil {
		return ""
	}
	return hex.EncodeToString(publicBytes)
}

func parseWireGuardAllowedIPs(value string) ([]netip.Prefix, error) {
	fields := splitDefaultTunList(value)
	out := make([]netip.Prefix, 0, len(fields))
	for _, field := range fields {
		if prefix, err := netip.ParsePrefix(field); err == nil {
			out = append(out, prefix)
			continue
		}
		addr, err := netip.ParseAddr(field)
		if err != nil {
			return nil, fmt.Errorf("%q is not an IP prefix", field)
		}
		out = append(out, netip.PrefixFrom(addr, addr.BitLen()))
	}
	return out, nil
}

// wireGuardOwnedIPAddrs returns normalized CIDR strings suitable for TunConfig.
// A bare IP is expanded to a host prefix, and duplicates are removed while
// preserving the user's first-seen order.
func wireGuardOwnedIPAddrs(value string) []string {
	prefixes, err := parseWireGuardAllowedIPs(value)
	if err != nil {
		return nil
	}
	if len(prefixes) == 0 {
		return nil
	}
	seen := map[string]struct{}{}
	out := make([]string, 0, len(prefixes))
	for _, prefix := range prefixes {
		addr := prefix.String()
		if _, ok := seen[addr]; ok {
			continue
		}
		seen[addr] = struct{}{}
		out = append(out, addr)
	}
	return out
}

func normalizeHexKey(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func shortWireGuardKey(value string) string {
	value = normalizeHexKey(value)
	if len(value) <= 12 {
		return value
	}
	return value[:12]
}

func peerLabel(peer wireGuardPeerConfig) string {
	if peer.id != "" {
		return peer.id
	}
	if peer.publicKey != "" {
		return shortWireGuardKey(peer.publicKey)
	}
	return "peer"
}

func attachedText(attached bool) string {
	if attached {
		return "attached"
	}
	return "detached"
}

func readonlyTextField(id, label, value string) formular.Item {
	return formular.Item{Type: formular.ItemField, ID: id, Label: label, Field: &formular.Field{
		Kind:     formular.FieldText,
		Value:    value,
		Readonly: true,
	}}
}

func readonlyIntField(id, label string, value int) formular.Item {
	return formular.Item{Type: formular.ItemField, ID: id, Label: label, Field: &formular.Field{
		Kind:     formular.FieldInt,
		Value:    value,
		Readonly: true,
	}}
}

func secretTextField(id, label, value string, statusErr bool, statusText string, multiline bool) formular.Item {
	field := &formular.Field{Kind: formular.FieldText, Value: value, Validation: true, Secret: true, Multiline: multiline}
	if statusErr {
		field.Status = formular.StatusError
		field.StatusText = statusText
	}
	return formular.Item{Type: formular.ItemField, ID: id, Label: label, Field: field}
}

func wireGuardPeerTemplate() formular.ArrayTemplate {
	return formular.ArrayTemplate{
		Name:  "peer",
		Label: "Peer",
		Items: []formular.Item{
			{Type: formular.ItemField, ID: "publicKey", Label: "Public key", Field: &formular.Field{Kind: formular.FieldText, Validation: true, Required: true}},
			{Type: formular.ItemField, ID: "presharedKey", Label: "Preshared key", Field: &formular.Field{Kind: formular.FieldText, Validation: true, Secret: true}},
			{Type: formular.ItemField, ID: "endpoint", Label: "Endpoint", Field: &formular.Field{Kind: formular.FieldText, Validation: true}},
			{Type: formular.ItemField, ID: "allowedIPs", Label: "Allowed IPs", Field: &formular.Field{Kind: formular.FieldText, Validation: true, Multiline: true}},
			{Type: formular.ItemField, ID: "persistentKeepaliveInterval", Label: "Keepalive", Field: &formular.Field{Kind: formular.FieldInt, Validation: true}},
		},
	}
}

func wireGuardPeerElements(peers []wireGuardPeerConfig) []formular.ArrayElement {
	out := make([]formular.ArrayElement, 0, len(peers))
	for i, peer := range peers {
		peer.normalize(i)
		out = append(out, formular.ArrayElement{
			ID:       peer.id,
			Template: "peer",
			Items: []formular.Item{
				{Type: formular.ItemField, ID: "publicKey", Label: "Public key", Field: &formular.Field{Kind: formular.FieldText, Value: peer.publicKey, Validation: true, Required: true}},
				{Type: formular.ItemField, ID: "presharedKey", Label: "Preshared key", Field: &formular.Field{Kind: formular.FieldText, Value: peer.presharedKey, Validation: true, Secret: true}},
				{Type: formular.ItemField, ID: "endpoint", Label: "Endpoint", Field: &formular.Field{Kind: formular.FieldText, Value: peer.endpoint, Validation: true}},
				{Type: formular.ItemField, ID: "allowedIPs", Label: "Allowed IPs", Field: &formular.Field{Kind: formular.FieldText, Value: peer.allowedIPs, Validation: true, Multiline: true}},
				{Type: formular.ItemField, ID: "persistentKeepaliveInterval", Label: "Keepalive", Field: &formular.Field{Kind: formular.FieldInt, Value: peer.persistentKeepaliveInterval, Validation: true}},
			},
		})
	}
	return out
}

func parseWireGuardPeers(raw any) ([]wireGuardPeerConfig, bool) {
	switch values := raw.(type) {
	case []formular.ArrayElementValue:
		return wireGuardPeersFromArray(values), true
	case []any:
		array := make([]formular.ArrayElementValue, 0, len(values))
		for _, rawElement := range values {
			element, ok := arrayElementValueFromAny(rawElement)
			if !ok {
				continue
			}
			array = append(array, element)
		}
		return wireGuardPeersFromArray(array), true
	default:
		return nil, false
	}
}

func wireGuardPeersFromArray(values []formular.ArrayElementValue) []wireGuardPeerConfig {
	out := make([]wireGuardPeerConfig, 0, len(values))
	for i, value := range values {
		if value.Template != "" && value.Template != "peer" {
			continue
		}
		peer := wireGuardPeerConfig{id: value.ID}
		peer.publicKey, _ = stringFromAny(value.Values["publicKey"])
		peer.presharedKey, _ = stringFromAny(value.Values["presharedKey"])
		peer.endpoint, _ = stringFromAny(value.Values["endpoint"])
		peer.allowedIPs, _ = stringFromAny(value.Values["allowedIPs"])
		if keepalive, ok := intFromAny(value.Values["persistentKeepaliveInterval"]); ok {
			peer.persistentKeepaliveInterval = keepalive
		}
		peer.normalize(i)
		out = append(out, peer)
	}
	return compactWireGuardPeers(out)
}

func compactWireGuardPeers(peers []wireGuardPeerConfig) []wireGuardPeerConfig {
	out := peers[:0]
	seenIDs := map[string]struct{}{}
	for i := range peers {
		peer := peers[i]
		peer.normalize(i)
		if peer.publicKey == "" && peer.presharedKey == "" && peer.endpoint == "" && peer.allowedIPs == "" && peer.persistentKeepaliveInterval == 0 {
			continue
		}
		if _, ok := seenIDs[peer.id]; ok {
			peer.id = fmt.Sprintf("%s-%d", peer.id, i+1)
		}
		seenIDs[peer.id] = struct{}{}
		out = append(out, peer)
	}
	return out
}

func wireGuardPeersConfig(peers []wireGuardPeerConfig) []map[string]any {
	out := make([]map[string]any, 0, len(peers))
	for _, peer := range peers {
		out = append(out, map[string]any{
			"id":                          peer.id,
			"publicKey":                   peer.publicKey,
			"presharedKey":                peer.presharedKey,
			"endpoint":                    peer.endpoint,
			"allowedIPs":                  peer.allowedIPs,
			"persistentKeepaliveInterval": peer.persistentKeepaliveInterval,
		})
	}
	return out
}

func readWireGuardPeers(cfg configer.Config) ([]wireGuardPeerConfig, bool) {
	raw, err := cfg.Get(configer.Path{"peers"})
	if err != nil {
		return nil, false
	}
	switch values := raw.(type) {
	case []map[string]any:
		out := make([]wireGuardPeerConfig, 0, len(values))
		for i, value := range values {
			out = append(out, wireGuardPeerFromConfigMap(i, value))
		}
		return compactWireGuardPeers(out), true
	case []any:
		out := make([]wireGuardPeerConfig, 0, len(values))
		for i, rawValue := range values {
			if value, ok := rawValue.(map[string]any); ok {
				out = append(out, wireGuardPeerFromConfigMap(i, value))
			}
		}
		return compactWireGuardPeers(out), true
	default:
		return nil, false
	}
}

func wireGuardPeerFromConfigMap(index int, value map[string]any) wireGuardPeerConfig {
	peer := wireGuardPeerConfig{}
	peer.id, _ = stringFromAny(value["id"])
	peer.publicKey, _ = stringFromAny(value["publicKey"])
	peer.presharedKey, _ = stringFromAny(value["presharedKey"])
	peer.endpoint, _ = stringFromAny(value["endpoint"])
	peer.allowedIPs, _ = stringFromAny(value["allowedIPs"])
	if keepalive, ok := intFromAny(value["persistentKeepaliveInterval"]); ok {
		peer.persistentKeepaliveInterval = keepalive
	}
	peer.normalize(index)
	return peer
}
