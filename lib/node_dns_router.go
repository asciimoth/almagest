package almagest

import (
	_ "embed"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/formular"
	"github.com/asciimoth/gonnect"
	"github.com/asciimoth/gonnect/dns"
	"github.com/asciimoth/pasta/pasta"
	"github.com/asciimoth/pasta/pasta/std"
)

func init() {
	regClass(func(e *engine) pasta.NodeClass {
		return dnsRouterClass{engine: e}
	}, false)
}

const (
	// NodeTypeDnsRouter is the node class name for a routing DNS middleware.
	NodeTypeDnsRouter = "almagest/DnsRouter"

	dnsRouterMaxSlots      = 16
	dnsRouterConfigBlockID = "config"
	dnsRouterRulesField    = "rules"
)

var (
	//go:embed node_dns_router.md
	dnsRouterClassLongDescription string

	dnsRouterNewRouter = func(spawner gonnect.Spawner) *dns.Router {
		return dns.NewRouter(spawner)
	}
)

type dnsRouterClass struct {
	engine *engine
}

func (dnsRouterClass) ClassName() string        { return NodeTypeDnsRouter }
func (dnsRouterClass) ShortDescription() string { return "DNS router" }
func (dnsRouterClass) LongDescription() string  { return dnsRouterClassLongDescription }
func (dnsRouterClass) DefaultNodeParams() pasta.NodeClassParams {
	return pasta.NodeClassParams{PrimaryType: TypeDNS, InitialPorts: []pasta.Port{
		DNSPort("left", "DNS"),
		DNSPort("right", slotPortName(1)),
	}}
}
func (c dnsRouterClass) NewNode(cfg configer.Config, _ ...*pasta.NodeClassState) (pasta.Node, error) {
	return &dnsRouterNode{
		NodeBase:  NodeBase{engine: c.engine},
		rulesText: configer.String(cfg, configer.Path{dnsRouterRulesField}, ""),
		slots: newSlotPorts(
			dnsRouterMaxSlots,
			"right",
			func(slot int) pasta.Port { return DNSPort("right", slotPortName(slot)) },
			slotPortName,
			func(name string) (int, bool) { return slotPortFromName(name, dnsRouterMaxSlots) },
		),
		backends: map[int]dns.Interface{},
	}, nil
}

// dnsRouterNode owns one gonnect/dns.Router. The left-directed DNS port emits
// detached wrappers around that stable router instance. Each right-directed
// slot port accepts one upstream DNS interface and installs it in the router
// under the same "slot N" name used by the graph port.
type dnsRouterNode struct {
	NodeBase

	out   uint64
	slots slotPorts

	router *dns.Router
	rules  dnsRouterRules

	rulesText string
	formError string
	ready     bool

	backends map[int]dns.Interface
	outputs  DNSOutputCache
}

func (n *dnsRouterNode) OnInit(w *pasta.Workspace, logger pasta.Logger, id uint64, _ string, restored *pasta.NodeInitData, _, _, _, _ bool) error {
	n.InitBaseNode(w, logger, id)
	if n.router == nil {
		n.router = dnsRouterNewRouter(n.dnsSpawner())
	}
	if n.backends == nil {
		n.backends = map[int]dns.Interface{}
	}
	if n.slots.max == 0 {
		n.slots = newSlotPorts(
			dnsRouterMaxSlots,
			"right",
			func(slot int) pasta.Port { return DNSPort("right", slotPortName(slot)) },
			slotPortName,
			func(name string) (int, bool) { return slotPortFromName(name, dnsRouterMaxSlots) },
		)
	}
	n.out = restoredLeft(restored, 0)
	n.slots.Init(w, id, restoredRightPorts(restored))
	if err := n.SetPrimary(TypeDNS); err != nil {
		return err
	}
	BindDNSResource(n.w, n.id, 0, n.router)

	rules, err := parseDNSRouterRules(n.rulesText)
	if err != nil {
		n.formError = err.Error()
	} else {
		n.rules = rules
		if err := n.router.SetRouter(rules.Route); err != nil {
			return fmt.Errorf("set DNS router rules: %w", err)
		}
	}
	if err := n.reviewOutputPorts(); err != nil {
		return err
	}
	if err := n.updateLabel(); err != nil {
		return err
	}
	n.sendMenuSnapshot()
	return nil
}

