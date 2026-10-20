package networknodes

import (
	"fmt"
	"slices"
	"strings"

	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/formular"
	"github.com/asciimoth/gonnect"
	"github.com/asciimoth/pasta/pasta"
	"github.com/asciimoth/pasta/pasta/std"

	"github.com/asciimoth/almagest/lib/nodekit"
)

const (
	// NodeTypeNetworkFirewall is the node class name for Network firewall
	// middleware.
	NodeTypeNetworkFirewall = "almagest/NetworkFirewall"

	// NetworkFirewallConfigBlockID identifies the editable policy block.
	NetworkFirewallConfigBlockID = "config"
	// NetworkFirewallRulesField identifies the JSON policy field.
	NetworkFirewallRulesField = "rules"
	// NetworkFirewallEffectiveBlockID identifies the computed policy block.
	NetworkFirewallEffectiveBlockID = "effective"
	// NetworkFirewallEffectiveField identifies the computed policy field.
	NetworkFirewallEffectiveField = "config"
)

// NetworkFirewallClass builds middleware based on gonnect.Firewall.
type NetworkFirewallClass struct{}

func (NetworkFirewallClass) ClassName() string { return NodeTypeNetworkFirewall }
func (NetworkFirewallClass) ShortDescription() string {
	return "Network firewall"
}
func (NetworkFirewallClass) LongDescription() string {
	return "Filters operations on a network."
}
func (NetworkFirewallClass) DefaultNodeParams() pasta.NodeClassParams {
	return pasta.NodeClassParams{PrimaryType: TypeNetwork, InitialPorts: []pasta.Port{
		NetworkPort("left", "Network"),
		{Direction: "left", Name: "Config", Types: []string{std.TypeObject}},
		NetworkPort("right", "Network"),
	}}
}
func (NetworkFirewallClass) NewNode(cfg configer.Config, _ ...*pasta.NodeClassState) (pasta.Node, error) {
	return &networkFirewallNode{
		rulesText: nodekit.FirewallConfigTextFromConfig(
			cfg,
			configer.Path{NetworkFirewallRulesField},
		),
		objectConfigs: map[uint64]*gonnect.FirewallConfig{},
		objectErrors:  map[uint64]string{},
		firewalls:     map[uint64]*gonnect.Firewall{},
	}, nil
}

// networkFirewallNode emits one independently detached gonnect.Firewall per
// output link. Policy changes use Firewall.SetConfig, which keeps live network
// wrapper identity stable for existing clients and listeners.
type networkFirewallNode struct {
	NodeBase

	out      uint64
	configIn uint64
	in       uint64
	inLink   uint64

	network NetworkCloser
	outputs NetworkOutputCache

	rulesText     string
	menuConfig    *gonnect.FirewallConfig
	effective     *gonnect.FirewallConfig
	formError     string
	objectConfigs map[uint64]*gonnect.FirewallConfig
	objectErrors  map[uint64]string
	firewalls     map[uint64]*gonnect.Firewall
}

func (n *networkFirewallNode) OnInit(w *pasta.Workspace, logger pasta.Logger, id uint64, _ string, restored *pasta.NodeInitData, _, _, _, _ bool) error {
	n.InitBaseNode(w, logger, id)
	n.out = restoredLeft(restored, 0)
	n.configIn = restoredLeft(restored, 1)
	n.in = restoredRight(restored, 0)
	n.ensureMaps()
	parsed, err := nodekit.ParseFirewallConfigJSON(n.rulesText)
	if err != nil {
		n.formError = err.Error()
		parsed = (&gonnect.FirewallConfig{}).Optimize()
	}
	n.menuConfig = parsed
	n.recomputeEffective()
	if err := n.SetPrimary(TypeNetwork); err != nil {
		return err
	}
	n.sendMenuSnapshot(false)
	return n.updateLabel()
}

func (n *networkFirewallNode) OnReady() error {
	n.RequestPort(n.in)
	n.RequestPort(n.configIn)
	n.sendAll()
	return nil
}

func (n *networkFirewallNode) OnStop() {
	n.outputs.CloseAll()
	clear(n.firewalls)
	_ = closeNetwork(n.network)
	n.network = nil
	n.inLink = 0
}

