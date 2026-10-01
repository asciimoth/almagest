package tunnodes

import (
	"errors"
	"fmt"
	"time"

	"github.com/asciimoth/almagest/lib/nodekit"
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
	// NodeTypeAutoTunSplitter is the node class name for a TUN splitter that
	// learns its routes from downstream TunConfig messages.
	NodeTypeAutoTunSplitter = "almagest/AutoTunSplitter"

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
	return "Splits one almagest/tun endpoint into routed gonnect/tun.Splitter frontend slots using bytecode routing rules. It preserves each output's route-to-address association so an upstream userspace network stack can select the correct local source address."
}
func (TunSplitterClass) DefaultNodeParams() pasta.NodeClassParams {
	return pasta.NodeClassParams{PrimaryType: TypeTun, InitialPorts: []pasta.Port{
		TunPort("left", "Tun"),
		TunPort("right", slotPortName(1)),
	}}
}
func (c TunSplitterClass) NewNode(cfg configer.Config, _ ...*pasta.NodeClassState) (pasta.Node, error) {
	return newTunSplitterNode(c.Runtime, cfg, false), nil
}

// AutoTunSplitterClass builds TUN splitters that learn packet routes from the
// TunConfig messages sent by consumers of their slot ports.
//
// Each slot has one current config snapshot. A new TunConfig replaces that
// complete snapshot; it is not a delta. Removing or replacing the slot link
// clears the snapshot before the slot can learn from its next consumer.
//
// Routes and source-route destinations claim packets by destination prefix.
// The longest prefix wins. For equal prefixes, an associated packet source
// selects its slot. The lower slot number wins when the source does not resolve
// the tie. Interface addresses provide source associations for plain routes,
// but an address alone does not claim packet destinations. Valid unmatched
// packets use slot 1, and malformed IP packets use slot 0 so Gonnect drops
// them.
//
// Learning is independent from upstream config propagation. The node also
// removes DNS, infers destination-to-source associations, merges every linked
// slot config, and sends that result to its TUN provider.
type AutoTunSplitterClass struct {
	Runtime Runtime
}

func (AutoTunSplitterClass) ClassName() string { return NodeTypeAutoTunSplitter }
func (AutoTunSplitterClass) ShortDescription() string {
	return "Automatic TUN splitter"
}
func (AutoTunSplitterClass) LongDescription() string {
	return "Splits one almagest/tun endpoint into gonnect/tun.Splitter frontend slots. It automatically learns source addresses and destination prefixes from each slot's TunConfig messages, uses the most specific destination prefix, and sends unmatched packets to slot 1. It also preserves each output's route-to-address association for upstream source selection."
}
func (AutoTunSplitterClass) DefaultNodeParams() pasta.NodeClassParams {
	return pasta.NodeClassParams{PrimaryType: TypeTun, InitialPorts: []pasta.Port{
		TunPort("left", "Tun"),
		TunPort("right", slotPortName(1)),
	}}
}
func (c AutoTunSplitterClass) NewNode(cfg configer.Config, _ ...*pasta.NodeClassState) (pasta.Node, error) {
	return newTunSplitterNode(c.Runtime, cfg, true), nil
}

func newTunSplitterNode(runtime Runtime, cfg configer.Config, automatic bool) *tunSplitterNode {
	pool := enginePool(runtime)
	return &tunSplitterNode{
		NodeBase:      NodeBase{Runtime: runtime},
		pool:          pool,
		automatic:     automatic,
		rulesText:     configer.String(cfg, configer.Path{tunSplitterRulesField}, ""),
		routingConfig: readTunSplitterRoutingConfig(cfg),
		slots: newSlotPorts(
			tunSplitterMaxSlots,
			"right",
			func(slot int) pasta.Port { return TunPort("right", slotPortName(slot)) },
			slotPortName,
			func(name string) (int, bool) { return slotPortFromName(name, tunSplitterMaxSlots) },
		),
		frontends:     map[uint64]tun.Tun{},
		outputCfgs:    TunConfigByLink{},
		autoSlotLinks: map[int]uint64{},
	}
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

	// automatic selects the immutable, TunConfig-derived routing strategy. The
	// field is fixed when the node is constructed and is never changed while
	// Gonnect workers can call the router.
	automatic bool

	in    uint64
	slots slotPorts

	inputLink  uint64
	inputTun   tun.Tun
	inputGen   uint64
	inputRetry *time.Timer

	splitter   *tun.Splitter
	router     routing.SplitRouter
	autoRouter *autoTunSplitterRouter

	rulesText            string
	routingConfig        tunSplitterRoutingConfig
	formError            string
	dnsCacheStorageError string

	outputTuns TunOutputCache
	frontends  map[uint64]tun.Tun
	outputCfgs TunConfigByLink
	// autoSlotLinks is the route-learning generation for each slot. Link IDs are
	// sufficient generation tokens because the workspace does not reuse a live
	// link ID. The map rejects delayed TunConfig events from a removed or
	// replaced link. Workspace callbacks serialize map access; packet workers
	// access only autoRouter's synchronized snapshot.
	autoSlotLinks map[int]uint64
}

