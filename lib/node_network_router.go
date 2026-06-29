package almagest

import (
	"errors"
	"net"

	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/formular"
	"github.com/asciimoth/gonnect"
	routing "github.com/asciimoth/gonnect-routing"
	"github.com/asciimoth/pasta/pasta"
	"github.com/asciimoth/pasta/pasta/std"
)

func init() {
	regClass(func(*engine) pasta.NodeClass {
		return networkRouterClass{}
	}, false)
}

const (
	NodeTypeNetworkRouter = "almagest/NetworkRouter"

	networkRouterMaxSlots = 16
)

type networkRouterClass struct{}

func (networkRouterClass) ClassName() string        { return NodeTypeNetworkRouter }
func (networkRouterClass) ShortDescription() string { return "Network router" }
func (networkRouterClass) LongDescription() string {
	return "Routes network operations to backend network slots using bytecode routing rules."
}
func (networkRouterClass) DefaultNodeParams() pasta.NodeClassParams {
	return pasta.NodeClassParams{PrimaryType: TypeNetwork, InitialPorts: []pasta.Port{
		NetworkPort("left", "Network"),
		NetworkPort("right", slotPortName(1)),
	}}
}
func (networkRouterClass) NewNode(cfg configer.Config, _ ...*pasta.NodeClassState) (pasta.Node, error) {
	return &networkRouterNode{
		rulesText: configer.String(cfg, configer.Path{"rules"}, ""),
		router:    gonnect.NewRouter(),
		slots: newSlotPorts(
			networkRouterMaxSlots,
			"right",
			func(slot int) pasta.Port { return NetworkPort("right", slotPortName(slot)) },
			slotPortName,
			func(name string) (int, bool) { return slotPortFromName(name, networkRouterMaxSlots) },
		),
		backends: map[int]NetworkCloser{},
	}, nil
}

type networkRouterNode struct {
	NodeBase

	in    uint64
	slots slotPorts

	router    *gonnect.Router
	cfg       routing.RouterCfg
	rulesText string
	formError string
	ready     bool
	backends  map[int]NetworkCloser
	outputs   NetworkOutputCache
}

func (n *networkRouterNode) OnInit(w *pasta.Workspace, _ pasta.Logger, id uint64, _ string, restored *pasta.NodeInitData, _, _, _, _ bool) error {
	n.InitBaseNode(w, nil, id)
	if n.router == nil {
		n.router = gonnect.NewRouter()
	}
	if n.backends == nil {
		n.backends = map[int]NetworkCloser{}
	}
	if n.slots.max == 0 {
		n.slots = newSlotPorts(
			networkRouterMaxSlots,
			"right",
			func(slot int) pasta.Port { return NetworkPort("right", slotPortName(slot)) },
			slotPortName,
			func(name string) (int, bool) { return slotPortFromName(name, networkRouterMaxSlots) },
		)
	}
	n.in = restoredLeft(restored, 0)
	n.slots.Init(w, id, restoredRightPorts(restored))
	if err := n.SetPrimary(TypeNetwork); err != nil {
		return err
	}
	_ = n.SetLabel("router")
	BindNetworkResource(n.w, n.id, 0, n.router)

	cfg, err := n.compileRules(n.rulesText)
	if err != nil {
		n.formError = err.Error()
	} else {
		n.cfg = cfg
		n.router.SetCfg(cfg)
	}
	if err := n.reviewOutputPorts(); err != nil {
		return err
	}
	n.sendMenuSnapshot()
	return nil
}

func (n *networkRouterNode) OnStop() {
	n.outputs.CloseAll()
	if n.router != nil {
		_ = n.router.Close()
	}
}

func (n *networkRouterNode) OnReady() error {
	n.ready = true
	n.requestSlots()
	if len(n.backends) > 0 || !n.hasSlotLinks() {
		n.sendAll()
	}
	return nil
}