func (n *dnsRouterNode) OnReady() error {
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
			std.RequestLocked(n.w, n.id, link)
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
	if !ok || !DNSIsReady(payload.DNS) || SameDNSInstance(n.backends[slot], payload.DNS) {
		return nil
	}
	n.detachSlot(slot)
	if err := n.router.Attach(slotPortName(slot), payload.DNS); err != nil {
		closeDNS(payload.DNS)
		return fmt.Errorf("attach DNS router %s: %w", slotPortName(slot), err)
	}
	BindDNSResource(n.w, n.id, event.Link, payload.DNS)
	n.backends[slot] = payload.DNS
	if n.ready {
		n.sendAll()
	}
	return n.updateLabel()
}

func (n *dnsRouterNode) OnFormularMsg(message any) error {
	switch msg := message.(type) {
	case formular.FormApplyMessage:
		if msg.MenuID != pasta.NodeMenuID(n.id) || msg.BlockID != dnsRouterConfigBlockID {
			return nil
		}
		return n.applyConfigForm(msg.Values)
	case formular.FieldValidateMessage:
		if msg.MenuID != pasta.NodeMenuID(n.id) || msg.Field.BlockID != dnsRouterConfigBlockID || msg.Field.FieldID != dnsRouterRulesField || len(msg.Field.ElementPath) > 0 {
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
	return cfg.Set(configer.Path{dnsRouterRulesField}, n.rulesText)
}

func (n *dnsRouterNode) applyConfigForm(values map[string]any) error {
	text, _ := formular.StringValue(values[dnsRouterRulesField])
	rules, err := parseDNSRouterRules(text)
	if err != nil {
		n.formError = err.Error()
		n.sendConfigBlock()
		return n.updateLabel()
	}
	if err := n.router.SetRouter(rules.Route); err != nil {
		return fmt.Errorf("set DNS router rules: %w", err)
	}
	n.rulesText = text
	n.rules = rules
	n.formError = ""
	if err := n.reviewOutputPorts(); err != nil {
		return err
	}
	n.sendForcedMenuSnapshot()
	n.sendAll()
	return n.updateLabel()
}

func (n *dnsRouterNode) sendRulesFieldStatus(msg formular.FieldValidateMessage) {
	text, _ := formular.StringValue(msg.Value)
	_, err := parseDNSRouterRules(text)
	sendNodeFieldStatus(n.w, n.id, msg.Field, err)
}

func (n *dnsRouterNode) reviewOutputPorts() error {
	desired := map[int]struct{}{}
	for _, slot := range n.rules.MentionedSlots() {
		desired[slot] = struct{}{}
	}
	return n.slots.Review(desired, slotPortReview{DefaultSlot: 1})
}

func (n *dnsRouterNode) requestSlots() {
	n.slots.ForEach(func(_ int, port uint64) {
		n.RequestPort(port)
	})
}

func (n *dnsRouterNode) sendAll() {
	BroadcastDNSPort(n.w, n.id, n.out, func(link uint64) {
		n.sendRouterToLink(link)
	})
}

func (n *dnsRouterNode) sendRouterToLink(link uint64) {
	if n.router == nil {
		return
	}
	n.outputs.SendLocked(n.w, n.id, link, n.router)
}

func (n *dnsRouterNode) refreshRouterToLink(link uint64) {
	if n.router == nil {
		return
	}
	n.outputs.RefreshLocked(n.w, n.id, link, n.router)
}

func (n *dnsRouterNode) detachSlot(slot int) {
	if slot < 1 || slot > dnsRouterMaxSlots {
		return
	}
	if old := n.backends[slot]; old != nil {
		if n.router != nil {
			_ = n.router.Detach(slotPortName(slot))
		}
		closeDNS(old)
		delete(n.backends, slot)
	}
}

func (n *dnsRouterNode) hasSlotLinks() bool {
	return n.slots.HasLinks()
}

func (n *dnsRouterNode) dnsSpawner() gonnect.Spawner {
	if n.workerSpawner.tracker == nil {
		return nil
	}
	return n.workerSpawner
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
	sendNodeMenuSnapshot(n.w, n.id, false, n.configBlock())
}

func (n *dnsRouterNode) sendForcedMenuSnapshot() {
	sendNodeMenuSnapshot(n.w, n.id, true, n.configBlock())
}

func (n *dnsRouterNode) sendConfigBlock() {
	sendNodeBlockSnapshot(n.w, n.id, n.configBlock())
}

func (n *dnsRouterNode) configBlock() formular.Block {
	field := &formular.Field{
		Kind:        formular.FieldText,
		Value:       n.rulesText,
		Multiline:   true,
		Validation:  true,
		Placeholder: "SUFFIX internal.test SLOT 1\nDEFAULT SLOT 2",
	}
	if n.formError != "" {
		field.Status = formular.StatusError
		field.StatusText = n.formError
	}
	return formular.Block{ID: dnsRouterConfigBlockID, Order: 10, Generation: 1, Form: true, Items: []formular.Item{
		{Type: formular.ItemField, ID: dnsRouterRulesField, Label: "Routing rules", Field: field},
	}}
}

type dnsRouterRules struct {
	rules []dnsRouterRule
}

func (r dnsRouterRules) Route(msg *dns.Message) string {
	if len(r.rules) == 0 {
		return slotPortName(1)
	}
	for _, rule := range r.rules {
		if rule.Match(msg) {
			return slotPortName(rule.slot)
		}
	}
	return ""
}

func (r dnsRouterRules) MentionedSlots() []int {
	seen := map[int]struct{}{}
	var slots []int
	for _, rule := range r.rules {
		if _, ok := seen[rule.slot]; ok {
			continue
		}
		seen[rule.slot] = struct{}{}
		slots = append(slots, rule.slot)
	}
	return slots
}

type dnsRouterNameMatch int

const (
	dnsRouterNameAny dnsRouterNameMatch = iota
	dnsRouterNameExact
	dnsRouterNameSuffix
)

type dnsRouterRule struct {
	nameMode dnsRouterNameMatch
	name     string
	qtype    uint16
	slot     int
}

func (r dnsRouterRule) Match(msg *dns.Message) bool {
	q, ok := firstDNSRouterQuestion(msg)
	if r.qtype != 0 {
		if !ok || q.Type != r.qtype {
			return false
		}
	}
	switch r.nameMode {
	case dnsRouterNameExact:
		return ok && canonicalDNSName(q.Name) == r.name
	case dnsRouterNameSuffix:
		return ok && dnsRouterNameHasSuffix(canonicalDNSName(q.Name), r.name)
	default:
		return true
	}
}

func parseDNSRouterRules(text string) (dnsRouterRules, error) {
	var rules []dnsRouterRule
	for i, raw := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		line := strings.TrimSpace(raw)
		if idx := strings.Index(line, "#"); idx >= 0 {
			line = strings.TrimSpace(line[:idx])
		}
		if line == "" {
			continue
		}
		rule, err := parseDNSRouterRuleLine(line)
		if err != nil {
			return dnsRouterRules{}, fmt.Errorf("line %d: %w", i+1, err)
		}
		rules = append(rules, rule)
	}
	return dnsRouterRules{rules: rules}, nil
}

func parseDNSRouterRuleLine(line string) (dnsRouterRule, error) {
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return dnsRouterRule{}, errors.New("empty rule")
	}
	var rule dnsRouterRule
	var sawName bool
	var sawSlot bool
	var sawType bool
	var sawDefault bool
	for i := 0; i < len(fields); {
		token := strings.ToUpper(fields[i])
		i++
		switch token {
		case "DEFAULT", "ANY", "TRUE":
			if sawName || sawType {
				return dnsRouterRule{}, errors.New("DEFAULT cannot be combined with NAME, SUFFIX, or TYPE")
			}
			rule.nameMode = dnsRouterNameAny
			sawDefault = true
		case "NAME", "EXACT":
			if sawDefault {
				return dnsRouterRule{}, errors.New("DEFAULT cannot be combined with NAME, SUFFIX, or TYPE")
			}
			if sawName {
				return dnsRouterRule{}, errors.New("duplicate name matcher")
			}
			value, ok := nextDNSRouterRuleValue(fields, &i)
			if !ok {
				return dnsRouterRule{}, fmt.Errorf("%s requires a DNS name", token)
			}
			rule.nameMode = dnsRouterNameExact
			rule.name = canonicalDNSName(value)
			sawName = true
		case "SUFFIX", "DOMAIN":
			if sawDefault {
				return dnsRouterRule{}, errors.New("DEFAULT cannot be combined with NAME, SUFFIX, or TYPE")
			}
			if sawName {
				return dnsRouterRule{}, errors.New("duplicate name matcher")
			}
			value, ok := nextDNSRouterRuleValue(fields, &i)
			if !ok {
				return dnsRouterRule{}, fmt.Errorf("%s requires a DNS name", token)
			}
			rule.nameMode = dnsRouterNameSuffix
			rule.name = canonicalDNSName(value)
			sawName = true
		case "TYPE":
			if sawDefault {
				return dnsRouterRule{}, errors.New("DEFAULT cannot be combined with NAME, SUFFIX, or TYPE")
			}
			if sawType {
				return dnsRouterRule{}, errors.New("duplicate TYPE matcher")
			}
			value, ok := nextDNSRouterRuleValue(fields, &i)
			if !ok {
				return dnsRouterRule{}, errors.New("TYPE requires a DNS record type")
			}
			typ, err := parseDNSRouterType(value)
			if err != nil {
				return dnsRouterRule{}, err
			}
			rule.qtype = typ
			sawType = true
		case "SLOT":
			if sawSlot {
				return dnsRouterRule{}, errors.New("duplicate SLOT action")
			}
			value, ok := nextDNSRouterRuleValue(fields, &i)
			if !ok {
				return dnsRouterRule{}, errors.New("SLOT requires a number")
			}
			slot, err := parseDNSRouterSlot(value)
			if err != nil {
				return dnsRouterRule{}, err
			}
			rule.slot = slot
			sawSlot = true
		default:
			return dnsRouterRule{}, fmt.Errorf("unknown token %q", fields[i-1])
		}
	}
	if !sawSlot {
		return dnsRouterRule{}, errors.New("missing SLOT action")
	}
	return rule, nil
}