func (n *tunSplitterNode) OnInit(w *pasta.Workspace, logger pasta.Logger, id uint64, _ string, restored *pasta.NodeInitData, _, isPlaceholderReplacement, _, isRestored bool) error {
	n.InitBaseNode(w, logger, id)
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
	if n.autoSlotLinks == nil {
		n.autoSlotLinks = map[int]uint64{}
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

	if n.automatic {
		if n.autoRouter == nil {
			n.autoRouter = newAutoTunSplitterRouter()
		}
		n.splitter.SetRouter(n.autoRouter)
	} else {
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
	}
	if err := n.reviewOutputPorts(slotPortReview{EnsureFree: true, PreserveExisting: isRestored || isPlaceholderReplacement}); err != nil {
		return err
	}
	if err := n.updateLabel(); err != nil {
		return err
	}
	if !n.automatic {
		n.sendMenuSnapshot()
	}
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
	n.invalidateInput()
	n.closeOutputTuns(false)
	if n.router != nil {
		_ = n.router.Close()
		n.router = nil
	}
	if n.autoRouter != nil {
		n.autoRouter.Reset()
	}
	if n.splitter != nil {
		_ = n.splitter.Close()
	}
	n.inputLink = 0
	n.outputCfgs = TunConfigByLink{}
	n.autoSlotLinks = map[int]uint64{}
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
		n.invalidateInput()
		n.inputLink = link
		std.RequestLocked(n.W, n.ID, link)
		n.sendMergedTunConfig()
	case n.slots.Slot(port) != 0 && portDirection == "right":
		if n.automatic {
			n.setAutoSlotLink(n.slots.Slot(port), link)
		}
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
			n.invalidateInput()
			n.inputLink = 0
			if n.splitter != nil {
				_ = n.splitter.Detach()
			}
		}
	case n.slots.Slot(port) != 0 && portDirection == "right":
		if n.automatic {
			n.removeAutoSlotLink(n.slots.Slot(port), link)
		}
		n.closeOutputTun(link)
		n.outputCfgs.Delete(link)
		if err := n.reviewOutputPorts(slotPortReview{EnsureFree: true}); err != nil {
			return err
		}
		n.sendMergedTunConfig()
	}
	return n.updateLabel()
}

func (n *tunSplitterNode) OnInbox(message pasta.InboxMessage) error {
	switch msg := message.Payload.(type) {
	case tunSplitterInputFailed:
		return n.handleInputFailed(msg)
	case tunSplitterInputRetry:
		return n.handleInputRetry(msg)
	default:
		return nil
	}
}