func (n *networkRouterNode) PreLinkAdd(port uint64, linkType, portDirection string) error {
	if port == n.in {
		if portDirection != "left" || linkType != TypeNetwork {
			return pasta.LinkTypeErr(linkType)
		}
		return nil
	}
	if slot := n.slots.Slot(port); slot != 0 {
		if portDirection != "right" || linkType != TypeNetwork {
			return pasta.LinkTypeErr(linkType)
		}
		if n.PortHasLinks(port) {
			return pasta.ErrLinkDup
		}
		return nil
	}
	return pasta.LinkTypeErr(linkType)
}

func (n *networkRouterNode) OnLinkAdd(link, port uint64, linkType, portDirection string) error {
	if port == n.in && portDirection == "left" && linkType == TypeNetwork {
		if n.ready {
			n.sendRouterToLink(link)
		}
		return nil
	}
	if n.slots.Slot(port) != 0 && portDirection == "right" && linkType == TypeNetwork {
		if n.ready {
			std.Request(n.w, n.id, link)
		}
	}
	return nil
}

func (n *networkRouterNode) OnLinkRemoved(link, port uint64, linkType, portDirection string) error {
	if port == n.in && portDirection == "left" && linkType == TypeNetwork {
		n.outputs.Close(link)
		return nil
	}
	if slot := n.slots.Slot(port); slot != 0 && portDirection == "right" && linkType == TypeNetwork {
		n.detachSlot(slot)
	}
	return nil
}

func (n *networkRouterNode) OnPortRemoved(port uint64, direction string) error {
	if direction != "right" {
		return nil
	}
	if slot := n.slots.Remove(port); slot != 0 {
		n.detachSlot(slot)
	}
	return nil
}

func (n *networkRouterNode) OnEvent(event pasta.Event, linkType string, _ []string, receiverPortDirection string) error {
	if event.ReceiverPort == n.in && receiverPortDirection == "left" && linkType == TypeNetwork && std.IsRequest(event.Payload) {
		n.sendRouterToLink(event.Link)
		return nil
	}
	slot := n.slots.Slot(event.ReceiverPort)
	if slot == 0 || receiverPortDirection != "right" || linkType != TypeNetwork {
		return nil
	}
	next, changed := acceptNetworkPayload(event, n.backends[slot])
	if !changed {
		return nil
	}
	if SameNetworkInstance(n.backends[slot], next) {
		return nil
	}
	n.detachSlot(slot)
	err := n.router.Attach(slot, next)
	BindNetworkResource(n.w, n.id, event.Link, next)
	n.backends[slot] = next
	if err != nil && !n.isNonFatalAttachError(err) {
		return err
	}
	if n.ready {
		n.sendAll()
	}
	return nil
}

func (n *networkRouterNode) OnFormularMsg(message any) error {
	switch msg := message.(type) {
	case formular.FormApplyMessage:
		if msg.MenuID != pasta.NodeMenuID(n.id) || msg.BlockID != "config" {
			return nil
		}
		return n.applyConfigForm(msg.Values)
	case formular.FieldValidateMessage:
		if msg.MenuID != pasta.NodeMenuID(n.id) || msg.Field.BlockID != "config" || msg.Field.FieldID != "rules" || len(msg.Field.ElementPath) > 0 {
			return nil
		}
		n.sendRulesFieldStatus(msg)
	}
	return nil
}

func (n *networkRouterNode) OnSave(cfg configer.Config) error {
	if err := pasta.DeleteNodeOwnedConfigKeys(cfg); err != nil {
		return err
	}
	return cfg.Set(configer.Path{"rules"}, n.rulesText)
}

func (n *networkRouterNode) applyConfigForm(values map[string]any) error {
	text, _ := formular.StringValue(values["rules"])
	cfg, err := n.compileRules(text)
	if err != nil {
		n.formError = err.Error()
		n.sendConfigBlock()
		return nil
	}
	n.rulesText = text
	n.formError = ""
	n.cfg = cfg
	n.router.SetCfg(cfg)
	if err := n.reviewOutputPorts(); err != nil {
		return err
	}
	n.sendForcedMenuSnapshot()
	return nil
}