func (n *networkFirewallNode) PreLinkAdd(port uint64, linkType, portDirection string) error {
	switch {
	case port == n.out && portDirection == "left" && linkType == TypeNetwork:
		return nil
	case port == n.configIn && portDirection == "left" && linkType == std.TypeObject:
		// This input intentionally accepts multiple object links. Each link owns
		// one independent policy contribution.
		return nil
	case port == n.in && portDirection == "right" && linkType == TypeNetwork:
		if n.PortHasLinks(port) {
			return pasta.ErrLinkDup
		}
		return nil
	default:
		return pasta.LinkTypeErr(linkType)
	}
}

func (n *networkFirewallNode) OnLinkAdd(link, port uint64, linkType, portDirection string) error {
	switch {
	case port == n.out && portDirection == "left" && linkType == TypeNetwork:
		n.sendToLink(link, false)
	case port == n.configIn && portDirection == "left" && linkType == std.TypeObject:
		std.RequestLocked(n.W, n.ID, link)
	case port == n.in && portDirection == "right" && linkType == TypeNetwork:
		n.inLink = link
		std.RequestLocked(n.W, n.ID, link)
	}
	return n.updateLabel()
}

func (n *networkFirewallNode) OnLinkRemoved(link uint64, port uint64, linkType, portDirection string) error {
	switch {
	case port == n.out && portDirection == "left" && linkType == TypeNetwork:
		n.outputs.Close(link)
		delete(n.firewalls, link)
	case port == n.configIn && portDirection == "left" && linkType == std.TypeObject:
		delete(n.objectConfigs, link)
		delete(n.objectErrors, link)
		n.installEffectiveConfig()
		n.sendEffectiveBlock()
	case port == n.in && portDirection == "right" && linkType == TypeNetwork && link == n.inLink:
		n.closeOutputs()
		_ = closeNetwork(n.network)
		n.network = nil
		n.inLink = 0
	}
	return n.updateLabel()
}

func (n *networkFirewallNode) OnEvent(event pasta.Event, linkType string, _ []string, receiverPortDirection string) error {
	if event.ReceiverPort == n.out && receiverPortDirection == "left" && linkType == TypeNetwork && std.IsRequest(event.Payload) {
		n.sendToLink(event.Link, true)
		return nil
	}
	if event.ReceiverPort == n.configIn && receiverPortDirection == "left" && linkType == std.TypeObject {
		return n.acceptObjectConfig(event)
	}
	if event.ReceiverPort != n.in || receiverPortDirection != "right" || linkType != TypeNetwork || event.Link != n.inLink {
		return nil
	}
	next, changed := AcceptNetworkPayload(n.W, n.ID, event, n.network)
	if !changed {
		return nil
	}
	n.closeOutputs()
	n.network = next
	n.sendAll()
	return n.updateLabel()
}

func (n *networkFirewallNode) OnFormularMsg(message any) error {
	switch msg := message.(type) {
	case formular.FormApplyMessage:
		if msg.MenuID != pasta.NodeMenuID(n.ID) || msg.BlockID != NetworkFirewallConfigBlockID {
			return nil
		}
		text, _ := formular.StringValue(msg.Values[NetworkFirewallRulesField])
		config, err := nodekit.ParseFirewallConfigJSON(text)
		if err != nil {
			n.formError = err.Error()
			n.sendConfigBlock()
			return n.updateLabel()
		}
		n.rulesText = text
		n.menuConfig = config
		n.formError = ""
		n.installEffectiveConfig()
		n.sendMenuSnapshot(true)
		return n.updateLabel()
	case formular.FieldValidateMessage:
		if msg.MenuID != pasta.NodeMenuID(n.ID) || msg.Field.BlockID != NetworkFirewallConfigBlockID || msg.Field.FieldID != NetworkFirewallRulesField || len(msg.Field.ElementPath) > 0 {
			return nil
		}
		text, _ := formular.StringValue(msg.Value)
		_, err := nodekit.ParseFirewallConfigJSON(text)
		sendNodeFieldStatus(n.W, n.ID, msg.Field, err)
	}
	return nil
}

func (n *networkFirewallNode) OnSave(cfg configer.Config) error {
	if err := pasta.DeleteNodeOwnedConfigKeys(cfg); err != nil {
		return err
	}
	return cfg.Set(
		configer.Path{NetworkFirewallRulesField},
		nodekit.FirewallConfigValue(n.menuConfig),
	)
}

