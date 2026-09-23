package networknodes

import (
	_ "embed"
	"fmt"
	"slices"

	"github.com/asciimoth/almagest/lib/nodekit"
	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/formular"
	"github.com/asciimoth/gonnect"
	"github.com/asciimoth/gonnect/routing"
	"github.com/asciimoth/gonnect/sniffer"
	"github.com/asciimoth/pasta/pasta"
	"github.com/asciimoth/pasta/pasta/std"

	dnsnodes "github.com/asciimoth/almagest/lib/dns"
	"github.com/asciimoth/almagest/lib/storage"
)

const (
	// NodeTypeSniffer is the node class name for client-first stream routing.
	NodeTypeSniffer = "almagest/Sniffer"

	snifferMaxSlots                 = 16
	snifferConfigBlockID            = "config"
	snifferRulesField               = "rules"
	snifferDNSCacheStorageNameField = "dnsCacheStorageName"
)

const snifferRulesPlaceholder = `# Intercept TCP dials, inspect client-first bytes, then route.
DIAL
TCP
AND
INTERCEPT

SNIFF HTTP URL:/admin
DROP

SNIFF TLS ALPN:h2
SLOT 2

SNIFF HTTP
ROUTE 1 DST:127.0.0.1:8080

SNIFF_NONE
SLOT 1`

var (
	//go:embed node_sniffer.md
	snifferClassLongDescription string
)

type SnifferClass struct {
	Runtime Runtime
}

func (SnifferClass) ClassName() string        { return NodeTypeSniffer }
func (SnifferClass) ShortDescription() string { return "Network sniffer" }
func (SnifferClass) LongDescription() string  { return snifferClassLongDescription }
func (SnifferClass) DefaultNodeParams() pasta.NodeClassParams {
	return pasta.NodeClassParams{PrimaryType: TypeNetwork, InitialPorts: []pasta.Port{
		NetworkPort("left", "Network"),
		NetworkPort("right", slotPortName(1)),
	}}
}
func (c SnifferClass) NewNode(cfg configer.Config, _ ...*pasta.NodeClassState) (pasta.Node, error) {
	return &snifferNode{
		NodeBase:            NodeBase{Runtime: c.Runtime},
		rulesText:           configer.String(cfg, configer.Path{snifferRulesField}, ""),
		dnsCacheStorageName: dnsnodes.NormalizeOptionalCacheStorageName(configer.String(cfg, configer.Path{snifferDNSCacheStorageNameField}, "")),
		slots: newSlotPorts(
			snifferMaxSlots,
			"right",
			func(slot int) pasta.Port { return NetworkPort("right", slotPortName(slot)) },
			slotPortName,
			func(name string) (int, bool) { return slotPortFromName(name, snifferMaxSlots) },
		),
		backends: map[int]NetworkCloser{},
	}, nil
}

// snifferNode emits per-output-link gonnect/sniffer.Sniffer wrappers. Gonnect
// Sniffer outputs are immutable, so the node closes and rebuilds only emitted
// wrappers when rules or backend slots change. It does not close backend slots
// through Sniffer.Close.
type snifferNode struct {
	NodeBase

	out   uint64
	slots slotPorts

	rules    routing.SnifferBytecodeRules
	controls routing.SnifferControls

	rulesText string
	formError string
	ready     bool

	backends map[int]NetworkCloser
	outputs  snifferOutputCache

	// dnsCacheStorageName optionally selects a storage.DnsCacheManager storage whose
	// synthetic PTR records are used by routing string and regexp address
	// matchers. It does not install a resolver on the emitted network.
	dnsCacheStorageName  string
	dnsCacheStorageError string
}

