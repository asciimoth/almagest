package almagest

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/asciimoth/bufpool"
	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/formular"
	routing "github.com/asciimoth/gonnect-routing"
	"github.com/asciimoth/gonnect/tun"
	"github.com/asciimoth/pasta/pasta"
	"github.com/asciimoth/pasta/pasta/std"
)

func init() {
	regClass(func(e *engine) pasta.NodeClass {
		return tunSplitterClass{engine: e}
	}, false)
}

const (
	// NodeTypeTunSplitter is the node class name for a routing TUN splitter.
	NodeTypeTunSplitter = "almagest/TunSplitter"

	tunSplitterMaxSlots = 16
)

type tunSplitterRoutingConfig struct {
	ruleCacheTTL         time.Duration
	ruleCacheMaxEntries  int
	routeCacheTTL        time.Duration
	routeCacheMaxEntries int
}

type tunSplitterClass struct {
	engine *engine
}

func (tunSplitterClass) ClassName() string        { return NodeTypeTunSplitter }
func (tunSplitterClass) ShortDescription() string { return "TUN splitter" }
func (tunSplitterClass) LongDescription() string {
	return "Splits one almagest/tun endpoint into routed gonnect/tun.Splitter frontend slots using bytecode routing rules."
}
func (tunSplitterClass) DefaultNodeParams() pasta.NodeClassParams {
	return pasta.NodeClassParams{PrimaryType: TypeTun, InitialPorts: []pasta.Port{
		TunPort("left", "Tun"),
		TunPort("right", tunSplitterSlotName(1)),
	}}
}
func (c tunSplitterClass) NewNode(cfg configer.Config, _ ...*pasta.NodeClassState) (pasta.Node, error) {
	pool := enginePool(c.engine)
	return &tunSplitterNode{
		engine:        c.engine,
		pool:          pool,
		rulesText:     readConfigString(cfg, "rules", ""),
		routingConfig: readTunSplitterRoutingConfig(cfg),
		splitter:      tun.NewSplitter(pool),
		sent:          map[uint64]tun.Tun{},
		frontends:     map[uint64]tun.Tun{},
		outputCfgs:    TunConfigByLink{},
	}, nil
}

// tunSplitterNode owns one gonnect/tun.Splitter. Its left-directed input
// accepts the backend Tun and each right-directed slot port publishes one
// SplitFrontend. TunConfig suggestions received from slot consumers are merged
// and forwarded to the backend provider through the input link.
type tunSplitterNode struct {
	pasta.BasicNode

	engine *engine
	pool   bufpool.Pool

	w  *pasta.Workspace
	id uint64

	in    uint64
	slots [tunSplitterMaxSlots + 1]uint64

	inputLink uint64
	inputTun  tun.Tun

	splitter *tun.Splitter
	router   routing.SplitRouter

	rulesText     string
	routingConfig tunSplitterRoutingConfig
	formError     string

	sent       map[uint64]tun.Tun
	frontends  map[uint64]tun.Tun
	outputCfgs TunConfigByLink
}

