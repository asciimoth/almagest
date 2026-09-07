package tunnodes

import (
	"errors"
	"fmt"
	"time"

	"github.com/asciimoth/bufpool"
	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/formular"
	"github.com/asciimoth/gonnect/routing"
	"github.com/asciimoth/gonnect/tun"
	"github.com/asciimoth/pasta/pasta"
	"github.com/asciimoth/pasta/pasta/std"

	dnsnodes "github.com/asciimoth/almagest/lib/dns"
	"github.com/asciimoth/almagest/lib/storage"
)

const (
	// NodeTypeTunSplitter is the node class name for a routing TUN splitter.
	NodeTypeTunSplitter = "almagest/TunSplitter"

	TunSplitterConfigBlockID            = "config"
	TunSplitterRulesField               = "rules"
	TunSplitterDNSCacheStorageNameField = "dnsCacheStorageName"

	tunSplitterMaxSlots                 = 16
	tunSplitterConfigBlockID            = TunSplitterConfigBlockID
	tunSplitterRulesField               = TunSplitterRulesField
	tunSplitterDNSCacheStorageNameField = TunSplitterDNSCacheStorageNameField
)

type tunSplitterRoutingConfig struct {
	ruleCacheTTL         time.Duration
	ruleCacheMaxEntries  int
	routeCacheTTL        time.Duration
	routeCacheMaxEntries int
	dnsCacheStorageName  string
}

type TunSplitterClass struct {
	Runtime Runtime
}

func (TunSplitterClass) ClassName() string        { return NodeTypeTunSplitter }
func (TunSplitterClass) ShortDescription() string { return "TUN splitter" }
func (TunSplitterClass) LongDescription() string {
	return "Splits one almagest/tun endpoint into routed gonnect/tun.Splitter frontend slots using bytecode routing rules."
}
func (TunSplitterClass) DefaultNodeParams() pasta.NodeClassParams {
	return pasta.NodeClassParams{PrimaryType: TypeTun, InitialPorts: []pasta.Port{
		TunPort("left", "Tun"),
		TunPort("right", slotPortName(1)),
	}}
}
func (c TunSplitterClass) NewNode(cfg configer.Config, _ ...*pasta.NodeClassState) (pasta.Node, error) {
	pool := enginePool(c.Runtime)
	return &tunSplitterNode{
		NodeBase:      NodeBase{Runtime: c.Runtime},
		pool:          pool,
		rulesText:     configer.String(cfg, configer.Path{tunSplitterRulesField}, ""),
		routingConfig: readTunSplitterRoutingConfig(cfg),
		slots: newSlotPorts(
			tunSplitterMaxSlots,
			"right",
			func(slot int) pasta.Port { return TunPort("right", slotPortName(slot)) },
			slotPortName,
			func(name string) (int, bool) { return slotPortFromName(name, tunSplitterMaxSlots) },
		),
		frontends:  map[uint64]tun.Tun{},
		outputCfgs: TunConfigByLink{},
	}, nil
}

// tunSplitterNode owns one gonnect/tun.Splitter. Its left-directed input
// accepts the backend Tun and each right-directed slot port publishes one
// SplitFrontend. TunConfig suggestions received from slot consumers are merged
// and forwarded to the backend provider through the input link. DNS suggestions
// are discarded because one DNS server cannot be selected safely from multiple
// routed outputs; use almagest/dns nodes for that topology.
type tunSplitterNode struct {
	NodeBase

	pool bufpool.Pool

	in    uint64
	slots slotPorts

	inputLink uint64
	inputTun  tun.Tun

	splitter *tun.Splitter
	router   routing.SplitRouter

	rulesText            string
	routingConfig        tunSplitterRoutingConfig
	formError            string
	dnsCacheStorageError string

	outputTuns TunOutputCache
	frontends  map[uint64]tun.Tun
	outputCfgs TunConfigByLink
}

func (n *tunSplitterNode) OnInit(w *pasta.Workspace, _ pasta.Logger, id uint64, _ string, restored *pasta.NodeInitData, _, isPlaceholderReplacement, _, isRestored bool) error {
	n.InitBaseNode(w, nil, id)
	if n.splitter == nil {
		// Nil spawner is intentional here; see the Gonnect spawner note in helpers.go.
		n.splitter = tun.NewSplitter(nil, n.pool)
	}
	if n.frontends == nil {
		n.frontends = map[uint64]tun.Tun{}
	}
	if n.outputCfgs == nil {
		n.outputCfgs = TunConfigByLink{}
	}
	if n.slots.Max() == 0 {
		n.slots = newSlotPorts(
			tunSplitterMaxSlots,
			"right",
			func(slot int) pasta.Port { return TunPort("right", slotPortName(slot)) },
			slotPortName,
			func(name string) (int, bool) { return slotPortFromName(name, tunSplitterMaxSlots) },
		)
	}
	n.in = restoredLeft(restored, 0)
	n.slots.Init(w, id, restoredRightPorts(restored))
	if err := n.SetPrimary(TypeTun); err != nil {
		return err
	}

	compileConfig := n.routingConfig
	if err := dnsnodes.ValidateOptionalCacheStorageName(compileConfig.dnsCacheStorageName); err != nil {
		n.dnsCacheStorageError = err.Error()
		compileConfig.dnsCacheStorageName = ""
	}
	router, err := n.compileRules(n.rulesText, compileConfig)
	if err != nil {
		n.formError = err.Error()
	} else {
		n.setRouter(router)
	}
	if err := n.reviewOutputPorts(slotPortReview{EnsureFree: true, PreserveExisting: isRestored || isPlaceholderReplacement}); err != nil {
		return err
	}
	if err := n.updateLabel(); err != nil {
		return err
	}
	n.sendMenuSnapshot()
	return nil
}

