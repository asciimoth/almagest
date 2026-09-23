package httpnodes

import (
	"crypto/tls"
	"strings"

	"github.com/asciimoth/almagest/lib/nodekit"
	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/formular"
	"github.com/asciimoth/pasta/pasta"
	"github.com/asciimoth/pasta/pasta/std"
)

// TODO: More providers
// - myip.ygg

const (
	// NodeTypeFetchAddr is the class name of the public-address lookup node.
	NodeTypeFetchAddr = "almagest/FetchAddr"

	// FetchAddrProviderIPify identifies the api.ipify.org provider.
	FetchAddrProviderIPify = "ipify"
	// FetchAddrProviderIfConfig identifies the ifconfig.co provider.
	FetchAddrProviderIfConfig = "ifconfig.co"

	// DefaultFetchAddrProvider is used when the saved provider is empty or invalid.
	DefaultFetchAddrProvider = FetchAddrProviderIfConfig
)

const (
	fetchAddrStatusReady   = "ready"
	fetchAddrStatusRunning = "checking..."
	fetchAddrStatusSuccess = "success"
	fetchAddrStatusFail    = "fail"
)

// FetchAddrClass creates nodes that query a public connection-information
// service through an attached almagest/network.
type FetchAddrClass struct {
	Runtime Runtime

	// providers is set only by tests that use local HTTP endpoints. Production
	// nodes use defaultFetchAddrProviders.
	providers []fetchAddrProvider
}

func (FetchAddrClass) ClassName() string        { return NodeTypeFetchAddr }
func (FetchAddrClass) ShortDescription() string { return "Fetch public address" }
func (FetchAddrClass) LongDescription() string {
	return "Fetches and normalizes the public IP address, country, city, latitude, and longitude seen through an attached almagest/network."
}

func (FetchAddrClass) DefaultNodeParams() pasta.NodeClassParams {
	return pasta.NodeClassParams{PrimaryType: TypeNetwork, InitialPorts: []pasta.Port{
		NetworkPort("right", "Network"),
		{Direction: "left", Name: "Check", Types: []string{std.TypeTrigger}},
		{Direction: "right", Name: "Success", Types: []string{std.TypeTrigger}},
		{Direction: "right", Name: "Fail", Types: []string{std.TypeTrigger}},
		{Direction: "right", Name: "Public IP", Types: []string{std.TypeString}},
		{Direction: "right", Name: "Country code", Types: []string{std.TypeString}},
		{Direction: "right", Name: "City", Types: []string{std.TypeString}},
		{Direction: "right", Name: "Latitude", Types: []string{std.TypeFloat}},
		{Direction: "right", Name: "Longitude", Types: []string{std.TypeFloat}},
	}}
}

func (c FetchAddrClass) NewNode(cfg configer.Config, _ ...*pasta.NodeClassState) (pasta.Node, error) {
	providers := c.providers
	if len(providers) == 0 {
		providers = defaultFetchAddrProviders
	}
	providers = append([]fetchAddrProvider(nil), providers...)
	provider := normalizeFetchAddrProvider(
		configer.String(cfg, configer.Path{"provider"}, DefaultFetchAddrProvider),
		providers,
	)
	return &fetchAddrNode{
		NodeBase:      NodeBase{Runtime: c.Runtime},
		providers:     providers,
		provider:      provider,
		tlsConfigName: readNodeTLSConfigName(cfg),
		statusText:    fetchAddrStatusReady,
	}, nil
}

// fetchAddrData is the provider-independent value published by the node.
// Empty strings and zero coordinates are the defaults for unavailable fields.
type fetchAddrData struct {
	PublicIP    string
	CountryCode string
	City        string
	Latitude    float64
	Longitude   float64
}

type fetchAddrNode struct {
	NodeBase

	triggerIn    uint64
	networkIn    uint64
	successOut   uint64
	failOut      uint64
	publicIPOut  uint64
	countryOut   uint64
	cityOut      uint64
	latitudeOut  uint64
	longitudeOut uint64

	network   NetworkCloser
	providers []fetchAddrProvider
	provider  string
	data      fetchAddrData

	tlsConfigName      string
	tlsConfig          *tls.Config
	tlsConfigEffective string
	tlsConfigErr       string

	requestID  uint64
	pending    bool
	statusText string
	errText    string

	jobs chan fetchAddrRequest
	stop chan struct{}
}

