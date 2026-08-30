package tunnodes

import (
	"fmt"
	"slices"
	"strings"

	"github.com/asciimoth/bufpool"
	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/formular"
	"github.com/asciimoth/gonnect"
	gonnecttun "github.com/asciimoth/gonnect/tun"
	"github.com/asciimoth/pasta/pasta"
	"github.com/asciimoth/pasta/pasta/std"

	"github.com/asciimoth/almagest/lib/nodekit"
)

const (
	// NodeTypeTunFirewall is the node class name for TUN firewall middleware.
	NodeTypeTunFirewall = "almagest/TunFirewall"

	// TunFirewallConfigBlockID identifies the editable policy block.
	TunFirewallConfigBlockID = "config"
	// TunFirewallRulesField identifies the JSON policy field.
	TunFirewallRulesField = "rules"
	// TunFirewallEffectiveBlockID identifies the computed policy block.
	TunFirewallEffectiveBlockID = "effective"
	// TunFirewallEffectiveField identifies the computed policy field.
	TunFirewallEffectiveField = "config"
)

// TunFirewallClass builds middleware based on gonnect/tun.Firewall.
type TunFirewallClass struct {
	Runtime Runtime
}

func (TunFirewallClass) ClassName() string        { return NodeTypeTunFirewall }
func (TunFirewallClass) ShortDescription() string { return "TUN firewall" }
func (TunFirewallClass) LongDescription() string {
	return "Filters IP packets on an almagest/tun link with the union of its menu policy and all attached pasta/object policy values."
}
func (TunFirewallClass) DefaultNodeParams() pasta.NodeClassParams {
	return pasta.NodeClassParams{PrimaryType: TypeTun, InitialPorts: []pasta.Port{
		TunPort("left", "Tun"),
		{Direction: "left", Name: "Config", Types: []string{std.TypeObject}},
		TunPort("right", "Tun"),
	}}
}
func (c TunFirewallClass) NewNode(cfg configer.Config, _ ...*pasta.NodeClassState) (pasta.Node, error) {
	return &tunFirewallNode{
		pool: enginePool(c.Runtime),
		rulesText: nodekit.FirewallConfigTextFromConfig(
			cfg,
			configer.Path{TunFirewallRulesField},
		),
		objectConfigs: map[uint64]*gonnect.FirewallConfig{},
		objectErrors:  map[uint64]string{},
		firewalls:     map[uint64]*gonnecttun.Firewall{},
		outputCfgs:    TunConfigByLink{},
	}, nil
}

// tunFirewallNode emits a detached gonnect/tun.Firewall on its output. It also
// preserves the bidirectional TypeTun contract by merging downstream TunConfig
// suggestions and sending them to the upstream provider.
type tunFirewallNode struct {
	NodeBase

	pool bufpool.Pool

	in       uint64
	configIn uint64
	out      uint64
	inLink   uint64

	tun        gonnecttun.Tun
	outputTuns TunOutputCache
	outputCfgs TunConfigByLink

	rulesText     string
	menuConfig    *gonnect.FirewallConfig
	effective     *gonnect.FirewallConfig
	formError     string
	objectConfigs map[uint64]*gonnect.FirewallConfig
	objectErrors  map[uint64]string
	firewalls     map[uint64]*gonnecttun.Firewall
}

func (n *tunFirewallNode) OnInit(w *pasta.Workspace, logger pasta.Logger, id uint64, _ string, restored *pasta.NodeInitData, _, _, _, _ bool) error {
	n.InitBaseNode(w, logger, id)
	n.in = restoredLeft(restored, 0)
	n.configIn = restoredLeft(restored, 1)
	n.out = restoredRight(restored, 0)
	n.ensureMaps()
	parsed, err := nodekit.ParseFirewallConfigJSON(n.rulesText)
	if err != nil {
		n.formError = err.Error()
		parsed = (&gonnect.FirewallConfig{}).Optimize()
	}
	n.menuConfig = parsed
	n.recomputeEffective()
	if err := n.SetPrimary(TypeTun); err != nil {
		return err
	}
	n.sendMenuSnapshot(false)
	return n.updateLabel()
}

func (n *tunFirewallNode) OnReady() error {
	n.RequestPort(n.in)
	n.RequestPort(n.configIn)
	n.sendAll()
	n.sendMergedTunConfig()
	return nil
}

