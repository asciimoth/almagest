package amnesianodes

import (
	"context"
	"crypto/tls"
	_ "embed"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/formular"
	"github.com/asciimoth/pasta/pasta"
	"github.com/asciimoth/pasta/pasta/std"
	wgoamnesia "github.com/asciimoth/wgo/amnesia"
	wgdevice "github.com/asciimoth/wgo/device"

	"github.com/asciimoth/almagest/lib/nodekit"
	"github.com/asciimoth/almagest/lib/storage"
	wireguardnodes "github.com/asciimoth/almagest/lib/wireguard"
)

const (
	// NodeTypeAmnesiaCtrl is the class name of the AmnesiaCtrl node.
	NodeTypeAmnesiaCtrl = "almagest/AmnesiaCtrl"

	amnesiaCtrlDeprecationWarning = "The AmnesiaCtrl node is deprecated. Use the Amnesia node instead."
	amnesiaImportTimeout          = 2 * time.Minute
)

var (
	// AmnesiaCtrlClassLongDescription is the user-visible description of the
	// AmnesiaCtrl node, including its attached WireGuard identity lifetime and
	// recovery procedure.
	//go:embed node_amnesia_ctrl.md
	AmnesiaCtrlClassLongDescription string

	// ErrCaptchaUnsupported reports that an Amnezia service requested a
	// CAPTCHA from the deprecated AmnesiaCtrl node, which does not support
	// interactive imports.
	ErrCaptchaUnsupported = errors.New("amnezia CAPTCHA is not supported")
)

// Client is the part of wgo/amnesia.Client used by the node. The interface
// permits deterministic control-plane tests without changing production
// behavior.
type Client interface {
	ImportNonInteractive(context.Context, string, wgoamnesia.NegotiationOptions) (*wgoamnesia.Profile, error)
}

// ClientFactory constructs one wgo/amnesia client for one import attempt.
type ClientFactory func(wgoamnesia.ClientOptions) (Client, error)

// AmnesiaCtrlClass creates nodes that import an Amnezia connection and own one
// tracked peer on a shared WireGuard device. A service-key import uses the
// device's current WireGuard identity. The identity remains unchanged across
// imports, reconnects, and daemon restarts until the WireGuard node rotates
// its private key.
type AmnesiaCtrlClass struct {
	Runtime   nodekit.Runtime
	NewClient ClientFactory
}

func (AmnesiaCtrlClass) ClassName() string        { return NodeTypeAmnesiaCtrl }
func (AmnesiaCtrlClass) ShortDescription() string { return "AmnesiaCtrl VPN controller" }
func (AmnesiaCtrlClass) LongDescription() string  { return AmnesiaCtrlClassLongDescription }
func (AmnesiaCtrlClass) DefaultNodeParams() pasta.NodeClassParams {
	return pasta.NodeClassParams{PrimaryType: wireguardnodes.TypeWGCtrl, InitialPorts: []pasta.Port{
		wireguardnodes.WGCtrlPort("right", "WireGuard"),
		nodekit.NetworkPort("right", "Network"),
	}}
}

func (c AmnesiaCtrlClass) NewNode(cfg configer.Config, _ ...*pasta.NodeClassState) (pasta.Node, error) {
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
	return &amnesiaNode{
		Base:      nodekit.Base{Runtime: c.Runtime},
		config:    readAmnesiaConfig(cfg),
		secrets:   secrets,
		kv:        kv,
		newClient: newClient,
	}, nil
}

