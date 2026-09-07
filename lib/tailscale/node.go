package tailscalenodes

import (
	"context"
	"crypto/tls"
	_ "embed"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/formular"
	"github.com/asciimoth/gonnect"
	"github.com/asciimoth/gonnect/dns"
	"github.com/asciimoth/pasta/pasta"
	"github.com/asciimoth/pasta/pasta/std"
	wgotailscale "github.com/asciimoth/wgo-tailscale"
	wgdevice "github.com/asciimoth/wgo/device"

	dnsnodes "github.com/asciimoth/almagest/lib/dns"
	"github.com/asciimoth/almagest/lib/nodekit"
	"github.com/asciimoth/almagest/lib/storage"
	wireguardnodes "github.com/asciimoth/almagest/lib/wireguard"
)

const (
	// NodeTypeTailscaleCtrl is the class name of the TailscaleCtrl node.
	NodeTypeTailscaleCtrl = "almagest/TailscaleCtrl"

	authBlockID     = "authentication"
	authResumeID    = "checkAuthentication"
	authPopupText   = "Tailscale authorization is required. Open the node menu to continue."
	interactionText = "Tailscale needs user action. Open the node menu for details."
)

var (
	// TailscaleCtrlClassLongDescription documents the control-plane network,
	// WireGuard ownership, MagicDNS, and firewall output contracts.
	//go:embed node_tailscale_ctrl.md
	TailscaleCtrlClassLongDescription string
)

// Client is the wgo-tailscale surface used by TailscaleCtrl. It permits
// deterministic lifecycle tests without replacing production networking.
type Client interface {
	Start(context.Context) error
	Close() error
	Snapshot() wgotailscale.Snapshot
	Subscribe(int) (<-chan wgotailscale.Event, func())
	ResumeInteraction(uint64) error
}

// ClientFactory constructs one wgo-tailscale client.
type ClientFactory func(gonnect.Network, wgdevice.DeviceAPI, wgotailscale.Options) (Client, error)

// TailscaleCtrlClass creates nodes that connect a Tailscale control plane to a
// shared WireGuard device.
type TailscaleCtrlClass struct {
	Runtime   nodekit.Runtime
	NewClient ClientFactory
}

func (TailscaleCtrlClass) ClassName() string        { return NodeTypeTailscaleCtrl }
func (TailscaleCtrlClass) ShortDescription() string { return "Tailscale controller" }
func (TailscaleCtrlClass) LongDescription() string  { return TailscaleCtrlClassLongDescription }
func (TailscaleCtrlClass) DefaultNodeParams() pasta.NodeClassParams {
	return pasta.NodeClassParams{PrimaryType: wireguardnodes.TypeWGCtrl, InitialPorts: []pasta.Port{
		dnsnodes.DNSPort("left", "DNS"),
		wireguardnodes.WGCtrlPort("right", "WireGuard"),
		nodekit.NetworkPort("right", "Network"),
		{Direction: "right", Name: "Firewall", Types: []string{std.TypeObject}},
	}}
}

func (c TailscaleCtrlClass) NewNode(cfg configer.Config, _ ...*pasta.NodeClassState) (pasta.Node, error) {
	newClient := c.NewClient
	if newClient == nil {
		newClient = func(network gonnect.Network, api wgdevice.DeviceAPI, options wgotailscale.Options) (Client, error) {
			return wgotailscale.New(network, api, options)
		}
	}
	var secrets storage.SecretsStorage
	var kv storage.KVManager
	if c.Runtime != nil {
		secrets = c.Runtime.SecretsStorage()
		kv = c.Runtime.KVManager()
	}
	return &tailscaleNode{
		Base:      nodekit.Base{Runtime: c.Runtime},
		config:    readTailscaleConfig(cfg),
		secrets:   secrets,
		kv:        kv,
		newClient: newClient,
		firewall:  emptyFirewallObject(),
	}, nil
}

// tailscaleNode keeps Pasta callbacks separate from wgo-tailscale work. All
// client and device operations run through generation-tagged workers. Runtime
// event messages also carry a client ID, so a late event cannot revive a
// detached or replaced controller.
type tailscaleNode struct {
	nodekit.Base

	nodeName string
	secrets  storage.SecretsStorage
	kv       storage.KVManager
	cache    *tailscaleCache

	dnsp      uint64
	ctrlp     uint64
	netp      uint64
	firewallp uint64

	ctrlLink uint64
	netLink  uint64

	controller wireguardnodes.WGCtrlAPI
	network    nodekit.NetworkCloser
	config     tailscaleConfig

	tlsConfig          *tls.Config
	tlsConfigEffective string
	tlsConfigErr       string

	newClient ClientFactory

	generation atomic.Uint64
	cancelMu   sync.Mutex
	cancelRun  context.CancelFunc

	runtimeMu          sync.Mutex
	runtimeClient      Client
	runtimeController  wireguardnodes.WGCtrlAPI
	runtimeID          uint64
	nextRuntimeID      uint64
	runtimeUnsubscribe func()

	dnsResolver *magicDNSResolver
	dnsProvider dns.Interface
	dnsOutputs  dnsnodes.DNSOutputCache
	firewall    std.Object

	pending             bool
	acceptRuntimeEvents bool
	activeRuntimeID     uint64
	activeRuntimeDone   <-chan struct{}
	snapshot            wgotailscale.Snapshot
	runtimeError        string
	formError           string
	authPopupID         uint64

	wgApplySequence uint64
	wgApplyPending  bool
	wgConfigured    bool
	wgAppliedID     uint64
	wgAppliedConfig wireguardnodes.WGCtrlInterfaceConfig
}