func (n *tunSplitterNode) OnReady() error {
	if n.slots.TakeDeferredReview() {
		if err := n.reviewOutputPorts(slotPortReview{EnsureFree: true}); err != nil {
			return err
		}
	}
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
		return TunPreLinkAdd(n.W, port, linkType)
	}
	if n.slots.Slot(port) != 0 {
		if portDirection != "right" {
			return pasta.LinkTypeErr(linkType)
		}
		return TunPreLinkAdd(n.W, port, linkType)
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
		std.RequestLocked(n.W, n.ID, link)
		n.sendMergedTunConfig()
	case n.slots.Slot(port) != 0 && portDirection == "right":
		n.sendOutputTunToLink(link, port)
		if err := n.reviewOutputPorts(slotPortReview{EnsureFree: true}); err != nil {
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
	case n.slots.Slot(port) != 0 && portDirection == "right":
		n.closeOutputTun(link)
		n.outputCfgs.Delete(link)
		if err := n.reviewOutputPorts(slotPortReview{EnsureFree: true}); err != nil {
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
	n.slots.Remove(port)
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
		next, changed := AcceptTunPayload(n.W, n.ID, event, n.inputLink, n.inputTun)
		if !changed {
			return nil
		}
		return n.attachInputTun(event.Link, next)
	}
	if n.slots.Slot(event.ReceiverPort) == 0 || receiverPortDirection != "right" {
		return nil
	}
	if std.IsRequest(event.Payload) {
		n.refreshOutputTunToLink(event.Link, event.ReceiverPort)
		return nil
	}
	cfg, ok := event.Payload.(TunConfig)
	if !ok {
		return nil
	}
	n.outputCfgs.Set(event.Link, cfg.WithoutDNS())
	n.sendMergedTunConfig()
	return nil
}

func (n *tunSplitterNode) OnFormularMsg(message any) error {
	switch msg := message.(type) {
	case formular.FormApplyMessage:
		if msg.MenuID != pasta.NodeMenuID(n.ID) || msg.BlockID != tunSplitterConfigBlockID {
			return nil
		}
		return n.applyConfigForm(msg.Values)
	case formular.FieldValidateMessage:
		if msg.MenuID != pasta.NodeMenuID(n.ID) || msg.Field.BlockID != tunSplitterConfigBlockID || len(msg.Field.ElementPath) > 0 {
			return nil
		}
		switch msg.Field.FieldID {
		case tunSplitterRulesField:
			n.sendRulesFieldStatus(msg)
		case "ruleCacheTTL", "ruleCacheMaxEntries", "routeCacheTTL", "routeCacheMaxEntries":
			n.sendIntFieldStatus(msg)
		case tunSplitterDNSCacheStorageNameField:
			n.sendDNSCacheStorageFieldStatus(msg)
		}
	}
	return nil
}

func (n *tunSplitterNode) OnSave(cfg configer.Config) error {
	if err := pasta.DeleteNodeOwnedConfigKeys(cfg); err != nil {
		return err
	}
	return configer.SetMap(cfg, map[string]any{
		tunSplitterRulesField:               n.rulesText,
		"ruleCacheTTL":                      durationMilliseconds(n.routingConfig.ruleCacheTTL),
		"ruleCacheMaxEntries":               n.routingConfig.ruleCacheMaxEntries,
		"routeCacheTTL":                     durationMilliseconds(n.routingConfig.routeCacheTTL),
		"routeCacheMaxEntries":              n.routingConfig.routeCacheMaxEntries,
		tunSplitterDNSCacheStorageNameField: n.routingConfig.dnsCacheStorageName,
	})
}

func (n *tunSplitterNode) applyConfigForm(values map[string]any) error {
	text := n.rulesText
	if value, ok := formular.StringValue(values[tunSplitterRulesField]); ok {
		text = value
	}
	config := n.routingConfig.withValues(values)
	if err := dnsnodes.ValidateOptionalCacheStorageName(config.dnsCacheStorageName); err != nil {
		n.dnsCacheStorageError = err.Error()
		syntaxConfig := config
		syntaxConfig.dnsCacheStorageName = ""
		if router, rulesErr := n.compileRules(text, syntaxConfig); rulesErr != nil {
			n.formError = rulesErr.Error()
		} else {
			n.formError = ""
			if router != nil {
				_ = router.Close()
			}
		}
		n.sendConfigBlock()
		return nil
	}
	router, err := n.compileRules(text, config)
	if err != nil {
		n.formError = err.Error()
		n.sendConfigBlock()
		return nil
	}
	n.rulesText = text
	n.routingConfig = config
	n.formError = ""
	n.dnsCacheStorageError = ""
	n.setRouter(router)
	if err := n.reviewOutputPorts(slotPortReview{EnsureFree: true}); err != nil {
		return err
	}
	n.sendForcedMenuSnapshot()
	return nil
}

func (n *tunSplitterNode) sendRulesFieldStatus(msg formular.FieldValidateMessage) {
	text, _ := formular.StringValue(msg.Value)
	config := n.routingConfig
	config.dnsCacheStorageName = ""
	router, err := n.compileRules(text, config)
	if err == nil && router != nil {
		_ = router.Close()
	}
	sendNodeFieldStatus(n.W, n.ID, msg.Field, err)
}

func (n *tunSplitterNode) sendIntFieldStatus(msg formular.FieldValidateMessage) {
	var err error
	if _, ok := formular.IntValue(msg.Value); !ok {
		err = errors.New("must be an integer")
	}
	sendNodeFieldStatus(n.W, n.ID, msg.Field, err)
}

func (n *tunSplitterNode) sendDNSCacheStorageFieldStatus(msg formular.FieldValidateMessage) {
	value, _ := formular.StringValue(msg.Value)
	value = dnsnodes.NormalizeOptionalCacheStorageName(value)
	sendNodeFieldStatus(n.W, n.ID, msg.Field, dnsnodes.ValidateOptionalCacheStorageName(value))
}

func (n *tunSplitterNode) compileRules(text string, config tunSplitterRoutingConfig) (routing.SplitRouter, error) {
	if n.System() == nil {
		return nil, fmt.Errorf("no sysnet system")
	}
	config.dnsCacheStorageName = dnsnodes.NormalizeOptionalCacheStorageName(config.dnsCacheStorageName)
	if err := dnsnodes.ValidateOptionalCacheStorageName(config.dnsCacheStorageName); err != nil {
		return nil, err
	}
	rules, err := routing.NewSplitBytecodeRules(n.System(), text)
	if err != nil {
		return nil, err
	}
	rules.RuleCacheTTL = config.ruleCacheTTL
	rules.RuleCacheMaxEntries = config.ruleCacheMaxEntries
	rules.RouteCacheTTL = config.routeCacheTTL
	rules.RouteCacheMaxEntries = config.routeCacheMaxEntries
	rules.DNSCacheStorage = dnsnodes.CacheStorageForRouting(n.cacheManager(), config.dnsCacheStorageName)
	return routing.NewBytecodeSplitRouter(rules)
}

func (n *tunSplitterNode) cacheManager() storage.DnsCacheManager {
	if n.Runtime == nil {
		return nil
	}
	return runtimeDNSCacheManager(n.Runtime)
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
	BindTunResource(n.W, n.ID, link, t)
	n.inputTun = t
	if err := n.splitter.Attach(t); err != nil {
		n.inputTun = nil
		return fmt.Errorf("attach splitter tun: %w", err)
	}
	n.sendAllOutputTuns()
	return n.updateLabel()
}

func (n *tunSplitterNode) reviewOutputPorts(opts slotPortReview) error {
	desired := map[int]struct{}{}
	if n.router != nil {
		for _, slot := range n.router.MentionedSlots() {
			if slot >= 1 && slot <= tunSplitterMaxSlots {
				desired[slot] = struct{}{}
			}
		}
	}
	return n.slots.Review(desired, opts)
}

func (n *tunSplitterNode) requestInput() {
	n.RequestPort(n.in)
}

func (n *tunSplitterNode) sendAllOutputTuns() {
	n.slots.ForEach(func(_ int, port uint64) {
		n.ForEachPortLink(port, func(link uint64) {
			n.sendOutputTunToLink(link, port)
		})
	})
}

func (n *tunSplitterNode) sendOutputTunToLink(link, port uint64) {
	slot := n.slots.Slot(port)
	if slot == 0 || n.splitter == nil {
		return
	}
	if frontend := n.frontends[link]; TunIsReady(frontend) && n.outputTuns.SendLocked(n.W, n.ID, link, frontend, n.pool) {
		return
	}
	n.closeOutputTun(link)
	frontend := n.splitter.Get(slot)
	if frontend == nil {
		return
	}
	BindTunResource(n.W, n.ID, link, frontend)
	n.frontends[link] = frontend
	if !n.outputTuns.SendLocked(n.W, n.ID, link, frontend, n.pool) {
		delete(n.frontends, link)
		_ = frontend.Close()
		return
	}
}

func (n *tunSplitterNode) refreshOutputTunToLink(link, port uint64) {
	slot := n.slots.Slot(port)
	if slot == 0 || n.splitter == nil {
		return
	}
	frontend := n.frontends[link]
	if !TunIsReady(frontend) {
		n.closeOutputTun(link)
		nextFrontend := n.splitter.Get(slot)
		if nextFrontend == nil {
			return
		}
		frontend = nextFrontend
		BindTunResource(n.W, n.ID, link, frontend)
		n.frontends[link] = frontend
	}
	if !n.outputTuns.RefreshLocked(n.W, n.ID, link, frontend, n.pool) {
		delete(n.frontends, link)
		_ = frontend.Close()
	}
}

func (n *tunSplitterNode) closeOutputTun(link uint64) {
	n.outputTuns.Close(link)
	frontend := n.frontends[link]
	delete(n.frontends, link)
	if frontend != nil {
		_ = frontend.Close()
	}
	n.outputTuns.Wait()
}

func (n *tunSplitterNode) closeOutputTuns() {
	n.outputTuns.CloseAll()
	for link := range n.frontends {
		frontend := n.frontends[link]
		delete(n.frontends, link)
		if frontend != nil {
			_ = frontend.Close()
		}
	}
	n.outputTuns.Wait()
}

func (n *tunSplitterNode) sendMergedTunConfig() {
	EmitMergedTunConfig(n.W, n.ID, n.inputLink, n.outputCfgs)
}

func (n *tunSplitterNode) updateLabel() error {
	if n.inputTun == nil {
		return n.SetLabel("waiting for tun")
	}
	return n.SetLabel("splitting")
}

func (n *tunSplitterNode) sendMenuSnapshot() {
	sendNodeMenuSnapshot(n.W, n.ID, false, n.configBlock())
}

func (n *tunSplitterNode) sendForcedMenuSnapshot() {
	sendNodeMenuSnapshot(n.W, n.ID, true, n.configBlock())
}

func (n *tunSplitterNode) sendConfigBlock() {
	sendNodeBlockSnapshot(n.W, n.ID, n.configBlock())
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
	return formular.Block{ID: tunSplitterConfigBlockID, Order: 10, Generation: 1, Form: true, Items: []formular.Item{
		{Type: formular.ItemField, ID: tunSplitterRulesField, Label: "Routing rules", Field: field},
		dnsnodes.CacheStorageSelectorField(
			tunSplitterDNSCacheStorageNameField,
			"DNS cache storage",
			n.routingConfig.dnsCacheStorageName,
			n.cacheManager(),
			n.dnsCacheStorageError != "",
			n.dnsCacheStorageError,
		),
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
		ruleCacheTTL:         time.Duration(configer.Int(cfg, configer.Path{"ruleCacheTTL"}, 0)) * time.Millisecond,
		ruleCacheMaxEntries:  configer.Int(cfg, configer.Path{"ruleCacheMaxEntries"}, 0),
		routeCacheTTL:        time.Duration(configer.Int(cfg, configer.Path{"routeCacheTTL"}, 0)) * time.Millisecond,
		routeCacheMaxEntries: configer.Int(cfg, configer.Path{"routeCacheMaxEntries"}, 0),
		dnsCacheStorageName:  dnsnodes.NormalizeOptionalCacheStorageName(configer.String(cfg, configer.Path{tunSplitterDNSCacheStorageNameField}, "")),
	}
}

func (c tunSplitterRoutingConfig) withValues(values map[string]any) tunSplitterRoutingConfig {
	if value, ok := formular.IntValue(values["ruleCacheTTL"]); ok {
		c.ruleCacheTTL = time.Duration(value) * time.Millisecond
	}
	if value, ok := formular.IntValue(values["ruleCacheMaxEntries"]); ok {
		c.ruleCacheMaxEntries = value
	}
	if value, ok := formular.IntValue(values["routeCacheTTL"]); ok {
		c.routeCacheTTL = time.Duration(value) * time.Millisecond
	}
	if value, ok := formular.IntValue(values["routeCacheMaxEntries"]); ok {
		c.routeCacheMaxEntries = value
	}
	if value, ok := formular.StringValue(values[tunSplitterDNSCacheStorageNameField]); ok {
		c.dnsCacheStorageName = dnsnodes.NormalizeOptionalCacheStorageName(value)
	}
	return c
}

func durationMilliseconds(d time.Duration) int {
	return int(d / time.Millisecond)
}