func (n *tunFirewallNode) OnStop() {
	n.closeOutputs()
	if n.tun != nil {
		_ = n.tun.Close()
		n.tun = nil
	}
	n.outputTuns.Wait()
	n.outputCfgs = TunConfigByLink{}
	n.inLink = 0
}

func (n *tunFirewallNode) PreLinkAdd(port uint64, linkType, portDirection string) error {
	switch {
	case port == n.in && portDirection == "left":
		return TunPreLinkAdd(n.W, port, linkType)
	case port == n.configIn && portDirection == "left" && linkType == std.TypeObject:
		// Unlike standard object consumers, the firewall intentionally merges
		// values from all links attached to this one input port.
		return nil
	case port == n.out && portDirection == "right":
		return TunPreLinkAdd(n.W, port, linkType)
	default:
		return pasta.LinkTypeErr(linkType)
	}
}

func (n *tunFirewallNode) OnLinkAdd(link, port uint64, linkType, portDirection string) error {
	switch {
	case port == n.in && portDirection == "left" && linkType == TypeTun:
		n.inLink = link
		std.RequestLocked(n.W, n.ID, link)
		n.sendMergedTunConfigToLink(link)
	case port == n.configIn && portDirection == "left" && linkType == std.TypeObject:
		std.RequestLocked(n.W, n.ID, link)
	case port == n.out && portDirection == "right" && linkType == TypeTun:
		n.sendToLink(link, false)
	}
	return n.updateLabel()
}

func (n *tunFirewallNode) OnLinkRemoved(link uint64, port uint64, linkType, portDirection string) error {
	switch {
	case port == n.in && portDirection == "left" && linkType == TypeTun && link == n.inLink:
		n.closeOutputs()
		if n.tun != nil {
			_ = n.tun.Close()
			n.tun = nil
		}
		n.outputTuns.Wait()
		n.inLink = 0
	case port == n.configIn && portDirection == "left" && linkType == std.TypeObject:
		delete(n.objectConfigs, link)
		delete(n.objectErrors, link)
		n.installEffectiveConfig()
		n.sendEffectiveBlock()
	case port == n.out && portDirection == "right" && linkType == TypeTun:
		n.outputTuns.Close(link)
		delete(n.firewalls, link)
		n.outputCfgs.Delete(link)
		n.sendMergedTunConfig()
	}
	return n.updateLabel()
}

func (n *tunFirewallNode) OnEvent(event pasta.Event, linkType string, _ []string, receiverPortDirection string) error {
	if event.ReceiverPort == n.configIn && receiverPortDirection == "left" && linkType == std.TypeObject {
		return n.acceptObjectConfig(event)
	}
	if linkType != TypeTun {
		return nil
	}
	if event.ReceiverPort == n.out && receiverPortDirection == "right" {
		if std.IsRequest(event.Payload) {
			n.sendToLink(event.Link, true)
			return nil
		}
		if cfg, ok := event.Payload.(TunConfig); ok {
			n.outputCfgs.Set(event.Link, cfg)
			n.sendMergedTunConfig()
		}
		return nil
	}
	if event.ReceiverPort == n.in && receiverPortDirection == "left" && std.IsRequest(event.Payload) {
		n.sendMergedTunConfigToLink(event.Link)
		return nil
	}
	if event.ReceiverPort != n.in || receiverPortDirection != "left" || event.Link != n.inLink {
		return nil
	}
	next, changed := AcceptTunPayload(n.W, n.ID, event, n.inLink, n.tun)
	if !changed {
		return nil
	}
	n.closeOutputs()
	n.outputTuns.Wait()
	n.tun = next
	n.sendAll()
	return n.updateLabel()
}

func (n *tunFirewallNode) OnFormularMsg(message any) error {
	switch msg := message.(type) {
	case formular.FormApplyMessage:
		if msg.MenuID != pasta.NodeMenuID(n.ID) || msg.BlockID != TunFirewallConfigBlockID {
			return nil
		}
		text, _ := formular.StringValue(msg.Values[TunFirewallRulesField])
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
		if msg.MenuID != pasta.NodeMenuID(n.ID) || msg.Field.BlockID != TunFirewallConfigBlockID || msg.Field.FieldID != TunFirewallRulesField || len(msg.Field.ElementPath) > 0 {
			return nil
		}
		text, _ := formular.StringValue(msg.Value)
		_, err := nodekit.ParseFirewallConfigJSON(text)
		sendNodeFieldStatus(n.W, n.ID, msg.Field, err)
	}
	return nil
}