func (n *fetchAddrNode) OnInit(w *pasta.Workspace, logger pasta.Logger, id uint64, _ string, restored *pasta.NodeInitData, _, _, _, _ bool) error {
	n.InitBaseNode(w, logger, id)
	if len(n.providers) == 0 {
		n.providers = append([]fetchAddrProvider(nil), defaultFetchAddrProviders...)
	}
	n.provider = normalizeFetchAddrProvider(n.provider, n.providers)
	n.refreshFetchAddrTLSConfig()

	n.triggerIn = restoredLeft(restored, 0)
	n.networkIn = restoredRight(restored, 0)
	n.successOut = restoredRight(restored, 1)
	n.failOut = restoredRight(restored, 2)
	n.publicIPOut = restoredRight(restored, 3)
	n.countryOut = restoredRight(restored, 4)
	n.cityOut = restoredRight(restored, 5)
	n.latitudeOut = restoredRight(restored, 6)
	n.longitudeOut = restoredRight(restored, 7)

	if err := n.SetPrimary(TypeNetwork); err != nil {
		return err
	}
	_ = n.updateFetchAddrLabel()
	n.startFetchAddrWorker()
	n.sendFetchAddrMenuSnapshot()
	return nil
}

func (n *fetchAddrNode) OnReady() error {
	n.RequestPort(n.networkIn)
	return nil
}

func (n *fetchAddrNode) OnStop() {
	n.requestID++
	n.stopFetchAddrWorker()
}

func (n *fetchAddrNode) PreLinkAdd(port uint64, linkType, portDirection string) error {
	switch port {
	case n.networkIn:
		if portDirection != "right" || linkType != TypeNetwork {
			return pasta.LinkTypeErr(linkType)
		}
		if n.PortHasLinks(port) {
			return pasta.ErrLinkDup
		}
	case n.triggerIn:
		if portDirection != "left" || linkType != std.TypeTrigger {
			return pasta.LinkTypeErr(linkType)
		}
	case n.successOut, n.failOut:
		if portDirection != "right" || linkType != std.TypeTrigger {
			return pasta.LinkTypeErr(linkType)
		}
	case n.publicIPOut, n.countryOut, n.cityOut:
		if portDirection != "right" || linkType != std.TypeString {
			return pasta.LinkTypeErr(linkType)
		}
	case n.latitudeOut, n.longitudeOut:
		if portDirection != "right" || linkType != std.TypeFloat {
			return pasta.LinkTypeErr(linkType)
		}
	default:
		return pasta.LinkTypeErr(linkType)
	}
	return nil
}

func (n *fetchAddrNode) OnLinkAdd(link, port uint64, _, _ string) error {
	switch port {
	case n.networkIn:
		std.RequestLocked(n.W, n.ID, link)
	case n.publicIPOut, n.countryOut, n.cityOut, n.latitudeOut, n.longitudeOut:
		n.emitFetchAddrDataToLink(port, link)
	}
	return nil
}

func (n *fetchAddrNode) OnLinkRemoved(_ uint64, port uint64, _, _ string) error {
	if port == n.networkIn {
		n.network = nil
		n.resetFetchAddrState()
	}
	return nil
}

func (n *fetchAddrNode) OnEvent(event pasta.Event, linkType string, _ []string, receiverPortDirection string) error {
	if event.ReceiverPort == n.networkIn && receiverPortDirection == "right" && linkType == TypeNetwork {
		next, changed := AcceptNetworkPayload(n.W, n.ID, event, n.network)
		if changed {
			n.network = next
			n.resetFetchAddrState()
		}
		return nil
	}

	if event.ReceiverPort == n.triggerIn && receiverPortDirection == "left" && linkType == std.TypeTrigger && !std.IsRequest(event.Payload) {
		n.checkFetchAddr()
		return nil
	}

	if receiverPortDirection == "right" && std.IsRequest(event.Payload) {
		n.emitFetchAddrDataToLink(event.ReceiverPort, event.Link)
	}
	return nil
}

