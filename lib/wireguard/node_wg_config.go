package wireguardnodes

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/formular"
	"github.com/asciimoth/pasta/pasta"
	"github.com/asciimoth/pasta/pasta/std"
	wgdevice "github.com/asciimoth/wgo/device"
)

const (
	// NodeTypeWGConfig is a peer-list controller for a WireGuard node.
	NodeTypeWGConfig = "almagest/WGConfig"
)

// WGConfigClass creates nodes that own a peer list and one named UDP
// transport on a shared WireGuard device.
type WGConfigClass struct {
	Runtime Runtime
}

func (WGConfigClass) ClassName() string        { return NodeTypeWGConfig }
func (WGConfigClass) ShortDescription() string { return "WireGuard peer configuration" }
func (WGConfigClass) LongDescription() string {
	return "Configures peers on an attached WireGuard device."
}
func (WGConfigClass) DefaultNodeParams() pasta.NodeClassParams {
	return pasta.NodeClassParams{PrimaryType: TypeWGCtrl, InitialPorts: []pasta.Port{
		WGCtrlPort("right", "WireGuard"),
		NetworkPort("right", "Network"),
	}}
}

func (c WGConfigClass) NewNode(cfg configer.Config, _ ...*pasta.NodeClassState) (pasta.Node, error) {
	var peers []wireGuardPeerConfig
	if cfg != nil {
		if parsed, ok := readWireGuardPeers(cfg); ok {
			peers = parsed
		}
	}
	var secrets SecretsStorage
	if c.Runtime != nil {
		secrets = c.Runtime.SecretsStorage()
	}
	return &wgConfigNode{
		NodeBase: NodeBase{Runtime: c.Runtime},
		secrets:  secrets,
		peers:    peers,
	}, nil
}

// WGConfigTransportID returns the custom wgo transport name owned by node.
// The class name and stable workspace node ID make names unique when several
// WGConfig nodes control one WireGuard device.
func WGConfigTransportID(node uint64) wgdevice.TransportID {
	return wgdevice.TransportID(fmt.Sprintf("%s/%d/udp", NodeTypeWGConfig, node))
}

// wgConfigNode keeps graph callbacks separate from wgo operations. Callbacks
// update desired state and schedule reconcile work. reconcileMu serializes all
// peer, transport, and detached-API lifecycle operations outside the workspace
// lock. generation rejects stale queued work after rapid link or form changes.
type wgConfigNode struct {
	NodeBase

	nodeName string
	secrets  SecretsStorage

	ctrlp uint64
	netp  uint64

	ctrlLink uint64
	netLink  uint64

	controller WGCtrlAPI
	network    NetworkCloser
	peers      []wireGuardPeerConfig

	generation  atomic.Uint64
	reconcileMu sync.Mutex

	runtimeController WGCtrlAPI
	runtimeNetwork    NetworkCloser
	runtimeTransport  bool
	runtimePeerKeys   map[wgdevice.NoisePublicKey]struct{}

	runtimeWGReady      bool
	runtimeNetworkReady bool
	runtimePeerCount    int
	reconcilePending    bool
	retryDelay          time.Duration
	formError           string
	runtimeError        string
}

type wgConfigRuntimeResult struct {
	generation   uint64
	wgReady      bool
	networkReady bool
	peerCount    int
	err          error
}

type wgConfigControllerClosed struct {
	controller WGCtrlAPI
}

type wgConfigRetryMessage struct {
	generation uint64
}

func (n *wgConfigNode) OnInit(w *pasta.Workspace, logger pasta.Logger, id uint64, name string, restored *pasta.NodeInitData, _, _, _, _ bool) error {
	n.InitBaseNode(w, logger, id)
	n.nodeName = name
	n.ctrlp = restoredRight(restored, 0)
	n.netp = restoredRight(restored, 1)
	for i := range n.peers {
		n.peers[i].normalize(i)
	}
	resolved, err := resolveWireGuardPeerSecrets(n.peerSecretsStorage(), n.currentNodeName(), n.peers)
	if err == nil {
		err = validateWGConfigPeers(resolved)
	}
	if err != nil {
		n.formError = err.Error()
	} else {
		n.peers = resolved
	}
	if err := n.SetPrimary(TypeWGCtrl); err != nil {
		return err
	}
	_ = n.updateLabel()
	n.sendMenuSnapshot()
	return nil
}