func nextDNSRouterRuleValue(fields []string, i *int) (string, bool) {
	if i == nil || *i >= len(fields) {
		return "", false
	}
	value := fields[*i]
	*i = *i + 1
	if strings.TrimSpace(value) == "" {
		return "", false
	}
	return value, true
}

func parseDNSRouterType(value string) (uint16, error) {
	if n, err := strconv.Atoi(value); err == nil {
		if n <= 0 || n > 65535 {
			return 0, fmt.Errorf("DNS type %d must be between 1 and 65535", n)
		}
		return uint16(n), nil
	}
	return staticDNSType(value)
}

func parseDNSRouterSlot(value string) (int, error) {
	slot, err := strconv.Atoi(value)
	if err != nil || slot < 1 || slot > dnsRouterMaxSlots {
		return 0, fmt.Errorf("slot must be between 1 and %d", dnsRouterMaxSlots)
	}
	return slot, nil
}

func firstDNSRouterQuestion(msg *dns.Message) (dns.Question, bool) {
	if msg == nil || len(msg.Questions) == 0 {
		return dns.Question{}, false
	}
	return msg.Questions[0], true
}

func dnsRouterNameHasSuffix(name, suffix string) bool {
	if suffix == "." {
		return true
	}
	if name == suffix {
		return true
	}
	return strings.HasSuffix(name, "."+suffix)
}