// amnesiaNode separates Pasta callbacks from HTTP and wgo operations.
// Workspace callbacks update desired state and schedule a generation-tagged
// reconcile. reconcileMu serializes client, tracked-peer, subscription, and
// detached-controller operations outside the workspace lock.
type amnesiaNode struct {
	nodekit.Base

	nodeName string
	secrets  storage.SecretsStorage
	kv       storage.KVManager

	ctrlp uint64
	netp  uint64

	ctrlLink uint64
	netLink  uint64

	controller wireguardnodes.WGCtrlAPI
	network    nodekit.NetworkCloser
	config     amnesiaConfig

	tlsConfig          *tls.Config
	tlsConfigEffective string
	tlsConfigErr       string
	tlsRevision        uint64
	forceRevision      uint64

	newClient ClientFactory

	generation atomic.Uint64
	cancelMu   sync.Mutex
	cancelRun  context.CancelFunc

	reconcileMu sync.Mutex
	nodeStore   storage.KVStore
	ephemeralID string

	runtimeController  wireguardnodes.WGCtrlAPI
	runtimeSession     wgdevice.DeviceAPI
	runtimeNetwork     nodekit.NetworkCloser
	runtimeMaterial    [32]byte
	runtimePeerKey     wgdevice.NoisePublicKey
	runtimeProfile     amnesiaProfileStatus
	runtimeFormat      wgoamnesia.InputFormat
	runtimeUUID        string
	runtimePersistent  bool
	runtimeUnsubscribe func()

	pending            bool
	runtimeReady       bool
	runtimeWGReady     bool
	apiNetworkRequired bool
	inputFormat        wgoamnesia.InputFormat
	profileStatus      amnesiaProfileStatus
	peerStatus         wgdevice.PeerSnapshot
	installationUUID   string
	uuidPersistent     bool
	formError          string
	runtimeError       string
	loggedState        string
}

type amnesiaRuntimeRequest struct {
	generation         uint64
	config             amnesiaConfig
	controller         wireguardnodes.WGCtrlAPI
	network            nodekit.NetworkCloser
	tlsConfig          *tls.Config
	tlsConfigErr       string
	tlsRevision        uint64
	forceRevision      uint64
	nodeName           string
	ctx                context.Context
	setInterfaceConfig func(context.Context, uint64, []string, string) error
}

type amnesiaRuntimeResult struct {
	generation       uint64
	wgReady          bool
	ready            bool
	requiresNetwork  bool
	format           wgoamnesia.InputFormat
	profile          amnesiaProfileStatus
	peer             wgdevice.PeerSnapshot
	installationUUID string
	persistent       bool
	err              error
}

type amnesiaControllerClosed struct {
	controller wireguardnodes.WGCtrlAPI
}

type amnesiaPeerStatus struct {
	generation uint64
	peer       wgdevice.PeerSnapshot
}

type amnesiaWGCtrlInterfaceConfigRequest struct {
	generation uint64
	addresses  []string
	dns        string
	result     chan<- error
}

type amnesiaProfileStatus struct {
	name          string
	serverCountry string
	endpoint      string
	publicKey     string
	addresses     []string
	dns           []string
	allowedIPs    []string
}

func (n *amnesiaNode) OnInit(w *pasta.Workspace, logger pasta.Logger, id uint64, _ string, restored *pasta.NodeInitData, _, _, _, _ bool) error {
	n.InitBaseNode(w, logger, id)
	if w != nil {
		if _, err := w.AddNodePopupLocked(id, pasta.NodePopupWard, amnesiaCtrlDeprecationWarning, true); err != nil {
			return fmt.Errorf("add AmnesiaCtrl deprecation warning: %w", err)
		}
	}
	n.nodeName = nodekit.InitialNodeName(w, id, restored)
	if n.nodeName == "" {
		return errors.New("amnesia controller node name is unavailable during initialization")
	}
	if !validAmnesiaStorageID(n.config.storageID) {
		n.config.storageID = amnesiaStorageID(n.nodeName)
	}
	n.ctrlp = nodekit.RestoredRight(restored, 0)
	n.netp = nodekit.RestoredRight(restored, 1)
	n.nodeStore = amnesiaNodeStore(n.kv, n.config.storageID)
	n.refreshTLSConfig()
	n.logDebugf(
		"Amnesia initialized: node=%d name=%q restored=%t storage_id=%s enabled=%t connection_secret_configured=%t country=%q connect_event=%t tls_configured=%t custom_gateway=%t gateway_public_key_configured=%t",
		n.ID, n.nodeName, restored != nil, n.config.storageID, n.config.enabled,
		n.config.connectionSecret != "", n.config.serverCountryCode, n.config.connectEvent,
		n.config.tlsConfigName != "", n.config.gatewayURL != "", n.config.gatewayPublicKeySecret != "",
	)
	if err := n.SetPrimary(wireguardnodes.TypeWGCtrl); err != nil {
		return err
	}
	_ = n.updateLabel()
	n.sendMenuSnapshot()
	return nil
}

