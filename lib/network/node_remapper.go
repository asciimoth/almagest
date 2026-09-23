package networknodes

import (
	_ "embed"
	"fmt"

	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/formular"
	"github.com/asciimoth/gonnect"
	"github.com/asciimoth/gonnect/routing"
	"github.com/asciimoth/pasta/pasta"
	"github.com/asciimoth/pasta/pasta/std"
)

const (
	// NodeTypeRemapper is the node class name for network address remapping
	// middleware.
	NodeTypeRemapper = "almagest/Remapper"

	RemapperConfigBlockID = "config"
	RemapperRulesField    = "rules"
)

const remapperRulesPlaceholder = `# Predicates use gonnect/routing bytecode. Each segment ends with REMAP.
DIAL
TCP
AND
ADDR_S example.test
AND
REMAP DST ADDR_PORT 127.0.0.1:8080

LISTEN
TCP
AND
REMAP SRC ADDR 127.0.0.1`

var (
	//go:embed node_remapper.md
	remapperClassLongDescription string
)

type RemapperClass struct{}

func (RemapperClass) ClassName() string        { return NodeTypeRemapper }
func (RemapperClass) ShortDescription() string { return "Network remapper" }
func (RemapperClass) LongDescription() string  { return remapperClassLongDescription }
func (RemapperClass) DefaultNodeParams() pasta.NodeClassParams {
	return pasta.NodeClassParams{PrimaryType: TypeNetwork, InitialPorts: []pasta.Port{
		NetworkPort("left", "Network"),
		NetworkPort("right", "Network"),
	}}
}
func (RemapperClass) NewNode(cfg configer.Config, _ ...*pasta.NodeClassState) (pasta.Node, error) {
	return &remapperNode{
		rulesText: configer.String(cfg, configer.Path{RemapperRulesField}, ""),
	}, nil
}

// remapperNode is transparent network middleware. Its right-directed Network
// port accepts one upstream network, and its left-directed Network port emits
// per-link detached gonnect.Remapper wrappers around that upstream network.
// The node keeps wrappers per output link so std.Request refreshes can replace
// a closed routed payload without rebuilding unrelated consumers.
type remapperNode struct {
	NodeBase

	out uint64
	in  uint64

	network   NetworkCloser
	networkID uint64
	rulesText string
	rules     []gonnect.RemapRule
	formError string
	outputs   NetworkOutputCache
}

func (n *remapperNode) OnInit(w *pasta.Workspace, logger pasta.Logger, id uint64, _ string, restored *pasta.NodeInitData, _, _, _, _ bool) error {
	n.InitBaseNode(w, logger, id)
	n.out = restoredLeft(restored, 0)
	n.in = restoredRight(restored, 0)
	rules, err := routing.NewRemapRules(n.rulesText)
	if err != nil {
		n.formError = err.Error()
	} else {
		n.rules = rules
	}
	if err := n.SetPrimary(TypeNetwork); err != nil {
		return err
	}
	n.sendMenuSnapshot()
	return n.updateLabel()
}

func (n *remapperNode) OnReady() error {
	n.RequestPort(n.in)
	n.sendAll()
	return nil
}

func (n *remapperNode) OnStop() {
	n.outputs.CloseAll()
	_ = closeNetwork(n.network)
	n.network = nil
	n.networkID = 0
}

func (n *remapperNode) PreLinkAdd(port uint64, linkType, portDirection string) error {
	switch port {
	case n.out:
		if portDirection != "left" || linkType != TypeNetwork {
			return pasta.LinkTypeErr(linkType)
		}
	case n.in:
		if portDirection != "right" || linkType != TypeNetwork {
			return pasta.LinkTypeErr(linkType)
		}
		if n.PortHasLinks(port) {
			return pasta.ErrLinkDup
		}
	default:
		return pasta.LinkTypeErr(linkType)
	}
	return nil
}

func (n *remapperNode) OnLinkAdd(link, port uint64, linkType, portDirection string) error {
	switch {
	case port == n.out && portDirection == "left" && linkType == TypeNetwork:
		n.sendOutputToLink(link)
	case port == n.in && portDirection == "right" && linkType == TypeNetwork:
		n.networkID = link
		std.RequestLocked(n.W, n.ID, link)
	}
	return n.updateLabel()
}

func (n *remapperNode) OnLinkRemoved(link, port uint64, linkType, portDirection string) error {
	switch {
	case port == n.out && portDirection == "left" && linkType == TypeNetwork:
		n.outputs.Close(link)
	case port == n.in && portDirection == "right" && linkType == TypeNetwork && link == n.networkID:
		n.outputs.InvalidateAllLocked(n.W, n.ID)
		_ = closeNetwork(n.network)
		n.network = nil
		n.networkID = 0
	}
	return n.updateLabel()
}

