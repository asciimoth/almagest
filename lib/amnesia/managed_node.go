package amnesianodes

import (
	"context"
	"crypto/tls"
	_ "embed"
	"errors"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/formular"
	"github.com/asciimoth/gonnect/tun"
	"github.com/asciimoth/pasta/pasta"
	"github.com/asciimoth/pasta/pasta/std"
	wgoamnesia "github.com/asciimoth/wgo/amnesia"
	wgdevice "github.com/asciimoth/wgo/device"

	"github.com/asciimoth/almagest/lib/nodekit"
	"github.com/asciimoth/almagest/lib/storage"
	wireguardnodes "github.com/asciimoth/almagest/lib/wireguard"
)

const (
	// NodeTypeAmnesia is the class name of the combined Amnezia client and
	// WireGuard device node.
	NodeTypeAmnesia = "almagest/Amnesia"
)

var (
	// AmnesiaClassLongDescription documents the combined node's ownership and
	// persistence boundaries.
	//go:embed node_amnesia.md
	AmnesiaClassLongDescription string
)

// AmnesiaClass creates nodes that own an Amnezia API client and an ephemeral
// userspace WireGuard device. The node persists only Amnezia client data.
type AmnesiaClass struct {
	Runtime   nodekit.Runtime
	NewClient ClientFactory
}

func (AmnesiaClass) ClassName() string        { return NodeTypeAmnesia }
func (AmnesiaClass) ShortDescription() string { return "Amnesia VPN" }
func (AmnesiaClass) LongDescription() string  { return AmnesiaClassLongDescription }
func (AmnesiaClass) DefaultNodeParams() pasta.NodeClassParams {
	return pasta.NodeClassParams{PrimaryType: wireguardnodes.TypeWGCtrl, InitialPorts: []pasta.Port{
		nodekit.TunPort("left", "Tun"),
		nodekit.NetworkPort("right", "Network"),
	}}
}

func (c AmnesiaClass) NewNode(cfg configer.Config, _ ...*pasta.NodeClassState) (pasta.Node, error) {
	newClient := c.NewClient
	if newClient == nil {
		newClient = func(options wgoamnesia.ClientOptions) (Client, error) {
			return wgoamnesia.NewClient(options)
		}
	}
	var secrets storage.SecretsStorage
	var kv storage.KVManager
	if c.Runtime != nil {
		secrets = c.Runtime.SecretsStorage()
		kv = c.Runtime.KVManager()
	}
	return &managedAmnesiaNode{
		Base:      nodekit.Base{Runtime: c.Runtime},
		config:    readManagedAmnesiaConfig(cfg),
		secrets:   secrets,
		kv:        kv,
		newClient: newClient,
	}, nil
}

// managedAmnesiaNode keeps graph callbacks separate from API and device work.
// Callbacks only update desired state and start a generation-tagged worker.
// managedMu serializes client and wgo calls outside the workspace lock.
type managedAmnesiaNode struct {
	nodekit.Base

	nodeName string
	secrets  storage.SecretsStorage
	kv       storage.KVManager

	tunp uint64
	netp uint64

	tunLink uint64
	netLink uint64

	tun     tun.Tun
	network nodekit.NetworkCloser
	config  managedAmnesiaConfig

	tlsConfig          *tls.Config
	tlsConfigEffective string
	tlsConfigErr       string
	tlsRevision        uint64

	newClient ClientFactory

	generation atomic.Uint64
	cancelMu   sync.Mutex
	cancelRun  context.CancelFunc

	managedMu   sync.Mutex
	nodeStore   storage.KVStore
	ephemeralID string

	runtimeClient        Client
	runtimeConfig        *wgoamnesia.Profile
	runtimeDevice        *wgdevice.Device
	runtimeHTTP          interface{ CloseIdleConnections() }
	runtimeTunSource     tun.Tun
	runtimeNetwork       nodekit.NetworkCloser
	runtimeMaterial      [32]byte
	runtimeProfile       amnesiaProfileStatus
	runtimeFormat        wgoamnesia.InputFormat
	runtimeUUID          string
	runtimePersistent    bool
	runtimeTunConfigured bool
	runtimeID            uint64
	nextRuntimeID        uint64
	runtimeUnsubscribe   func()

	pending          bool
	runtimeReady     bool
	inputFormat      wgoamnesia.InputFormat
	profileStatus    amnesiaProfileStatus
	runtimeStats     wgdevice.RuntimeStats
	installationUUID string
	formError        string
	runtimeError     string
	loggedState      string
}

