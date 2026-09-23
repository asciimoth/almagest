package dnsnodes

import (
	_ "embed"
	"fmt"
	"slices"
	"strings"

	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/formular"
	"github.com/asciimoth/gonnect"
	"github.com/asciimoth/gonnect/dns"
	"github.com/asciimoth/gonnect/routing"
	"github.com/asciimoth/pasta/pasta"
	"github.com/asciimoth/pasta/pasta/std"

	"github.com/asciimoth/almagest/lib/nodekit"
)

const (
	// NodeTypeDnsRouter is the node class name for a routing DNS middleware.
	NodeTypeDnsRouter = "almagest/DnsRouter"

	dnsRouterMaxSlots      = 16
	DNSRouterConfigBlockID = "config"
	DNSRouterRulesField    = "rules"
)

const dnsRouterDefaultRules = "TRUE\nBACKEND slot 1"

var (
	//go:embed node_dns_router.md
	dnsRouterClassLongDescription string

	dnsRouterNewRouter = func(spawner gonnect.Spawner) *dns.Router {
		return dns.NewRouter(spawner)
	}
)

// DNSRouterClass builds routing DNS middleware nodes.
type DNSRouterClass struct {
	Runtime nodekit.Runtime
}

func (DNSRouterClass) ClassName() string        { return NodeTypeDnsRouter }
func (DNSRouterClass) ShortDescription() string { return "DNS router" }
func (DNSRouterClass) LongDescription() string  { return dnsRouterClassLongDescription }
func (DNSRouterClass) DefaultNodeParams() pasta.NodeClassParams {
	return pasta.NodeClassParams{PrimaryType: TypeDNS, InitialPorts: []pasta.Port{
		DNSPort("left", "DNS"),
		DNSPort("right", nodekit.SlotPortName(1)),
	}}
}
func (c DNSRouterClass) NewNode(cfg configer.Config, _ ...*pasta.NodeClassState) (pasta.Node, error) {
	return &dnsRouterNode{
		Base:      nodekit.Base{Runtime: c.Runtime},
		rulesText: configer.String(cfg, configer.Path{DNSRouterRulesField}, ""),
		slots: nodekit.NewSlotPorts(
			dnsRouterMaxSlots,
			"right",
			func(slot int) pasta.Port { return DNSPort("right", nodekit.SlotPortName(slot)) },
			nodekit.SlotPortName,
			func(name string) (int, bool) { return nodekit.SlotPortFromName(name, dnsRouterMaxSlots) },
		),
		backends: map[int]dns.Interface{},
	}, nil
}

// dnsRouterNode owns one gonnect/dns.Router. The left-directed DNS port emits
// detached wrappers around that stable router instance. Each right-directed
// slot port accepts one upstream DNS interface and installs it in the router
// under the same "slot N" name used by the graph port.
type dnsRouterNode struct {
	nodekit.Base

	out   uint64
	slots nodekit.SlotPorts

	router *dns.Router
	rules  routing.DNSBytecodeRules

	rulesText string
	formError string
	ready     bool

	backends map[int]dns.Interface
	outputs  DNSOutputCache
}

func (n *dnsRouterNode) OnInit(w *pasta.Workspace, logger pasta.Logger, id uint64, _ string, restored *pasta.NodeInitData, _, isPlaceholderReplacement, _, isRestored bool) error {
	n.InitBaseNode(w, logger, id)
	if n.router == nil {
		n.router = dnsRouterNewRouter(n.dnsSpawner())
	}
	if n.backends == nil {
		n.backends = map[int]dns.Interface{}
	}
	if n.slots.Max() == 0 {
		n.slots = nodekit.NewSlotPorts(
			dnsRouterMaxSlots,
			"right",
			func(slot int) pasta.Port { return DNSPort("right", nodekit.SlotPortName(slot)) },
			nodekit.SlotPortName,
			func(name string) (int, bool) { return nodekit.SlotPortFromName(name, dnsRouterMaxSlots) },
		)
	}
	n.out = nodekit.RestoredLeft(restored, 0)
	n.slots.Init(w, id, nodekit.RestoredRightPorts(restored))
	if err := n.SetPrimary(TypeDNS); err != nil {
		return err
	}
	BindDNSResource(n.W, n.ID, 0, n.router)

	rules, route, err := n.compileRules(n.rulesText)
	if err != nil {
		n.formError = err.Error()
	} else {
		n.rules = rules
		if err := n.router.SetRouter(route); err != nil {
			return fmt.Errorf("set DNS router rules: %w", err)
		}
	}
	if err := n.reviewOutputPorts(nodekit.SlotPortReview{DefaultSlot: 1, PreserveExisting: isRestored || isPlaceholderReplacement}); err != nil {
		return err
	}
	if err := n.updateLabel(); err != nil {
		return err
	}
	n.sendMenuSnapshot()
	return nil
}