func (n *networkRouterNode) sendRulesFieldStatus(msg formular.FieldValidateMessage) {
	text, _ := formular.StringValue(msg.Value)
	if _, err := n.compileRules(text); err != nil {
		sendNodeFieldStatus(n.w, n.id, msg.Field, err)
		return
	}
	sendNodeFieldStatus(n.w, n.id, msg.Field, nil)
}

func (n *networkRouterNode) compileRules(text string) (routing.RouterCfg, error) {
	rules, err := routing.NewBytecodeRulesProgram(text)
	if err != nil {
		return nil, err
	}
	return routing.NewBytecodeRouterCfg(rules)
}

func (n *networkRouterNode) reviewOutputPorts() error {
	desired := map[int]struct{}{}
	if n.cfg != nil {
		for _, slot := range n.cfg.MentionedSlots() {
			if slot >= 1 && slot <= networkRouterMaxSlots {
				desired[slot] = struct{}{}
			}
		}
	}
	return n.slots.Review(desired, slotPortReview{DefaultSlot: 1})
}

func (n *networkRouterNode) sendAll() {
	n.ForEachPortLink(n.in, func(link uint64) {
		n.sendRouterToLink(link)
	})
}

func (n *networkRouterNode) requestSlots() {
	n.slots.ForEach(func(_ int, portID uint64) {
		n.RequestPort(portID)
	})
}

func (n *networkRouterNode) sendRouterToLink(link uint64) {
	if n.router == nil {
		return
	}
	n.outputs.Send(n.w, n.id, link, n.router)
}

func (n *networkRouterNode) isNonFatalAttachError(err error) bool {
	if err == nil || !allErrorsAre(err, net.ErrClosed) {
		return false
	}
	up, upErr := n.router.IsUp()
	return upErr == nil && up
}

func allErrorsAre(err, target error) bool {
	if err == nil {
		return true
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		errs := joined.Unwrap()
		if len(errs) == 0 {
			return errors.Is(err, target)
		}
		for _, child := range errs {
			if !allErrorsAre(child, target) {
				return false
			}
		}
		return true
	}
	if wrapped, ok := err.(interface{ Unwrap() error }); ok {
		child := wrapped.Unwrap()
		if child != nil {
			return allErrorsAre(child, target)
		}
	}
	return errors.Is(err, target)
}

func (n *networkRouterNode) detachSlot(slot int) {
	if slot < 1 || slot > networkRouterMaxSlots {
		return
	}
	if old := n.backends[slot]; old != nil {
		_ = n.router.Detach(slot)
		_ = old.Close()
		delete(n.backends, slot)
	}
}

func (n *networkRouterNode) hasSlotLinks() bool {
	return n.slots.HasLinks()
}

func (n *networkRouterNode) sendMenuSnapshot() {
	sendNodeMenuSnapshot(n.w, n.id, false, n.configBlock())
}

func (n *networkRouterNode) sendForcedMenuSnapshot() {
	sendNodeMenuSnapshot(n.w, n.id, true, n.configBlock())
}

func (n *networkRouterNode) sendConfigBlock() {
	sendNodeBlockSnapshot(n.w, n.id, n.configBlock())
}

func (n *networkRouterNode) configBlock() formular.Block {
	field := &formular.Field{
		Kind:        formular.FieldText,
		Value:       n.rulesText,
		Multiline:   true,
		Validation:  true,
		Placeholder: "[DialTCP]\nTRUE\nSLOT 1",
	}
	if n.formError != "" {
		field.Status = formular.StatusError
		field.StatusText = n.formError
	}
	return formular.Block{ID: "config", Order: 10, Generation: 1, Form: true, Items: []formular.Item{
		{Type: formular.ItemField, ID: "rules", Label: "Routing rules", Field: field},
	}}
}