// OnTrigger starts a check when the workspace directly triggers the node.
func (n *fetchAddrNode) OnTrigger() error {
	n.checkFetchAddr()
	return nil
}

func (n *fetchAddrNode) OnInbox(message pasta.InboxMessage) error {
	switch payload := message.Payload.(type) {
	case fetchAddrResult:
		n.handleFetchAddrResult(payload)
	case TlsConfigChanged:
		n.handleFetchAddrTLSConfigChanged(payload)
	}
	return nil
}

func (n *fetchAddrNode) OnFormularMsg(message any) error {
	switch msg := message.(type) {
	case formular.FieldUpdateMessage:
		n.handleFetchAddrFieldUpdate(msg)
	case formular.ButtonPressMessage:
		if msg.MenuID == pasta.NodeMenuID(n.ID) && msg.BlockID == "check" && msg.ButtonID == "check" {
			n.checkFetchAddr()
		}
	}
	return nil
}

func (n *fetchAddrNode) OnSave(cfg configer.Config) error {
	if err := pasta.DeleteNodeOwnedConfigKeys(cfg); err != nil {
		return err
	}
	return nodekit.SetConfigMap(cfg, map[string]any{
		"provider":  n.provider,
		"tlsConfig": n.tlsConfigName,
	})
}

func (n *fetchAddrNode) handleFetchAddrFieldUpdate(msg formular.FieldUpdateMessage) {
	if msg.MenuID != pasta.NodeMenuID(n.ID) || msg.Field.BlockID != "check" || len(msg.Field.ElementPath) != 0 {
		return
	}
	switch msg.Field.FieldID {
	case "provider":
		value, ok := formular.StringValue(msg.Value)
		if !ok {
			n.sendFetchAddrCheckBlock()
			return
		}
		n.setFetchAddrProvider(value)
	case nodeTLSConfigFieldID:
		value, ok := nodeTLSConfigValueFromAny(msg.Value)
		if !ok {
			n.sendFetchAddrCheckBlock()
			return
		}
		n.setFetchAddrTLSConfig(value)
	}
}

func (n *fetchAddrNode) setFetchAddrProvider(value string) {
	value = normalizeFetchAddrProvider(value, n.providers)
	if n.provider == value {
		n.sendFetchAddrCheckBlock()
		return
	}
	n.provider = value
	n.resetFetchAddrState()
}

func (n *fetchAddrNode) setFetchAddrTLSConfig(value string) {
	value = normalizeNodeTLSConfigName(value)
	if n.tlsConfigName == value {
		n.sendFetchAddrCheckBlock()
		return
	}
	n.tlsConfigName = value
	n.refreshFetchAddrTLSConfig()
	n.resetFetchAddrState()
}

func (n *fetchAddrNode) refreshFetchAddrTLSConfig() {
	result, err := resolveNodeTLSClientConfig(&n.NodeBase, n.tlsConfigName)
	n.tlsConfig = result.config
	n.tlsConfigEffective = result.effectiveName
	n.tlsConfigErr = ""
	if err != nil {
		n.tlsConfigErr = err.Error()
	}
}

func (n *fetchAddrNode) handleFetchAddrTLSConfigChanged(msg TlsConfigChanged) {
	nextName, relevant := handleNodeTLSConfigChanged(n.tlsConfigName, n.tlsConfigEffective, msg)
	if !relevant {
		n.sendFetchAddrCheckBlock()
		return
	}
	n.tlsConfigName = nextName
	n.refreshFetchAddrTLSConfig()
	n.resetFetchAddrState()
}

func normalizeFetchAddrProvider(value string, providers []fetchAddrProvider) string {
	value = strings.TrimSpace(value)
	for _, provider := range providers {
		if provider.name == value {
			return value
		}
	}
	for _, provider := range providers {
		if provider.name == DefaultFetchAddrProvider {
			return provider.name
		}
	}
	if len(providers) > 0 {
		return providers[0].name
	}
	return DefaultFetchAddrProvider
}
