package tornodes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/formular"
	sandbox "github.com/asciimoth/gobox"
	"github.com/asciimoth/gonnect"
	"github.com/asciimoth/pasta/pasta"
	"github.com/asciimoth/pasta/pasta/std"
	tor "github.com/asciimoth/tor-driver"

	"github.com/asciimoth/almagest/lib/links"
	"github.com/asciimoth/almagest/lib/nodekit"
	"github.com/asciimoth/almagest/lib/storage"
)

const (
	// NodeTypeTorDriver is the Tor driver class name.
	NodeTypeTorDriver = "almagest/TorDriver"

	torOutboundPortName = "out"
	torStatusBlockID    = "status"
	torControlBlockID   = "control"
	torRestartButtonID  = "restart"

	torSettingsBlockID   = "Tor settings"
	torPoliciesBlockID   = "Network policies"
	torNetworksBlockID   = "Networks"
	torBridgesBlockID    = "Bridges"
	torServicesBlockID   = "Onion services"
	torClientAuthBlockID = "Client authorization"
)

// TorDriverClass creates Tor process nodes that use an owner-bound Almagest
// sandbox. The sandbox policy selects executable images and host isolation.
type TorDriverClass struct {
	Runtime nodekit.Runtime

	className string
	start     torStartFactory
	sandboxes func(context.Context) (sandbox.OwnerSandboxes, error)
}

func (c TorDriverClass) ClassName() string {
	if c.className != "" {
		return c.className
	}
	return NodeTypeTorDriver
}
func (TorDriverClass) ShortDescription() string { return "Tor driver" }
func (TorDriverClass) LongDescription() string {
	return "Provides independently isolated Tor Networks and listen-only v3 onion-service Networks. An optional outbound Almagest Network carries Tor traffic."
}
func (TorDriverClass) DefaultNodeParams() pasta.NodeClassParams {
	return pasta.NodeClassParams{PrimaryType: links.TypeNetwork, InitialPorts: []pasta.Port{
		torNetworkPort("default"),
		nodekit.NetworkPort("right", torOutboundPortName),
	}}
}

// BindRuntime binds the stable host-facing class to one engine. The daemon
// registers this class unconditionally because its process access is limited
// to the engine's owner-bound sandbox capability.
func (c TorDriverClass) BindRuntime(runtime nodekit.Runtime) pasta.NodeClass {
	c.Runtime = runtime
	return c
}

func (c TorDriverClass) NewNode(cfg configer.Config, previous ...*pasta.NodeClassState) (pasta.Node, error) {
	configuration := defaultTorDriverConfig()
	if cfg != nil {
		if _, err := cfg.Get(configer.Path{torDriverConfigurationField}); err == nil {
			if err = configer.Unmarshal(cfg.View(configer.Path{torDriverConfigurationField}), &configuration); err != nil {
				return nil, fmt.Errorf("read TorDriver configuration: %w", err)
			}
		}
	}
	configuration.normalize()
	if err := validateTorDriverConfig(configuration); err != nil {
		return nil, err
	}
	if torConfigGeneratesSecrets(configuration) && !nodekit.CanPersistNodeConfig(c.Runtime) {
		return nil, errors.New("TorDriver cannot generate secrets because durable node configuration persistence is unavailable; configure explicit secret selectors")
	}
	if len(previous) > 0 && previous[0] != nil {
		reconcileTorClassState(configuration, previous[0])
	}
	start := c.start
	if start == nil {
		start = startRealTorDriver
	}
	return &torDriverNode{
		Base:        nodekit.Base{Runtime: c.Runtime},
		config:      configuration,
		start:       start,
		sandboxes:   c.sandboxes,
		outputPorts: make(map[string]uint64),
		portOutputs: make(map[uint64]string),
		services:    make(map[string]torServiceStatus),
		persistStop: make(chan struct{}),
	}, nil
}