func (n *amnesiaNode) OnReady() error {
	n.logDebugf("Amnesia ready callback: requesting controller_port=%d network_port=%d", n.ctrlp, n.netp)
	n.RequestPort(n.ctrlp)
	n.RequestPort(n.netp)
	n.scheduleReconcile(true)
	return nil
}

func (n *amnesiaNode) OnStop() {
	generation := n.generation.Add(1)
	n.logDebugf("Amnesia stopping: invalidated_generation=%d", generation)
	n.cancelCurrentRun()
	go n.stopRuntime()
}

func (n *amnesiaNode) PreLinkAdd(port uint64, linkType, portDirection string) error {
	var err error
	switch port {
	case n.ctrlp:
		err = wireguardnodes.WGCtrlPreLinkAdd(n.W, port, linkType, portDirection)
	case n.netp:
		if portDirection != "right" || linkType != nodekit.TypeNetwork {
			err = pasta.LinkTypeErr(linkType)
		} else if n.PortHasLinks(port) {
			err = pasta.ErrLinkDup
		}
	default:
		err = pasta.LinkTypeErr(linkType)
	}
	n.logDebugf("Amnesia link validation: port=%d type=%q direction=%q accepted=%t", port, linkType, portDirection, err == nil)
	return err
}

func (n *amnesiaNode) OnLinkAdd(link, port uint64, linkType, portDirection string) error {
	role := "unknown"
	switch {
	case port == n.ctrlp && portDirection == "right" && linkType == wireguardnodes.TypeWGCtrl:
		role = "WireGuard controller"
		n.ctrlLink = link
		std.RequestLocked(n.W, n.ID, link)
	case port == n.netp && portDirection == "right" && linkType == nodekit.TypeNetwork:
		role = "API network"
		n.netLink = link
		std.RequestLocked(n.W, n.ID, link)
	}
	n.logDebugf("Amnesia link added: role=%q link=%d port=%d type=%q direction=%q", role, link, port, linkType, portDirection)
	n.scheduleReconcile(true)
	return nil
}

func (n *amnesiaNode) OnLinkRemoved(link, port uint64, linkType, portDirection string) error {
	role := "unknown"
	matched := false
	switch {
	case port == n.ctrlp && portDirection == "right" && linkType == wireguardnodes.TypeWGCtrl && link == n.ctrlLink:
		role = "WireGuard controller"
		matched = true
		n.ctrlLink = 0
		n.controller = nil
	case port == n.netp && portDirection == "right" && linkType == nodekit.TypeNetwork && link == n.netLink:
		role = "API network"
		matched = true
		n.netLink = 0
		n.network = nil
	}
	invalidate := port == n.ctrlp || n.inputFormat == wgoamnesia.InputFormatUnknown || n.apiNetworkRequired
	n.logDebugf("Amnesia link removed: role=%q link=%d port=%d type=%q direction=%q matched=%t invalidate=%t", role, link, port, linkType, portDirection, matched, invalidate)
	n.scheduleReconcile(invalidate)
	return nil
}

func (n *amnesiaNode) OnEvent(event pasta.Event, linkType string, _ []string, receiverPortDirection string) error {
	if event.ReceiverPort == n.ctrlp && receiverPortDirection == "right" && linkType == wireguardnodes.TypeWGCtrl {
		if event.Link != n.ctrlLink {
			n.logDebugf("Amnesia ignored stale controller event: link=%d current_link=%d payload_type=%T", event.Link, n.ctrlLink, event.Payload)
			return nil
		}
		next, changed := wireguardnodes.AcceptWGCtrlPayload(n.W, event, n.ctrlLink, n.controller)
		if !changed {
			n.logDebugf("Amnesia ignored unchanged controller event: link=%d payload_type=%T", event.Link, event.Payload)
			return nil
		}
		n.controller = next
		n.logDebugf("Amnesia accepted controller event: link=%d ready=%t", event.Link, wireguardnodes.WGCtrlIsReady(next))
		n.watchController(next)
		n.scheduleReconcile(true)
		return nil
	}
	if event.ReceiverPort == n.netp && receiverPortDirection == "right" && linkType == nodekit.TypeNetwork {
		if std.IsRequest(event.Payload) || event.Link != n.netLink {
			n.logDebugf("Amnesia ignored network event: link=%d current_link=%d request=%t payload_type=%T", event.Link, n.netLink, std.IsRequest(event.Payload), event.Payload)
			return nil
		}
		next, changed := nodekit.AcceptNetworkPayload(n.W, n.ID, event, n.network)
		if !changed {
			n.logDebugf("Amnesia ignored unchanged network event: link=%d payload_type=%T", event.Link, event.Payload)
			return nil
		}
		n.network = next
		invalidate := n.inputFormat == wgoamnesia.InputFormatUnknown || n.apiNetworkRequired
		n.logDebugf("Amnesia accepted network event: link=%d ready=%t invalidate=%t", event.Link, nodekit.NetworkIsReady(next), invalidate)
		n.scheduleReconcile(invalidate)
	}
	return nil
}