func (n *dnsRouterNode) OnReady() error {
	if n.slots.TakeDeferredReview() {
		if err := n.reviewOutputPorts(nodekit.SlotPortReview{DefaultSlot: 1}); err != nil {
			return err
		}
	}
	n.ready = true
	n.requestSlots()
	if len(n.backends) > 0 || !n.hasSlotLinks() {
		n.sendAll()
	}
	return n.updateLabel()
}

func (n *dnsRouterNode) OnStop() {
	n.outputs.CloseAll()
	for slot := range n.backends {
		n.detachSlot(slot)
	}
	if n.router != nil {
		_ = n.router.Close()
	}
}

func (n *dnsRouterNode) PreLinkAdd(port uint64, linkType, portDirection string) error {
	if port == n.out {
		if portDirection != "left" || linkType != TypeDNS {
			return pasta.LinkTypeErr(linkType)
		}
		return nil
	}
	if n.slots.Slot(port) != 0 {
		if portDirection != "right" || linkType != TypeDNS {
			return pasta.LinkTypeErr(linkType)
		}
		if n.PortHasLinks(port) {
			return pasta.ErrLinkDup
		}
		return nil
	}
	return pasta.LinkTypeErr(linkType)
}

func (n *dnsRouterNode) OnLinkAdd(link, port uint64, linkType, portDirection string) error {
	if linkType != TypeDNS {
		return nil
	}
	switch {
	case port == n.out && portDirection == "left":
		if n.ready {
			n.sendRouterToLink(link)
		}
	case n.slots.Slot(port) != 0 && portDirection == "right":
		if n.ready {
			std.RequestLocked(n.W, n.ID, link)
		}
	}
	return n.updateLabel()
}

func (n *dnsRouterNode) OnLinkRemoved(link, port uint64, linkType, portDirection string) error {
	if linkType != TypeDNS {
		return nil
	}
	switch {
	case port == n.out && portDirection == "left":
		n.outputs.Close(link)
	case n.slots.Slot(port) != 0 && portDirection == "right":
		n.detachSlot(n.slots.Slot(port))
		if err := n.reviewOutputPorts(nodekit.SlotPortReview{DefaultSlot: 1}); err != nil {
			return err
		}
	}
	return n.updateLabel()
}

func (n *dnsRouterNode) OnPortRemoved(port uint64, direction string) error {
	if direction != "right" {
		return nil
	}
	if slot := n.slots.Remove(port); slot != 0 {
		n.detachSlot(slot)
	}
	return n.updateLabel()
}

func (n *dnsRouterNode) OnEvent(event pasta.Event, linkType string, _ []string, receiverPortDirection string) error {
	if linkType != TypeDNS {
		return nil
	}
	if event.ReceiverPort == n.out && receiverPortDirection == "left" && std.IsRequest(event.Payload) {
		n.refreshRouterToLink(event.Link)
		return nil
	}
	slot := n.slots.Slot(event.ReceiverPort)
	if slot == 0 || receiverPortDirection != "right" {
		return nil
	}
	if std.IsRequest(event.Payload) {
		return nil
	}
	payload, ok := event.Payload.(DNSPayload)
	if !ok || SameDNSInstance(n.backends[slot], payload.DNS) {
		return nil
	}
	if payload.DNS == nil {
		n.detachSlot(slot)
		return n.updateLabel()
	}
	if !DNSIsReady(payload.DNS) {
		return nil
	}
	n.detachSlot(slot)
	if err := n.router.Attach(nodekit.SlotPortName(slot), payload.DNS); err != nil {
		CloseDNS(payload.DNS)
		return fmt.Errorf("attach DNS router %s: %w", nodekit.SlotPortName(slot), err)
	}
	BindDNSResource(n.W, n.ID, event.Link, payload.DNS)
	n.backends[slot] = payload.DNS
	if n.ready {
		n.sendAll()
	}
	return n.updateLabel()
}

