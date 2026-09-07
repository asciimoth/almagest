package wireguardnodes

import (
	_ "embed"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/asciimoth/almagest/lib/links"
	conn "github.com/asciimoth/batchudp"
	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/formular"
	"github.com/asciimoth/gonnect/tun"
	"github.com/asciimoth/pasta/pasta"
	"github.com/asciimoth/pasta/pasta/std"
	wgdevice "github.com/asciimoth/wgo/device"
)

const (
	// NodeTypeWireGuard is the node class name for the embedded WireGuard
	// device wrapper.
	NodeTypeWireGuard = "almagest/WireGuard"
)

type WireGuardClass struct {
	Runtime Runtime
}

var (
	//go:embed node_wireguard.md
	WireGuardClassLongDescription string
)

func (WireGuardClass) ClassName() string        { return NodeTypeWireGuard }
func (WireGuardClass) ShortDescription() string { return "WireGuard" }
func (WireGuardClass) LongDescription() string  { return WireGuardClassLongDescription }
func (WireGuardClass) DefaultNodeParams() pasta.NodeClassParams {
	return pasta.NodeClassParams{PrimaryType: links.TypeWGCtrl, InitialPorts: []pasta.Port{
		TunPort("left", "Tun"),
		{Direction: "left", Name: "Peers", Types: []string{std.TypeObject}},
		WGCtrlPort("left", "WGCtrl"),
		NetworkPort("right", "Network"),
	}}
}
func (c WireGuardClass) NewNode(cfg configer.Config, _ ...*pasta.NodeClassState) (pasta.Node, error) {
	config := defaultWireGuardConfig()
	config.read(cfg)
	var secrets SecretsStorage
	if c.Runtime != nil {
		secrets = c.Runtime.SecretsStorage()
	}
	return &wireGuardNode{
		NodeBase:             NodeBase{Runtime: c.Runtime},
		secrets:              secrets,
		config:               config,
		objectPeerInputs:     map[uint64][]wireGuardPeerConfig{},
		objectPeers:          map[uint64][]wireGuardPeerConfig{},
		objectErrors:         map[uint64]string{},
		wgctrlPayloads:       map[uint64]WGCtrlPayload{},
		wgctrlConfigs:        map[uint64]WGCtrlInterfaceConfig{},
		wgctrlConfigOrder:    map[uint64]uint64{},
		wgctrlPendingApplies: map[*wgCtrlInterfaceConfigApply]uint64{},
	}, nil
}