func (n *amnesiaNode) OnInbox(message pasta.InboxMessage) error {
	switch msg := message.Payload.(type) {
	case amnesiaRuntimeResult:
		n.handleRuntimeResult(msg)
	case amnesiaWGCtrlInterfaceConfigRequest:
		n.handleWGCtrlInterfaceConfigRequest(msg)
	case amnesiaPeerStatus:
		n.handlePeerStatus(msg)
	case amnesiaControllerClosed:
		if wireguardnodes.SameWGCtrlInstance(n.controller, msg.controller) {
			n.logDebugf("Amnesia controller closed: current=true")
			n.controller = nil
			n.scheduleReconcile(true)
		} else {
			n.logDebugf("Amnesia ignored stale controller close: current=false")
		}
	case storage.SecretValueChanged:
		n.handleSecretChanged(msg)
	case storage.TlsConfigChanged:
		n.handleTLSConfigChanged(msg)
	}
	return nil
}

func (n *amnesiaNode) OnFormularMsg(message any) error {
	switch msg := message.(type) {
	case formular.FormApplyMessage:
		if msg.MenuID != pasta.NodeMenuID(n.ID) || msg.BlockID != "config" {
			return nil
		}
		return n.commitConfig(msg.Values)
	case formular.FieldValidateMessage:
		if msg.MenuID == pasta.NodeMenuID(n.ID) && msg.Field.BlockID == "config" {
			n.validateConfigField(msg)
		}
	case formular.ButtonPressMessage:
		if msg.MenuID == pasta.NodeMenuID(n.ID) && msg.BlockID == "config" && msg.ButtonID == "reconnect" {
			n.forceRevision++
			n.logDebugf("Amnesia reconnect requested: force_revision=%d", n.forceRevision)
			n.scheduleReconcile(true)
		}
	}
	return nil
}

func (n *amnesiaNode) OnSave(cfg configer.Config) error {
	n.logDebugf("Amnesia saving config: storage_id=%s", n.config.storageID)
	if err := pasta.DeleteNodeOwnedConfigKeys(cfg); err != nil {
		return err
	}
	return configer.SetMap(cfg, map[string]any{
		fieldEnabled:                n.config.enabled,
		fieldConnectionSecret:       n.config.connectionSecret,
		fieldServerCountryCode:      n.config.serverCountryCode,
		fieldConnectEvent:           n.config.connectEvent,
		nodekit.TLSConfigFieldID:    n.config.tlsConfigName,
		fieldGatewayURL:             n.config.gatewayURL,
		fieldGatewayPublicKeySecret: n.config.gatewayPublicKeySecret,
		fieldStorageID:              n.config.storageID,
	})
}