type managedAmnesiaRuntimeRequest struct {
	generation   uint64
	config       managedAmnesiaConfig
	tunLinked    bool
	tun          tun.Tun
	network      nodekit.NetworkCloser
	tlsConfig    *tls.Config
	tlsConfigErr string
	tlsRevision  uint64
	nodeName     string
	ctx          context.Context
}

type managedAmnesiaRuntimeResult struct {
	generation       uint64
	runtimeID        uint64
	waitingForTun    bool
	prepared         bool
	ready            bool
	format           wgoamnesia.InputFormat
	profile          amnesiaProfileStatus
	stats            wgdevice.RuntimeStats
	installationUUID string
	persistent       bool
	err              error
}

type managedAmnesiaStats struct {
	generation uint64
	stats      wgdevice.RuntimeStats
}

func (n *managedAmnesiaNode) OnInit(w *pasta.Workspace, logger pasta.Logger, id uint64, className string, restored *pasta.NodeInitData, _, _, _, _ bool) error {
	n.InitBaseNode(w, logger, id)
	n.nodeName = className
	if n.W != nil {
		if snapshot, ok := n.W.NodeSnapshotLocked(n.ID); ok && snapshot.Name != "" {
			n.nodeName = snapshot.Name
		}
	}
	if !validAmnesiaStorageID(n.config.storageID) {
		n.config.storageID = amnesiaStorageID(n.nodeName)
	}
	n.tunp = nodekit.RestoredLeft(restored, 0)
	n.netp = nodekit.RestoredRight(restored, 0)
	n.nodeStore = amnesiaNodeStore(n.kv, n.config.storageID)
	n.refreshTLSConfig()
	if err := n.SetPrimary(wireguardnodes.TypeWGCtrl); err != nil {
		return err
	}
	n.logDebugf("Amnesia initialized: node=%d name=%q restored=%t storage_id=%s", n.ID, n.nodeName, restored != nil, n.config.storageID)
	_ = n.updateLabel()
	n.sendMenuSnapshot()
	return nil
}

func (n *managedAmnesiaNode) OnReady() error {
	n.sendTunConfig()
	n.RequestPort(n.tunp)
	n.RequestPort(n.netp)
	n.scheduleReconcile(true)
	return nil
}

func (n *managedAmnesiaNode) OnStop() {
	n.generation.Add(1)
	n.cancelCurrentRun()
	go n.stopRuntime()
}