// wireGuardNode owns exactly one wgo Device.
//
// Locking/lifecycle strategy:
//   - Pasta invokes node callbacks while holding the workspace lock. Callbacks
//     must therefore never block on runtimeMu and must never run wgo operations
//     that may spawn workers, close workers, open sockets, or wait for device
//     goroutines.
//   - Callbacks update workspace-owned desired state, bump generations, bind
//     payload resources to the graph, and schedule runtime work. Physical
//     device operations run later on goroutines serialized by runtimeMu.
//   - runtimeMu protects the wgo device and runtime-owned attachment state. It
//     may be held across wgo calls only from runtime goroutines, not workspace
//     callbacks.
//   - tunGeneration/networkGeneration describe the current desired attachment
//     identity. current*Generation records which desired generation is actually
//     installed in the device. A stale installed attachment is treated as
//     inactive immediately, even before its async detach work has completed.
//   - runtimeGeneration invalidates config-apply/readiness completions. It is
//     separate from the attachment generations so a config edit does not drop a
//     still-current TUN/network payload that was already queued.
//
// This avoids lock inversions such as:
// workspace callback -> runtimeMu -> wgo -> pasta worker spawn -> workspace.
type wireGuardNode struct {
	NodeBase

	nodeName string
	secrets  SecretsStorage

	tunp  uint64
	peerp uint64
	ctrlp uint64
	netp  uint64

	tunLink uint64
	netLink uint64
	// Payload receipt is workspace-owned. It prevents OnReady from repeating an
	// OnLinkAdd request while the accepted attachment is still queued for async
	// runtime installation.
	tunPayloadReceived     bool
	networkPayloadReceived bool

	tunGeneration     atomic.Uint64
	networkGeneration atomic.Uint64

	currentTun               tun.Tun
	currentTunGeneration     uint64
	currentNetwork           NetworkCloser
	currentNetworkGeneration uint64
	currentBind              conn.Bind
	currentBindGen           uint64
	currentBindOpen          *atomic.Uint64
	currentBindFailed        bool

	config wireGuardConfig
	dev    *wgdevice.Device
	// runtimeDeviceUp records device state controlled by this node. TUN
	// lifecycle events are filtered, so only the node changes this state.
	runtimeDeviceUp bool

	// controllerSource is published only after a successful device config
	// apply. Workspace callbacks can load it without taking runtimeMu.
	controllerSource atomic.Pointer[wgdevice.Device]
	// wgctrlPayloads is workspace-owned and keeps one detached API per link.
	// Re-emitting the same live payload preserves controller-owned resources.
	wgctrlPayloads map[uint64]WGCtrlPayload
	// wgctrlConfigs is workspace-owned and keeps the latest interface setting
	// suggestions received from each attached controller link.
	wgctrlConfigs map[uint64]WGCtrlInterfaceConfig
	// wgctrlConfigOrder records when each current controller suggestion was
	// received. It selects the last received DNS suggestion across links.
	wgctrlConfigOrder map[uint64]uint64
	wgctrlConfigSeq   uint64
	// wgctrlPendingApplies waits for the TUN generation caused by an
	// acknowledged interface update to become physically attached.
	wgctrlPendingApplies map[*wgCtrlInterfaceConfigApply]uint64

	// objectPeerInputs keeps the latest source-owned value for each attached
	// object link. objectPeers contains only resolved and validated values that
	// currently contribute to the device. Keeping the input separately lets a
	// SecretValueChanged event retry a source after its secret becomes valid.
	objectPeerInputs map[uint64][]wireGuardPeerConfig
	objectPeers      map[uint64][]wireGuardPeerConfig
	objectErrors     map[uint64]string

	// runtimeOwnedPeerKeys contains only peers contributed by this node's menu
	// and object inputs. Controller peers are deliberately absent so config
	// rebuilds can remove stale local peers without deleting WGConfig peers.
	runtimeOwnedPeerKeys map[wgdevice.NoisePublicKey]struct{}

	runtimeMu         sync.Mutex
	runtimeGeneration atomic.Uint64
	// runtimeFailureGeneration prevents a successful async completion that was
	// scheduled before a newer runtime failure from clearing that failure or
	// cancelling its retry.
	runtimeFailureGeneration atomic.Uint64
	runtimeReadyGeneration   uint64
	labelRefreshQueued       atomic.Bool

	runtimeStats            wgdevice.RuntimeStats
	runtimeListenPort       int
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

func (n *wireGuardNode) OnInit(w *pasta.Workspace, logger pasta.Logger, id uint64, name string, restored *pasta.NodeInitData, _, _, _, _ bool) error {
	n.InitBaseNode(w, logger, id)
	n.nodeName = name
	n.config.normalize()
	n.tunp = restoredLeft(restored, 0)
	n.peerp = restoredLeft(restored, 1)
	n.ctrlp = restoredLeft(restored, 2)
	n.netp = restoredRight(restored, 0)
	n.ensureObjectPeerMaps()
	if n.wgctrlPayloads == nil {
		n.wgctrlPayloads = map[uint64]WGCtrlPayload{}
	}
	if n.wgctrlConfigs == nil {
		n.wgctrlConfigs = map[uint64]WGCtrlInterfaceConfig{}
	}
	if n.wgctrlConfigOrder == nil {
		n.wgctrlConfigOrder = map[uint64]uint64{}
	}
	if n.wgctrlPendingApplies == nil {
		n.wgctrlPendingApplies = map[*wgCtrlInterfaceConfigApply]uint64{}
	}
	if err := n.SetPrimary(links.TypeWGCtrl); err != nil {
		return err
	}
	config, err := n.resolvePrivateKeyConfig(n.config)
	if err != nil {
		n.formError = err.Error()
	} else {
		n.config = config
		generation := n.nextRuntimeGeneration()
		n.scheduleConfigApply(n.config.clone(), true, generation, 0, false)
	}
	_ = n.updateLabel()
	n.sendMenuSnapshot()
	return nil
}
func (n *wireGuardNode) OnReady() error {
	n.sendTunConfig()
	if !n.tunPayloadReceived {
		n.RequestPort(n.tunp)
	}
	n.RequestPort(n.peerp)
	if !n.networkPayloadReceived {
		n.RequestPort(n.netp)
	}
	n.sendWGCtrl()
	return nil
}
func (n *wireGuardNode) OnStop() {
	n.controllerSource.Store(nil)
	n.cancelConfigRetry()
	n.nextRuntimeGeneration()
	n.nextTunGeneration()
	n.nextNetworkGeneration()
	n.scheduleRuntimeStop()
}
func (n *wireGuardNode) PreLinkAdd(port uint64, linkType, portDirection string) error {
	switch {
	case port == n.tunp:
		if portDirection != "left" {
			return pasta.LinkTypeErr(linkType)
		}
		return TunPreLinkAdd(n.W, port, linkType)
	case port == n.peerp && portDirection == "left" && linkType == std.TypeObject:
		// Each object link owns one independent peer-list contribution.
		return nil
	case port == n.ctrlp:
		return WGCtrlPreLinkAdd(n.W, port, linkType, portDirection)
	case port == n.netp:
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
		n.tunPayloadReceived = false
		n.nextTunGeneration()
		n.nextRuntimeGeneration()
		n.sendTunConfigToLink(link)
		std.RequestLocked(n.W, n.ID, link)
	case port == n.peerp && portDirection == "left" && linkType == std.TypeObject:
		std.RequestLocked(n.W, n.ID, link)
	case port == n.ctrlp && portDirection == "left" && linkType == TypeWGCtrl:
		n.sendWGCtrlToLink(link)
	case port == n.netp && portDirection == "right" && linkType == TypeNetwork:
		n.netLink = link
		n.networkPayloadReceived = false
		n.nextNetworkGeneration()
		n.nextRuntimeGeneration()
		std.RequestLocked(n.W, n.ID, link)
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
		n.tunPayloadReceived = false
		tunGeneration := n.nextTunGeneration()
		n.nextRuntimeGeneration()
		n.scheduleTunDetach(tunGeneration)
	case port == n.peerp && portDirection == "left" && linkType == std.TypeObject:
		delete(n.objectPeerInputs, link)
		delete(n.objectPeers, link)
		delete(n.objectErrors, link)
		n.applyEffectivePeerConfig()
	case port == n.ctrlp && portDirection == "left" && linkType == TypeWGCtrl:
		delete(n.wgctrlPayloads, link)
		delete(n.wgctrlConfigs, link)
		delete(n.wgctrlConfigOrder, link)
		n.failWGCtrlInterfaceConfigApplies(link, errors.New("WireGuard controller link was removed"))
		n.sendTunConfig()
	case port == n.netp && portDirection == "right" && linkType == TypeNetwork && link == n.netLink:
		n.cancelConfigRetry()
		n.netLink = 0
		n.networkPayloadReceived = false
		networkGeneration := n.nextNetworkGeneration()
		n.nextRuntimeGeneration()
		n.scheduleNetworkDetach(networkGeneration)
	}
	_ = n.updateLabel()
	n.sendStatusBlock()
	return nil
}
func (n *wireGuardNode) OnEvent(event pasta.Event, linkType string, _ []string, receiverPortDirection string) error {
	if event.ReceiverPort == n.ctrlp && receiverPortDirection == "left" && linkType == TypeWGCtrl {
		if std.IsRequest(event.Payload) {
			n.refreshWGCtrlToLink(event.Link)
			return nil
		}
		switch payload := event.Payload.(type) {
		case WGCtrlInterfaceConfig:
			_ = n.acceptWGCtrlInterfaceConfig(event.Link, payload)
		case *wgCtrlInterfaceConfigApply:
			payload.link = event.Link
			payload.err = n.acceptWGCtrlInterfaceConfig(event.Link, payload.config)
		case wgCtrlInterfaceConfigCommit:
			n.commitWGCtrlInterfaceConfigApply(event.Link, payload.apply)
		}
		return nil
	}
	if event.ReceiverPort == n.peerp && receiverPortDirection == "left" && linkType == std.TypeObject {
		return n.acceptObjectPeers(event)
	}
	if event.ReceiverPort == n.tunp && receiverPortDirection == "left" && linkType == TypeTun {
		if std.IsRequest(event.Payload) {
			n.sendTunConfigToLink(event.Link)
			return nil
		}
		if event.Link != n.tunLink {
			return nil
		}
		payload, ok := event.Payload.(TunPayload)
		if !ok || payload.Tun == nil {
			return nil
		}
		if !TunIsReady(payload.Tun) {
			go func(t tun.Tun) {
				_ = t.Close()
			}(payload.Tun)
			return nil
		}
		n.tunPayloadReceived = true
		BindTunResource(n.W, n.ID, event.Link, payload.Tun)
		n.cancelConfigRetry()
		tunGeneration := n.nextTunGeneration()
		n.nextRuntimeGeneration()
		n.scheduleTunAttach(tunGeneration, payload.Tun, n.config.batchSize)
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
			go func(network NetworkCloser) {
				_ = network.Close()
			}(payload.Network)
			return nil
		}
		n.networkPayloadReceived = true
		BindNetworkResource(n.W, n.ID, event.Link, payload.Network)
		n.cancelConfigRetry()
		networkGeneration := n.nextNetworkGeneration()
		n.nextRuntimeGeneration()
		n.scheduleNetworkAttach(networkGeneration, payload.Network, n.config.batchSize)
		return nil
	}
	return nil
}
func (n *wireGuardNode) OnFormularMsg(message any) error {
	switch msg := message.(type) {
	case formular.FormApplyMessage:
		if msg.MenuID != pasta.NodeMenuID(n.ID) {
			return nil
		}
		return n.applyFormBlock(msg.BlockID, msg.Values)
	case formular.FieldValidateMessage:
		if msg.MenuID != pasta.NodeMenuID(n.ID) {
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
	case wireGuardRuntimeAttachmentResultMessage:
		n.handleRuntimeAttachmentResult(msg)
	case wireGuardWGCtrlInterfaceConfigCheckResult:
		n.handleWGCtrlInterfaceConfigCheckResult(msg)
	case wireGuardLabelRefreshMessage:
		n.labelRefreshQueued.Store(false)
		_ = n.updateLabel()
	case wireGuardRuntimeStatsMessage:
		n.handleRuntimeStats(msg)
	case SecretValueChanged:
		n.handleSecretValueChanged(msg)
	}
	return nil
}
func (n *wireGuardNode) OnSave(cfg configer.Config) error {
	if err := pasta.DeleteNodeOwnedConfigKeys(cfg); err != nil {
		return err
	}
	return configer.SetMap(cfg, map[string]any{
		"privateKeySecret":           n.config.privateKeySecret,
		"listenPort":                 n.config.listenPort,
		"batchSize":                  n.config.batchSize,
		"ownedIPs":                   n.config.ownedIPs,
		"dns":                        n.config.dns,
		wireGuardAmneziaEnabledField: n.config.amneziaWGEnabled,
		"amneziaWG":                  wireGuardAmneziaConfigMap(n.config.amneziaWG),
		"peers":                      wireGuardPeersConfig(n.config.peers),
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
	next, err := n.resolvePrivateKeyConfig(next)
	if err != nil {
		n.rejectConfigForm(err)
		return nil
	}
	if err := validateWireGuardConfig(next); err != nil {
		n.rejectConfigForm(err)
		return nil
	}
	effective := n.effectiveConfigWithMenu(next)
	if err := validateWireGuardConfig(effective); err != nil {
		n.rejectConfigForm(err)
		return nil
	}
	n.formError = ""
	n.config = next
	generation := n.nextRuntimeGeneration()
	n.cancelConfigRetry()
	if opts.emitTunConfig {
		n.sendTunConfig()
	}
	n.sendForcedMenuSnapshot()
	_ = n.updateLabel()

	// Applying config may open sockets, update peer endpoints, and bring the
	// device up. Schedule it after the form callback has released workspace
	// state so daemon restore and snapshots are not blocked by runtime I/O.
	n.scheduleConfigApply(effective, opts.rebuildPeers, generation, 0, false)
	return nil
}
func (n *wireGuardNode) rejectConfigForm(err error) {
	n.formError = err.Error()
	n.sendForcedMenuSnapshot()
	_ = n.updateLabel()
}

// acceptObjectPeers replaces only the contribution owned by event.Link. A bad
// latest value removes the previous valid contribution from that link, records
// a link-specific error, and still applies all remaining valid peers.
func (n *wireGuardNode) acceptObjectPeers(event pasta.Event) error {
	if std.IsRequest(event.Payload) {
		return nil
	}
	object, ok := std.ObjectFromPayload(event.Payload)
	if !ok {
		return nil
	}
	input, err := wireGuardPeersFromObject(object)
	if err == nil {
		// Store the source value before secret resolution so a later secret
		// notification can recover this link without another producer event.
		n.objectPeerInputs[event.Link] = slices.Clone(input)
	}
	peers := input
	if err == nil {
		peers, err = n.resolveObjectPeerSecrets(input)
	}
	if err == nil {
		candidate := n.config.clone()
		candidate.peers = slices.Clone(peers)
		err = validateWireGuardConfig(candidate)
	}
	if err == nil {
		n.objectPeerInputs[event.Link] = wireGuardPeerInputs(peers)
		n.objectPeers[event.Link] = slices.Clone(peers)
		delete(n.objectErrors, event.Link)
		effective := n.effectiveConfig()
		if effectiveErr := validateWireGuardConfig(effective); effectiveErr != nil {
			delete(n.objectPeers, event.Link)
			n.objectErrors[event.Link] = effectiveErr.Error()
		}
	} else {
		if input == nil {
			delete(n.objectPeerInputs, event.Link)
		}
		delete(n.objectPeers, event.Link)
		n.objectErrors[event.Link] = err.Error()
	}
	n.applyEffectivePeerConfig()
	return nil
}

// applyEffectivePeerConfig invalidates older async applies before it installs
// the newest merged peer snapshot. ReplacePeers is required so detach and
// object replacement remove peers that are no longer present.
func (n *wireGuardNode) applyEffectivePeerConfig() {
	generation := n.nextRuntimeGeneration()
	n.cancelConfigRetry()
	n.sendTunConfig()
	_ = n.updateLabel()
	n.sendStatusBlock()
	n.scheduleConfigApply(n.effectiveConfig(), true, generation, 0, false)
}

func (n *wireGuardNode) effectiveConfig() wireGuardConfig {
	return n.effectiveConfigWithMenu(n.config)
}

func (n *wireGuardNode) effectiveConfigWithMenu(menu wireGuardConfig) wireGuardConfig {
	effective := menu.clone()
	effective.peers = mergeWireGuardPeers(menu.peers, n.objectPeers)
	return effective
}

func (n *wireGuardNode) ensureObjectPeerMaps() {
	if n.objectPeerInputs == nil {
		n.objectPeerInputs = map[uint64][]wireGuardPeerConfig{}
	}
	if n.objectPeers == nil {
		n.objectPeers = map[uint64][]wireGuardPeerConfig{}
	}
	if n.objectErrors == nil {
		n.objectErrors = map[uint64]string{}
	}
}

func wireGuardPeerInputs(peers []wireGuardPeerConfig) []wireGuardPeerConfig {
	out := slices.Clone(peers)
	for i := range out {
		// Resolved key material is runtime-only. Keep the secret name so a later
		// secret notification can fetch a new value without another object event.
		out[i].presharedKey = ""
	}
	return out
}

func (n *wireGuardNode) objectErrorText() string {
	links := make([]uint64, 0, len(n.objectErrors))
	for link := range n.objectErrors {
		links = append(links, link)
	}
	slices.Sort(links)
	parts := make([]string, 0, len(links))
	for _, link := range links {
		parts = append(parts, fmt.Sprintf("link %d: %s", link, n.objectErrors[link]))
	}
	return strings.Join(parts, "; ")
}
