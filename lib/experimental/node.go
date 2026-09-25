package experimentalnodes

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
	"github.com/asciimoth/gonnect"
	"github.com/asciimoth/pasta/pasta"
	"github.com/asciimoth/pasta/pasta/std"
	tor "github.com/asciimoth/tor-driver"

	"github.com/asciimoth/almagest/lib/links"
	"github.com/asciimoth/almagest/lib/nodekit"
	"github.com/asciimoth/almagest/lib/storage"
)

const (
	// NodeTypeXTorDriver is the experimental Tor driver class name.
	NodeTypeXTorDriver = "almagest/XTorDriver"

	xTorOutboundPortName = "out"
	xTorStatusBlockID    = "status"
	xTorControlBlockID   = "control"
	xTorRestartButtonID  = "restart"

	xTorSettingsBlockID   = "Tor settings"
	xTorPoliciesBlockID   = "Network policies"
	xTorNetworksBlockID   = "Networks"
	xTorBridgesBlockID    = "Bridges"
	xTorServicesBlockID   = "Onion services"
	xTorClientAuthBlockID = "Client authorization"
)

// XTorDriverClass creates experimental Tor process nodes. The daemon must pass
// this class explicitly to BuildEngine. BuildEngine binds its Runtime before
// any node is created.
type XTorDriverClass struct {
	Host XTorDriverHostConfig

	runtime   nodekit.Runtime
	newSystem xTorSystemFactory
	start     xTorStartFactory
}

func (XTorDriverClass) ClassName() string        { return NodeTypeXTorDriver }
func (XTorDriverClass) ShortDescription() string { return "Experimental Tor driver" }
func (XTorDriverClass) LongDescription() string {
	return "Provides independently isolated Tor Networks and listen-only v3 onion-service Networks. An optional outbound Almagest Network carries Tor traffic."
}
func (XTorDriverClass) DefaultNodeParams() pasta.NodeClassParams {
	return pasta.NodeClassParams{PrimaryType: links.TypeNetwork, InitialPorts: []pasta.Port{
		xTorNetworkPort("default"),
		nodekit.NetworkPort("right", xTorOutboundPortName),
	}}
}

// BindRuntime returns a copy bound to one engine. It is called only because
// the daemon supplied the class in BuildEngine's extra class list.
func (c XTorDriverClass) BindRuntime(runtime nodekit.Runtime) pasta.NodeClass {
	c.runtime = runtime
	return c
}

func (c XTorDriverClass) NewNode(cfg configer.Config, previous ...*pasta.NodeClassState) (pasta.Node, error) {
	configuration := defaultXTorDriverConfig()
	if cfg != nil {
		if _, err := cfg.Get(configer.Path{xTorDriverConfigurationField}); err == nil {
			if err = configer.Unmarshal(cfg.View(configer.Path{xTorDriverConfigurationField}), &configuration); err != nil {
				return nil, fmt.Errorf("read XTorDriver configuration: %w", err)
			}
		}
	}
	configuration.normalize()
	if err := validateXTorDriverConfig(configuration); err != nil {
		return nil, err
	}
	if err := validateXTorHostCompatibility(configuration, c.Host); err != nil {
		return nil, err
	}
	if xTorConfigGeneratesSecrets(configuration) && !nodekit.CanPersistNodeConfig(c.runtime) {
		return nil, errors.New("XTorDriver cannot generate secrets because durable node configuration persistence is unavailable; configure explicit secret selectors")
	}
	if len(previous) > 0 && previous[0] != nil {
		reconcileXTorClassState(configuration, previous[0])
	}
	newSystem := c.newSystem
	if newSystem == nil {
		newSystem = newDirectXTorSystem
	}
	start := c.start
	if start == nil {
		start = startRealXTorDriver
	}
	return &xTorDriverNode{
		Base:        nodekit.Base{Runtime: c.runtime},
		host:        c.Host,
		config:      configuration,
		newSystem:   newSystem,
		start:       start,
		outputPorts: make(map[string]uint64),
		portOutputs: make(map[uint64]string),
		services:    make(map[string]xTorServiceStatus),
		persistStop: make(chan struct{}),
	}, nil
}

type xTorDriverNode struct {
	nodekit.Base

	host             XTorDriverHostConfig
	config           XTorDriverConfig
	newSystem        xTorSystemFactory
	start            xTorStartFactory
	nodeName         string
	outbound         uint64
	outLink          uint64
	outNetwork       nodekit.NetworkCloser
	outboundRevision uint64
	attachMu         sync.Mutex

	outputPorts map[string]uint64
	portOutputs map[uint64]string
	outputs     nodekit.NetworkOutputCache

	generation  atomic.Uint64
	cancelMu    sync.Mutex
	cancelRun   context.CancelFunc
	runtimeMu   sync.Mutex
	runtime     *xTorRuntime
	reconcileMu sync.Mutex
	nextID      uint64
	generatedMu sync.Mutex
	generated   map[uint64][]xTorGeneratedSecret
	persistNext uint64
	persisting  map[uint64]*xTorPendingPersistence
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
	services                map[string]xTorServiceStatus
}