func (n *amnesiaNode) commitConfig(values map[string]any) error {
	next := n.config
	applyAmnesiaConfigValues(&next, values)
	if err := validateAmnesiaConfig(next); err != nil {
		n.logDebugf("Amnesia config rejected: submitted_fields=%s error=%v", amnesiaSubmittedFields(values), err)
		n.formError = err.Error()
		n.sendForcedMenuSnapshot()
		_ = n.updateLabel()
		return nil
	}
	if next.equal(n.config) {
		n.logDebugf("Amnesia config unchanged: submitted_fields=%s", amnesiaSubmittedFields(values))
		n.formError = ""
		n.sendForcedMenuSnapshot()
		return nil
	}
	tlsChanged := next.tlsConfigName != n.config.tlsConfigName
	changedFields := amnesiaChangedConfigFields(n.config, next)
	n.config = next
	n.logDebugf(
		"Amnesia config committed: changed_fields=%s enabled=%t connection_secret_configured=%t country=%q connect_event=%t tls_configured=%t custom_gateway=%t gateway_public_key_configured=%t",
		changedFields, n.config.enabled, n.config.connectionSecret != "", n.config.serverCountryCode,
		n.config.connectEvent, n.config.tlsConfigName != "", n.config.gatewayURL != "",
		n.config.gatewayPublicKeySecret != "",
	)
	n.formError = ""
	if tlsChanged {
		n.refreshTLSConfig()
		n.tlsRevision++
	}
	n.sendForcedMenuSnapshot()
	n.scheduleReconcile(true)
	return nil
}

func (n *amnesiaNode) validateConfigField(msg formular.FieldValidateMessage) {
	next := n.config
	values := map[string]any{msg.Field.FieldID: msg.Value}
	applyAmnesiaConfigValues(&next, values)
	var err error
	switch msg.Field.FieldID {
	case fieldConnectionSecret:
		err = n.validateConnectionSecret(next.connectionSecret)
	case fieldGatewayPublicKeySecret:
		err = n.validateGatewayPublicKeySecret(next.gatewayPublicKeySecret)
	default:
		err = validateAmnesiaConfigField(next, msg.Field.FieldID)
	}
	nodekit.SendNodeFieldStatus(n.W, n.ID, msg.Field, err)
	n.logDebugf("Amnesia config field validated: field=%q valid=%t", msg.Field.FieldID, err == nil)
}

func (n *amnesiaNode) validateConnectionSecret(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return errors.New("connection secret is required")
	}
	if n.secrets == nil {
		return errors.New("secret storage is unavailable")
	}
	secret, err := n.secrets.GetSecret(name, n.currentNodeName())
	if err != nil {
		return err
	}
	_, err = wgoamnesia.ParseInput(secret.Value)
	return err
}

func (n *amnesiaNode) validateGatewayPublicKeySecret(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil
	}
	if n.secrets == nil {
		return errors.New("secret storage is unavailable")
	}
	secret, err := n.secrets.GetSecret(name, n.currentNodeName())
	if err != nil {
		return err
	}
	if strings.TrimSpace(secret.Value) == "" {
		return errors.New("gateway public key secret is empty")
	}
	return nil
}

func (n *amnesiaNode) handleSecretChanged(msg storage.SecretValueChanged) {
	prev := strings.TrimSpace(msg.SecretNamePrev)
	next := strings.TrimSpace(msg.SecretNameNew)
	relevant := false
	if n.config.connectionSecret == prev || n.config.connectionSecret == next {
		relevant = true
		if n.config.connectionSecret == prev && next != "" {
			n.config.connectionSecret = next
		}
	}
	if n.config.gatewayPublicKeySecret == prev || n.config.gatewayPublicKeySecret == next {
		relevant = true
		if n.config.gatewayPublicKeySecret == prev && next != "" {
			n.config.gatewayPublicKeySecret = next
		}
	}
	if !relevant {
		return
	}
	n.logDebugf(
		"Amnesia relevant secret event: connection_secret=%t gateway_public_key=%t renamed=%t",
		n.config.connectionSecret == prev || n.config.connectionSecret == next,
		n.config.gatewayPublicKeySecret == prev || n.config.gatewayPublicKeySecret == next,
		prev != next && next != "",
	)
	n.sendForcedMenuSnapshot()
	n.scheduleReconcile(true)
}

func (n *amnesiaNode) handleTLSConfigChanged(msg storage.TlsConfigChanged) {
	next, relevant := nodekit.HandleTLSConfigChanged(n.config.tlsConfigName, n.tlsConfigEffective, msg)
	if !relevant {
		n.logDebugf("Amnesia ignored unrelated TLS config event")
		n.sendConfigBlock()
		return
	}
	n.config.tlsConfigName = next
	n.refreshTLSConfig()
	n.tlsRevision++
	n.logDebugf("Amnesia TLS config event applied: revision=%d configured=%t valid=%t", n.tlsRevision, next != "", n.tlsConfigErr == "")
	n.sendForcedMenuSnapshot()
	invalidate := n.inputFormat == wgoamnesia.InputFormatUnknown || n.apiNetworkRequired
	n.scheduleReconcile(invalidate)
}

