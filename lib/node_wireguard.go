package almagest

import (
	_ "embed"
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

// wireGuardNode owns exactly one wgo Device. Link attach/detach operations
// replace only the live TUN or UDP bind attachments, preserving configured
// peers and device identity across graph rewiring.
type wireGuardNode struct {
	NodeBase

	tunp uint64
	netp uint64

	tunLink uint64
	netLink uint64

	currentTun        tun.Tun
	currentNetwork    NetworkCloser
	currentBind       conn.Bind
	currentBindGen    uint64
	currentBindOpen   *atomic.Uint64
	currentBindFailed bool

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
type wireGuardConfigCommitOptions struct {
	rebuildPeers  bool
	emitTunConfig bool
}

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
	n.currentBindGen = 0
	n.currentBindOpen = nil
	n.currentBindFailed = false
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
			n.currentBindGen = 0
			n.currentBindOpen = nil
			n.currentBindFailed = false
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
	return n.commitConfigForm(next, wireGuardConfigCommitOptions{rebuildPeers: true, emitTunConfig: true})
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
