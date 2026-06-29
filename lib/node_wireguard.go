package almagest

import (
	_ "embed"
	"errors"
	"fmt"
	"maps"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	conn "github.com/asciimoth/batchudp"
	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/formular"
	"github.com/asciimoth/gonnect/tun"
	"github.com/asciimoth/pasta/pasta"
	"github.com/asciimoth/pasta/pasta/std"
	wgdevice "github.com/asciimoth/wgo/device"
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
	return &wireGuardNode{NodeBase: NodeBase{engine: c.engine}, config: config}, nil
}

type wireGuardConfig struct {
	privateKey string
	listenPort int
	ownedIPs   string
	amneziaWG  wireGuardAmneziaConfig
	peers      []wireGuardPeerConfig
}

type wireGuardPeerConfig struct {
	id                          string
	publicKey                   string
	presharedKey                string
	endpoint                    string
	allowedIPs                  string
	persistentKeepaliveInterval int
	amneziaWGOverride           bool
	amneziaWG                   wireGuardAmneziaConfig
}

type wireGuardAmneziaConfig struct {
	junkCount         int
	junkMin           int
	junkMax           int
	initHeader        string
	responseHeader    string
	cookieHeader      string
	transportHeader   string
	initPadding       int
	responsePadding   int
	cookiePadding     int
	transportPadding  int
	initiationPackets [wireGuardAmneziaInitiationPacketCount]string
}

const wireGuardAmneziaInitiationPacketCount = 5

type wireGuardAmneziaIntField struct {
	formID   string
	configID string
	label    string
	value    func(wireGuardAmneziaConfig) int
	set      func(*wireGuardAmneziaConfig, int)
}

type wireGuardAmneziaStringField struct {
	formID   string
	configID string
	label    string
	value    func(wireGuardAmneziaConfig) string
	set      func(*wireGuardAmneziaConfig, string)
}

var wireGuardAmneziaIntFieldSpecs = []wireGuardAmneziaIntField{
	{
		formID:   "amneziaJunkCount",
		configID: "junkCount",
		label:    "AWG Jc",
		value:    func(c wireGuardAmneziaConfig) int { return c.junkCount },
		set:      func(c *wireGuardAmneziaConfig, value int) { c.junkCount = value },
	},
	{
		formID:   "amneziaJunkMin",
		configID: "junkMin",
		label:    "AWG Jmin",
		value:    func(c wireGuardAmneziaConfig) int { return c.junkMin },
		set:      func(c *wireGuardAmneziaConfig, value int) { c.junkMin = value },
	},
	{
		formID:   "amneziaJunkMax",
		configID: "junkMax",
		label:    "AWG Jmax",
		value:    func(c wireGuardAmneziaConfig) int { return c.junkMax },
		set:      func(c *wireGuardAmneziaConfig, value int) { c.junkMax = value },
	},
	{
		formID:   "amneziaInitPadding",
		configID: "initPadding",
		label:    "AWG S1",
		value:    func(c wireGuardAmneziaConfig) int { return c.initPadding },
		set:      func(c *wireGuardAmneziaConfig, value int) { c.initPadding = value },
	},
	{
		formID:   "amneziaResponsePadding",
		configID: "responsePadding",
		label:    "AWG S2",
		value:    func(c wireGuardAmneziaConfig) int { return c.responsePadding },
		set:      func(c *wireGuardAmneziaConfig, value int) { c.responsePadding = value },
	},
	{
		formID:   "amneziaCookiePadding",
		configID: "cookiePadding",
		label:    "AWG S3",
		value:    func(c wireGuardAmneziaConfig) int { return c.cookiePadding },
		set:      func(c *wireGuardAmneziaConfig, value int) { c.cookiePadding = value },
	},
	{
		formID:   "amneziaTransportPadding",
		configID: "transportPadding",
		label:    "AWG S4",
		value:    func(c wireGuardAmneziaConfig) int { return c.transportPadding },
		set:      func(c *wireGuardAmneziaConfig, value int) { c.transportPadding = value },
	},
}

var wireGuardAmneziaStringFieldSpecs = []wireGuardAmneziaStringField{
	{
		formID:   "amneziaInitHeader",
		configID: "initHeader",
		label:    "AWG H1",
		value:    func(c wireGuardAmneziaConfig) string { return c.initHeader },
		set:      func(c *wireGuardAmneziaConfig, value string) { c.initHeader = value },
	},
	{
		formID:   "amneziaResponseHeader",
		configID: "responseHeader",
		label:    "AWG H2",
		value:    func(c wireGuardAmneziaConfig) string { return c.responseHeader },
		set:      func(c *wireGuardAmneziaConfig, value string) { c.responseHeader = value },
	},
	{
		formID:   "amneziaCookieHeader",
		configID: "cookieHeader",
		label:    "AWG H3",
		value:    func(c wireGuardAmneziaConfig) string { return c.cookieHeader },
		set:      func(c *wireGuardAmneziaConfig, value string) { c.cookieHeader = value },
	},
	{
		formID:   "amneziaTransportHeader",
		configID: "transportHeader",
		label:    "AWG H4",
		value:    func(c wireGuardAmneziaConfig) string { return c.transportHeader },
		set:      func(c *wireGuardAmneziaConfig, value string) { c.transportHeader = value },
	},
	wireGuardAmneziaPacketField(0, "amneziaInitiationPacket1", "AWG I1"),
	wireGuardAmneziaPacketField(1, "amneziaInitiationPacket2", "AWG I2"),
	wireGuardAmneziaPacketField(2, "amneziaInitiationPacket3", "AWG I3"),
	wireGuardAmneziaPacketField(3, "amneziaInitiationPacket4", "AWG I4"),
	wireGuardAmneziaPacketField(4, "amneziaInitiationPacket5", "AWG I5"),
}

func wireGuardAmneziaPacketField(index int, formID, label string) wireGuardAmneziaStringField {
	return wireGuardAmneziaStringField{
		formID: formID,
		label:  label,
		value: func(c wireGuardAmneziaConfig) string {
			return c.initiationPackets[index]
		},
		set: func(c *wireGuardAmneziaConfig, value string) {
			c.initiationPackets[index] = value
		},
	}
}

func defaultWireGuardConfig() wireGuardConfig {
	return wireGuardConfig{amneziaWG: defaultWireGuardAmneziaConfig()}
}

func (c *wireGuardConfig) read(cfg configer.Config) {
	if cfg == nil {
		return
	}
	c.privateKey = configer.String(cfg, configer.Path{"privateKey"}, c.privateKey)
	c.listenPort = configer.Int(cfg, configer.Path{"listenPort"}, c.listenPort)
	c.ownedIPs = configer.String(cfg, configer.Path{"ownedIPs"}, c.ownedIPs)
	c.amneziaWG = readWireGuardAmneziaConfig(cfg, c.amneziaWG)
	if peers, ok := readWireGuardPeers(cfg); ok {
		c.peers = peers
	}
	c.normalize()
}

func (c *wireGuardConfig) normalize() {
	c.privateKey = normalizeWireGuardKey(c.privateKey)
	if c.listenPort < 0 || c.listenPort > 65535 {
		c.listenPort = 0
	}
	c.ownedIPs = normalizeListText(c.ownedIPs)
	c.amneziaWG.normalize()
	for i := range c.peers {
		c.peers[i].normalize(i)
	}
	c.peers = compactWireGuardPeers(c.peers)
}