type tailscaleControllerClosed struct {
	controller wireguardnodes.WGCtrlAPI
}

type tailscaleRuntimeRequest struct {
	generation uint64
	config     tailscaleConfig
	controller wireguardnodes.WGCtrlAPI
	network    nodekit.NetworkCloser
	tlsConfig  *tls.Config
	tlsErr     string
	nodeName   string
	ctx        context.Context
}

type tailscaleRuntimeResult struct {
	generation uint64
	runtimeID  uint64
	snapshot   wgotailscale.Snapshot
	done       <-chan struct{}
	activate   chan struct{}
	err        error
}

type tailscaleRuntimeEvent struct {
	runtimeID uint64
	snapshot  wgotailscale.Snapshot
}

type tailscaleWGApplyResult struct {
	runtimeID uint64
	sequence  uint64
	config    wireguardnodes.WGCtrlInterfaceConfig
	err       error
}

type tailscaleInteractionResult struct {
	runtimeID uint64
	err       error
}

func (n *tailscaleNode) OnInit(w *pasta.Workspace, logger pasta.Logger, id uint64, _ string, restored *pasta.NodeInitData, _, _, _, _ bool) error {
	n.InitBaseNode(w, logger, id)
	n.nodeName = nodekit.InitialNodeName(w, id, restored)
	if n.nodeName == "" {
		return errors.New("tailscale node name is unavailable during initialization")
	}
	if !validTailscaleStorageID(n.config.storageID) {
		n.config.storageID = tailscaleStorageID(n.nodeName)
	}
	n.cache = &tailscaleCache{store: tailscaleNodeStore(n.kv, n.config.storageID, n.nodeName)}
	n.dnsp = nodekit.RestoredLeft(restored, 0)
	n.ctrlp = nodekit.RestoredRight(restored, 0)
	n.netp = nodekit.RestoredRight(restored, 1)
	n.firewallp = nodekit.RestoredRight(restored, 2)
	n.dnsResolver = &magicDNSResolver{}
	n.dnsProvider = dns.NewResolverProvider(n.dnsResolver, 0, n.WorkerSpawner)
	dnsnodes.BindDNSResource(n.W, n.ID, 0, n.dnsProvider)
	n.refreshTLSConfig()
	if err := n.SetPrimary(wireguardnodes.TypeWGCtrl); err != nil {
		return err
	}
	_ = n.updateLabel()
	n.sendMenuSnapshot(false)
	return nil
}

func (n *tailscaleNode) OnReady() error {
	if n.controller == nil {
		n.RequestPort(n.ctrlp)
	}
	if !nodekit.NetworkIsReady(n.network) {
		n.RequestPort(n.netp)
	}
	n.sendDNSAll()
	n.sendFirewallAll()
	n.scheduleReconcile()
	return nil
}

func (n *tailscaleNode) OnStop() {
	n.generation.Add(1)
	n.acceptRuntimeEvents = false
	n.cancelCurrentRun()
	n.clearInteractionPopup()
	n.dnsOutputs.CloseAll()
	if n.dnsProvider != nil {
		_ = n.dnsProvider.Close()
		n.dnsProvider = nil
	}
	go n.stopRuntime()
}

func (n *tailscaleNode) PreLinkAdd(port uint64, linkType, portDirection string) error {
	switch port {
	case n.dnsp:
		if portDirection != "left" || linkType != dnsnodes.TypeDNS {
			return pasta.LinkTypeErr(linkType)
		}
	case n.ctrlp:
		return wireguardnodes.WGCtrlPreLinkAdd(n.W, port, linkType, portDirection)
	case n.netp:
		if portDirection != "right" || linkType != nodekit.TypeNetwork {
			return pasta.LinkTypeErr(linkType)
		}
		if n.PortHasLinks(port) {
			return pasta.ErrLinkDup
		}
	case n.firewallp:
		if portDirection != "right" || linkType != std.TypeObject {
			return pasta.LinkTypeErr(linkType)
		}
	default:
		return pasta.LinkTypeErr(linkType)
	}
	return nil
}