func (n *snifferNode) OnInit(w *pasta.Workspace, logger pasta.Logger, id uint64, _ string, restored *pasta.NodeInitData, _, isPlaceholderReplacement, _, isRestored bool) error {
	n.InitBaseNode(w, logger, id)
	if n.backends == nil {
		n.backends = map[int]NetworkCloser{}
	}
	if n.slots.Max() == 0 {
		n.slots = newSlotPorts(
			snifferMaxSlots,
			"right",
			func(slot int) pasta.Port { return NetworkPort("right", slotPortName(slot)) },
			slotPortName,
			func(name string) (int, bool) { return slotPortFromName(name, snifferMaxSlots) },
		)
	}
	n.out = restoredLeft(restored, 0)
	n.slots.Init(w, id, restoredRightPorts(restored))
	if err := n.SetPrimary(TypeNetwork); err != nil {
		return err
	}

	storageName := n.dnsCacheStorageName
	if err := dnsnodes.ValidateOptionalCacheStorageName(storageName); err != nil {
		n.dnsCacheStorageError = err.Error()
		storageName = ""
	}
	rules, controls, err := n.compileRules(n.rulesText, storageName)
	if err != nil {
		n.formError = err.Error()
	} else {
		n.rules = rules
		n.controls = controls
	}
	if err := n.reviewOutputPorts(slotPortReview{DefaultSlot: 1, PreserveExisting: isRestored || isPlaceholderReplacement}); err != nil {
		return err
	}
	n.sendMenuSnapshot()
	return n.updateLabel()
}

func (n *snifferNode) OnReady() error {
	if n.slots.TakeDeferredReview() {
		if err := n.reviewOutputPorts(slotPortReview{DefaultSlot: 1}); err != nil {
			return err
		}
	}
	n.ready = true
	n.requestSlots()
	n.sendAll()
	return n.updateLabel()
}

func (n *snifferNode) OnStop() {
	n.outputs.CloseAll()
	for slot := range n.backends {
		n.detachSlot(slot)
	}
}