// clone makes form/validation edits independent from async runtime readers.
// The peer slice contains mutable structs, so a plain struct copy would let
// normalization race with a device apply still reading the previous config.
func (c wireGuardConfig) clone() wireGuardConfig {
	c.peers = slices.Clone(c.peers)
	return c
}

func (c *wireGuardConfig) ensurePrivateKey() error {
	if c.privateKey != "" {
		return nil
	}
	key, err := generateWireGuardPrivateKeyBase64()
	if err != nil {
		return fmt.Errorf("generate private key: %w", err)
	}
	c.privateKey = key
	return nil
}

func defaultWireGuardAmneziaConfig() wireGuardAmneziaConfig {
	return wireGuardAmneziaConfigFromDevice(wgdevice.DefaultAmneziaWGConfig())
}

func wireGuardAmneziaConfigFromDevice(cfg wgdevice.AmneziaWGConfig) wireGuardAmneziaConfig {
	out := wireGuardAmneziaConfig{
		junkCount:        cfg.JunkCount,
		junkMin:          cfg.JunkMin,
		junkMax:          cfg.JunkMax,
		initHeader:       cfg.InitHeader.Spec(),
		responseHeader:   cfg.ResponseHeader.Spec(),
		cookieHeader:     cfg.CookieHeader.Spec(),
		transportHeader:  cfg.TransportHeader.Spec(),
		initPadding:      cfg.InitPadding,
		responsePadding:  cfg.ResponsePadding,
		cookiePadding:    cfg.CookiePadding,
		transportPadding: cfg.TransportPadding,
	}
	copy(out.initiationPackets[:], cfg.InitiationPackets[:])
	return out
}

func (c *wireGuardAmneziaConfig) normalize() {
	defaults := defaultWireGuardAmneziaConfig()
	c.initHeader = normalizeWireGuardAmneziaHeader(c.initHeader, defaults.initHeader)
	c.responseHeader = normalizeWireGuardAmneziaHeader(c.responseHeader, defaults.responseHeader)
	c.cookieHeader = normalizeWireGuardAmneziaHeader(c.cookieHeader, defaults.cookieHeader)
	c.transportHeader = normalizeWireGuardAmneziaHeader(c.transportHeader, defaults.transportHeader)
	for i := range c.initiationPackets {
		c.initiationPackets[i] = strings.TrimSpace(c.initiationPackets[i])
	}
}

func normalizeWireGuardAmneziaHeader(value, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	return value
}

func (c wireGuardAmneziaConfig) deviceConfig() (wgdevice.AmneziaWGConfig, error) {
	defaults := wgdevice.DefaultAmneziaWGConfig()
	cfg := defaults
	cfg.JunkCount = c.junkCount
	cfg.JunkMin = c.junkMin
	cfg.JunkMax = c.junkMax
	cfg.InitPadding = c.initPadding
	cfg.ResponsePadding = c.responsePadding
	cfg.CookiePadding = c.cookiePadding
	cfg.TransportPadding = c.transportPadding

	var err error
	if cfg.InitHeader, err = parseWireGuardAmneziaHeader(c.initHeader, defaults.InitHeader); err != nil {
		return wgdevice.AmneziaWGConfig{}, fmt.Errorf("init header: %w", err)
	}
	if cfg.ResponseHeader, err = parseWireGuardAmneziaHeader(c.responseHeader, defaults.ResponseHeader); err != nil {
		return wgdevice.AmneziaWGConfig{}, fmt.Errorf("response header: %w", err)
	}
	if cfg.CookieHeader, err = parseWireGuardAmneziaHeader(c.cookieHeader, defaults.CookieHeader); err != nil {
		return wgdevice.AmneziaWGConfig{}, fmt.Errorf("cookie header: %w", err)
	}
	if cfg.TransportHeader, err = parseWireGuardAmneziaHeader(c.transportHeader, defaults.TransportHeader); err != nil {
		return wgdevice.AmneziaWGConfig{}, fmt.Errorf("transport header: %w", err)
	}
	copy(cfg.InitiationPackets[:], c.initiationPackets[:])
	return cfg, nil
}

func parseWireGuardAmneziaHeader(value string, fallback wgdevice.AmneziaWGHeaderRange) (wgdevice.AmneziaWGHeaderRange, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback, nil
	}
	return wgdevice.ParseAmneziaWGHeaderRange(value)
}

func (p *wireGuardPeerConfig) normalize(index int) {
	p.publicKey = normalizeWireGuardKey(p.publicKey)
	p.presharedKey = normalizeWireGuardKey(p.presharedKey)
	p.endpoint = strings.TrimSpace(p.endpoint)
	p.allowedIPs = normalizeListText(p.allowedIPs)
	p.amneziaWG.normalize()
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
	NodeBase

	tunp uint64
	netp uint64

	tunLink uint64
	netLink uint64

	currentTun     tun.Tun
	currentNetwork NetworkCloser
	currentBind    conn.Bind

	config wireGuardConfig
	dev    *wgdevice.Device

	// runtimeMu protects wgo device operations and attached runtime resources.
	// Long-running runtime work must happen outside workspace callbacks, so
	// runtimeGeneration lets async completions detect that their inputs went
	// stale before they update status or retry state.
	runtimeMu         sync.Mutex
	runtimeGeneration atomic.Uint64

	runtimeStats            wgdevice.RuntimeStats
	unsubscribeRuntimeStats func()
	unsubscribeReceiveErrs  func()

	// Runtime stats callbacks can be frequent and originate inside wgo runtime
	// goroutines. Keep only the latest pending value and deliver one inbox
	// message so stats never block workspace or device locks.
	statsMu                sync.Mutex
	pendingRuntimeStatsDev *wgdevice.Device
	pendingRuntimeStats    wgdevice.RuntimeStats
	runtimeStatsQueued     bool

	formError    string
	runtimeError string

	retryMu         sync.Mutex
	retryGeneration uint64
	retryDelay      time.Duration
	retryCancel     chan struct{}
}

type wireGuardRetryMessage struct {
	generation uint64
}

type wireGuardReceiveErrorMessage struct {
	dev        *wgdevice.Device
	generation uint64
	err        error
}

// wireGuardRuntimeResultMessage is the single completion path for async
// runtime work. It covers bind attach, config apply, and retry attempts so stale
// generation checks and retry/error handling stay in one place.
type wireGuardRuntimeResultMessage struct {
	generation      uint64
	retryGeneration uint64
	checkRetry      bool
	retryOnError    bool
	err             error
}

// wireGuardRuntimeWork runs without the workspace callback on the stack.
// retryOnError says whether a runtime failure should schedule another retry;
// ok is false when the work became stale and no completion should be delivered.
type wireGuardRuntimeWork func() (retryOnError bool, err error, ok bool)

type wireGuardRuntimeStatsMessage struct {
	dev   *wgdevice.Device
	stats wgdevice.RuntimeStats
}

type wireGuardConfigCommitOptions struct {
	rebuildPeers  bool
	emitTunConfig bool
}