type torDriverNode struct {
	nodekit.Base

	config           TorDriverConfig
	start            torStartFactory
	sandboxes        func(context.Context) (sandbox.OwnerSandboxes, error)
	nodeName         string
	outbound         uint64
	outLink          uint64
	outNetwork       nodekit.NetworkCloser
	outboundRevision uint64
	outboundLatched  bool
	latchedRevision  uint64
	attachMu         sync.Mutex

	outputPorts map[string]uint64
	portOutputs map[uint64]string
	outputs     nodekit.NetworkOutputCache

	generation  atomic.Uint64
	cancelMu    sync.Mutex
	cancelRun   context.CancelFunc
	runtimeMu   sync.Mutex
	runtime     *torRuntime
	reconcileMu sync.Mutex
	nextID      uint64
	generatedMu sync.Mutex
	generated   map[uint64][]torGeneratedSecret
	persistNext uint64
	persisting  map[uint64]*torPendingPersistence
	persistStop chan struct{}

	activeID                uint64
	activeSources           map[string]gonnect.Network
	ready                   bool
	pending                 bool
	stopped                 bool
	terminal                bool
	runtimeError            string
	runtimeErrorRecoverable bool
	formError               string
	formErrorBlock          string
	bootstrap               tor.BootstrapEvent
	transport               tor.TransportEvent
	outboundState           tor.OutboundState
	services                map[string]torServiceStatus
}

type torServiceStatus struct {
	Address     string
	Publication tor.PublicationState
	Problem     string
}

func (n *torDriverNode) OnInit(w *pasta.Workspace, logger pasta.Logger, id uint64, _ string, restored *pasta.NodeInitData, _, _, _, _ bool) error {
	n.InitBaseNode(w, logger, id)
	n.nodeName = nodekit.InitialNodeName(w, id, restored)
	if n.nodeName == "" {
		return errors.New("TorDriver node name is unavailable during initialization")
	}
	n.outbound = restoredRight(restored, 0)
	if err := n.SetPrimary(links.TypeNetwork); err != nil {
		return err
	}
	if err := n.rebuildPorts(); err != nil {
		return err
	}
	_ = n.updateLabel()
	n.sendMenuSnapshot(false)
	return nil
}

func (n *torDriverNode) OnReady() error {
	if !nodekit.NetworkIsReady(n.currentOutboundNetwork()) {
		n.RequestPort(n.outbound)
	}
	n.scheduleRebuild("workspace ready")
	return nil
}

func (n *torDriverNode) OnStop() {
	n.stopped = true
	n.generation.Add(1)
	n.cancelCurrentRun()
	n.discardAllGeneratedSecrets()
	n.discardAllPersistingSecrets()
	close(n.persistStop)
	n.outputs.CloseAll()
	n.activeSources = nil
	go n.closeRuntime()
}

func (n *torDriverNode) PreLinkAdd(port uint64, linkType, direction string) error {
	if linkType != links.TypeNetwork {
		return pasta.LinkTypeErr(linkType)
	}
	if port == n.outbound {
		if direction != "right" {
			return pasta.LinkTypeErr(linkType)
		}
		if n.PortHasLinks(port) {
			return pasta.ErrLinkDup
		}
		return nil
	}
	if n.portOutputs[port] != "" && direction == "left" {
		return nil
	}
	return pasta.LinkTypeErr(linkType)
}

func (n *torDriverNode) OnLinkAdd(link, port uint64, linkType, direction string) error {
	if linkType != links.TypeNetwork {
		return nil
	}
	if port == n.outbound && direction == "right" {
		n.outLink = link
		std.RequestLocked(n.W, n.ID, link)
		return nil
	}
	if key := n.portOutputs[port]; key != "" && direction == "left" {
		n.sendOutputToLink(key, link, false)
	}
	return nil
}

func (n *torDriverNode) OnLinkRemoved(link, port uint64, linkType, direction string) error {
	if linkType != links.TypeNetwork {
		return nil
	}
	if port == n.outbound && direction == "right" && link == n.outLink {
		n.outLink = 0
		n.setOutboundNetwork(nil)
		n.applyOutbound(nil)
		return nil
	}
	if n.portOutputs[port] != "" && direction == "left" {
		n.outputs.Close(link)
	}
	return nil
}

func (n *torDriverNode) OnPortRemoved(port uint64, direction string) error {
	if direction == "right" && port == n.outbound {
		n.outbound = 0
		n.outLink = 0
		n.setOutboundNetwork(nil)
		n.applyOutbound(nil)
		return nil
	}
	if direction == "left" {
		delete(n.outputPorts, n.portOutputs[port])
		delete(n.portOutputs, port)
	}
	return nil
}

func (n *torDriverNode) OnEvent(event pasta.Event, linkType string, _ []string, direction string) error {
	if linkType != links.TypeNetwork {
		return nil
	}
	if event.ReceiverPort == n.outbound && direction == "right" && event.Link == n.outLink {
		if std.IsRequest(event.Payload) {
			return nil
		}
		current := n.currentOutboundNetwork()
		next, changed := nodekit.AcceptNetworkPayload(n.W, n.ID, event, current)
		if changed {
			n.setOutboundNetwork(next)
			n.applyOutbound(next)
		}
		return nil
	}
	if key := n.portOutputs[event.ReceiverPort]; key != "" && direction == "left" && std.IsRequest(event.Payload) {
		n.sendOutputToLink(key, event.Link, true)
	}
	return nil
}