func (n *remapperNode) OnEvent(event pasta.Event, linkType string, _ []string, receiverPortDirection string) error {
	if event.ReceiverPort == n.out && receiverPortDirection == "left" && linkType == TypeNetwork && std.IsRequest(event.Payload) {
		n.refreshOutputToLink(event.Link)
		return nil
	}
	if event.ReceiverPort != n.in || receiverPortDirection != "right" || linkType != TypeNetwork || event.Link != n.networkID {
		return nil
	}
	next, changed := AcceptNetworkPayload(n.W, n.ID, event, n.network)
	if !changed {
		return nil
	}
	n.outputs.InvalidateAllLocked(n.W, n.ID)
	n.network = next
	n.sendAll()
	return n.updateLabel()
}

func (n *remapperNode) OnFormularMsg(message any) error {
	switch msg := message.(type) {
	case formular.FormApplyMessage:
		if msg.MenuID != pasta.NodeMenuID(n.ID) || msg.BlockID != RemapperConfigBlockID {
			return nil
		}
		return n.applyConfigForm(msg.Values)
	case formular.FieldValidateMessage:
		if msg.MenuID != pasta.NodeMenuID(n.ID) || msg.Field.BlockID != RemapperConfigBlockID || msg.Field.FieldID != RemapperRulesField || len(msg.Field.ElementPath) > 0 {
			return nil
		}
		n.sendRulesFieldStatus(msg)
	}
	return nil
}

func (n *remapperNode) OnSave(cfg configer.Config) error {
	if err := pasta.DeleteNodeOwnedConfigKeys(cfg); err != nil {
		return err
	}
	return cfg.Set(configer.Path{RemapperRulesField}, n.rulesText)
}

func (n *remapperNode) applyConfigForm(values map[string]any) error {
	text, _ := formular.StringValue(values[RemapperRulesField])
	rules, err := routing.NewRemapRules(text)
	if err != nil {
		n.formError = err.Error()
		n.sendConfigBlock()
		return n.updateLabel()
	}
	n.rulesText = text
	n.rules = rules
	n.formError = ""
	n.outputs.InvalidateAllLocked(n.W, n.ID)
	n.sendForcedMenuSnapshot()
	n.sendAll()
	return n.updateLabel()
}

func (n *remapperNode) sendRulesFieldStatus(msg formular.FieldValidateMessage) {
	text, _ := formular.StringValue(msg.Value)
	_, err := routing.NewRemapRules(text)
	sendNodeFieldStatus(n.W, n.ID, msg.Field, err)
}

func (n *remapperNode) ready() bool {
	return n.formError == "" && NetworkIsReady(n.network)
}

func (n *remapperNode) sendAll() {
	if !n.ready() {
		return
	}
	rules := append([]gonnect.RemapRule(nil), n.rules...)
	n.outputs.SendWrappedPortLocked(n.W, n.ID, n.out, n.network, func(output NetworkCloser) NetworkCloser {
		return gonnect.NewRemapper(output, rules)
	})
}

func (n *remapperNode) sendOutputToLink(link uint64) {
	if !n.ready() {
		return
	}
	rules := append([]gonnect.RemapRule(nil), n.rules...)
	n.outputs.SendWrappedLocked(n.W, n.ID, link, n.network, func(output NetworkCloser) NetworkCloser {
		return gonnect.NewRemapper(output, rules)
	})
}

func (n *remapperNode) refreshOutputToLink(link uint64) {
	if !n.ready() {
		return
	}
	rules := append([]gonnect.RemapRule(nil), n.rules...)
	n.outputs.RefreshWrappedLocked(n.W, n.ID, link, n.network, func(output NetworkCloser) NetworkCloser {
		return gonnect.NewRemapper(output, rules)
	})
}

func (n *remapperNode) updateLabel() error {
	switch {
	case n.formError != "":
		return n.SetLabel("invalid config")
	case !NetworkIsReady(n.network):
		return n.SetLabel("waiting for network")
	case len(n.rules) == 0:
		return n.SetLabel("remapping network")
	case len(n.rules) == 1:
		return n.SetLabel("remapping 1 rule")
	default:
		return n.SetLabel(fmt.Sprintf("remapping %d rules", len(n.rules)))
	}
}

func (n *remapperNode) sendMenuSnapshot() {
	sendNodeMenuSnapshot(n.W, n.ID, false, n.configBlock())
}

func (n *remapperNode) sendForcedMenuSnapshot() {
	sendNodeMenuSnapshot(n.W, n.ID, true, n.configBlock())
}

func (n *remapperNode) sendConfigBlock() {
	sendNodeBlockSnapshot(n.W, n.ID, n.configBlock())
}

func (n *remapperNode) configBlock() formular.Block {
	field := &formular.Field{
		Kind:        formular.FieldText,
		Value:       n.rulesText,
		Multiline:   true,
		Validation:  true,
		Placeholder: remapperRulesPlaceholder,
	}
	if n.formError != "" {
		field.Status = formular.StatusError
		field.StatusText = n.formError
	}
	return formular.Block{ID: RemapperConfigBlockID, Order: 10, Generation: 1, Form: true, Items: []formular.Item{
		{Type: formular.ItemField, ID: RemapperRulesField, Label: "Remap rules", Field: field},
	}}
}