func (n *managedAmnesiaNode) PreLinkAdd(port uint64, linkType, portDirection string) error {
	switch port {
	case n.tunp:
		if portDirection != "left" {
			return pasta.LinkTypeErr(linkType)
		}
		return nodekit.TunPreLinkAdd(n.W, port, linkType)
	case n.netp:
		if portDirection != "right" || linkType != nodekit.TypeNetwork {
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

func (n *managedAmnesiaNode) OnLinkAdd(link, port uint64, linkType, portDirection string) error {
	switch {
	case port == n.tunp && portDirection == "left" && linkType == nodekit.TypeTun:
		n.tunLink = link
		n.sendTunConfigToLink(link)
		std.RequestLocked(n.W, n.ID, link)
	case port == n.netp && portDirection == "right" && linkType == nodekit.TypeNetwork:
		n.netLink = link
		std.RequestLocked(n.W, n.ID, link)
	}
	n.scheduleReconcile(true)
	return nil
}

func (n *managedAmnesiaNode) OnLinkRemoved(link, port uint64, linkType, portDirection string) error {
	switch {
	case port == n.tunp && portDirection == "left" && linkType == nodekit.TypeTun && link == n.tunLink:
		n.tunLink = 0
		n.tun = nil
	case port == n.netp && portDirection == "right" && linkType == nodekit.TypeNetwork && link == n.netLink:
		n.netLink = 0
		n.network = nil
	}
	n.scheduleReconcile(true)
	return nil
}

func (n *managedAmnesiaNode) OnEvent(event pasta.Event, linkType string, _ []string, receiverPortDirection string) error {
	if event.ReceiverPort == n.tunp && receiverPortDirection == "left" && linkType == nodekit.TypeTun {
		if std.IsRequest(event.Payload) {
			n.sendTunConfigToLink(event.Link)
			return nil
		}
		if event.Link != n.tunLink {
			return nil
		}
		next, changed := nodekit.AcceptTunPayload(n.W, n.ID, event, n.tunLink, n.tun)
		if changed {
			n.tun = next
			n.scheduleReconcile(true)
		}
		return nil
	}
	if event.ReceiverPort == n.netp && receiverPortDirection == "right" && linkType == nodekit.TypeNetwork {
		if std.IsRequest(event.Payload) || event.Link != n.netLink {
			return nil
		}
		next, changed := nodekit.AcceptNetworkPayload(n.W, n.ID, event, n.network)
		if changed {
			n.network = next
			n.scheduleReconcile(true)
		}
	}
	return nil
}

func (n *managedAmnesiaNode) OnInbox(message pasta.InboxMessage) error {
	switch msg := message.Payload.(type) {
	case managedAmnesiaRuntimeResult:
		n.handleRuntimeResult(msg)
	case managedAmnesiaStats:
		if msg.generation == n.generation.Load() {
			n.runtimeStats = msg.stats
			n.sendStatusBlock()
		}
	case storage.SecretValueChanged:
		n.handleSecretChanged(msg)
	case storage.TlsConfigChanged:
		n.handleTLSConfigChanged(msg)
	}
	return nil
}

func (n *managedAmnesiaNode) OnFormularMsg(message any) error {
	switch msg := message.(type) {
	case formular.FormApplyMessage:
		if msg.MenuID == pasta.NodeMenuID(n.ID) && msg.BlockID == "config" {
			return n.commitConfig(msg.Values)
		}
	case formular.FieldValidateMessage:
		if msg.MenuID == pasta.NodeMenuID(n.ID) && msg.Field.BlockID == "config" {
			n.validateConfigField(msg)
		}
	}
	return nil
}

func (n *managedAmnesiaNode) OnSave(cfg configer.Config) error {
	if err := pasta.DeleteNodeOwnedConfigKeys(cfg); err != nil {
		return err
	}
	return configer.SetMap(cfg, map[string]any{
		fieldEnabled:                n.config.enabled,
		fieldConnectionSecret:       n.config.connectionSecret,
		fieldServerCountryCode:      n.config.serverCountryCode,
		nodekit.TLSConfigFieldID:    n.config.tlsConfigName,
		fieldGatewayURL:             n.config.gatewayURL,
		fieldGatewayPublicKeySecret: n.config.gatewayPublicKeySecret,
		fieldStorageID:              n.config.storageID,
	})
}

func (n *managedAmnesiaNode) commitConfig(values map[string]any) error {
	next := n.config
	applyManagedAmnesiaConfigValues(&next, values)
	if err := validateManagedAmnesiaConfig(next); err != nil {
		n.formError = err.Error()
		n.sendForcedMenuSnapshot()
		_ = n.updateLabel()
		return nil
	}
	if next == n.config {
		n.formError = ""
		n.sendForcedMenuSnapshot()
		return nil
	}
	tlsChanged := next.tlsConfigName != n.config.tlsConfigName
	n.config = next
	n.formError = ""
	if tlsChanged {
		n.refreshTLSConfig()
		n.tlsRevision++
	}
	n.sendForcedMenuSnapshot()
	n.scheduleReconcile(true)
	return nil
}

func (n *managedAmnesiaNode) validateConfigField(msg formular.FieldValidateMessage) {
	next := n.config
	applyManagedAmnesiaConfigValues(&next, map[string]any{msg.Field.FieldID: msg.Value})
	var err error
	switch msg.Field.FieldID {
	case fieldConnectionSecret:
		err = n.validateConnectionSecret(next.connectionSecret)
	case fieldGatewayPublicKeySecret:
		err = n.validateGatewayPublicKeySecret(next.gatewayPublicKeySecret)
	default:
		err = validateManagedAmnesiaConfigField(next, msg.Field.FieldID)
	}
	nodekit.SendNodeFieldStatus(n.W, n.ID, msg.Field, err)
}

func (n *managedAmnesiaNode) validateConnectionSecret(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return errors.New("connection secret is required")
	}
	value, err := n.readSecret(name, n.currentNodeName())
	if err != nil {
		return err
	}
	_, err = wgoamnesia.ParseInput(value)
	return err
}

func (n *managedAmnesiaNode) validateGatewayPublicKeySecret(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil
	}
	_, err := n.readSecret(name, n.currentNodeName())
	return err
}

func (n *managedAmnesiaNode) handleSecretChanged(msg storage.SecretValueChanged) {
	previous := strings.TrimSpace(msg.SecretNamePrev)
	next := strings.TrimSpace(msg.SecretNameNew)
	relevant := false
	if n.config.connectionSecret == previous || n.config.connectionSecret == next {
		relevant = true
		if n.config.connectionSecret == previous && next != "" {
			n.config.connectionSecret = next
		}
	}
	if n.config.gatewayPublicKeySecret == previous || n.config.gatewayPublicKeySecret == next {
		relevant = true
		if n.config.gatewayPublicKeySecret == previous && next != "" {
			n.config.gatewayPublicKeySecret = next
		}
	}
	if relevant {
		n.sendForcedMenuSnapshot()
		n.scheduleReconcile(true)
	}
}

func (n *managedAmnesiaNode) handleTLSConfigChanged(msg storage.TlsConfigChanged) {
	next, relevant := nodekit.HandleTLSConfigChanged(n.config.tlsConfigName, n.tlsConfigEffective, msg)
	if !relevant {
		n.sendConfigBlock()
		return
	}
	n.config.tlsConfigName = next
	n.refreshTLSConfig()
	n.tlsRevision++
	n.sendForcedMenuSnapshot()
	n.scheduleReconcile(true)
}

func (n *managedAmnesiaNode) refreshTLSConfig() {
	result, err := nodekit.ResolveTLSClientConfig(&n.Base, n.config.tlsConfigName)
	n.tlsConfig = result.Config
	n.tlsConfigEffective = result.EffectiveName
	n.tlsConfigErr = ""
	if err != nil {
		n.tlsConfigErr = err.Error()
	}
}

func (n *managedAmnesiaNode) currentNodeName() string {
	if n.W != nil && n.ID != 0 {
		if snapshot, ok := n.W.NodeSnapshotLocked(n.ID); ok {
			return snapshot.Name
		}
	}
	return n.nodeName
}

func (n *managedAmnesiaNode) scheduleReconcile(invalidate bool) {
	if n.W == nil || n.ID == 0 {
		return
	}
	generation := n.generation.Add(1)
	ctx, cancel := context.WithCancel(n.Context())
	n.cancelMu.Lock()
	if n.cancelRun != nil {
		n.cancelRun()
	}
	n.cancelRun = cancel
	n.cancelMu.Unlock()
	if invalidate {
		n.pending = true
		n.runtimeReady = false
		n.runtimeStats = wgdevice.RuntimeStats{}
		n.runtimeError = ""
	}
	_ = n.updateLabel()
	n.sendStatusBlock()
	request := managedAmnesiaRuntimeRequest{
		generation: generation,
		config:     n.config, tunLinked: n.tunLink != 0, tun: n.tun, network: n.network,
		tlsConfig: nodekit.CloneTLSClientConfig(n.tlsConfig), tlsConfigErr: n.tlsConfigErr,
		tlsRevision: n.tlsRevision, nodeName: n.currentNodeName(), ctx: ctx,
	}
	if err := n.SpawnWorker("Amnesia reconcile", func() {
		result, ok := n.reconcile(request)
		if ok {
			n.W.SendInbox(pasta.InboxMessage{ReceiverNode: n.ID, Payload: result})
		}
	}); err != nil {
		cancel()
		n.W.SendInbox(pasta.InboxMessage{ReceiverNode: n.ID, Payload: managedAmnesiaRuntimeResult{
			generation: generation,
			err:        err,
		}})
	}
}

func (n *managedAmnesiaNode) cancelCurrentRun() {
	n.cancelMu.Lock()
	if n.cancelRun != nil {
		n.cancelRun()
		n.cancelRun = nil
	}
	n.cancelMu.Unlock()
}

func (n *managedAmnesiaNode) handleRuntimeResult(msg managedAmnesiaRuntimeResult) {
	if msg.generation != n.generation.Load() {
		return
	}
	n.pending = false
	n.runtimeReady = false
	n.inputFormat = msg.format
	n.profileStatus = msg.profile
	n.runtimeStats = msg.stats
	if msg.installationUUID != "" {
		n.installationUUID = msg.installationUUID
	}
	n.runtimeError = ""
	if msg.err != nil {
		n.runtimeError = msg.err.Error()
		if n.Logger != nil {
			n.Logger.Errf("Amnesia node error: %v", msg.err)
		}
		n.profileStatus = amnesiaProfileStatus{}
		n.runtimeStats = wgdevice.RuntimeStats{}
		n.sendTunConfig()
	} else if msg.waitingForTun {
		// Keep the negotiated profile while the linked TUN provider replaces its
		// payload. The next TUN event schedules device creation and activation.
	} else if msg.prepared {
		// Configure the graph TUN before the WireGuard device exists. A provider
		// can synchronously replace its TUN and advance the generation here. The
		// next reconcile creates the device with the final configured TUN.
		if !n.markRuntimeTunConfigured(msg.runtimeID) {
			return
		}
		n.sendProfileTunConfig(msg.profile)
		if msg.generation != n.generation.Load() {
			return
		}
		// Some providers apply configuration without replacing their TUN. Run
		// the activation phase explicitly when no replacement event did it.
		n.scheduleReconcile(false)
		return
	} else if msg.ready {
		n.runtimeReady = true
	} else {
		n.profileStatus = amnesiaProfileStatus{}
		n.runtimeStats = wgdevice.RuntimeStats{}
		n.sendTunConfig()
	}
	_ = n.updateLabel()
	n.sendStatusBlock()
}

// markRuntimeTunConfigured advances only the negotiated runtime named by the
// result. A late result must not create a device for a replacement profile.
func (n *managedAmnesiaNode) markRuntimeTunConfigured(runtimeID uint64) bool {
	n.managedMu.Lock()
	defer n.managedMu.Unlock()
	if runtimeID == 0 || runtimeID != n.runtimeID || n.runtimeClient == nil ||
		n.runtimeConfig == nil || n.runtimeDevice != nil {
		return false
	}
	n.runtimeTunConfigured = true
	return true
}

func (n *managedAmnesiaNode) sendTunConfig() {
	if n.tunLink == 0 {
		return
	}
	n.sendTunConfigToLink(n.tunLink)
}

func (n *managedAmnesiaNode) sendTunConfigToLink(link uint64) {
	if n.W == nil || n.ID == 0 || link == 0 || link != n.tunLink {
		return
	}
	if !n.runtimeReady {
		nodekit.EmitTunConfig(n.W, n.ID, link, nodekit.TunConfig{})
		return
	}
	n.sendProfileTunConfig(n.profileStatus)
}

func (n *managedAmnesiaNode) sendProfileTunConfig(profile amnesiaProfileStatus) {
	if n.W == nil || n.ID == 0 || n.tunLink == 0 {
		return
	}
	nodekit.EmitTunConfig(n.W, n.ID, n.tunLink, nodekit.TunConfig{
		Addrs:  append([]string(nil), profile.addresses...),
		Routes: append([]string(nil), profile.allowedIPs...),
		DNS:    firstAmnesiaDNS(profile.dns),
	})
}

func (n *managedAmnesiaNode) stateText() string {
	switch {
	case !n.config.enabled:
		return "disabled"
	case n.formError != "" || n.runtimeError != "":
		return "error"
	case n.config.connectionSecret == "":
		return "waiting for connection secret"
	case n.tunLink == 0:
		return "waiting for TUN"
	case n.netLink == 0:
		return "waiting for network"
	case n.pending || !nodekit.TunIsReady(n.tun) || !nodekit.NetworkIsReady(n.network):
		return "starting"
	case n.runtimeReady:
		return "up"
	default:
		return "starting"
	}
}

func (n *managedAmnesiaNode) updateLabel() error {
	state := n.stateText()
	if state != n.loggedState {
		n.logDebugf("Amnesia state changed: previous=%q current=%q", n.loggedState, state)
		n.loggedState = state
	}
	return n.SetLabel(state)
}

func (n *managedAmnesiaNode) stopRuntime() {
	n.managedMu.Lock()
	n.closeRuntimeLocked()
	n.managedMu.Unlock()
}

func (n *managedAmnesiaNode) readSecret(name, nodeName string) (string, error) {
	if n.secrets == nil {
		return "", errors.New("secret storage is unavailable")
	}
	secret, err := n.secrets.GetSecret(name, nodeName)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(secret.Value) == "" {
		return "", errors.New("secret is empty")
	}
	return secret.Value, nil
}

func (n *managedAmnesiaNode) logDebugf(format string, args ...any) {
	if n.Logger != nil {
		n.Logger.Debugf(format, args...)
	}
}