func (n *networkFirewallNode) acceptObjectConfig(event pasta.Event) error {
	if std.IsRequest(event.Payload) {
		return nil
	}
	object, ok := std.ObjectFromPayload(event.Payload)
	if !ok {
		return nil
	}
	config, err := nodekit.FirewallConfigFromObject(object)
	if err != nil {
		delete(n.objectConfigs, event.Link)
		n.objectErrors[event.Link] = err.Error()
	} else {
		n.objectConfigs[event.Link] = config
		delete(n.objectErrors, event.Link)
	}
	n.installEffectiveConfig()
	n.sendEffectiveBlock()
	return n.updateLabel()
}

func (n *networkFirewallNode) installEffectiveConfig() {
	n.recomputeEffective()
	for _, firewall := range n.firewalls {
		firewall.SetConfig(n.effective)
	}
}

func (n *networkFirewallNode) recomputeEffective() {
	n.effective = nodekit.MergeFirewallConfigMap(n.menuConfig, n.objectConfigs)
}

func (n *networkFirewallNode) sendAll() {
	n.ForEachPortLink(n.out, func(link uint64) { n.sendToLink(link, false) })
}

func (n *networkFirewallNode) sendToLink(link uint64, refresh bool) {
	if !NetworkIsReady(n.network) {
		return
	}
	wrap := func(output NetworkCloser) NetworkCloser {
		firewall := gonnect.NewFirewall(output, n.effective)
		n.firewalls[link] = firewall
		return firewall
	}
	if refresh {
		delete(n.firewalls, link)
		n.outputs.RefreshWrappedLocked(n.W, n.ID, link, n.network, wrap)
		return
	}
	n.outputs.SendWrappedLocked(n.W, n.ID, link, n.network, wrap)
}

func (n *networkFirewallNode) closeOutputs() {
	n.outputs.InvalidateAllLocked(n.W, n.ID)
	clear(n.firewalls)
}

func (n *networkFirewallNode) ensureMaps() {
	if n.objectConfigs == nil {
		n.objectConfigs = map[uint64]*gonnect.FirewallConfig{}
	}
	if n.objectErrors == nil {
		n.objectErrors = map[uint64]string{}
	}
	if n.firewalls == nil {
		n.firewalls = map[uint64]*gonnect.Firewall{}
	}
}

func (n *networkFirewallNode) updateLabel() error {
	switch {
	case n.formError != "":
		return n.SetLabel("invalid config")
	case len(n.objectErrors) != 0:
		return n.SetLabel("invalid object config")
	case !NetworkIsReady(n.network):
		return n.SetLabel("waiting for network")
	default:
		count := len(n.effective.Exclude) + len(n.effective.Include)
		return n.SetLabel(fmt.Sprintf("filtering %d rules", count))
	}
}

func (n *networkFirewallNode) sendMenuSnapshot(force bool) {
	sendNodeMenuSnapshot(n.W, n.ID, force, n.configBlock(), n.effectiveBlock())
}

func (n *networkFirewallNode) sendConfigBlock() {
	sendNodeBlockSnapshot(n.W, n.ID, n.configBlock())
}

func (n *networkFirewallNode) sendEffectiveBlock() {
	sendNodeBlockSnapshot(n.W, n.ID, n.effectiveBlock())
}

func (n *networkFirewallNode) configBlock() formular.Block {
	field := &formular.Field{
		Kind:        formular.FieldText,
		Value:       n.rulesText,
		Multiline:   true,
		Validation:  true,
		Placeholder: nodekit.FirewallRulesPlaceholder,
	}
	if n.formError != "" {
		field.Status = formular.StatusError
		field.StatusText = n.formError
	}
	return formular.Block{ID: NetworkFirewallConfigBlockID, Order: 10, Generation: 1, Form: true, Items: []formular.Item{{
		Type: formular.ItemField, ID: NetworkFirewallRulesField, Label: "Firewall rules", Field: field,
	}}}
}

func (n *networkFirewallNode) effectiveBlock() formular.Block {
	item := nodekit.ReadonlyMultilineTextField(
		NetworkFirewallEffectiveField,
		"Effective config",
		n.effectiveJSON(),
	)
	if len(n.objectErrors) != 0 {
		item.Status = formular.StatusError
		item.StatusText = n.objectErrorText()
	}
	return formular.Block{ID: NetworkFirewallEffectiveBlockID, Order: 20, Generation: 1, Items: []formular.Item{item}}
}

func (n *networkFirewallNode) effectiveJSON() string {
	return nodekit.FormatFirewallConfigJSON(n.effective)
}

func (n *networkFirewallNode) objectErrorText() string {
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