const (
	wireGuardConfigRetryInitialDelay = 100 * time.Millisecond
	wireGuardConfigRetryMaxDelay     = 10 * time.Second
)

func (n *wireGuardNode) OnInit(w *pasta.Workspace, logger pasta.Logger, id uint64, _ string, restored *pasta.NodeInitData, _, _, _, _ bool) error {
	n.InitBaseNode(w, logger, id)
	n.config.normalize()
	n.tunp = restoredLeft(restored, 0)
	n.netp = restoredRight(restored, 0)
	if err := n.SetPrimary(TypeTun); err != nil {
		return err
	}
	n.runtimeMu.Lock()
	n.ensureDeviceLocked()
	err := n.applyConfigToDeviceLocked(true)
	n.runtimeMu.Unlock()
	if err != nil {
		n.setRuntimeError(err)
	}
	_ = n.updateLabel()
	n.sendMenuSnapshot()
	return nil
}

func (n *wireGuardNode) OnReady() error {
	n.sendTunConfig()
	n.RequestPort(n.tunp)
	n.RequestPort(n.netp)
	return nil
}

func (n *wireGuardNode) OnStop() {
	n.cancelConfigRetry()
	n.runtimeMu.Lock()
	n.nextRuntimeGenerationLocked()
	n.unsubscribeFromRuntimeStatsLocked()
	n.unsubscribeFromReceiveErrorsLocked()
	if n.dev != nil {
		n.dev.Close()
		n.dev = nil
	}
	n.currentTun = nil
	n.currentNetwork = nil
	n.currentBind = nil
	n.runtimeMu.Unlock()
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
		if n.PortHasLinks(port) {
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
		err := n.detachDevicePart("TUN", func() error { return n.dev.DetachTUN() }, func() {
			n.currentTun = nil
			n.nextRuntimeGenerationLocked()
		})
		if err != nil {
			n.setRuntimeError(err)
		} else {
			n.clearRuntimeError()
		}
	case port == n.netp && portDirection == "right" && linkType == TypeNetwork && link == n.netLink:
		n.cancelConfigRetry()
		n.netLink = 0
		err := n.detachDevicePart("network bind", func() error { return n.dev.DetachBind() }, func() {
			n.currentNetwork = nil
			n.currentBind = nil
			n.nextRuntimeGenerationLocked()
		})
		if err != nil {
			n.setRuntimeError(err)
		} else {
			n.clearRuntimeError()
		}
	}
	_ = n.updateLabel()
	n.sendStatusBlock()
	return nil
}

func (n *wireGuardNode) detachDevicePart(name string, detach func() error, update func()) error {
	n.runtimeMu.Lock()
	defer n.runtimeMu.Unlock()
	update()
	if n.dev == nil {
		return nil
	}
	var detachErr error
	if err := n.dev.Down(); err != nil {
		detachErr = fmt.Errorf("bring device down before detaching %s: %w", name, err)
	}
	if err := detach(); err != nil {
		detachErr = fmt.Errorf("detach %s: %w", name, err)
	}
	return detachErr
}

func (n *wireGuardNode) OnEvent(event pasta.Event, linkType string, _ []string, receiverPortDirection string) error {
	if event.ReceiverPort == n.tunp && receiverPortDirection == "left" && linkType == TypeTun {
		if std.IsRequest(event.Payload) {
			n.sendTunConfigToLink(event.Link)
			return nil
		}
		next, changed := AcceptTunPayload(n.w, n.id, event, n.tunLink, n.currentTun)
		if !changed {
			return nil
		}
		n.attachTun(next)
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
	case formular.FormApplyMessage:
		if msg.MenuID != pasta.NodeMenuID(n.id) {
			return nil
		}
		return n.applyFormBlock(msg.BlockID, msg.Values)
	case formular.FieldValidateMessage:
		if msg.MenuID != pasta.NodeMenuID(n.id) {
			return nil
		}
		n.sendFormFieldStatus(msg)
	}
	return nil
}

// OnInbox handles node-owned runtime messages. These are intentionally separate
// from formular messages so async device work and retry delivery do not use the
// frontend menu channel.
func (n *wireGuardNode) OnInbox(message pasta.InboxMessage) error {
	switch msg := message.Payload.(type) {
	case wireGuardRetryMessage:
		n.retryConfigToDevice(msg.generation)
	case wireGuardReceiveErrorMessage:
		n.handleReceiveError(msg)
	case wireGuardRuntimeResultMessage:
		n.handleRuntimeResult(msg)
	case wireGuardRuntimeStatsMessage:
		n.handleRuntimeStats(msg)
	}
	return nil
}

func (n *wireGuardNode) OnSave(cfg configer.Config) error {
	if err := pasta.DeleteNodeOwnedConfigKeys(cfg); err != nil {
		return err
	}
	return configer.SetMap(cfg, map[string]any{
		"privateKey": n.config.privateKey,
		"listenPort": n.config.listenPort,
		"ownedIPs":   n.config.ownedIPs,
		"amneziaWG":  wireGuardAmneziaConfigMap(n.config.amneziaWG),
		"peers":      wireGuardPeersConfig(n.config.peers),
	})
}

func (n *wireGuardNode) ensureDeviceLocked() {
	if n.dev != nil {
		return
	}
	var logger wgdevice.Logger = wgdevice.NopLogger{}
	if n.logger != nil {
		logger = n.logger
	}
	n.dev = wgdevice.NewDevice(nil, nil, logger)
	n.runtimeStats = n.dev.RuntimeStats()
	n.subscribeToRuntimeStatsLocked(n.dev)
	n.subscribeToReceiveErrorsLocked(n.dev)
}

func (n *wireGuardNode) applyConfigForm(values map[string]any) error {
	next := n.config.clone()
	applyWireGuardConfigValues(&next, values)
	return n.commitConfigForm(next, wireGuardConfigCommitOptions{rebuildPeers: true, emitTunConfig: true})
}

func (n *wireGuardNode) applyPeersForm(values map[string]any) error {
	next := n.config.clone()
	if peers, ok := parseWireGuardPeers(values["peers"]); ok {
		next.peers = peers
	}
	return n.commitConfigForm(next, wireGuardConfigCommitOptions{rebuildPeers: true})
}

func (n *wireGuardNode) applyFormBlock(blockID string, values map[string]any) error {
	switch blockID {
	case "config":
		return n.applyConfigForm(values)
	case "peers":
		return n.applyPeersForm(values)
	default:
		return nil
	}
}

func (n *wireGuardNode) commitConfigForm(next wireGuardConfig, opts wireGuardConfigCommitOptions) error {
	next.normalize()
	if err := next.ensurePrivateKey(); err != nil {
		n.rejectConfigForm(err)
		return nil
	}
	if err := validateWireGuardConfig(next); err != nil {
		n.rejectConfigForm(err)
		return nil
	}
	n.formError = ""
	n.runtimeMu.Lock()
	n.config = next
	generation := n.nextRuntimeGenerationLocked()
	n.runtimeMu.Unlock()
	n.cancelConfigRetry()
	if opts.emitTunConfig {
		n.sendTunConfig()
	}
	n.sendForcedMenuSnapshot()
	_ = n.updateLabel()

	// Applying config may open sockets, update peer endpoints, and bring the
	// device up. Schedule it after the form callback has released workspace
	// state so daemon restore and snapshots are not blocked by runtime I/O.
	n.scheduleConfigApply(opts.rebuildPeers, generation, 0, false)
	return nil
}