func (n *amnesiaNode) refreshTLSConfig() {
	result, err := nodekit.ResolveTLSClientConfig(&n.Base, n.config.tlsConfigName)
	n.tlsConfig = result.Config
	n.tlsConfigEffective = result.EffectiveName
	n.tlsConfigErr = ""
	if err != nil {
		n.tlsConfigErr = err.Error()
	}
	n.logDebugf("Amnesia TLS config resolved: configured=%t effective=%t valid=%t", n.config.tlsConfigName != "", n.tlsConfigEffective != "", err == nil)
}

func (n *amnesiaNode) currentNodeName() string {
	if n.W != nil && n.ID != 0 {
		if snapshot, ok := n.W.NodeSnapshotLocked(n.ID); ok {
			return snapshot.Name
		}
	}
	return n.nodeName
}

func (n *amnesiaNode) watchController(controller wireguardnodes.WGCtrlAPI) {
	if !wireguardnodes.WGCtrlIsReady(controller) || n.W == nil || n.ID == 0 {
		n.logDebugf("Amnesia controller watcher not started: controller_ready=%t workspace_ready=%t", wireguardnodes.WGCtrlIsReady(controller), n.W != nil && n.ID != 0)
		return
	}
	n.logDebugf("Amnesia controller watcher starting")
	_ = n.SpawnWorker("Amnesia controller watcher", func() {
		<-controller.Wait()
		n.logDebugf("Amnesia controller watcher observed close")
		n.W.SendInbox(pasta.InboxMessage{
			ReceiverNode: n.ID,
			Payload:      amnesiaControllerClosed{controller: controller},
		})
	})
}

func (n *amnesiaNode) scheduleReconcile(invalidate bool) {
	if n.W == nil || n.ID == 0 {
		n.logDebugf("Amnesia reconcile not scheduled: workspace unavailable")
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
		n.peerStatus = wgdevice.PeerSnapshot{}
		n.runtimeError = ""
	}
	n.logDebugf(
		"Amnesia reconcile scheduled: generation=%d invalidate=%t enabled=%t controller_ready=%t network_ready=%t connection_secret_configured=%t force_revision=%d tls_revision=%d",
		generation, invalidate, n.config.enabled, wireguardnodes.WGCtrlIsReady(n.controller),
		nodekit.NetworkIsReady(n.network), n.config.connectionSecret != "", n.forceRevision, n.tlsRevision,
	)
	_ = n.updateLabel()
	n.sendStatusBlock()

	request := amnesiaRuntimeRequest{
		generation: generation, config: n.config,
		controller: n.controller, network: n.network,
		tlsConfig: nodekit.CloneTLSClientConfig(n.tlsConfig), tlsConfigErr: n.tlsConfigErr,
		tlsRevision: n.tlsRevision, forceRevision: n.forceRevision,
		nodeName: n.currentNodeName(), ctx: ctx,
		setInterfaceConfig: n.setWGCtrlInterfaceConfig,
	}
	if err := n.SpawnWorker("Amnesia reconcile", func() {
		result, ok := n.reconcile(request)
		if ok {
			n.W.SendInbox(pasta.InboxMessage{ReceiverNode: n.ID, Payload: result})
		} else {
			n.logDebugf("Amnesia reconcile produced no result: generation=%d stale_or_cancelled=true", request.generation)
		}
	}); err != nil {
		n.logDebugf("Amnesia reconcile worker failed to start: generation=%d", generation)
		cancel()
		n.W.SendInboxLocked(pasta.InboxMessage{ReceiverNode: n.ID, Payload: amnesiaRuntimeResult{
			generation: generation, err: err,
		}})
	}
}

