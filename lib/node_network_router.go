package almagest

import (
	"strconv"
	"strings"

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
		NetworkPort("right", networkRouterSlotName(1)),
	}}
}
func (networkRouterClass) NewNode(cfg configer.Config, _ ...*pasta.NodeClassState) (pasta.Node, error) {
	return &networkRouterNode{
		rulesText: readConfigString(cfg, "rules", ""),
		router:    gonnect.NewRouter(),
		backends:  map[int]NetworkCloser{},
		sent:      map[uint64]NetworkCloser{},
	}, nil
}

type networkRouterNode struct {
	pasta.BasicNode

	w  *pasta.Workspace
	id uint64

	in    uint64
	slots [networkRouterMaxSlots + 1]uint64

	router    *gonnect.Router
	cfg       routing.RouterCfg
	rulesText string
	formError string
	backends  map[int]NetworkCloser
	sent      map[uint64]NetworkCloser
}

func (n *networkRouterNode) OnInit(w *pasta.Workspace, _ pasta.Logger, id uint64, _ string, restored *pasta.NodeInitData, _, _, _, _ bool) error {
	n.w = w
	n.id = id
	if n.router == nil {
		n.router = gonnect.NewRouter()
	}
	if n.backends == nil {
		n.backends = map[int]NetworkCloser{}
	}
	if n.sent == nil {
		n.sent = map[uint64]NetworkCloser{}
	}
	if restored != nil {
		if len(restored.LeftPorts) > 0 {
			n.in = restored.LeftPorts[0]
		}
		for i, port := range restored.RightPorts {
			slot := n.slotFromPort(port, i+1)
			if slot >= 1 && slot <= networkRouterMaxSlots && n.slots[slot] == 0 {
				n.slots[slot] = port
			}
		}
	}
	if err := n.w.SetNodePrimary(n.id, TypeNetwork); err != nil {
		return err
	}
	_ = n.w.SetNodeLabel(n.id, "router")
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
	if n.router != nil {
		_ = n.router.Close()
	}
}

func (n *networkRouterNode) OnReady() error {
	n.sendAll()
	return nil
}

func (n *networkRouterNode) PreLinkAdd(port uint64, linkType, portDirection string) error {
	if port == n.in {
		if portDirection != "left" || linkType != TypeNetwork {
			return pasta.LinkTypeErr(linkType)
		}
		return nil
	}
	if slot := n.portSlot(port); slot != 0 {
		if portDirection != "right" || linkType != TypeNetwork {
			return pasta.LinkTypeErr(linkType)
		}
		if n.portHasLinks(port) {
			return pasta.ErrLinkDup
		}
		return nil
	}
	return pasta.LinkTypeErr(linkType)
}

func (n *networkRouterNode) OnLinkAdd(link, port uint64, linkType, portDirection string) error {
	if port == n.in && portDirection == "left" && linkType == TypeNetwork {
		n.sendRouterToLink(link)
		return nil
	}
	if n.portSlot(port) != 0 && portDirection == "right" && linkType == TypeNetwork {
		std.Request(n.w, n.id, link)
	}
	return nil
}

func (n *networkRouterNode) OnLinkRemoved(link, port uint64, linkType, portDirection string) error {
	if port == n.in && portDirection == "left" && linkType == TypeNetwork {
		n.closeSentNetwork(link)
		return nil
	}
	if slot := n.portSlot(port); slot != 0 && portDirection == "right" && linkType == TypeNetwork {
		n.detachSlot(slot)
	}
	return nil
}

func (n *networkRouterNode) OnPortRemoved(port uint64, direction string) error {
	if direction != "right" {
		return nil
	}
	for slot, id := range n.slots {
		if id == port {
			n.slots[slot] = 0
			n.detachSlot(slot)
			return nil
		}
	}
	return nil
}