func (n *wireGuardNode) rejectConfigForm(err error) {
	n.formError = err.Error()
	n.sendForcedMenuSnapshot()
	_ = n.updateLabel()
}

func (n *wireGuardNode) applyConfigToDevice(rebuildPeers bool) error {
	n.runtimeMu.Lock()
	defer n.runtimeMu.Unlock()
	return n.applyConfigToDeviceLocked(rebuildPeers)
}

func (n *wireGuardNode) applyConfigToDeviceLocked(rebuildPeers bool) error {
	n.ensureDeviceLocked()
	if err := validateWireGuardConfig(n.config); err != nil {
		n.formError = err.Error()
		return err
	}
	active := n.hasActiveAttachmentsLocked()
	if !active {
		if err := n.dev.Down(); err != nil {
			return err
		}
	}
	deviceConfig, err := wireGuardDeviceConfig(n.config)
	if err != nil {
		return err
	}
	if err := n.dev.ApplyConfig(deviceConfig, wgdevice.ApplyConfigOptions{
		ReplacePeers:   rebuildPeers,
		ApplyEndpoints: active,
		ActivatePeers:  active,
	}); err != nil {
		return fmt.Errorf("apply config: %w", err)
	}
	n.refreshRuntimeStatsLocked()
	if !active {
		return nil
	}
	if err := n.dev.Up(); err != nil {
		return fmt.Errorf("bring device up: %w", err)
	}
	n.refreshRuntimeStatsLocked()
	return nil
}

func (n *wireGuardNode) hasActiveAttachmentsLocked() bool {
	return n.currentTun != nil && n.currentBind != nil
}

func (n *wireGuardNode) nextRuntimeGenerationLocked() uint64 {
	return n.runtimeGeneration.Add(1)
}

// currentRuntimeGeneration is atomic so completion handlers can discard stale
// work without taking runtimeMu while the runtime worker is still active.
func (n *wireGuardNode) currentRuntimeGeneration() uint64 {
	return n.runtimeGeneration.Load()
}

func (n *wireGuardNode) attachTun(t tun.Tun) {
	n.runtimeMu.Lock()
	n.ensureDeviceLocked()
	if SameComparableValue(n.currentTun, t) {
		n.runtimeMu.Unlock()
		return
	}
	var err error
	if n.currentTun == nil {
		err = n.dev.AttachTUN(t)
	} else {
		err = n.dev.ReplaceTUN(t)
	}
	if err != nil {
		n.runtimeMu.Unlock()
		_ = t.Close()
		n.setRuntimeError(fmt.Errorf("attach TUN: %w", err))
		return
	}
	n.currentTun = t
	generation := n.nextRuntimeGenerationLocked()
	hasBind := n.currentBind != nil
	n.runtimeMu.Unlock()
	n.cancelConfigRetry()
	if hasBind {
		// Reconfiguring after TUN attach can open/listen on the network bind, so
		// keep it on the async runtime path.
		n.scheduleConfigApply(false, generation, 0, false)
	} else {
		n.clearRuntimeError()
	}
	_ = n.updateLabel()
	n.sendStatusBlock()
}

func (n *wireGuardNode) scheduleConfigApply(rebuildPeers bool, runtimeGeneration, retryGeneration uint64, checkRetry bool) {
	n.scheduleRuntimeWork(runtimeGeneration, retryGeneration, checkRetry, func() (bool, error, bool) {
		n.runtimeMu.Lock()
		defer n.runtimeMu.Unlock()
		if runtimeGeneration != n.currentRuntimeGeneration() {
			return false, nil, false
		}
		err := n.applyConfigToDeviceLocked(rebuildPeers)
		return n.hasActiveAttachmentsLocked(), err, true
	})
}

// scheduleRuntimeWork serializes expensive device operations behind runtimeMu
// while keeping them out of workspace callbacks. Completion is sent back through
// the node inbox so status and retry changes happen on the normal node path.
func (n *wireGuardNode) scheduleRuntimeWork(generation, retryGeneration uint64, checkRetry bool, work wireGuardRuntimeWork) {
	if n.w == nil || n.id == 0 {
		return
	}
	go func() {
		if checkRetry && !n.isCurrentConfigRetry(retryGeneration) {
			return
		}
		retryOnError, err, ok := work()
		if !ok {
			return
		}
		n.sendInternal(wireGuardRuntimeResultMessage{
			generation:      generation,
			retryGeneration: retryGeneration,
			checkRetry:      checkRetry,
			retryOnError:    retryOnError,
			err:             err,
		})
	}()
}

// handleRuntimeResult applies only the currently relevant async completion.
// Older completions are expected during restore/relink/retry churn and are
// dropped before they can clear a newer error or cancel a newer retry.
func (n *wireGuardNode) handleRuntimeResult(msg wireGuardRuntimeResultMessage) {
	if !n.acceptRuntimeResult(msg) {
		return
	}
	if msg.err != nil {
		n.failRuntime(msg.err, msg.retryOnError)
		return
	}
	n.cancelConfigRetry()
	n.clearRuntimeError()
	_ = n.updateLabel()
	n.sendStatusBlock()
}

// acceptRuntimeResult validates both runtime resource generation and retry
// generation. Retry generation is checked only for completions that came from a
// scheduled retry; first-attempt async work has no retry generation to match.
func (n *wireGuardNode) acceptRuntimeResult(msg wireGuardRuntimeResultMessage) bool {
	if msg.generation != n.currentRuntimeGeneration() {
		return false
	}
	if msg.checkRetry && !n.isCurrentConfigRetry(msg.retryGeneration) {
		return false
	}
	return true
}

// sendInternal delivers node-owned messages through the workspace inbox rather
// than the formular channel. The inbox is used here as the async boundary
// between runtime goroutines and node state updates.
func (n *wireGuardNode) sendInternal(payload any) {
	if n.w == nil || n.id == 0 || payload == nil {
		return
	}
	n.w.SendInbox(pasta.InboxMessage{ReceiverNode: n.id, Payload: payload})
}

// deferInternal prevents inbox handlers from blocking the workspace lock behind
// runtimeMu. Handlers that need runtime state requeue briefly if runtime work is
// still holding the device lock.
func (n *wireGuardNode) deferInternal(payload any) {
	if n.w == nil || n.id == 0 || payload == nil {
		return
	}
	go func() {
		timer := time.NewTimer(10 * time.Millisecond)
		defer timer.Stop()
		<-timer.C
		n.sendInternal(payload)
	}()
}

func (n *wireGuardNode) attachNetwork(link uint64, network NetworkCloser) {
	n.cancelConfigRetry()
	n.runtimeMu.Lock()
	n.ensureDeviceLocked()
	n.currentNetwork = network
	generation := n.nextRuntimeGenerationLocked()
	n.runtimeMu.Unlock()
	BindNetworkResource(n.w, n.id, link, network)
	_ = n.updateLabel()
	n.sendStatusBlock()

	// Attaching the bind may touch sockets and apply WireGuard config, so it is
	// scheduled after the network resource has been made visible to the graph.
	n.scheduleNetworkBindAttach(generation, 0, false)
}