func (n *snifferNode) PreLinkAdd(port uint64, linkType, portDirection string) error {
	if port == n.out {
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

func (n *snifferNode) OnLinkAdd(link, port uint64, linkType, portDirection string) error {
	if port == n.out && portDirection == "left" && linkType == TypeNetwork {
		if n.ready {
			n.sendOutputToLink(link)
		}
		return n.updateLabel()
	}
	if n.slots.Slot(port) != 0 && portDirection == "right" && linkType == TypeNetwork {
		if n.ready {
			std.RequestLocked(n.W, n.ID, link)
		}
		return n.updateLabel()
	}
	return nil
}

func (n *snifferNode) OnLinkRemoved(link, port uint64, linkType, portDirection string) error {
	if port == n.out && portDirection == "left" && linkType == TypeNetwork {
		n.outputs.Close(link)
		return n.updateLabel()
	}
	if slot := n.slots.Slot(port); slot != 0 && portDirection == "right" && linkType == TypeNetwork {
		n.detachSlot(slot)
		if err := n.reviewOutputPorts(slotPortReview{DefaultSlot: 1}); err != nil {
			return err
		}
		if n.ready {
			n.sendAll()
		}
		return n.updateLabel()
	}
	return nil
}

func (n *snifferNode) OnPortRemoved(port uint64, direction string) error {
	if direction != "right" {
		return nil
	}
	if slot := n.slots.Remove(port); slot != 0 {
		n.detachSlot(slot)
	}
	return nil
}

func (n *snifferNode) OnEvent(event pasta.Event, linkType string, _ []string, receiverPortDirection string) error {
	if event.ReceiverPort == n.out && receiverPortDirection == "left" && linkType == TypeNetwork && std.IsRequest(event.Payload) {
		n.refreshOutputToLink(event.Link)
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
	BindNetworkResource(n.W, n.ID, event.Link, next)
	n.backends[slot] = next
	n.outputs.CloseAll()
	if n.ready {
		n.sendAll()
	}
	return n.updateLabel()
}

func (n *snifferNode) OnFormularMsg(message any) error {
	switch msg := message.(type) {
	case formular.FormApplyMessage:
		if msg.MenuID != pasta.NodeMenuID(n.ID) || msg.BlockID != snifferConfigBlockID {
			return nil
		}
		return n.applyConfigForm(msg.Values)
	case formular.FieldValidateMessage:
		if msg.MenuID != pasta.NodeMenuID(n.ID) || msg.Field.BlockID != snifferConfigBlockID || len(msg.Field.ElementPath) > 0 {
			return nil
		}
		switch msg.Field.FieldID {
		case snifferRulesField:
			n.sendRulesFieldStatus(msg)
		case snifferDNSCacheStorageNameField:
			n.sendDNSCacheStorageFieldStatus(msg)
		}
	}
	return nil
}

func (n *snifferNode) OnSave(cfg configer.Config) error {
	if err := pasta.DeleteNodeOwnedConfigKeys(cfg); err != nil {
		return err
	}
	return nodekit.SetConfigMap(cfg, map[string]any{
		snifferRulesField:               n.rulesText,
		snifferDNSCacheStorageNameField: n.dnsCacheStorageName,
	})
}

func (n *snifferNode) applyConfigForm(values map[string]any) error {
	text := n.rulesText
	if value, ok := formular.StringValue(values[snifferRulesField]); ok {
		text = value
	}
	storageName := n.dnsCacheStorageName
	if value, ok := formular.StringValue(values[snifferDNSCacheStorageNameField]); ok {
		storageName = dnsnodes.NormalizeOptionalCacheStorageName(value)
	}
	if err := dnsnodes.ValidateOptionalCacheStorageName(storageName); err != nil {
		n.dnsCacheStorageError = err.Error()
		if _, _, rulesErr := n.compileRules(text, ""); rulesErr != nil {
			n.formError = rulesErr.Error()
		} else {
			n.formError = ""
		}
		n.sendConfigBlock()
		return n.updateLabel()
	}
	rules, controls, err := n.compileRules(text, storageName)
	if err != nil {
		n.formError = err.Error()
		n.sendConfigBlock()
		return n.updateLabel()
	}
	n.rulesText = text
	n.dnsCacheStorageName = storageName
	n.formError = ""
	n.dnsCacheStorageError = ""
	n.rules = rules
	n.controls = controls
	if err := n.reviewOutputPorts(slotPortReview{DefaultSlot: 1}); err != nil {
		return err
	}
	n.outputs.CloseAll()
	n.sendForcedMenuSnapshot()
	n.sendAll()
	return n.updateLabel()
}

func (n *snifferNode) sendRulesFieldStatus(msg formular.FieldValidateMessage) {
	text, _ := formular.StringValue(msg.Value)
	_, _, err := n.compileRules(text, "")
	sendNodeFieldStatus(n.W, n.ID, msg.Field, err)
}

func (n *snifferNode) sendDNSCacheStorageFieldStatus(msg formular.FieldValidateMessage) {
	value, _ := formular.StringValue(msg.Value)
	value = dnsnodes.NormalizeOptionalCacheStorageName(value)
	sendNodeFieldStatus(n.W, n.ID, msg.Field, dnsnodes.ValidateOptionalCacheStorageName(value))
}

func (n *snifferNode) compileRules(text, dnsCacheStorageName string) (routing.SnifferBytecodeRules, routing.SnifferControls, error) {
	dnsCacheStorageName = dnsnodes.NormalizeOptionalCacheStorageName(dnsCacheStorageName)
	if err := dnsnodes.ValidateOptionalCacheStorageName(dnsCacheStorageName); err != nil {
		return routing.SnifferBytecodeRules{}, nil, err
	}
	rules, err := routing.NewSnifferBytecodeRules(nil, text)
	if err != nil {
		return routing.SnifferBytecodeRules{}, nil, err
	}
	rules.DNSCacheStorage = dnsnodes.CacheStorageForRouting(n.cacheManager(), dnsCacheStorageName)
	controls, err := routing.NewBytecodeSnifferControls(rules)
	if err != nil {
		return routing.SnifferBytecodeRules{}, nil, err
	}
	for _, slot := range controls.MentionedSlots() {
		if slot > snifferMaxSlots {
			return routing.SnifferBytecodeRules{}, nil, fmt.Errorf("slot %d exceeds Sniffer maximum slot %d", slot, snifferMaxSlots)
		}
	}
	return rules, controls, nil
}

func (n *snifferNode) cacheManager() storage.DnsCacheManager {
	runtime := n.NodeRuntime()
	if runtime == nil {
		return nil
	}
	return runtimeDNSCacheManager(runtime)
}

func (n *snifferNode) reviewOutputPorts(opts slotPortReview) error {
	desired := map[int]struct{}{}
	for _, slot := range n.neededSlots() {
		desired[slot] = struct{}{}
	}
	return n.slots.Review(desired, opts)
}

func (n *snifferNode) requestSlots() {
	n.slots.ForEach(func(_ int, portID uint64) {
		n.RequestPort(portID)
	})
}

func (n *snifferNode) sendAll() {
	BroadcastNetworkPort(n.W, n.ID, n.out, func(link uint64) {
		n.sendOutputToLink(link)
	})
}

func (n *snifferNode) sendOutputToLink(link uint64) {
	if !n.readyToSend() {
		return
	}
	n.outputs.SendLocked(n.W, n.ID, link, n.newSniffer)
}

func (n *snifferNode) refreshOutputToLink(link uint64) {
	if !n.readyToSend() {
		return
	}
	n.outputs.RefreshLocked(n.W, n.ID, link, n.newSniffer)
}

func (n *snifferNode) newSniffer() NetworkCloser {
	if n.controls == nil {
		return nil
	}
	outputs := n.snifferOutputs()
	wrapper, err := sniffer.NewSniffer(sniffer.SnifferConfig{
		Outputs:      outputs,
		Control:      n.controls.Control,
		SniffControl: n.controls.SniffControl,
		Classifiers:  n.controls.Classifiers(),
		Pool:         enginePool(n.NodeRuntime()),
	})
	if err != nil {
		if n.Logger != nil {
			n.Logger.Errf("failed to create sniffer: %v", err)
		}
		return nil
	}
	return wrapper
}

func (n *snifferNode) snifferOutputs() []gonnect.Network {
	maxSlot := 0
	for _, slot := range n.neededSlots() {
		if slot > maxSlot {
			maxSlot = slot
		}
	}
	outputs := make([]gonnect.Network, maxSlot)
	for slot, backend := range n.backends {
		if slot >= 1 && slot <= maxSlot && NetworkIsReady(backend) {
			outputs[slot-1] = backend
		}
	}
	return outputs
}

func (n *snifferNode) readyToSend() bool {
	return n.ready && n.formError == "" && n.dnsCacheStorageError == "" && n.controls != nil && len(n.missingSlots()) == 0
}

func (n *snifferNode) neededSlots() []int {
	if n.controls == nil {
		return nil
	}
	seen := map[int]struct{}{}
	for _, slot := range n.controls.MentionedSlots() {
		if slot >= 1 && slot <= snifferMaxSlots {
			seen[slot] = struct{}{}
		}
	}
	out := make([]int, 0, len(seen))
	for slot := range seen {
		out = append(out, slot)
	}
	slices.Sort(out)
	return out
}

func (n *snifferNode) missingSlots() []int {
	needed := n.neededSlots()
	missing := make([]int, 0, len(needed))
	for _, slot := range needed {
		if !NetworkIsReady(n.backends[slot]) {
			missing = append(missing, slot)
		}
	}
	return missing
}

func (n *snifferNode) detachSlot(slot int) {
	if slot < 1 || slot > snifferMaxSlots {
		return
	}
	if old := n.backends[slot]; old != nil {
		_ = closeNetwork(old)
		delete(n.backends, slot)
		n.outputs.CloseAll()
	}
}

func (n *snifferNode) updateLabel() error {
	missing := n.missingSlots()
	switch {
	case n.formError != "" || n.dnsCacheStorageError != "":
		return n.SetLabel("invalid config")
	case len(missing) == 1:
		return n.SetLabel(fmt.Sprintf("waiting for %s", slotPortName(missing[0])))
	case len(missing) > 1:
		return n.SetLabel(fmt.Sprintf("waiting for %d slots", len(missing)))
	case len(n.rules.Classifiers) == 1:
		return n.SetLabel("sniffing 1 classifier")
	case len(n.rules.Classifiers) > 1:
		return n.SetLabel(fmt.Sprintf("sniffing %d classifiers", len(n.rules.Classifiers)))
	default:
		return n.SetLabel("sniffing network")
	}
}

func (n *snifferNode) sendMenuSnapshot() {
	sendNodeMenuSnapshot(n.W, n.ID, false, n.configBlock())
}

func (n *snifferNode) sendForcedMenuSnapshot() {
	sendNodeMenuSnapshot(n.W, n.ID, true, n.configBlock())
}

func (n *snifferNode) sendConfigBlock() {
	sendNodeBlockSnapshot(n.W, n.ID, n.configBlock())
}

func (n *snifferNode) configBlock() formular.Block {
	field := &formular.Field{
		Kind:        formular.FieldText,
		Value:       n.rulesText,
		Multiline:   true,
		Validation:  true,
		Placeholder: snifferRulesPlaceholder,
	}
	if n.formError != "" {
		field.Status = formular.StatusError
		field.StatusText = n.formError
	}
	return formular.Block{ID: snifferConfigBlockID, Order: 10, Generation: 1, Form: true, Items: []formular.Item{
		{Type: formular.ItemField, ID: snifferRulesField, Label: "Sniffer rules", Field: field},
		dnsnodes.CacheStorageSelectorField(
			snifferDNSCacheStorageNameField,
			"DNS cache storage",
			n.dnsCacheStorageName,
			n.cacheManager(),
			n.dnsCacheStorageError != "",
			n.dnsCacheStorageError,
		),
	}}
}

type snifferOutputEntry struct {
	output NetworkCloser
}

type snifferOutputCache struct {
	entries map[uint64]snifferOutputEntry
}

func (c *snifferOutputCache) SendLocked(w *pasta.Workspace, node, link uint64, build func() NetworkCloser) bool {
	if w == nil || link == 0 || build == nil {
		return false
	}
	if _, ok := w.LinkSnapshotLocked(link); !ok {
		return false
	}
	if c.entries != nil {
		if entry, ok := c.entries[link]; ok && NetworkIsReady(entry.output) {
			w.EmitEventLocked(node, link, NetworkPayload{Network: entry.output})
			return true
		}
	}
	c.Close(link)
	return c.emitLocked(w, node, link, build)
}

func (c *snifferOutputCache) RefreshLocked(w *pasta.Workspace, node, link uint64, build func() NetworkCloser) bool {
	if w == nil || link == 0 || build == nil {
		return false
	}
	if _, ok := w.LinkSnapshotLocked(link); !ok {
		return false
	}
	c.Close(link)
	return c.emitLocked(w, node, link, build)
}

func (c *snifferOutputCache) emitLocked(w *pasta.Workspace, node, link uint64, build func() NetworkCloser) bool {
	output := build()
	if output == nil {
		return false
	}
	BindNetworkResource(w, node, link, output)
	if c.entries == nil {
		c.entries = map[uint64]snifferOutputEntry{}
	}
	c.entries[link] = snifferOutputEntry{output: output}
	w.EmitEventLocked(node, link, NetworkPayload{Network: output})
	return true
}

func (c *snifferOutputCache) Close(link uint64) {
	if c == nil || c.entries == nil {
		return
	}
	entry := c.entries[link]
	delete(c.entries, link)
	if entry.output != nil {
		_ = entry.output.Close()
	}
}

func (c *snifferOutputCache) CloseAll() {
	if c == nil {
		return
	}
	for link, entry := range c.entries {
		delete(c.entries, link)
		if entry.output != nil {
			_ = entry.output.Close()
		}
	}
}