func (n *networkRouterNode) OnEvent(event pasta.Event, linkType string, _ []string, receiverPortDirection string) error {
	if event.ReceiverPort == n.in && receiverPortDirection == "left" && linkType == TypeNetwork && std.IsRequest(event.Payload) {
		n.sendRouterToLink(event.Link)
		return nil
	}
	slot := n.portSlot(event.ReceiverPort)
	if slot == 0 || receiverPortDirection != "right" || linkType != TypeNetwork {
		return nil
	}
	payload, ok := event.Payload.(NetworkPayload)
	if !ok || payload.Network == nil {
		return nil
	}
	if current := n.backends[slot]; SameNetworkInstance(current, payload.Network) {
		return nil
	}
	n.detachSlot(slot)
	BindNetworkResource(n.w, n.id, event.Link, payload.Network)
	n.backends[slot] = payload.Network
	return n.router.Attach(slot, payload.Network)
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
	text, _ := stringFromAny(values["rules"])
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
	text, _ := stringFromAny(msg.Value)
	status := formular.StatusOK
	statusText := ""
	if _, err := n.compileRules(text); err != nil {
		status = formular.StatusError
		statusText = err.Error()
	}
	n.w.SendNodeMenuMsg(n.id, formular.FieldStatusMessage{
		MessageBase: formular.MessageBase{
			Type:            formular.MessageFieldStatus,
			MenuID:          pasta.NodeMenuID(n.id),
			MenuGeneration:  1,
			BlockGeneration: 1,
		},
		Field:      msg.Field,
		Status:     status,
		StatusText: statusText,
	})
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
	for slot, port := range n.slots {
		if slot == 0 || port == 0 {
			continue
		}
		if n.portHasLinks(port) {
			desired[slot] = struct{}{}
		}
	}
	if len(desired) == 0 {
		desired[1] = struct{}{}
	}

	for slot := 1; slot <= networkRouterMaxSlots; slot++ {
		port := n.slots[slot]
		_, keep := desired[slot]
		if !keep && port != 0 && !n.portHasLinks(port) {
			n.w.RemovePort(port)
			n.slots[slot] = 0
			continue
		}
		if keep && port == 0 {
			id, err := n.w.AddPort(pasta.Port{
				Node:      n.id,
				Direction: "right",
				Name:      networkRouterSlotName(slot),
				Types:     []string{TypeNetwork},
			})
			if err != nil {
				return err
			}
			n.slots[slot] = id
			continue
		}
		if keep && port != 0 {
			_ = n.w.SetPortName(port, networkRouterSlotName(slot))
		}
	}

	order := make([]uint64, 0, len(desired))
	for slot := 1; slot <= networkRouterMaxSlots; slot++ {
		if n.slots[slot] != 0 {
			order = append(order, n.slots[slot])
		}
	}
	if len(order) > 0 {
		return n.w.SetNodePortOrder(n.id, "right", order)
	}
	return nil
}

func (n *networkRouterNode) sendAll() {
	port, ok := n.w.PortSnapshot(n.in)
	if !ok {
		return
	}
	for _, link := range port.Links {
		n.sendRouterToLink(link)
	}
}

func (n *networkRouterNode) sendRouterToLink(link uint64) {
	wrapper := n.sent[link]
	if wrapper == nil {
		wrapper = gonnect.DetachNetwork(n.router, nil)
		BindNetworkResource(n.w, n.id, link, wrapper)
		n.closeSentNetwork(link)
		n.sent[link] = wrapper
	}
	n.w.EmitEvent(n.id, link, NetworkPayload{Network: wrapper})
}

func (n *networkRouterNode) closeSentNetwork(link uint64) {
	if n.sent == nil {
		return
	}
	if old := n.sent[link]; old != nil {
		_ = old.Close()
		delete(n.sent, link)
	}
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

func (n *networkRouterNode) portSlot(port uint64) int {
	for slot, id := range n.slots {
		if id == port {
			return slot
		}
	}
	return 0
}

func (n *networkRouterNode) slotFromPort(port uint64, fallback int) int {
	snapshot, ok := n.w.PortSnapshot(port)
	if !ok {
		return fallback
	}
	if slot, ok := networkRouterSlotFromName(snapshot.Name); ok {
		return slot
	}
	return fallback
}

func (n *networkRouterNode) portHasLinks(port uint64) bool {
	snapshot, ok := n.w.PortSnapshot(port)
	return ok && len(snapshot.Links) > 0
}

func (n *networkRouterNode) sendMenuSnapshot() {
	if n.w == nil || n.id == 0 {
		return
	}
	n.w.SendNodeMenuMsg(n.id, formular.MenuSnapshotMessage{
		MessageBase: formular.MessageBase{Type: formular.MessageMenuSnapshot, MenuID: pasta.NodeMenuID(n.id), MenuGeneration: 1},
		Blocks:      []formular.Block{n.configBlock()},
	})
}

func (n *networkRouterNode) sendForcedMenuSnapshot() {
	if n.w == nil || n.id == 0 {
		return
	}
	n.w.SendNodeMenuMsg(n.id, formular.MenuSnapshotMessage{
		MessageBase: formular.MessageBase{Type: formular.MessageMenuSnapshot, MenuID: pasta.NodeMenuID(n.id), MenuGeneration: 1},
		Force:       true,
		Blocks:      []formular.Block{n.configBlock()},
	})
}

func (n *networkRouterNode) sendConfigBlock() {
	if n.w == nil || n.id == 0 {
		return
	}
	n.w.SendNodeMenuMsg(n.id, formular.BlockSnapshotMessage{
		MessageBase: formular.MessageBase{Type: formular.MessageBlockSnapshot, MenuID: pasta.NodeMenuID(n.id), MenuGeneration: 1, BlockGeneration: 1},
		Block:       n.configBlock(),
	})
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

func networkRouterSlotName(slot int) string {
	return "slot " + strconv.Itoa(slot)
}

func networkRouterSlotFromName(name string) (int, bool) {
	parts := strings.Fields(strings.ToLower(name))
	if len(parts) != 2 || parts[0] != "slot" {
		return 0, false
	}
	slot, err := strconv.Atoi(parts[1])
	if err != nil || slot < 1 || slot > networkRouterMaxSlots {
		return 0, false
	}
	return slot, true
}