func (n *wireGuardNode) scheduleNetworkBindAttach(generation, retryGeneration uint64, checkRetry bool) {
	n.scheduleRuntimeWork(generation, retryGeneration, checkRetry, func() (bool, error, bool) {
		return n.attachCurrentNetworkBind(generation)
	})
}

// attachCurrentNetworkBind owns the bind swap and optional device activation for
// one runtime generation. It reports whether failures are retryable so callers
// do not need to reread mutable runtime state after the work completes.
func (n *wireGuardNode) attachCurrentNetworkBind(generation uint64) (bool, error, bool) {
	n.runtimeMu.Lock()
	defer n.runtimeMu.Unlock()
	if generation != n.currentRuntimeGeneration() {
		return false, nil, false
	}
	if n.currentNetwork == nil {
		return false, errors.New("network is detached"), true
	}
	networkBind := conn.NewDefaultBind(n.currentNetwork)
	if !n.currentNetwork.IsNative() {
		networkBind = conn.NewDefaultBindWithOptions(n.currentNetwork, conn.StdNetBindOptions{
			AllowSingleFamily: true,
		})
	}
	var err error
	if n.currentBind == nil {
		err = n.dev.AttachBind(networkBind)
	} else {
		err = n.dev.ReplaceBind(networkBind)
	}
	if err != nil {
		_ = n.dev.DetachBind()
		_ = networkBind.Close()
		n.currentBind = nil
		return n.currentNetwork != nil, fmt.Errorf("attach network: %w", err), true
	}
	n.currentBind = networkBind
	active := n.hasActiveAttachmentsLocked()
	if active {
		return active, n.applyConfigToDeviceLocked(false), true
	}
	return false, nil, true
}

func (n *wireGuardNode) notifyReceiveError(dev *wgdevice.Device, err error) {
	if n.w == nil || n.id == 0 {
		return
	}
	go n.sendInternal(wireGuardReceiveErrorMessage{
		dev:        dev,
		generation: n.currentRuntimeGeneration(),
		err:        err,
	})
}

func (n *wireGuardNode) handleReceiveError(msg wireGuardReceiveErrorMessage) {
	if msg.dev == nil || msg.err == nil {
		return
	}
	if msg.generation != n.currentRuntimeGeneration() {
		return
	}
	if !n.runtimeMu.TryLock() {
		n.deferInternal(msg)
		return
	}
	current := msg.dev == n.dev && msg.generation == n.currentRuntimeGeneration()
	retry := n.hasActiveAttachmentsLocked()
	n.runtimeMu.Unlock()
	if !current {
		return
	}
	n.failRuntime(fmt.Errorf("receive from bind: %w", msg.err), retry)
	_ = n.updateLabel()
	n.sendStatusBlock()
}

func (n *wireGuardNode) retryConfigToDevice(generation uint64) {
	if !n.isCurrentConfigRetry(generation) {
		return
	}
	n.runtimeMu.Lock()
	needsBind := n.currentBind == nil && n.currentNetwork != nil
	runtimeGeneration := n.currentRuntimeGeneration()
	n.runtimeMu.Unlock()
	if needsBind {
		// If an earlier bind attach failed before currentBind was installed, the
		// retry has to recreate the bind before config can be applied.
		n.scheduleNetworkBindAttach(runtimeGeneration, generation, true)
		return
	}
	n.scheduleConfigApply(false, runtimeGeneration, generation, true)
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

		n.sendInternal(wireGuardRetryMessage{generation: generation})
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
	EmitTunConfig(n.w, n.id, link, n.tunConfig())
}

func (n *wireGuardNode) tunConfig() TunConfig {
	return TunConfig{Addrs: wireGuardOwnedIPAddrs(n.config.ownedIPs)}
}

func (n *wireGuardNode) updateLabel() error {
	return n.SetLabel(n.stateText())
}