func (n *dnsRouterNode) OnFormularMsg(message any) error {
	switch msg := message.(type) {
	case formular.FormApplyMessage:
		if msg.MenuID != pasta.NodeMenuID(n.ID) || msg.BlockID != DNSRouterConfigBlockID {
			return nil
		}
		return n.applyConfigForm(msg.Values)
	case formular.FieldValidateMessage:
		if msg.MenuID != pasta.NodeMenuID(n.ID) || msg.Field.BlockID != DNSRouterConfigBlockID || msg.Field.FieldID != DNSRouterRulesField || len(msg.Field.ElementPath) > 0 {
			return nil
		}
		n.sendRulesFieldStatus(msg)
	}
	return nil
}

func (n *dnsRouterNode) OnSave(cfg configer.Config) error {
	if err := pasta.DeleteNodeOwnedConfigKeys(cfg); err != nil {
		return err
	}
	return cfg.Set(configer.Path{DNSRouterRulesField}, n.rulesText)
}

func (n *dnsRouterNode) applyConfigForm(values map[string]any) error {
	text, _ := formular.StringValue(values[DNSRouterRulesField])
	rules, route, err := n.compileRules(text)
	if err != nil {
		n.formError = err.Error()
		n.sendConfigBlock()
		return n.updateLabel()
	}
	if err := n.router.SetRouter(route); err != nil {
		return fmt.Errorf("set DNS router rules: %w", err)
	}
	n.rulesText = text
	n.rules = rules
	n.formError = ""
	if err := n.reviewOutputPorts(nodekit.SlotPortReview{DefaultSlot: 1}); err != nil {
		return err
	}
	n.sendForcedMenuSnapshot()
	n.sendAll()
	return n.updateLabel()
}

func (n *dnsRouterNode) sendRulesFieldStatus(msg formular.FieldValidateMessage) {
	text, _ := formular.StringValue(msg.Value)
	_, _, err := n.compileRules(text)
	nodekit.SendNodeFieldStatus(n.W, n.ID, msg.Field, err)
}

func (n *dnsRouterNode) reviewOutputPorts(opts nodekit.SlotPortReview) error {
	desired := map[int]struct{}{}
	for _, slot := range dnsRouterRuleSlots(n.rules) {
		desired[slot] = struct{}{}
	}
	return n.slots.Review(desired, opts)
}

func (n *dnsRouterNode) requestSlots() {
	n.slots.ForEach(func(_ int, port uint64) {
		n.RequestPort(port)
	})
}

func (n *dnsRouterNode) sendAll() {
	n.outputs.SendPortLocked(n.W, n.ID, n.out, n.router)
}

func (n *dnsRouterNode) sendRouterToLink(link uint64) {
	n.outputs.SendLocked(n.W, n.ID, link, n.router)
}

func (n *dnsRouterNode) refreshRouterToLink(link uint64) {
	n.outputs.RefreshLocked(n.W, n.ID, link, n.router)
}

func (n *dnsRouterNode) detachSlot(slot int) {
	if slot < 1 || slot > dnsRouterMaxSlots {
		return
	}
	if old := n.backends[slot]; old != nil {
		if n.router != nil {
			_ = n.router.Detach(nodekit.SlotPortName(slot))
		}
		CloseDNS(old)
		delete(n.backends, slot)
	}
}

func (n *dnsRouterNode) hasSlotLinks() bool {
	return n.slots.HasLinks()
}