func (n *wgConfigNode) OnReady() error {
	n.RequestPort(n.ctrlp)
	n.RequestPort(n.netp)
	n.scheduleReconcile()
	return nil
}

func (n *wgConfigNode) OnStop() {
	n.generation.Add(1)
	go n.stopRuntime()
}

func (n *wgConfigNode) PreLinkAdd(port uint64, linkType, portDirection string) error {
	switch port {
	case n.ctrlp:
		return WGCtrlPreLinkAdd(n.W, port, linkType, portDirection)
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

func (n *wgConfigNode) OnLinkAdd(link, port uint64, linkType, portDirection string) error {
	switch {
	case port == n.ctrlp && portDirection == "right" && linkType == TypeWGCtrl:
		n.ctrlLink = link
		std.RequestLocked(n.W, n.ID, link)
	case port == n.netp && portDirection == "right" && linkType == TypeNetwork:
		n.netLink = link
		std.RequestLocked(n.W, n.ID, link)
	}
	n.scheduleReconcile()
	_ = n.updateLabel()
	n.sendStatusBlock()
	return nil
}

func (n *wgConfigNode) OnLinkRemoved(link, port uint64, linkType, portDirection string) error {
	switch {
	case port == n.ctrlp && portDirection == "right" && linkType == TypeWGCtrl && link == n.ctrlLink:
		n.ctrlLink = 0
		n.controller = nil
	case port == n.netp && portDirection == "right" && linkType == TypeNetwork && link == n.netLink:
		n.netLink = 0
		n.network = nil
	}
	n.scheduleReconcile()
	_ = n.updateLabel()
	n.sendStatusBlock()
	return nil
}

func (n *wgConfigNode) OnEvent(event pasta.Event, linkType string, _ []string, receiverPortDirection string) error {
	if event.ReceiverPort == n.ctrlp && receiverPortDirection == "right" && linkType == TypeWGCtrl {
		if event.Link != n.ctrlLink {
			return nil
		}
		next, changed := AcceptWGCtrlPayload(n.W, event, n.ctrlLink, n.controller)
		if !changed {
			return nil
		}
		n.controller = next
		n.watchController(next)
		n.scheduleReconcile()
		_ = n.updateLabel()
		n.sendStatusBlock()
		return nil
	}
	if event.ReceiverPort == n.netp && receiverPortDirection == "right" && linkType == TypeNetwork {
		if std.IsRequest(event.Payload) || event.Link != n.netLink {
			return nil
		}
		payload, ok := event.Payload.(NetworkPayload)
		if !ok || !NetworkIsReady(payload.Network) || SameNetworkInstance(n.network, payload.Network) {
			return nil
		}
		BindNetworkResource(n.W, n.ID, event.Link, payload.Network)
		n.network = payload.Network
		n.scheduleReconcile()
		_ = n.updateLabel()
		n.sendStatusBlock()
	}
	return nil
}

func (n *wgConfigNode) OnInbox(message pasta.InboxMessage) error {
	switch msg := message.Payload.(type) {
	case wgConfigRuntimeResult:
		n.handleRuntimeResult(msg)
	case wgConfigControllerClosed:
		if SameWGCtrlInstance(n.controller, msg.controller) {
			n.controller = nil
			n.scheduleReconcile()
			_ = n.updateLabel()
			n.sendStatusBlock()
		}
	case wgConfigRetryMessage:
		if msg.generation == n.generation.Load() {
			n.scheduleReconcile()
			_ = n.updateLabel()
			n.sendStatusBlock()
		}
	case SecretValueChanged:
		n.handleSecretValueChanged(msg)
	}
	return nil
}

func (n *wgConfigNode) OnFormularMsg(message any) error {
	switch msg := message.(type) {
	case formular.FormApplyMessage:
		if msg.MenuID != pasta.NodeMenuID(n.ID) || msg.BlockID != "peers" {
			return nil
		}
		peers, ok := parseWireGuardPeers(msg.Values["peers"])
		if !ok {
			n.rejectPeersForm(errors.New("peers must be an array"))
			return nil
		}
		return n.commitPeersForm(peers)
	case formular.FieldValidateMessage:
		if msg.MenuID == pasta.NodeMenuID(n.ID) && msg.Field.BlockID == "peers" {
			n.sendPeersFieldStatus(msg)
		}
	}
	return nil
}

func (n *wgConfigNode) OnSave(cfg configer.Config) error {
	if err := pasta.DeleteNodeOwnedConfigKeys(cfg); err != nil {
		return err
	}
	return cfg.Set(configer.Path{"peers"}, wireGuardPeersConfig(n.peers))
}

func (n *wgConfigNode) commitPeersForm(peers []wireGuardPeerConfig) error {
	resolved, err := resolveWireGuardPeerSecrets(n.peerSecretsStorage(), n.currentNodeName(), peers)
	if err == nil {
		err = validateWGConfigPeers(resolved)
	}
	if err != nil {
		n.rejectPeersForm(err)
		return nil
	}
	n.formError = ""
	n.peers = resolved
	n.sendForcedMenuSnapshot()
	n.scheduleReconcile()
	_ = n.updateLabel()
	return nil
}

func (n *wgConfigNode) rejectPeersForm(err error) {
	n.formError = err.Error()
	n.sendForcedMenuSnapshot()
	_ = n.updateLabel()
}

func (n *wgConfigNode) handleSecretValueChanged(msg SecretValueChanged) {
	prev := strings.TrimSpace(msg.SecretNamePrev)
	next := strings.TrimSpace(msg.SecretNameNew)
	if !wireGuardPeerSecretChangeRelevant(n.peers, prev, next) {
		return
	}
	peers := slices.Clone(n.peers)
	for i := range peers {
		if peers[i].presharedKeySecret == prev && next != "" && next != prev {
			peers[i].presharedKeySecret = next
		}
		peers[i].presharedKey = ""
	}
	_ = n.commitPeersForm(peers)
}

func (n *wgConfigNode) peerSecretsStorage() SecretsStorage {
	if n.secrets != nil {
		return n.secrets
	}
	return n.SecretsStorage()
}

func (n *wgConfigNode) currentNodeName() string {
	if n.W != nil && n.ID != 0 {
		if snapshot, ok := n.W.NodeSnapshotLocked(n.ID); ok {
			return snapshot.Name
		}
	}
	return n.nodeName
}

func validateWGConfigPeers(peers []wireGuardPeerConfig) error {
	deviceConfig := wgdevice.DeviceConfig{
		AmneziaWG: wgdevice.DefaultAmneziaWGConfig(),
		Peers:     make([]wgdevice.PeerConfig, 0, len(peers)),
	}
	for _, peer := range peers {
		converted, err := wireGuardPeerDeviceConfig(peer)
		if err != nil {
			return err
		}
		deviceConfig.Peers = append(deviceConfig.Peers, converted)
	}
	return wgdevice.ValidateConfig(deviceConfig)
}

func (n *wgConfigNode) scheduleReconcile() {
	if n.W == nil || n.ID == 0 {
		return
	}
	generation := n.generation.Add(1)
	n.reconcilePending = true
	controller := n.controller
	network := n.network
	peers := slices.Clone(n.peers)
	go func() {
		wgReady, networkReady, peerCount, err, ok := n.reconcile(generation, controller, network, peers)
		if !ok {
			return
		}
		n.W.SendInbox(pasta.InboxMessage{ReceiverNode: n.ID, Payload: wgConfigRuntimeResult{
			generation: generation, wgReady: wgReady, networkReady: networkReady,
			peerCount: peerCount, err: err,
		}})
	}()
}

// watchController observes middleware-driven payload closure. Select closes
// the previously routed payload without removing WGConfig's graph link, so the
// node needs this signal to clear stale readiness and prepare for re-selection.
func (n *wgConfigNode) watchController(controller WGCtrlAPI) {
	if !WGCtrlIsReady(controller) || n.W == nil || n.ID == 0 {
		return
	}
	go func() {
		<-controller.Wait()
		n.W.SendInbox(pasta.InboxMessage{
			ReceiverNode: n.ID,
			Payload:      wgConfigControllerClosed{controller: controller},
		})
	}()
}

func (n *wgConfigNode) reconcile(generation uint64, controller WGCtrlAPI, network NetworkCloser, peers []wireGuardPeerConfig) (bool, bool, int, error, bool) {
	n.reconcileMu.Lock()
	defer n.reconcileMu.Unlock()
	if generation != n.generation.Load() {
		return false, false, 0, nil, false
	}

	if !SameWGCtrlInstance(n.runtimeController, controller) || !WGCtrlIsReady(n.runtimeController) {
		n.closeRuntimeControllerLocked()
		if WGCtrlIsReady(controller) {
			n.runtimeController = controller
		}
	}
	if !WGCtrlIsReady(n.runtimeController) {
		return false, false, 0, nil, true
	}

	networkReady := NetworkIsReady(network)
	if !networkReady {
		if err := n.removeRuntimeTransportLocked(); err != nil {
			return true, false, len(n.runtimePeerKeys), err, true
		}
		n.runtimeNetwork = nil
	} else if !n.runtimeTransport || !SameNetworkInstance(n.runtimeNetwork, network) {
		if err := n.installRuntimeTransportLocked(network); err != nil {
			return true, false, len(n.runtimePeerKeys), err, true
		}
	}

	if err := n.syncRuntimePeersLocked(peers, n.runtimeTransport); err != nil {
		return true, n.runtimeTransport, len(n.runtimePeerKeys), err, true
	}
	return true, n.runtimeTransport, len(n.runtimePeerKeys), nil, true
}

func (n *wgConfigNode) closeRuntimeControllerLocked() {
	controller := n.runtimeController
	n.runtimeController = nil
	n.runtimeNetwork = nil
	n.runtimeTransport = false
	n.runtimePeerKeys = nil
	CloseWGCtrl(controller)
}

func (n *wgConfigNode) installRuntimeTransportLocked(network NetworkCloser) error {
	bind := newWireGuardNetworkBind(network, n.runtimeController.BatchSize())
	config := wgdevice.TransportConfig{Bind: bind}
	id := WGConfigTransportID(n.ID)
	oldNetwork := n.runtimeNetwork
	var err error
	if n.runtimeTransport {
		err = n.runtimeController.ReplaceTrackedTransport(id, config)
	} else {
		err = n.runtimeController.AddTrackedTransport(id, config)
	}
	if err != nil {
		_ = bind.Close()
		return fmt.Errorf("install UDP transport %q: %w", id, err)
	}
	n.runtimeNetwork = network
	n.runtimeTransport = true
	if oldNetwork != nil && !SameNetworkInstance(oldNetwork, network) {
		_ = oldNetwork.Close()
	}
	return nil
}

func (n *wgConfigNode) removeRuntimeTransportLocked() error {
	if !n.runtimeTransport || !WGCtrlIsReady(n.runtimeController) {
		n.runtimeTransport = false
		return nil
	}
	err := n.runtimeController.RemoveTrackedTransport(WGConfigTransportID(n.ID))
	if err != nil && !errors.Is(err, wgdevice.ErrTransportNotFound) && !errors.Is(err, wgdevice.ErrDeviceClosed) {
		return fmt.Errorf("remove UDP transport: %w", err)
	}
	n.runtimeTransport = false
	return nil
}

func (n *wgConfigNode) syncRuntimePeersLocked(peers []wireGuardPeerConfig, useTransport bool) error {
	if n.runtimePeerKeys == nil {
		n.runtimePeerKeys = map[wgdevice.NoisePublicKey]struct{}{}
	}
	nextKeys := make(map[wgdevice.NoisePublicKey]struct{}, len(peers))
	specs := make([]wgdevice.PeerSpec, 0, len(peers))
	for _, peer := range peers {
		config, err := wireGuardPeerDeviceConfig(peer)
		if err != nil {
			return err
		}
		spec := wgdevice.PeerSpec{
			PublicKey:                   config.PublicKey,
			PresharedKey:                config.PresharedKey,
			ProtocolVersion:             config.ProtocolVersion,
			PersistentKeepaliveInterval: config.PersistentKeepaliveInterval,
			AllowedIPs:                  slices.Clone(config.AllowedIPs),
			AmneziaWG:                   config.AmneziaWG,
			Activation:                  wgdevice.PeerActivationEager,
		}
		if useTransport && config.Endpoint != "" {
			spec.Endpoint = &wgdevice.PeerEndpoint{
				Transport: WGConfigTransportID(n.ID),
				Address:   config.Endpoint,
			}
		}
		nextKeys[spec.PublicKey] = struct{}{}
		specs = append(specs, spec)
	}
	for key := range n.runtimePeerKeys {
		if _, keep := nextKeys[key]; keep {
			continue
		}
		if _, err := n.runtimeController.DeleteTrackedPeer(key); err != nil {
			return fmt.Errorf("delete peer: %w", err)
		}
		// Update ownership after each operation. A later failure must not make
		// this successful deletion appear pending again or hide a partial apply.
		delete(n.runtimePeerKeys, key)
	}
	for _, spec := range specs {
		// Upsert can change the device before it reports an error. Claim the key
		// first so a later desired snapshot can always delete a partially applied
		// peer. DeleteTrackedPeer is idempotent when the peer was not created.
		n.runtimePeerKeys[spec.PublicKey] = struct{}{}
		if err := n.runtimeController.UpsertTrackedPeer(spec); err != nil {
			return fmt.Errorf("apply peer: %w", err)
		}
	}
	return nil
}

func (n *wgConfigNode) stopRuntime() {
	n.reconcileMu.Lock()
	n.closeRuntimeControllerLocked()
	n.reconcileMu.Unlock()
}

func (n *wgConfigNode) handleRuntimeResult(msg wgConfigRuntimeResult) {
	if msg.generation != n.generation.Load() {
		return
	}
	n.reconcilePending = false
	n.runtimeWGReady = msg.wgReady
	n.runtimeNetworkReady = msg.networkReady
	n.runtimePeerCount = msg.peerCount
	if msg.err != nil {
		n.runtimeError = msg.err.Error()
		if n.Logger != nil {
			n.Logger.Errf("WGConfig node error: %v", msg.err)
		}
		n.scheduleRetry(msg.generation)
	} else {
		n.runtimeError = ""
		n.retryDelay = wireGuardConfigRetryInitialDelay
	}
	_ = n.updateLabel()
	n.sendStatusBlock()
}

func (n *wgConfigNode) scheduleRetry(generation uint64) {
	if n.W == nil || n.ID == 0 {
		return
	}
	delay := n.retryDelay
	if delay <= 0 {
		delay = wireGuardConfigRetryInitialDelay
	}
	n.retryDelay = min(delay*2, wireGuardConfigRetryMaxDelay)
	go func() {
		timer := time.NewTimer(delay)
		defer timer.Stop()
		<-timer.C
		if generation != n.generation.Load() {
			return
		}
		n.W.SendInbox(pasta.InboxMessage{
			ReceiverNode: n.ID,
			Payload:      wgConfigRetryMessage{generation: generation},
		})
	}()
}

func (n *wgConfigNode) stateText() string {
	switch {
	case n.formError != "" || n.runtimeError != "":
		return "error"
	case n.ctrlLink == 0:
		return "waiting for WireGuard"
	case n.reconcilePending:
		return "starting"
	case !n.runtimeWGReady:
		return "starting"
	case n.netLink == 0 || !n.runtimeNetworkReady:
		return "network detached; peer traffic is dropped"
	default:
		return "up"
	}
}

func (n *wgConfigNode) updateLabel() error {
	return n.SetLabel(n.stateText())
}