func (n *wireGuardNode) stateText() string {
	n.runtimeMu.Lock()
	hasTun := n.currentTun != nil
	hasNetwork := n.currentNetwork != nil
	n.runtimeMu.Unlock()
	switch {
	case n.formError != "":
		return "invalid config"
	case n.runtimeError != "":
		return "error"
	case n.config.privateKey == "":
		return "missing private key"
	case !hasTun && !hasNetwork:
		return "waiting for TUN and network"
	case !hasTun:
		return "waiting for TUN"
	case !hasNetwork:
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

func (n *wireGuardNode) subscribeToRuntimeStatsLocked(dev *wgdevice.Device) {
	if dev == nil || n.w == nil || n.id == 0 {
		return
	}
	n.unsubscribeFromRuntimeStatsLocked()
	n.unsubscribeRuntimeStats = dev.SubscribeRuntimeStats(func(stats wgdevice.RuntimeStats) {
		n.queueRuntimeStats(dev, stats)
	})
}

func (n *wireGuardNode) unsubscribeFromRuntimeStatsLocked() {
	if n.unsubscribeRuntimeStats == nil {
		return
	}
	n.unsubscribeRuntimeStats()
	n.unsubscribeRuntimeStats = nil
}

func (n *wireGuardNode) subscribeToReceiveErrorsLocked(dev *wgdevice.Device) {
	if dev == nil || n.w == nil || n.id == 0 {
		return
	}
	n.unsubscribeFromReceiveErrorsLocked()
	n.unsubscribeReceiveErrs = dev.SubscribeReceiveErrors(func(receiveErr wgdevice.ReceiveError) {
		if !wgdevice.ReceiveErrorShouldRecover(receiveErr.Err) {
			return
		}
		n.notifyReceiveError(dev, receiveErr.Err)
	})
}

func (n *wireGuardNode) unsubscribeFromReceiveErrorsLocked() {
	if n.unsubscribeReceiveErrs == nil {
		return
	}
	n.unsubscribeReceiveErrs()
	n.unsubscribeReceiveErrs = nil
}

func (n *wireGuardNode) queueRuntimeStats(dev *wgdevice.Device, stats wgdevice.RuntimeStats) {
	if n.w == nil || n.id == 0 {
		return
	}
	n.statsMu.Lock()
	n.pendingRuntimeStatsDev = dev
	n.pendingRuntimeStats = stats
	if n.runtimeStatsQueued {
		n.statsMu.Unlock()
		return
	}
	n.runtimeStatsQueued = true
	n.statsMu.Unlock()
	go n.sendInternal(wireGuardRuntimeStatsMessage{dev: dev})
}

// takeRuntimeStats consumes the latest coalesced stats only after the inbox
// handler can take runtimeMu. That avoids losing an update while runtime work is
// still mutating the device state.
func (n *wireGuardNode) takeRuntimeStats(dev *wgdevice.Device) (wgdevice.RuntimeStats, bool) {
	n.statsMu.Lock()
	defer n.statsMu.Unlock()
	if !n.runtimeStatsQueued || n.pendingRuntimeStatsDev != dev {
		return wgdevice.RuntimeStats{}, false
	}
	stats := n.pendingRuntimeStats
	n.runtimeStatsQueued = false
	n.pendingRuntimeStatsDev = nil
	n.pendingRuntimeStats = wgdevice.RuntimeStats{}
	return stats, true
}

func (n *wireGuardNode) handleRuntimeStats(msg wireGuardRuntimeStatsMessage) {
	if msg.dev == nil {
		return
	}
	if !n.runtimeMu.TryLock() {
		n.deferInternal(msg)
		return
	}
	stats, ok := n.takeRuntimeStats(msg.dev)
	if !ok {
		stats = msg.stats
	}
	if msg.dev != n.dev {
		n.runtimeMu.Unlock()
		return
	}
	if n.runtimeStats.Equal(stats) {
		n.runtimeMu.Unlock()
		return
	}
	n.runtimeStats = stats
	n.runtimeMu.Unlock()
	n.sendStatusBlock()
}

func (n *wireGuardNode) refreshRuntimeStatsLocked() {
	if n.dev == nil {
		n.runtimeStats = wgdevice.RuntimeStats{}
		return
	}
	n.runtimeStats = n.dev.RuntimeStats()
}

func (n *wireGuardNode) sendMenuSnapshot() {
	sendNodeMenuSnapshot(n.w, n.id, false, n.menuBlocks()...)
}

func (n *wireGuardNode) sendForcedMenuSnapshot() {
	sendNodeMenuSnapshot(n.w, n.id, true, n.menuBlocks()...)
}

func (n *wireGuardNode) menuBlocks() []formular.Block {
	return []formular.Block{n.statusBlock(), n.configBlock(), n.peersBlock()}
}

func (n *wireGuardNode) sendStatusBlock() {
	sendNodeBlockSnapshot(n.w, n.id, n.statusBlock())
}

func (n *wireGuardNode) statusBlock() formular.Block {
	publicKey := ""
	if n.config.privateKey != "" {
		if key, err := parseWireGuardPrivateKey(n.config.privateKey); err == nil && !key.IsZero() {
			publicKey = wireGuardPublicKeyBase64(key)
		}
	}
	listenPort := n.config.listenPort
	n.runtimeMu.Lock()
	active := n.hasActiveAttachmentsLocked()
	if n.dev != nil && active {
		listenPort = int(n.dev.ListenPort())
	}
	tunAttached := n.currentTun != nil
	networkAttached := n.currentNetwork != nil
	stats := n.runtimeStats
	n.runtimeMu.Unlock()
	statusErr := n.formError
	if statusErr == "" {
		statusErr = n.runtimeError
	}
	return formular.Block{ID: "status", Order: 10, Generation: 1, Items: []formular.Item{
		readonlyTextField("state", "State", n.stateText()),
		readonlyTextField("publicKey", "Public key", publicKey),
		readonlyIntField("listenPort", "Listen port", listenPort),
		readonlyTextField("tun", "TUN", attachedText(tunAttached)),
		readonlyTextField("network", "Network", attachedText(networkAttached)),
		readonlyIntField("peers", "Peers", len(n.config.peers)),
		readonlyIntField("runtimePeers", "Runtime peers", stats.PeerCount),
		readonlyIntField("activePeers", "Active peers", stats.ActivePeerCount),
		readonlyIntField("connectedPeers", "Connected peers", stats.ConnectedPeerCount),
		readonlyTextField("rxBytes", "RX bytes", wireGuardUint64Text(stats.RxBytes)),
		readonlyTextField("txBytes", "TX bytes", wireGuardUint64Text(stats.TxBytes)),
		readonlyTextField("rxPackets", "RX packets", wireGuardUint64Text(stats.RxPackets)),
		readonlyTextField("txPackets", "TX packets", wireGuardUint64Text(stats.TxPackets)),
		readonlyTextField("latestHandshake", "Latest handshake", wireGuardTimeText(stats.LastHandshakeTime)),
		readonlyTextField("error", "Error", statusErr),
	}}
}

func (n *wireGuardNode) configBlock() formular.Block {
	statusErr := n.formError != ""
	items := []formular.Item{
		secretTextField("privateKey", "Private key", n.config.privateKey, statusErr, n.formError, false),
		configIntField("listenPort", "Listen port", n.config.listenPort, statusErr, n.formError),
		configTextField("ownedIPs", "Owned IPs", n.config.ownedIPs, statusErr, n.formError, true),
	}
	items = append(items, wireGuardAmneziaConfigItems(n.config.amneziaWG, statusErr, n.formError)...)
	return formular.Block{ID: "config", Order: 20, Generation: 1, Form: true, Items: items}
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
	next := n.config.clone()
	switch msg.Field.FieldID {
	case "privateKey":
		if value, ok := formular.StringValue(msg.Value); ok {
			next.privateKey = value
		}
	case "listenPort":
		if value, ok := formular.IntValue(msg.Value); ok {
			next.listenPort = value
		}
	case "ownedIPs":
		if value, ok := formular.StringValue(msg.Value); ok {
			next.ownedIPs = value
		}
	default:
		if !applyWireGuardAmneziaValue(&next.amneziaWG, msg.Field.FieldID, msg.Value) {
			return
		}
	}
	next.normalize()
	n.sendFieldStatus(msg, validateWireGuardConfig(next))
}

func (n *wireGuardNode) sendFormFieldStatus(msg formular.FieldValidateMessage) {
	switch msg.Field.BlockID {
	case "config":
		n.sendConfigFieldStatus(msg)
	case "peers":
		n.sendPeersFieldStatus(msg)
	}
}

func (n *wireGuardNode) sendPeersFieldStatus(msg formular.FieldValidateMessage) {
	if len(msg.Field.ElementPath) == 0 {
		return
	}
	next := n.config.clone()
	peers := next.peers
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
		if value, ok := formular.StringValue(msg.Value); ok {
			peers[index].publicKey = value
		}
	case "presharedKey":
		if value, ok := formular.StringValue(msg.Value); ok {
			peers[index].presharedKey = value
		}
	case "endpoint":
		if value, ok := formular.StringValue(msg.Value); ok {
			peers[index].endpoint = value
		}
	case "allowedIPs":
		if value, ok := formular.StringValue(msg.Value); ok {
			peers[index].allowedIPs = value
		}
	case "persistentKeepaliveInterval":
		if value, ok := formular.IntValue(msg.Value); ok {
			peers[index].persistentKeepaliveInterval = value
		}
	case "amneziaOverride":
		if value, ok := formular.BoolValue(msg.Value); ok {
			peers[index].amneziaWGOverride = value
		}
	default:
		if !applyWireGuardAmneziaValue(&peers[index].amneziaWG, msg.Field.FieldID, msg.Value) {
			return
		}
	}
	next.peers = peers
	next.normalize()
	n.sendFieldStatus(msg, validateWireGuardConfig(next))
}

func (n *wireGuardNode) sendFieldStatus(msg formular.FieldValidateMessage, err error) {
	sendNodeFieldStatus(n.w, n.id, msg.Field, err)
}

func applyWireGuardConfigValues(config *wireGuardConfig, values map[string]any) {
	if value, ok := formular.StringValue(values["privateKey"]); ok {
		config.privateKey = value
	}
	if value, ok := formular.IntValue(values["listenPort"]); ok {
		config.listenPort = value
	}
	if value, ok := formular.StringValue(values["ownedIPs"]); ok {
		config.ownedIPs = value
	}
	applyWireGuardAmneziaValues(&config.amneziaWG, values)
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
	deviceConfig, err := wireGuardDeviceConfig(config)
	if err != nil {
		return err
	}
	return wgdevice.ValidateConfig(deviceConfig)
}

func wireGuardDeviceConfig(config wireGuardConfig) (wgdevice.DeviceConfig, error) {
	privateKey, err := parseWireGuardPrivateKey(config.privateKey)
	if err != nil {
		return wgdevice.DeviceConfig{}, fmt.Errorf("private key: %w", err)
	}
	amneziaWG, err := config.amneziaWG.deviceConfig()
	if err != nil {
		return wgdevice.DeviceConfig{}, fmt.Errorf("amneziawg: %w", err)
	}
	out := wgdevice.DeviceConfig{
		PrivateKey: privateKey,
		ListenPort: uint16(config.listenPort),
		AmneziaWG:  amneziaWG,
		Peers:      make([]wgdevice.PeerConfig, 0, len(config.peers)),
	}
	for _, peer := range config.peers {
		if peer.publicKey == "" {
			return wgdevice.DeviceConfig{}, errors.New("peer public key is required")
		}
		publicKey, err := parseWireGuardPublicKey(peer.publicKey)
		if err != nil {
			return wgdevice.DeviceConfig{}, fmt.Errorf("peer %s public key: %w", peerLabel(peer), err)
		}
		presharedKey, err := parseWireGuardPresharedKey(peer.presharedKey)
		if err != nil {
			return wgdevice.DeviceConfig{}, fmt.Errorf("peer %s preshared key: %w", peerLabel(peer), err)
		}
		allowedIPs, err := parseWireGuardAllowedIPs(peer.allowedIPs)
		if err != nil {
			return wgdevice.DeviceConfig{}, fmt.Errorf("peer %s allowed IPs: %w", peerLabel(peer), err)
		}
		if peer.persistentKeepaliveInterval < 0 || peer.persistentKeepaliveInterval > 65535 {
			return wgdevice.DeviceConfig{}, fmt.Errorf("peer %s persistent keepalive must be between 0 and 65535", peerLabel(peer))
		}
		peerConfig := wgdevice.PeerConfig{
			PublicKey:                   publicKey,
			PresharedKey:                presharedKey,
			ProtocolVersion:             1,
			Endpoint:                    peer.endpoint,
			PersistentKeepaliveInterval: uint16(peer.persistentKeepaliveInterval),
			AllowedIPs:                  allowedIPs,
		}
		if peer.amneziaWGOverride {
			amneziaWG, err := peer.amneziaWG.deviceConfig()
			if err != nil {
				return wgdevice.DeviceConfig{}, fmt.Errorf("peer %s amneziawg: %w", peerLabel(peer), err)
			}
			peerConfig.AmneziaWG = &amneziaWG
		}
		out.Peers = append(out.Peers, peerConfig)
	}
	return out, nil
}

func parseWireGuardPrivateKey(value string) (wgdevice.NoisePrivateKey, error) {
	return wgdevice.ParsePrivateKey(strings.TrimSpace(value))
}

func generateWireGuardPrivateKeyBase64() (string, error) {
	key, err := wgdevice.GeneratePrivateKey()
	if err != nil {
		return "", err
	}
	return key.Base64(), nil
}

func parseWireGuardPublicKey(value string) (wgdevice.NoisePublicKey, error) {
	return wgdevice.ParsePublicKey(strings.TrimSpace(value))
}

func parseWireGuardPresharedKey(value string) (wgdevice.NoisePresharedKey, error) {
	return wgdevice.ParsePresharedKey(strings.TrimSpace(value))
}

func wireGuardPublicKeyBase64(privateKey wgdevice.NoisePrivateKey) string {
	if privateKey.IsZero() {
		return ""
	}
	return privateKey.PublicKey().Base64()
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

func normalizeWireGuardKey(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if key, err := wgdevice.ParsePresharedKey(value); err == nil {
		return key.Base64()
	}
	return value
}

func shortWireGuardKey(value string) string {
	value = normalizeWireGuardKey(value)
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

func wireGuardUint64Text(value uint64) string {
	return strconv.FormatUint(value, 10)
}

func wireGuardTimeText(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.Format(time.RFC3339)
}

func secretTextField(id, label, value string, statusErr bool, statusText string, multiline bool) formular.Item {
	opts := []formular.FieldOption{formular.Validation, formular.Secret}
	if multiline {
		opts = append(opts, formular.Multiline)
	}
	if statusErr {
		opts = append(opts, formular.Status(formular.StatusError), formular.StatusText(statusText))
	}
	return formular.TextField(id, label, value, opts...)
}

func wireGuardPeerTemplate() formular.ArrayTemplate {
	return formular.ArrayTemplate{
		Name:  "peer",
		Label: "Peer",
		Items: append([]formular.Item{
			formular.TextField("publicKey", "Public key", "", formular.Validation, formular.Required),
			formular.SecretTextField("presharedKey", "Preshared key", "", formular.Validation),
			formular.TextField("endpoint", "Endpoint", "", formular.Validation),
			formular.MultilineTextField("allowedIPs", "Allowed IPs", "", formular.Validation),
			formular.IntField("persistentKeepaliveInterval", "Keepalive", 0, formular.Validation),
			formular.CheckboxField("amneziaOverride", "AWG override", false),
		}, wireGuardPeerAmneziaTemplateItems()...),
	}
}

func wireGuardAmneziaConfigItems(config wireGuardAmneziaConfig, statusErr bool, statusText string) []formular.Item {
	out := make([]formular.Item, 0, len(wireGuardAmneziaIntFieldSpecs)+len(wireGuardAmneziaStringFieldSpecs))
	for _, spec := range wireGuardAmneziaIntFieldSpecs {
		out = append(out, configIntField(spec.formID, spec.label, spec.value(config), statusErr, statusText))
	}
	for _, spec := range wireGuardAmneziaStringFieldSpecs {
		out = append(out, configTextField(spec.formID, spec.label, spec.value(config), statusErr, statusText, false))
	}
	return out
}

func wireGuardPeerElements(peers []wireGuardPeerConfig) []formular.ArrayElement {
	template := wireGuardPeerTemplate()
	out := make([]formular.ArrayElement, 0, len(peers))
	for i, peer := range peers {
		peer.normalize(i)
		out = append(out, formular.InstantiateArrayTemplate(template, peer.id, wireGuardPeerFormValues(peer)))
	}
	return out
}

func wireGuardPeerFormValues(peer wireGuardPeerConfig) map[string]any {
	values := map[string]any{
		"publicKey":                   peer.publicKey,
		"presharedKey":                peer.presharedKey,
		"endpoint":                    peer.endpoint,
		"allowedIPs":                  peer.allowedIPs,
		"persistentKeepaliveInterval": peer.persistentKeepaliveInterval,
		"amneziaOverride":             peer.amneziaWGOverride,
	}
	maps.Copy(values, wireGuardAmneziaElementValues(peer.amneziaWG))
	return values
}

func wireGuardAmneziaElementValues(config wireGuardAmneziaConfig) map[string]any {
	values := make(map[string]any, len(wireGuardAmneziaIntFieldSpecs)+len(wireGuardAmneziaStringFieldSpecs))
	for _, spec := range wireGuardAmneziaIntFieldSpecs {
		values[spec.formID] = spec.value(config)
	}
	for _, spec := range wireGuardAmneziaStringFieldSpecs {
		values[spec.formID] = spec.value(config)
	}
	return values
}

func wireGuardPeerAmneziaTemplateItems() []formular.Item {
	out := make([]formular.Item, 0, len(wireGuardAmneziaIntFieldSpecs)+len(wireGuardAmneziaStringFieldSpecs))
	for _, spec := range wireGuardAmneziaIntFieldSpecs {
		out = append(out, formular.IntField(spec.formID, spec.label, 0, formular.Validation))
	}
	for _, spec := range wireGuardAmneziaStringFieldSpecs {
		out = append(out, formular.TextField(spec.formID, spec.label, "", formular.Validation))
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
			element, ok := formular.ArrayElementValueFromAny(rawElement)
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
		peer.publicKey, _ = formular.StringValue(value.Values["publicKey"])
		peer.presharedKey, _ = formular.StringValue(value.Values["presharedKey"])
		peer.endpoint, _ = formular.StringValue(value.Values["endpoint"])
		peer.allowedIPs, _ = formular.StringValue(value.Values["allowedIPs"])
		if keepalive, ok := formular.IntValue(value.Values["persistentKeepaliveInterval"]); ok {
			peer.persistentKeepaliveInterval = keepalive
		}
		if override, ok := formular.BoolValue(value.Values["amneziaOverride"]); ok {
			peer.amneziaWGOverride = override
		}
		applyWireGuardAmneziaValues(&peer.amneziaWG, value.Values)
		peer.normalize(i)
		out = append(out, peer)
	}
	return compactWireGuardPeers(out)
}

func applyWireGuardAmneziaValues(config *wireGuardAmneziaConfig, values map[string]any) {
	for _, spec := range wireGuardAmneziaIntFieldSpecs {
		if value, ok := formular.IntValue(values[spec.formID]); ok {
			spec.set(config, value)
		}
	}
	for _, spec := range wireGuardAmneziaStringFieldSpecs {
		if value, ok := formular.StringValue(values[spec.formID]); ok {
			spec.set(config, value)
		}
	}
}

func applyWireGuardAmneziaValue(config *wireGuardAmneziaConfig, fieldID string, raw any) bool {
	for _, spec := range wireGuardAmneziaIntFieldSpecs {
		if fieldID != spec.formID {
			continue
		}
		if value, ok := formular.IntValue(raw); ok {
			spec.set(config, value)
		}
		return true
	}
	for _, spec := range wireGuardAmneziaStringFieldSpecs {
		if fieldID != spec.formID {
			continue
		}
		if value, ok := formular.StringValue(raw); ok {
			spec.set(config, value)
		}
		return true
	}
	return false
}

func compactWireGuardPeers(peers []wireGuardPeerConfig) []wireGuardPeerConfig {
	out := peers[:0]
	seenIDs := map[string]struct{}{}
	for i := range peers {
		peer := peers[i]
		peer.normalize(i)
		if peer.publicKey == "" && peer.presharedKey == "" && peer.endpoint == "" && peer.allowedIPs == "" && peer.persistentKeepaliveInterval == 0 && !peer.amneziaWGOverride {
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

func wireGuardAmneziaConfigMap(config wireGuardAmneziaConfig) map[string]any {
	values := make(map[string]any, len(wireGuardAmneziaIntFieldSpecs)+len(wireGuardAmneziaStringFieldSpecs))
	for _, spec := range wireGuardAmneziaIntFieldSpecs {
		values[spec.configID] = spec.value(config)
	}
	for _, spec := range wireGuardAmneziaStringFieldSpecs {
		if spec.configID != "" {
			values[spec.configID] = spec.value(config)
		}
	}
	values["initiationPackets"] = wireGuardAmneziaInitiationPacketsConfig(config.initiationPackets)
	return values
}

func wireGuardAmneziaInitiationPacketsConfig(packets [wireGuardAmneziaInitiationPacketCount]string) []string {
	out := make([]string, len(packets))
	copy(out, packets[:])
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
			"amneziaWGOverride":           peer.amneziaWGOverride,
			"amneziaWG":                   wireGuardAmneziaConfigMap(peer.amneziaWG),
		})
	}
	return out
}

func readWireGuardAmneziaConfig(cfg configer.Config, fallback wireGuardAmneziaConfig) wireGuardAmneziaConfig {
	raw, err := cfg.Get(configer.Path{"amneziaWG"})
	if err != nil {
		return fallback
	}
	return readWireGuardAmneziaConfigMap(raw, fallback)
}

func readWireGuardAmneziaConfigMap(raw any, fallback wireGuardAmneziaConfig) wireGuardAmneziaConfig {
	values, ok := configer.MapStringAny(raw)
	if !ok {
		return fallback
	}
	out := fallback
	for _, spec := range wireGuardAmneziaIntFieldSpecs {
		if value, ok := formular.IntValue(values[spec.configID]); ok {
			spec.set(&out, value)
		}
	}
	for _, spec := range wireGuardAmneziaStringFieldSpecs {
		if spec.configID == "" {
			continue
		}
		if value, ok := formular.StringValue(values[spec.configID]); ok {
			spec.set(&out, value)
		}
	}
	if packets, ok := wireGuardAmneziaInitiationPacketsFromAny(values["initiationPackets"]); ok {
		out.initiationPackets = packets
	}
	out.normalize()
	return out
}

func wireGuardAmneziaInitiationPacketsFromAny(raw any) ([wireGuardAmneziaInitiationPacketCount]string, bool) {
	var out [wireGuardAmneziaInitiationPacketCount]string
	switch values := raw.(type) {
	case []string:
		for i, value := range values {
			if i >= len(out) {
				break
			}
			out[i] = value
		}
		return out, true
	case []any:
		for i, rawValue := range values {
			if i >= len(out) {
				break
			}
			out[i], _ = formular.StringValue(rawValue)
		}
		return out, true
	default:
		return out, false
	}
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
	peer.id, _ = formular.StringValue(value["id"])
	peer.publicKey, _ = formular.StringValue(value["publicKey"])
	peer.presharedKey, _ = formular.StringValue(value["presharedKey"])
	peer.endpoint, _ = formular.StringValue(value["endpoint"])
	peer.allowedIPs, _ = formular.StringValue(value["allowedIPs"])
	if keepalive, ok := formular.IntValue(value["persistentKeepaliveInterval"]); ok {
		peer.persistentKeepaliveInterval = keepalive
	}
	if override, ok := formular.BoolValue(value["amneziaWGOverride"]); ok {
		peer.amneziaWGOverride = override
	}
	peer.amneziaWG = readWireGuardAmneziaConfigMap(value["amneziaWG"], peer.amneziaWG)
	peer.normalize(index)
	return peer
}