func (n *amnesiaNode) setWGCtrlInterfaceConfig(ctx context.Context, generation uint64, addresses []string, dns string) error {
	if n.W == nil || n.ID == 0 {
		return errors.New("workspace is unavailable")
	}
	result := make(chan error, 1)
	n.W.SendInbox(pasta.InboxMessage{ReceiverNode: n.ID, Payload: amnesiaWGCtrlInterfaceConfigRequest{
		generation: generation,
		addresses:  append([]string(nil), addresses...),
		dns:        dns,
		result:     result,
	}})
	select {
	case err := <-result:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (n *amnesiaNode) handleWGCtrlInterfaceConfigRequest(msg amnesiaWGCtrlInterfaceConfigRequest) {
	if msg.generation != n.generation.Load() {
		msg.result <- context.Canceled
		return
	}
	cfg := wireguardnodes.WGCtrlInterfaceConfig{
		OwnedIPs: append([]string(nil), msg.addresses...),
		DNS:      msg.dns,
	}
	n.logDebugf(
		"Amnesia WGCtrl interface update preparing before peer apply: link=%d generation=%d address_count=%d addresses=%v dns=%q",
		n.ctrlLink, msg.generation, len(cfg.OwnedIPs), cfg.OwnedIPs, cfg.DNS,
	)
	if !wireguardnodes.EmitWGCtrlInterfaceConfigApply(n.W, n.ID, n.ctrlLink, cfg, msg.result) {
		msg.result <- errors.New("WireGuard controller link is unavailable")
	}
}

func (n *amnesiaNode) cancelCurrentRun() {
	n.cancelMu.Lock()
	if n.cancelRun != nil {
		n.logDebugf("Amnesia cancelling current reconcile")
		n.cancelRun()
		n.cancelRun = nil
	}
	n.cancelMu.Unlock()
}

func (n *amnesiaNode) handleRuntimeResult(msg amnesiaRuntimeResult) {
	if msg.generation != n.generation.Load() {
		n.logDebugf("Amnesia ignored stale runtime result: result_generation=%d current_generation=%d", msg.generation, n.generation.Load())
		return
	}
	n.pending = false
	n.runtimeWGReady = msg.wgReady
	n.runtimeReady = msg.ready
	n.apiNetworkRequired = msg.requiresNetwork
	n.inputFormat = msg.format
	n.profileStatus = msg.profile
	n.peerStatus = msg.peer
	if msg.installationUUID != "" {
		n.installationUUID = msg.installationUUID
		n.uuidPersistent = msg.persistent
	}
	n.runtimeError = ""
	if msg.err != nil {
		n.runtimeError = msg.err.Error()
		if n.Logger != nil {
			n.Logger.Errf("AmnesiaCtrl node error: %v", msg.err)
		}
	}
	n.logDebugf(
		"Amnesia runtime result applied: generation=%d ready=%t wireguard_ready=%t network_required=%t format=%s uuid=%q uuid_persistent=%t error=%t",
		msg.generation, msg.ready, msg.wgReady, msg.requiresNetwork, msg.format,
		n.installationUUID, n.uuidPersistent, msg.err != nil,
	)
	n.sendWGCtrlInterfaceConfig()
	_ = n.updateLabel()
	n.sendStatusBlock()
}

func (n *amnesiaNode) sendWGCtrlInterfaceConfig() {
	if n.W == nil || n.ID == 0 || n.ctrlLink == 0 {
		n.logDebugf("Amnesia WireGuard interface config not sent: workspace_ready=%t controller_link=%d", n.W != nil && n.ID != 0, n.ctrlLink)
		return
	}
	var cfg wireguardnodes.WGCtrlInterfaceConfig
	action := "withdraw"
	if n.runtimeReady {
		cfg.OwnedIPs = append([]string(nil), n.profileStatus.addresses...)
		cfg.DNS = firstAmnesiaDNS(n.profileStatus.dns)
		if !cfg.IsZero() {
			action = "replace"
		}
	}
	n.logDebugf(
		"Amnesia WGCtrl interface update preparing: link=%d action=%s runtime_ready=%t address_count=%d addresses=%v dns=%q",
		n.ctrlLink, action, n.runtimeReady, len(cfg.OwnedIPs), cfg.OwnedIPs, cfg.DNS,
	)
	emitted := wireguardnodes.EmitWGCtrlInterfaceConfig(n.W, n.ID, n.ctrlLink, cfg)
	n.logDebugf(
		"Amnesia WGCtrl interface update submitted: link=%d action=%s address_count=%d addresses=%v dns=%q emitted=%t",
		n.ctrlLink, action, len(cfg.OwnedIPs), cfg.OwnedIPs, cfg.DNS, emitted,
	)
}

func firstAmnesiaDNS(values []string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

func (n *amnesiaNode) handlePeerStatus(msg amnesiaPeerStatus) {
	if msg.generation != n.generation.Load() {
		n.logDebugf("Amnesia ignored stale peer status: result_generation=%d current_generation=%d", msg.generation, n.generation.Load())
		return
	}
	n.peerStatus = msg.peer
	n.logDebugf(
		"Amnesia peer status changed: generation=%d peer_public_key=%s active=%t connected=%t rx_bytes=%d tx_bytes=%d latest_handshake=%s revision=%d",
		msg.generation, amnesiaPublicKeyText(msg.peer.Spec.PublicKey), msg.peer.Active, msg.peer.Connected,
		msg.peer.RxBytes, msg.peer.TxBytes, amnesiaTimeText(msg.peer.LastHandshakeTime), msg.peer.Revision,
	)
	n.sendStatusBlock()
}

func (n *amnesiaNode) stateText() string {
	switch {
	case !n.config.enabled:
		return "disabled"
	case n.formError != "":
		return "error"
	case n.ctrlLink == 0:
		return "waiting for WireGuard"
	case n.config.connectionSecret == "":
		return "waiting for connection secret"
	case n.apiNetworkRequired && n.netLink == 0:
		return "waiting for API network"
	case n.runtimeError != "":
		return "error"
	case n.pending || !n.runtimeWGReady:
		return "starting"
	case n.runtimeReady:
		return "up"
	default:
		return "starting"
	}
}

func (n *amnesiaNode) updateLabel() error {
	state := n.stateText()
	if state != n.loggedState {
		n.logDebugf("Amnesia state changed: previous=%q current=%q", n.loggedState, state)
		n.loggedState = state
	}
	return n.SetLabel(state)
}

func (n *amnesiaNode) stopRuntime() {
	n.logDebugf("Amnesia runtime shutdown starting")
	n.reconcileMu.Lock()
	n.closeRuntimeControllerLocked()
	n.reconcileMu.Unlock()
	n.logDebugf("Amnesia runtime shutdown complete")
}

func (n *amnesiaNode) logDebugf(format string, args ...any) {
	if n.Logger != nil {
		n.Logger.Debugf(format, args...)
	}
}

func formatRuntimeError(prefix string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", prefix, err)
}

func amnesiaPublicKeyText(key wgdevice.NoisePublicKey) string {
	if key.IsZero() {
		return "none"
	}
	return key.Base64()
}

func amnesiaSubmittedFields(values map[string]any) string {
	fields := make([]string, 0, len(values))
	for _, field := range []string{
		fieldEnabled, fieldConnectionSecret, fieldServerCountryCode, fieldConnectEvent,
		nodekit.TLSConfigFieldID, fieldGatewayURL, fieldGatewayPublicKeySecret,
	} {
		if _, ok := values[field]; ok {
			fields = append(fields, field)
		}
	}
	return strings.Join(fields, ",")
}

func amnesiaChangedConfigFields(previous, next amnesiaConfig) string {
	fields := make([]string, 0, 7)
	if previous.enabled != next.enabled {
		fields = append(fields, fieldEnabled)
	}
	if previous.connectionSecret != next.connectionSecret {
		fields = append(fields, fieldConnectionSecret)
	}
	if previous.serverCountryCode != next.serverCountryCode {
		fields = append(fields, fieldServerCountryCode)
	}
	if previous.connectEvent != next.connectEvent {
		fields = append(fields, fieldConnectEvent)
	}
	if previous.tlsConfigName != next.tlsConfigName {
		fields = append(fields, nodekit.TLSConfigFieldID)
	}
	if previous.gatewayURL != next.gatewayURL {
		fields = append(fields, fieldGatewayURL)
	}
	if previous.gatewayPublicKeySecret != next.gatewayPublicKeySecret {
		fields = append(fields, fieldGatewayPublicKeySecret)
	}
	return strings.Join(fields, ",")
}