func (n *tunSplitterNode) OnPortRemoved(port uint64, direction string) error {
	if direction != "right" {
		return nil
	}
	if n.automatic {
		n.removeAutoSlotLink(n.slots.Slot(port), 0)
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
	if n.automatic {
		slot := n.slots.Slot(event.ReceiverPort)
		if n.autoSlotLinks[slot] != event.Link {
			// A delayed event from a detached link must not replace the route
			// snapshot learned from the current consumer of this slot.
			return nil
		}
		n.autoRouter.SetConfig(slot, cfg)
	}
	n.outputCfgs.Set(event.Link, cfg.WithoutDNS().WithInferredSourceRoutes())
	n.sendMergedTunConfig()
	return nil
}

func (n *tunSplitterNode) OnFormularMsg(message any) error {
	if n.automatic {
		return nil
	}
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
	if n.automatic {
		return nil
	}
	return nodekit.SetConfigMap(cfg, map[string]any{
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
		// Preserve rejected desired rules for repair and persistence. Keep the
		// current router active until the complete desired program compiles.
		n.rulesText = text
		n.routingConfig = config
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
	n.stopInputRetry()
	n.inputGen++
	if t == nil {
		n.inputTun = nil
		if n.splitter != nil {
			_ = n.splitter.Detach()
		}
		n.closeOutputTuns(true)
		return n.updateLabel()
	}
	generation := n.inputGen
	observed := newTunInputFailureObserver(t, func(err error) {
		// SendInbox takes the workspace lock. The Splitter waits for its read
		// worker during shutdown while OnStop already holds that lock.
		// Dispatch the report separately so a shutdown read error cannot
		// deadlock the workspace.
		go n.W.SendInbox(pasta.InboxMessage{
			ReceiverNode: n.ID,
			Payload: tunSplitterInputFailed{
				link:       link,
				generation: generation,
				err:        err,
			},
		})
	})
	BindTunResource(n.W, n.ID, link, t)
	n.inputTun = t
	if err := n.splitter.Attach(observed); err != nil {
		n.inputTun = nil
		return fmt.Errorf("attach splitter tun: %w", err)
	}
	n.sendAllOutputTuns()
	return n.updateLabel()
}

const tunSplitterInputRetryDelay = 50 * time.Millisecond

type tunSplitterInputFailed struct {
	link       uint64
	generation uint64
	err        error
}

type tunSplitterInputRetry struct {
	link       uint64
	generation uint64
}

func (n *tunSplitterNode) handleInputFailed(msg tunSplitterInputFailed) error {
	if msg.link != n.inputLink || msg.generation != n.inputGen || n.inputTun == nil {
		return nil
	}
	if n.Logger != nil {
		n.Logger.Warnf(
			"TUN splitter input failed; requesting a replacement: link=%d generation=%d terminal=%t error=%v input={%s}",
			msg.link,
			msg.generation,
			tun.IsTunTermError(msg.err),
			msg.err,
			tunDebugString(n.inputTun),
		)
	}
	n.inputGen++
	generation := n.inputGen
	n.inputTun = nil
	if n.splitter != nil {
		_ = n.splitter.Detach()
	}
	// A frontend read can fail while the Splitter changes from the input
	// metadata to its detached defaults. Rebuild all frontends so no consumer
	// keeps a pump that ended during this transition.
	n.closeOutputTuns(true)
	if err := n.updateLabel(); err != nil {
		return err
	}
	n.stopInputRetry()
	w := n.W
	node := n.ID
	n.inputRetry = time.AfterFunc(tunSplitterInputRetryDelay, func() {
		w.SendInbox(pasta.InboxMessage{
			ReceiverNode: node,
			Payload: tunSplitterInputRetry{
				link:       msg.link,
				generation: generation,
			},
		})
	})
	return nil
}

func (n *tunSplitterNode) handleInputRetry(msg tunSplitterInputRetry) error {
	if msg.link != n.inputLink || msg.generation != n.inputGen || n.inputTun != nil {
		return nil
	}
	n.inputRetry = nil
	std.RequestLocked(n.W, n.ID, msg.link)
	return nil
}

func (n *tunSplitterNode) invalidateInput() {
	n.inputGen++
	n.inputTun = nil
	n.stopInputRetry()
}

func (n *tunSplitterNode) stopInputRetry() {
	if n.inputRetry == nil {
		return
	}
	n.inputRetry.Stop()
	n.inputRetry = nil
}

// setAutoSlotLink starts a new route-learning generation for slot. It clears
// the old snapshot synchronously, before the new consumer receives its
// frontend. Thus, no packet can use a route learned from the previous link
// while the new consumer has not sent its first config.
func (n *tunSplitterNode) setAutoSlotLink(slot int, link uint64) {
	if slot == 0 || link == 0 || n.autoRouter == nil {
		return
	}
	n.autoSlotLinks[slot] = link
	n.autoRouter.ClearConfig(slot)
}

// removeAutoSlotLink forgets learned routes only when link still owns slot.
// This identity check makes a delayed removal from an old link harmless. A
// zero link is used when the port itself is removed and forces cleanup.
func (n *tunSplitterNode) removeAutoSlotLink(slot int, link uint64) {
	if slot == 0 || n.autoRouter == nil {
		return
	}
	current := n.autoSlotLinks[slot]
	if link != 0 && current != link {
		return
	}
	delete(n.autoSlotLinks, slot)
	n.autoRouter.ClearConfig(slot)
}

func (n *tunSplitterNode) reviewOutputPorts(opts slotPortReview) error {
	desired := map[int]struct{}{}
	if n.automatic {
		for slot := range n.autoSlotLinks {
			desired[slot] = struct{}{}
		}
	} else if n.router != nil {
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

func (n *tunSplitterNode) closeOutputTuns(notify bool) {
	if notify {
		n.outputTuns.InvalidateAllLocked(n.W, n.ID)
	} else {
		n.outputTuns.CloseAll()
	}
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