type xTorServiceStatus struct {
	Address     string
	Publication tor.PublicationState
	Problem     string
}

func (n *xTorDriverNode) OnInit(w *pasta.Workspace, logger pasta.Logger, id uint64, _ string, restored *pasta.NodeInitData, _, _, _, _ bool) error {
	n.InitBaseNode(w, logger, id)
	n.nodeName = nodekit.InitialNodeName(w, id, restored)
	if n.nodeName == "" {
		return errors.New("XTorDriver node name is unavailable during initialization")
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

func (n *xTorDriverNode) OnReady() error {
	if !nodekit.NetworkIsReady(n.currentOutboundNetwork()) {
		n.RequestPort(n.outbound)
	}
	n.scheduleRebuild("workspace ready")
	return nil
}

func (n *xTorDriverNode) OnStop() {
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

func (n *xTorDriverNode) PreLinkAdd(port uint64, linkType, direction string) error {
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

func (n *xTorDriverNode) OnLinkAdd(link, port uint64, linkType, direction string) error {
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

func (n *xTorDriverNode) OnLinkRemoved(link, port uint64, linkType, direction string) error {
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

func (n *xTorDriverNode) OnPortRemoved(port uint64, direction string) error {
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

func (n *xTorDriverNode) OnEvent(event pasta.Event, linkType string, _ []string, direction string) error {
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

func (n *xTorDriverNode) OnInbox(message pasta.InboxMessage) error {
	switch value := message.Payload.(type) {
	case xTorStarted:
		n.handleStarted(value)
	case xTorDriverEvent:
		n.handleDriverEvent(value)
	case xTorServiceEvent:
		n.handleServiceEvent(value)
	case xTorOutboundResult:
		n.handleOutboundResult(value)
	case storage.SecretValueChanged:
		n.handleSecretChanged(value)
	case xTorConfigPersisted:
		n.handleConfigPersisted(value)
	}
	return nil
}

func (n *xTorDriverNode) OnFormularMsg(message any) error {
	switch msg := message.(type) {
	case formular.FormApplyMessage:
		if msg.MenuID == pasta.NodeMenuID(n.ID) && isXTorConfigBlock(msg.BlockID) {
			return n.applyConfigForm(msg.BlockID, msg.Values)
		}
	case formular.ButtonPressMessage:
		if msg.MenuID == pasta.NodeMenuID(n.ID) && msg.BlockID == xTorControlBlockID && msg.ButtonID == xTorRestartButtonID {
			n.scheduleRebuild("menu restart")
		}
	}
	return nil
}

func (n *xTorDriverNode) OnTrigger() error {
	n.scheduleRebuild("node trigger")
	return nil
}

func (n *xTorDriverNode) OnSave(cfg configer.Config) error {
	if err := pasta.DeleteNodeOwnedConfigKeys(cfg); err != nil {
		return err
	}
	return configer.Marshal(cfg.View(configer.Path{xTorDriverConfigurationField}), n.config)
}

func (n *xTorDriverNode) handleSecretChanged(msg storage.SecretValueChanged) {
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

func xTorConfigEqual(a, b XTorDriverConfig) bool {
	aJSON, _ := json.Marshal(a)
	bJSON, _ := json.Marshal(b)
	return slices.Equal(aJSON, bJSON)
}

func (n *xTorDriverNode) currentOutboundNetwork() nodekit.NetworkCloser {
	n.attachMu.Lock()
	defer n.attachMu.Unlock()
	return n.outNetwork
}

func (n *xTorDriverNode) currentOutboundState() (nodekit.NetworkCloser, uint64) {
	n.attachMu.Lock()
	defer n.attachMu.Unlock()
	return n.outNetwork, n.outboundRevision
}

func (n *xTorDriverNode) currentOutboundRevision() uint64 {
	n.attachMu.Lock()
	defer n.attachMu.Unlock()
	return n.outboundRevision
}

func (n *xTorDriverNode) setOutboundNetwork(network nodekit.NetworkCloser) {
	n.attachMu.Lock()
	n.outNetwork = network
	n.outboundRevision++
	n.attachMu.Unlock()
}

func restoredRight(restored *pasta.NodeInitData, index int) uint64 {
	return nodekit.RestoredRight(restored, index)
}

var _ nodekit.RuntimeNodeClass = XTorDriverClass{}