func (n *torDriverNode) OnInbox(message pasta.InboxMessage) error {
	switch value := message.Payload.(type) {
	case torStarted:
		n.handleStarted(value)
	case torDriverEvent:
		n.handleDriverEvent(value)
	case torServiceEvent:
		n.handleServiceEvent(value)
	case torOutboundResult:
		n.handleOutboundResult(value)
	case storage.SecretValueChanged:
		n.handleSecretChanged(value)
	case torConfigPersisted:
		n.handleConfigPersisted(value)
	}
	return nil
}

func (n *torDriverNode) OnFormularMsg(message any) error {
	switch msg := message.(type) {
	case formular.FormApplyMessage:
		if msg.MenuID == pasta.NodeMenuID(n.ID) && isTorConfigBlock(msg.BlockID) {
			return n.applyConfigForm(msg.BlockID, msg.Values)
		}
	case formular.ButtonPressMessage:
		if msg.MenuID == pasta.NodeMenuID(n.ID) && msg.BlockID == torControlBlockID && msg.ButtonID == torRestartButtonID {
			n.scheduleRebuild("menu restart")
		}
	}
	return nil
}

func (n *torDriverNode) OnTrigger() error {
	n.scheduleRebuild("node trigger")
	return nil
}

func (n *torDriverNode) OnSave(cfg configer.Config) error {
	if err := pasta.DeleteNodeOwnedConfigKeys(cfg); err != nil {
		return err
	}
	return configer.Marshal(cfg.View(configer.Path{torDriverConfigurationField}), n.config)
}

func (n *torDriverNode) handleSecretChanged(msg storage.SecretValueChanged) {
	previous := strings.TrimSpace(msg.SecretNamePrev)
	next := strings.TrimSpace(msg.SecretNameNew)
	relevant := false
	configChanged := false
	for i := range n.config.Services {
		if n.config.Services[i].KeySecret != previous && n.config.Services[i].KeySecret != next {
			continue
		}
		relevant = true
		if n.config.Services[i].KeySecret == previous && next != "" {
			n.config.Services[i].KeySecret = next
			configChanged = configChanged || previous != next
		}
	}
	for i := range n.config.ClientAuthorizations {
		if n.config.ClientAuthorizations[i].PrivateKeySecret != previous && n.config.ClientAuthorizations[i].PrivateKeySecret != next {
			continue
		}
		relevant = true
		if n.config.ClientAuthorizations[i].PrivateKeySecret == previous && next != "" {
			n.config.ClientAuthorizations[i].PrivateKeySecret = next
			configChanged = configChanged || previous != next
		}
	}
	if relevant {
		if configChanged {
			n.renameTrackedGeneratedSecret(previous, next)
			_ = n.NotifyConfigChanged()
		}
		n.scheduleRebuild("secret changed")
		n.sendMenuSnapshot(true)
	}
}

func torConfigEqual(a, b TorDriverConfig) bool {
	aJSON, _ := json.Marshal(a)
	bJSON, _ := json.Marshal(b)
	return slices.Equal(aJSON, bJSON)
}

func (n *torDriverNode) currentOutboundNetwork() nodekit.NetworkCloser {
	n.attachMu.Lock()
	defer n.attachMu.Unlock()
	return n.outNetwork
}

func (n *torDriverNode) currentOutboundState() (nodekit.NetworkCloser, uint64) {
	n.attachMu.Lock()
	defer n.attachMu.Unlock()
	return n.outNetwork, n.outboundRevision
}

func (n *torDriverNode) currentOutboundRevision() uint64 {
	n.attachMu.Lock()
	defer n.attachMu.Unlock()
	return n.outboundRevision
}

func (n *torDriverNode) setOutboundNetwork(network nodekit.NetworkCloser) {
	n.attachMu.Lock()
	n.outNetwork = network
	n.outboundRevision++
	n.attachMu.Unlock()
}

func restoredRight(restored *pasta.NodeInitData, index int) uint64 {
	return nodekit.RestoredRight(restored, index)
}

var _ nodekit.RuntimeNodeClass = TorDriverClass{}