func (n *tunSplitterNode) OnInit(w *pasta.Workspace, _ pasta.Logger, id uint64, _ string, restored *pasta.NodeInitData, _, _, _, _ bool) error {
	n.w = w
	n.id = id
	if n.splitter == nil {
		n.splitter = tun.NewSplitter(n.pool)
	}
	if n.sent == nil {
		n.sent = map[uint64]tun.Tun{}
	}
	if n.frontends == nil {
		n.frontends = map[uint64]tun.Tun{}
	}
	if n.outputCfgs == nil {
		n.outputCfgs = TunConfigByLink{}
	}
	if restored != nil {
		if len(restored.LeftPorts) > 0 {
			n.in = restored.LeftPorts[0]
		}
		for i, port := range restored.RightPorts {
			slot := n.slotFromPort(port, i+1)
			if slot >= 1 && slot <= tunSplitterMaxSlots && n.slots[slot] == 0 {
				n.slots[slot] = port
			}
		}
	}
	if err := n.w.SetNodePrimary(n.id, TypeTun); err != nil {
		return err
	}

	router, err := n.compileRules(n.rulesText, n.routingConfig)
	if err != nil {
		n.formError = err.Error()
	} else {
		n.setRouter(router)
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

func (n *tunSplitterNode) OnReady() error {
	n.requestInput()
	n.sendAllOutputTuns()
	n.sendMergedTunConfig()
	return nil
}

func (n *tunSplitterNode) OnStop() {
	n.closeOutputTuns()
	if n.router != nil {
		_ = n.router.Close()
		n.router = nil
	}
	if n.splitter != nil {
		_ = n.splitter.Close()
	}
	n.inputLink = 0
	n.inputTun = nil
	n.outputCfgs = TunConfigByLink{}
}

func (n *tunSplitterNode) PreLinkAdd(port uint64, linkType, portDirection string) error {
	if port == n.in {
		if portDirection != "left" {
			return pasta.LinkTypeErr(linkType)
		}
		return TunPreLinkAdd(n.w, port, linkType)
	}
	if n.portSlot(port) != 0 {
		if portDirection != "right" {
			return pasta.LinkTypeErr(linkType)
		}
		return TunPreLinkAdd(n.w, port, linkType)
	}
	return pasta.LinkTypeErr(linkType)
}

func (n *tunSplitterNode) OnLinkAdd(link, port uint64, linkType, portDirection string) error {
	if linkType != TypeTun {
		return nil
	}
	switch {
	case port == n.in && portDirection == "left":
		n.inputLink = link
		std.Request(n.w, n.id, link)
		n.sendMergedTunConfig()
	case n.portSlot(port) != 0 && portDirection == "right":
		n.sendOutputTunToLink(link, port)
		if err := n.reviewOutputPorts(); err != nil {
			return err
		}
	}
	return n.updateLabel()
}

func (n *tunSplitterNode) OnLinkRemoved(link uint64, port uint64, linkType, portDirection string) error {
	if linkType != TypeTun {
		return nil
	}
	switch {
	case port == n.in && portDirection == "left":
		if link == n.inputLink {
			n.inputLink = 0
			n.inputTun = nil
			if n.splitter != nil {
				_ = n.splitter.Detach()
			}
		}
	case n.portSlot(port) != 0 && portDirection == "right":
		n.closeOutputTun(link)
		n.outputCfgs.Delete(link)
		if err := n.reviewOutputPorts(); err != nil {
			return err
		}
		n.sendMergedTunConfig()
	}
	return n.updateLabel()
}

func (n *tunSplitterNode) OnPortRemoved(port uint64, direction string) error {
	if direction != "right" {
		return nil
	}
	for slot, id := range n.slots {
		if id == port {
			n.slots[slot] = 0
			return nil
		}
	}
	return nil
}

func (n *tunSplitterNode) OnEvent(event pasta.Event, linkType string, _ []string, receiverPortDirection string) error {
	if linkType != TypeTun {
		return nil
	}
	if event.ReceiverPort == n.in && receiverPortDirection == "left" {
		if std.IsRequest(event.Payload) {
			n.sendMergedTunConfig()
			return nil
		}
		payload, ok := event.Payload.(TunPayload)
		if !ok || payload.Tun == nil || event.Link != n.inputLink {
			return nil
		}
		return n.attachInputTun(event.Link, payload.Tun)
	}
	if n.portSlot(event.ReceiverPort) == 0 || receiverPortDirection != "right" {
		return nil
	}
	if std.IsRequest(event.Payload) {
		n.sendOutputTunToLink(event.Link, event.ReceiverPort)
		return nil
	}
	cfg, ok := event.Payload.(TunConfig)
	if !ok {
		return nil
	}
	n.outputCfgs.Set(event.Link, cfg)
	n.sendMergedTunConfig()
	return nil
}

func (n *tunSplitterNode) OnFormularMsg(message any) error {
	switch msg := message.(type) {
	case formular.FormApplyMessage:
		if msg.MenuID != pasta.NodeMenuID(n.id) || msg.BlockID != "config" {
			return nil
		}
		return n.applyConfigForm(msg.Values)
	case formular.FieldValidateMessage:
		if msg.MenuID != pasta.NodeMenuID(n.id) || msg.Field.BlockID != "config" || len(msg.Field.ElementPath) > 0 {
			return nil
		}
		switch msg.Field.FieldID {
		case "rules":
			n.sendRulesFieldStatus(msg)
		case "ruleCacheTTL", "ruleCacheMaxEntries", "routeCacheTTL", "routeCacheMaxEntries":
			n.sendIntFieldStatus(msg)
		}
	}
	return nil
}

func (n *tunSplitterNode) OnSave(cfg configer.Config) error {
	if err := pasta.DeleteNodeOwnedConfigKeys(cfg); err != nil {
		return err
	}
	values := map[string]any{
		"rules":                n.rulesText,
		"ruleCacheTTL":         durationMilliseconds(n.routingConfig.ruleCacheTTL),
		"ruleCacheMaxEntries":  n.routingConfig.ruleCacheMaxEntries,
		"routeCacheTTL":        durationMilliseconds(n.routingConfig.routeCacheTTL),
		"routeCacheMaxEntries": n.routingConfig.routeCacheMaxEntries,
	}
	for key, value := range values {
		if err := cfg.Set(configer.Path{key}, value); err != nil {
			return err
		}
	}
	return nil
}

func (n *tunSplitterNode) applyConfigForm(values map[string]any) error {
	text := n.rulesText
	if value, ok := stringFromAny(values["rules"]); ok {
		text = value
	}
	config := n.routingConfig.withValues(values)
	router, err := n.compileRules(text, config)
	if err != nil {
		n.formError = err.Error()
		n.sendConfigBlock()
		return nil
	}
	n.rulesText = text
	n.routingConfig = config
	n.formError = ""
	n.setRouter(router)
	if err := n.reviewOutputPorts(); err != nil {
		return err
	}
	n.sendForcedMenuSnapshot()
	return nil
}

func (n *tunSplitterNode) sendRulesFieldStatus(msg formular.FieldValidateMessage) {
	text, _ := stringFromAny(msg.Value)
	status := formular.StatusOK
	statusText := ""
	router, err := n.compileRules(text, n.routingConfig)
	if err != nil {
		status = formular.StatusError
		statusText = err.Error()
	} else if router != nil {
		_ = router.Close()
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

func (n *tunSplitterNode) sendIntFieldStatus(msg formular.FieldValidateMessage) {
	status := formular.StatusOK
	statusText := ""
	if _, ok := intFromAny(msg.Value); !ok {
		status = formular.StatusError
		statusText = "must be an integer"
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

func (n *tunSplitterNode) compileRules(text string, config tunSplitterRoutingConfig) (routing.SplitRouter, error) {
	if n.engine == nil || n.engine.sys == nil {
		return nil, fmt.Errorf("no sysnet system")
	}
	rules, err := routing.NewSplitBytecodeRules(n.engine.sys, text)
	if err != nil {
		return nil, err
	}
	rules.RuleCacheTTL = config.ruleCacheTTL
	rules.RuleCacheMaxEntries = config.ruleCacheMaxEntries
	rules.RouteCacheTTL = config.routeCacheTTL
	rules.RouteCacheMaxEntries = config.routeCacheMaxEntries
	return routing.NewBytecodeSplitRouter(rules)
}

func (n *tunSplitterNode) setRouter(router routing.SplitRouter) {
	old := n.router
	n.router = router
	if n.splitter != nil {
		n.splitter.SetRouter(router)
	}
	if old != nil && old != router {
		_ = old.Close()
	}
}

func (n *tunSplitterNode) attachInputTun(link uint64, t tun.Tun) error {
	if link != n.inputLink || SameTunInstance(n.inputTun, t) {
		return nil
	}
	BindTunResource(n.w, n.id, link, t)
	n.inputTun = t
	if err := n.splitter.Attach(t); err != nil {
		n.inputTun = nil
		return fmt.Errorf("attach splitter tun: %w", err)
	}
	n.sendAllOutputTuns()
	return n.updateLabel()
}

func (n *tunSplitterNode) reviewOutputPorts() error {
	desired := map[int]struct{}{}
	if n.router != nil {
		for _, slot := range n.router.MentionedSlots() {
			if slot >= 1 && slot <= tunSplitterMaxSlots {
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
	for slot := 1; slot <= tunSplitterMaxSlots; slot++ {
		if _, used := desired[slot]; !used {
			desired[slot] = struct{}{}
			break
		}
	}

	for slot := 1; slot <= tunSplitterMaxSlots; slot++ {
		port := n.slots[slot]
		_, keep := desired[slot]
		if !keep && port != 0 && !n.portHasLinks(port) {
			n.w.RemovePort(port)
			n.slots[slot] = 0
			continue
		}
		if keep && port == 0 {
			port := TunPort("right", tunSplitterSlotName(slot))
			port.Node = n.id
			id, err := n.w.AddPort(port)
			if err != nil {
				return err
			}
			n.slots[slot] = id
			continue
		}
		if keep && port != 0 {
			_ = n.w.SetPortName(port, tunSplitterSlotName(slot))
		}
	}

	order := make([]uint64, 0, len(desired))
	for slot := 1; slot <= tunSplitterMaxSlots; slot++ {
		if n.slots[slot] != 0 {
			order = append(order, n.slots[slot])
		}
	}
	if len(order) > 0 {
		return n.w.SetNodePortOrder(n.id, "right", order)
	}
	return nil
}

func (n *tunSplitterNode) requestInput() {
	port, ok := n.w.PortSnapshot(n.in)
	if !ok {
		return
	}
	for _, link := range port.Links {
		std.Request(n.w, n.id, link)
	}
}

func (n *tunSplitterNode) sendAllOutputTuns() {
	for _, port := range n.slots {
		if port == 0 {
			continue
		}
		snapshot, ok := n.w.PortSnapshot(port)
		if !ok {
			continue
		}
		for _, link := range snapshot.Links {
			n.sendOutputTunToLink(link, port)
		}
	}
}

func (n *tunSplitterNode) sendOutputTunToLink(link, port uint64) {
	slot := n.portSlot(port)
	if slot == 0 || n.splitter == nil {
		return
	}
	n.closeOutputTun(link)
	frontend := n.splitter.Get(slot)
	if frontend == nil {
		return
	}
	BindTunResource(n.w, n.id, link, frontend)
	wrapper := DetachTunForLink(n.w, n.id, link, frontend, n.pool)
	if wrapper == nil {
		_ = frontend.Close()
		return
	}
	n.frontends[link] = frontend
	n.sent[link] = wrapper
	n.w.EmitEvent(n.id, link, TunPayload{Tun: wrapper})
}

func (n *tunSplitterNode) closeOutputTun(link uint64) {
	wrapper := n.sent[link]
	delete(n.sent, link)
	if wrapper != nil {
		_ = wrapper.Close()
	}
	frontend := n.frontends[link]
	delete(n.frontends, link)
	if frontend != nil {
		_ = frontend.Close()
	}
	waitTun(wrapper)
}

func (n *tunSplitterNode) closeOutputTuns() {
	for link := range n.sent {
		n.closeOutputTun(link)
	}
	for link := range n.frontends {
		n.closeOutputTun(link)
	}
}

func (n *tunSplitterNode) sendMergedTunConfig() {
	if n.inputLink == 0 {
		return
	}
	n.w.EmitEvent(n.id, n.inputLink, n.outputCfgs.Merge())
}

func (n *tunSplitterNode) portSlot(port uint64) int {
	for slot, id := range n.slots {
		if id == port {
			return slot
		}
	}
	return 0
}

func (n *tunSplitterNode) slotFromPort(port uint64, fallback int) int {
	snapshot, ok := n.w.PortSnapshot(port)
	if !ok {
		return fallback
	}
	if slot, ok := tunSplitterSlotFromName(snapshot.Name); ok {
		return slot
	}
	return fallback
}

func (n *tunSplitterNode) portHasLinks(port uint64) bool {
	snapshot, ok := n.w.PortSnapshot(port)
	return ok && len(snapshot.Links) > 0
}

func (n *tunSplitterNode) updateLabel() error {
	if n.inputTun == nil {
		return n.w.SetNodeLabel(n.id, "waiting for tun")
	}
	return n.w.SetNodeLabel(n.id, "splitting")
}

func (n *tunSplitterNode) sendMenuSnapshot() {
	if n.w == nil || n.id == 0 {
		return
	}
	n.w.SendNodeMenuMsg(n.id, formular.MenuSnapshotMessage{
		MessageBase: formular.MessageBase{Type: formular.MessageMenuSnapshot, MenuID: pasta.NodeMenuID(n.id), MenuGeneration: 1},
		Blocks:      []formular.Block{n.configBlock()},
	})
}

func (n *tunSplitterNode) sendForcedMenuSnapshot() {
	if n.w == nil || n.id == 0 {
		return
	}
	n.w.SendNodeMenuMsg(n.id, formular.MenuSnapshotMessage{
		MessageBase: formular.MessageBase{Type: formular.MessageMenuSnapshot, MenuID: pasta.NodeMenuID(n.id), MenuGeneration: 1},
		Force:       true,
		Blocks:      []formular.Block{n.configBlock()},
	})
}

func (n *tunSplitterNode) sendConfigBlock() {
	if n.w == nil || n.id == 0 {
		return
	}
	n.w.SendNodeMenuMsg(n.id, formular.BlockSnapshotMessage{
		MessageBase: formular.MessageBase{Type: formular.MessageBlockSnapshot, MenuID: pasta.NodeMenuID(n.id), MenuGeneration: 1, BlockGeneration: 1},
		Block:       n.configBlock(),
	})
}

func (n *tunSplitterNode) configBlock() formular.Block {
	field := &formular.Field{
		Kind:        formular.FieldText,
		Value:       n.rulesText,
		Multiline:   true,
		Validation:  true,
		Placeholder: "PORT 443\nSLOT 1",
	}
	if n.formError != "" {
		field.Status = formular.StatusError
		field.StatusText = n.formError
	}
	return formular.Block{ID: "config", Order: 10, Generation: 1, Form: true, Items: []formular.Item{
		{Type: formular.ItemField, ID: "rules", Label: "Routing rules", Field: field},
		tunSplitterIntField(
			"ruleCacheTTL",
			"Rule cache TTL (ms)",
			durationMilliseconds(n.routingConfig.ruleCacheTTL),
			"How long OP_RULE matcher results are cached by flow. 0 uses the router default; negative disables the rule cache.",
		),
		tunSplitterIntField(
			"ruleCacheMaxEntries",
			"Rule cache max entries",
			n.routingConfig.ruleCacheMaxEntries,
			"Maximum OP_RULE cache entries. 0 uses the router default; negative disables the rule cache.",
		),
		tunSplitterIntField(
			"routeCacheTTL",
			"Route cache TTL (ms)",
			durationMilliseconds(n.routingConfig.routeCacheTTL),
			"How long whole route results are cached by packet flow. 0 inherits the rule cache settings; negative disables the route cache.",
		),
		tunSplitterIntField(
			"routeCacheMaxEntries",
			"Route cache max entries",
			n.routingConfig.routeCacheMaxEntries,
			"Maximum route-result cache entries. 0 inherits the rule cache settings or router default; negative disables the route cache.",
		),
	}}
}

func tunSplitterIntField(id, label string, value int, help string) formular.Item {
	item := configIntField(id, label, value, false, "")
	item.Help = help
	return item
}

func readTunSplitterRoutingConfig(cfg configer.Config) tunSplitterRoutingConfig {
	return tunSplitterRoutingConfig{
		ruleCacheTTL:         time.Duration(readConfigInt(cfg, "ruleCacheTTL", 0)) * time.Millisecond,
		ruleCacheMaxEntries:  readConfigInt(cfg, "ruleCacheMaxEntries", 0),
		routeCacheTTL:        time.Duration(readConfigInt(cfg, "routeCacheTTL", 0)) * time.Millisecond,
		routeCacheMaxEntries: readConfigInt(cfg, "routeCacheMaxEntries", 0),
	}
}

func (c tunSplitterRoutingConfig) withValues(values map[string]any) tunSplitterRoutingConfig {
	if value, ok := intFromAny(values["ruleCacheTTL"]); ok {
		c.ruleCacheTTL = time.Duration(value) * time.Millisecond
	}
	if value, ok := intFromAny(values["ruleCacheMaxEntries"]); ok {
		c.ruleCacheMaxEntries = value
	}
	if value, ok := intFromAny(values["routeCacheTTL"]); ok {
		c.routeCacheTTL = time.Duration(value) * time.Millisecond
	}
	if value, ok := intFromAny(values["routeCacheMaxEntries"]); ok {
		c.routeCacheMaxEntries = value
	}
	return c
}

func durationMilliseconds(d time.Duration) int {
	return int(d / time.Millisecond)
}

func tunSplitterSlotName(slot int) string {
	return "slot " + strconv.Itoa(slot)
}

func tunSplitterSlotFromName(name string) (int, bool) {
	parts := strings.Fields(strings.ToLower(name))
	if len(parts) != 2 || parts[0] != "slot" {
		return 0, false
	}
	slot, err := strconv.Atoi(parts[1])
	if err != nil || slot < 1 || slot > tunSplitterMaxSlots {
		return 0, false
	}
	return slot, true
}