func (n *tailscaleNode) OnLinkAdd(link, port uint64, linkType, portDirection string) error {
	switch {
	case port == n.dnsp && portDirection == "left" && linkType == dnsnodes.TypeDNS:
		n.sendDNSToLink(link)
	case port == n.ctrlp && portDirection == "right" && linkType == wireguardnodes.TypeWGCtrl:
		n.ctrlLink = link
		std.RequestLocked(n.W, n.ID, link)
		n.scheduleReconcile()
	case port == n.netp && portDirection == "right" && linkType == nodekit.TypeNetwork:
		n.netLink = link
		std.RequestLocked(n.W, n.ID, link)
		n.scheduleReconcile()
	case port == n.firewallp && portDirection == "right" && linkType == std.TypeObject:
		n.sendFirewallToLink(link)
	}
	return nil
}

func (n *tailscaleNode) OnLinkRemoved(link, port uint64, linkType, portDirection string) error {
	switch {
	case port == n.dnsp && portDirection == "left" && linkType == dnsnodes.TypeDNS:
		n.dnsOutputs.Close(link)
	case port == n.ctrlp && portDirection == "right" && linkType == wireguardnodes.TypeWGCtrl && link == n.ctrlLink:
		n.ctrlLink = 0
		n.controller = nil
		n.scheduleReconcile()
	case port == n.netp && portDirection == "right" && linkType == nodekit.TypeNetwork && link == n.netLink:
		n.netLink = 0
		n.network = nil
		n.scheduleReconcile()
	}
	return nil
}

func (n *tailscaleNode) OnEvent(event pasta.Event, linkType string, _ []string, receiverPortDirection string) error {
	switch {
	case event.ReceiverPort == n.dnsp && receiverPortDirection == "left" && linkType == dnsnodes.TypeDNS && std.IsRequest(event.Payload):
		n.dnsOutputs.RefreshLocked(n.W, n.ID, event.Link, n.dnsProvider)
	case event.ReceiverPort == n.firewallp && receiverPortDirection == "right" && linkType == std.TypeObject && std.IsRequest(event.Payload):
		n.sendFirewallToLink(event.Link)
	case event.ReceiverPort == n.ctrlp && receiverPortDirection == "right" && linkType == wireguardnodes.TypeWGCtrl:
		if event.Link != n.ctrlLink {
			return nil
		}
		next, changed := wireguardnodes.AcceptWGCtrlPayload(n.W, event, n.ctrlLink, n.controller)
		if changed {
			n.controller = next
			n.watchController(next)
			n.scheduleReconcile()
		}
	case event.ReceiverPort == n.netp && receiverPortDirection == "right" && linkType == nodekit.TypeNetwork:
		if event.Link != n.netLink || std.IsRequest(event.Payload) {
			return nil
		}
		next, changed := nodekit.AcceptNetworkPayload(n.W, n.ID, event, n.network)
		if changed {
			n.network = next
			n.scheduleReconcile()
		}
	}
	return nil
}

func (n *tailscaleNode) OnInbox(message pasta.InboxMessage) error {
	switch msg := message.Payload.(type) {
	case tailscaleRuntimeResult:
		n.handleRuntimeResult(msg)
	case tailscaleRuntimeEvent:
		n.handleRuntimeEvent(msg)
	case tailscaleWGApplyResult:
		n.handleWGApplyResult(msg)
	case tailscaleInteractionResult:
		n.handleInteractionResult(msg)
	case tailscaleControllerClosed:
		if wireguardnodes.SameWGCtrlInstance(n.controller, msg.controller) {
			n.controller = nil
			n.scheduleReconcile()
		}
	case storage.SecretValueChanged:
		n.handleSecretChanged(msg)
	case storage.TlsConfigChanged:
		n.handleTLSConfigChanged(msg)
	}
	return nil
}

func (n *tailscaleNode) OnFormularMsg(message any) error {
	switch msg := message.(type) {
	case formular.FormApplyMessage:
		if msg.MenuID == pasta.NodeMenuID(n.ID) && msg.BlockID == "config" {
			return n.commitConfig(msg.Values)
		}
	case formular.FieldValidateMessage:
		if msg.MenuID == pasta.NodeMenuID(n.ID) && msg.Field.BlockID == "config" {
			n.validateConfigField(msg)
		}
	case formular.ButtonPressMessage:
		if msg.MenuID == pasta.NodeMenuID(n.ID) && msg.BlockID == authBlockID && msg.ButtonID == authResumeID {
			n.resumeInteraction()
		}
	}
	return nil
}