func (n *tunFirewallNode) OnSave(cfg configer.Config) error {
	if err := pasta.DeleteNodeOwnedConfigKeys(cfg); err != nil {
		return err
	}
	return cfg.Set(
		configer.Path{TunFirewallRulesField},
		nodekit.FirewallConfigValue(n.menuConfig),
	)
}

func (n *tunFirewallNode) acceptObjectConfig(event pasta.Event) error {
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

func (n *tunFirewallNode) installEffectiveConfig() {
	n.recomputeEffective()
	for _, firewall := range n.firewalls {
		firewall.SetConfig(n.effective)
	}
}

func (n *tunFirewallNode) recomputeEffective() {
	n.effective = nodekit.MergeFirewallConfigMap(n.menuConfig, n.objectConfigs)
}

func (n *tunFirewallNode) sendAll() {
	n.ForEachPortLink(n.out, func(link uint64) { n.sendToLink(link, false) })
}

func (n *tunFirewallNode) sendToLink(link uint64, refresh bool) {
	if !TunIsReady(n.tun) {
		return
	}
	wrap := func(output gonnecttun.Tun) gonnecttun.Tun {
		firewall := gonnecttun.NewFirewall(output, n.effective)
		n.firewalls[link] = firewall
		return firewall
	}
	if refresh {
		delete(n.firewalls, link)
		n.outputTuns.RefreshWrappedLocked(n.W, n.ID, link, n.tun, wrap, n.pool)
		return
	}
	n.outputTuns.SendWrappedLocked(n.W, n.ID, link, n.tun, wrap, n.pool)
}

func (n *tunFirewallNode) closeOutputs() {
	n.outputTuns.CloseAll()
	clear(n.firewalls)
}

func (n *tunFirewallNode) sendMergedTunConfig() {
	BroadcastMergedTunConfig(n.W, n.ID, n.in, n.outputCfgs)
}

func (n *tunFirewallNode) sendMergedTunConfigToLink(link uint64) {
	EmitMergedTunConfig(n.W, n.ID, link, n.outputCfgs)
}

func (n *tunFirewallNode) ensureMaps() {
	if n.objectConfigs == nil {
		n.objectConfigs = map[uint64]*gonnect.FirewallConfig{}
	}
	if n.objectErrors == nil {
		n.objectErrors = map[uint64]string{}
	}
	if n.firewalls == nil {
		n.firewalls = map[uint64]*gonnecttun.Firewall{}
	}
	if n.outputCfgs == nil {
		n.outputCfgs = TunConfigByLink{}
	}
}

func (n *tunFirewallNode) updateLabel() error {
	switch {
	case n.formError != "":
		return n.SetLabel("invalid config")
	case len(n.objectErrors) != 0:
		return n.SetLabel("invalid object config")
	case !TunIsReady(n.tun):
		return n.SetLabel("waiting for TUN")
	default:
		count := len(n.effective.Exclude) + len(n.effective.Include)
		return n.SetLabel(fmt.Sprintf("filtering %d rules", count))
	}
}

func (n *tunFirewallNode) sendMenuSnapshot(force bool) {
	sendNodeMenuSnapshot(n.W, n.ID, force, n.configBlock(), n.effectiveBlock())
}

func (n *tunFirewallNode) sendConfigBlock() {
	sendNodeBlockSnapshot(n.W, n.ID, n.configBlock())
}

func (n *tunFirewallNode) sendEffectiveBlock() {
	sendNodeBlockSnapshot(n.W, n.ID, n.effectiveBlock())
}

func (n *tunFirewallNode) configBlock() formular.Block {
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
	return formular.Block{ID: TunFirewallConfigBlockID, Order: 10, Generation: 1, Form: true, Items: []formular.Item{{
		Type: formular.ItemField, ID: TunFirewallRulesField, Label: "Firewall rules", Field: field,
	}}}
}

func (n *tunFirewallNode) effectiveBlock() formular.Block {
	item := nodekit.ReadonlyMultilineTextField(
		TunFirewallEffectiveField,
		"Effective config",
		n.effectiveJSON(),
	)
	if len(n.objectErrors) != 0 {
		item.Status = formular.StatusError
		item.StatusText = n.objectErrorText()
	}
	return formular.Block{ID: TunFirewallEffectiveBlockID, Order: 20, Generation: 1, Items: []formular.Item{item}}
}

func (n *tunFirewallNode) effectiveJSON() string {
	return nodekit.FormatFirewallConfigJSON(n.effective)
}

func (n *tunFirewallNode) objectErrorText() string {
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