func (n *dnsRouterNode) dnsSpawner() gonnect.Spawner {
	if n.WorkerSpawner == nil {
		return nil
	}
	return n.WorkerSpawner
}

func (n *dnsRouterNode) updateLabel() error {
	switch {
	case n.formError != "":
		return n.SetLabel("invalid config")
	case len(n.backends) == 0:
		return n.SetLabel("waiting for DNS")
	case len(n.backends) == 1:
		return n.SetLabel("routing DNS")
	default:
		return n.SetLabel(fmt.Sprintf("routing %d DNS backends", len(n.backends)))
	}
}

func (n *dnsRouterNode) sendMenuSnapshot() {
	nodekit.SendNodeMenuSnapshot(n.W, n.ID, false, n.configBlock())
}

func (n *dnsRouterNode) sendForcedMenuSnapshot() {
	nodekit.SendNodeMenuSnapshot(n.W, n.ID, true, n.configBlock())
}

func (n *dnsRouterNode) sendConfigBlock() {
	nodekit.SendNodeBlockSnapshot(n.W, n.ID, n.configBlock())
}

func (n *dnsRouterNode) configBlock() formular.Block {
	field := &formular.Field{
		Kind:        formular.FieldText,
		Value:       n.rulesText,
		Multiline:   true,
		Validation:  true,
		Placeholder: "ADDR_RE \\.internal\\.test\\.$\nBACKEND slot 1\n\nTRUE\nBACKEND slot 2",
	}
	if n.formError != "" {
		field.Status = formular.StatusError
		field.StatusText = n.formError
	}
	return formular.Block{ID: DNSRouterConfigBlockID, Order: 10, Generation: 1, Form: true, Items: []formular.Item{
		{Type: formular.ItemField, ID: DNSRouterRulesField, Label: "Routing rules", Field: field},
	}}
}

func (n *dnsRouterNode) compileRules(text string) (routing.DNSBytecodeRules, dns.RouteFunc, error) {
	rules, err := routing.NewDNSBytecodeRules(dnsRouterCompileText(text))
	if err != nil {
		return routing.DNSBytecodeRules{}, nil, err
	}
	if _, err := dnsRouterBackendSlots(rules); err != nil {
		return routing.DNSBytecodeRules{}, nil, err
	}
	route, err := routing.NewBytecodeDNSRouteFunc(rules)
	if err != nil {
		return routing.DNSBytecodeRules{}, nil, err
	}
	return rules, route, nil
}

func dnsRouterCompileText(text string) string {
	for _, raw := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		trimmed := strings.TrimSpace(raw)
		if trimmed != "" && !strings.HasPrefix(trimmed, "#") {
			return text
		}
	}
	return dnsRouterDefaultRules
}

func dnsRouterRuleSlots(rules routing.DNSBytecodeRules) []int {
	slots, _ := dnsRouterBackendSlots(rules)
	return slots
}

// CompileRouterRules compiles DNS router bytecode rules and their route function.
func CompileRouterRules(text string) (routing.DNSBytecodeRules, dns.RouteFunc, error) {
	return (&dnsRouterNode{}).compileRules(text)
}

// RouterRuleSlots returns the numbered backend slots used by compiled rules.
func RouterRuleSlots(rules routing.DNSBytecodeRules) []int {
	return dnsRouterRuleSlots(rules)
}

func dnsRouterBackendSlots(rules routing.DNSBytecodeRules) ([]int, error) {
	seen := map[int]struct{}{}
	var slots []int
	for _, backend := range rules.Backends {
		slot, ok := nodekit.SlotPortFromName(backend, dnsRouterMaxSlots)
		if !ok {
			return nil, fmt.Errorf("backend %q must be named %q through %q", backend, nodekit.SlotPortName(1), nodekit.SlotPortName(dnsRouterMaxSlots))
		}
		if _, ok := seen[slot]; ok {
			continue
		}
		seen[slot] = struct{}{}
		slots = append(slots, slot)
	}
	slices.Sort(slots)
	return slots, nil
}