func (n *tailscaleNode) OnSave(cfg configer.Config) error {
	if err := pasta.DeleteNodeOwnedConfigKeys(cfg); err != nil {
		return err
	}
	return configer.SetMap(cfg, map[string]any{
		fieldEnabled:             n.config.enabled,
		fieldControlURL:          n.config.controlURL,
		fieldHostname:            n.config.hostname,
		fieldAuthKeySecret:       n.config.authKeySecret,
		fieldEphemeral:           n.config.ephemeral,
		fieldListenPort:          n.config.listenPort,
		fieldDisableDERP:         n.config.disableDERP,
		fieldDisableDiscovery:    n.config.disableDiscovery,
		nodekit.TLSConfigFieldID: n.config.tlsConfigName,
		fieldStorageID:           n.config.storageID,
	})
}

func (n *tailscaleNode) commitConfig(values map[string]any) error {
	next := n.config
	applyTailscaleConfigValues(&next, values)
	if err := validateTailscaleConfig(next); err != nil {
		n.formError = err.Error()
		n.sendMenuSnapshot(true)
		_ = n.updateLabel()
		return nil
	}
	if next == n.config {
		n.formError = ""
		n.sendMenuSnapshot(true)
		return nil
	}
	tlsChanged := next.tlsConfigName != n.config.tlsConfigName
	n.config = next
	n.formError = ""
	if tlsChanged {
		n.refreshTLSConfig()
	}
	n.sendMenuSnapshot(true)
	n.scheduleReconcile()
	return nil
}

func (n *tailscaleNode) validateConfigField(msg formular.FieldValidateMessage) {
	next := n.config
	applyTailscaleConfigValues(&next, map[string]any{msg.Field.FieldID: msg.Value})
	var err error
	if msg.Field.FieldID == fieldAuthKeySecret {
		err = n.validateAuthKeySecret(next.authKeySecret)
	} else {
		err = validateTailscaleConfigField(next, msg.Field.FieldID)
	}
	nodekit.SendNodeFieldStatus(n.W, n.ID, msg.Field, err)
}

func (n *tailscaleNode) validateAuthKeySecret(name string) error {
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
		return errors.New("auth key secret is empty")
	}
	return nil
}

func (n *tailscaleNode) handleSecretChanged(msg storage.SecretValueChanged) {
	previous := strings.TrimSpace(msg.SecretNamePrev)
	next := strings.TrimSpace(msg.SecretNameNew)
	if n.config.authKeySecret != previous && n.config.authKeySecret != next {
		return
	}
	if n.config.authKeySecret == previous && next != "" {
		n.config.authKeySecret = next
	}
	n.sendMenuSnapshot(true)
	n.scheduleReconcile()
}

func (n *tailscaleNode) handleTLSConfigChanged(msg storage.TlsConfigChanged) {
	next, relevant := nodekit.HandleTLSConfigChanged(n.config.tlsConfigName, n.tlsConfigEffective, msg)
	if !relevant {
		n.sendConfigBlock()
		return
	}
	n.config.tlsConfigName = next
	n.refreshTLSConfig()
	n.sendMenuSnapshot(true)
	n.scheduleReconcile()
}

func (n *tailscaleNode) refreshTLSConfig() {
	result, err := nodekit.ResolveTLSClientConfig(&n.Base, n.config.tlsConfigName)
	n.tlsConfig = result.Config
	n.tlsConfigEffective = result.EffectiveName
	n.tlsConfigErr = ""
	if err != nil {
		n.tlsConfigErr = err.Error()
	}
}

func (n *tailscaleNode) currentNodeName() string {
	if n.W != nil && n.ID != 0 {
		if snapshot, ok := n.W.NodeSnapshotLocked(n.ID); ok {
			return snapshot.Name
		}
	}
	return n.nodeName
}

func (n *tailscaleNode) watchController(controller wireguardnodes.WGCtrlAPI) {
	if !wireguardnodes.WGCtrlIsReady(controller) || n.W == nil || n.ID == 0 {
		return
	}
	_ = n.SpawnWorker("Tailscale controller watcher", func() {
		<-controller.Wait()
		n.W.SendInbox(pasta.InboxMessage{ReceiverNode: n.ID, Payload: tailscaleControllerClosed{controller: controller}})
	})
}

func (n *tailscaleNode) readAuthKey(config tailscaleConfig, nodeName string) (string, error) {
	if config.authKeySecret == "" {
		return "", nil
	}
	if n.secrets == nil {
		return "", errors.New("secret storage is unavailable")
	}
	secret, err := n.secrets.GetSecret(config.authKeySecret, nodeName)
	if err != nil {
		return "", fmt.Errorf("read auth key secret: %w", err)
	}
	if strings.TrimSpace(secret.Value) == "" {
		return "", errors.New("auth key secret is empty")
	}
	return strings.TrimSpace(secret.Value), nil
}
