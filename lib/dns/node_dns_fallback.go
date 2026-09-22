package dnsnodes

import (
	_ "embed"
	"fmt"

	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/formular"
	"github.com/asciimoth/gonnect"
	"github.com/asciimoth/gonnect/dns"
	"github.com/asciimoth/pasta/pasta"
	"github.com/asciimoth/pasta/pasta/std"

	"github.com/asciimoth/almagest/lib/nodekit"
)

const (
	// NodeTypeDnsFallback is the node class name for ordered DNS fallback.
	NodeTypeDnsFallback = "almagest/DnsFallback"

	dnsFallbackMaxSlots = dns.FallbackSlots

	// DNSFallbackConfigBlockID is the Formular block ID for fallback options.
	DNSFallbackConfigBlockID = "config"
	// DNSFallbackAttemptTimeoutMSField is the Formular and saved-config key for
	// the per-upstream fallback attempt timeout in milliseconds.
	DNSFallbackAttemptTimeoutMSField = "attemptTimeoutMS"
)

var (
	//go:embed node_dns_fallback.md
	dnsFallbackClassLongDescription string

	dnsFallbackNewFallback = func(options dns.FallbackOptions, spawner gonnect.Spawner) *dns.Fallback {
		return dns.NewFallbackWithOptions(options, spawner)
	}
)

// DNSFallbackClass builds ordered DNS fallback nodes.
type DNSFallbackClass struct {
	Runtime nodekit.Runtime
}

func (DNSFallbackClass) ClassName() string        { return NodeTypeDnsFallback }
func (DNSFallbackClass) ShortDescription() string { return "DNS fallback" }
func (DNSFallbackClass) LongDescription() string  { return dnsFallbackClassLongDescription }
func (DNSFallbackClass) DefaultNodeParams() pasta.NodeClassParams {
	return pasta.NodeClassParams{PrimaryType: TypeDNS, InitialPorts: []pasta.Port{
		DNSPort("left", "DNS"),
		DNSPort("right", nodekit.SlotPortName(1)),
	}}
}
func (c DNSFallbackClass) NewNode(cfg configer.Config, _ ...*pasta.NodeClassState) (pasta.Node, error) {
	attemptTimeoutMS := configer.Int(cfg, configer.Path{DNSFallbackAttemptTimeoutMSField}, 0)
	return &dnsFallbackNode{
		Base:             nodekit.Base{Runtime: c.Runtime},
		attemptTimeoutMS: attemptTimeoutMS,
		slots: nodekit.NewSlotPorts(
			dnsFallbackMaxSlots,
			"right",
			func(slot int) pasta.Port { return DNSPort("right", nodekit.SlotPortName(slot)) },
			nodekit.SlotPortName,
			func(name string) (int, bool) { return nodekit.SlotPortFromName(name, dnsFallbackMaxSlots) },
		),
		inputLinks: make(map[int]uint64),
		upstreams:  make(map[int]dns.Interface),
		formError:  dnsTimeoutErrorText(attemptTimeoutMS),
	}, nil
}

// dnsFallbackNode owns one stable gonnect DNS fallback. Its numbered input
// ports correspond directly to fallback slots. The node keeps one unlinked
// slot visible, up to the slot limit, so users can extend a fallback chain
// without configuring its size first.
type dnsFallbackNode struct {
	nodekit.Base

	out   uint64
	slots nodekit.SlotPorts

	fallback   *dns.Fallback
	inputLinks map[int]uint64
	upstreams  map[int]dns.Interface
	outputs    DNSOutputCache
	ready      bool

	attemptTimeoutMS int
	formError        string
}

func (n *dnsFallbackNode) OnInit(w *pasta.Workspace, logger pasta.Logger, id uint64, _ string, restored *pasta.NodeInitData, _, isPlaceholderReplacement, _, isRestored bool) error {
	n.InitBaseNode(w, logger, id)
	if n.fallback == nil {
		n.fallback = n.newFallback()
	}
	if n.upstreams == nil {
		n.upstreams = make(map[int]dns.Interface)
	}
	if n.inputLinks == nil {
		n.inputLinks = make(map[int]uint64)
	}
	if n.slots.Max() == 0 {
		n.slots = nodekit.NewSlotPorts(
			dnsFallbackMaxSlots,
			"right",
			func(slot int) pasta.Port { return DNSPort("right", nodekit.SlotPortName(slot)) },
			nodekit.SlotPortName,
			func(name string) (int, bool) { return nodekit.SlotPortFromName(name, dnsFallbackMaxSlots) },
		)
	}
	n.out = nodekit.RestoredLeft(restored, 0)
	n.slots.Init(w, id, nodekit.RestoredRightPorts(restored))
	if err := n.SetPrimary(TypeDNS); err != nil {
		return err
	}
	BindDNSResource(n.W, n.ID, 0, n.fallback)
	if err := n.reviewInputPorts(nodekit.SlotPortReview{
		EnsureFree:       true,
		PreserveExisting: isRestored || isPlaceholderReplacement,
	}); err != nil {
		return err
	}
	n.sendMenuSnapshot()
	return n.updateLabel()
}

func (n *dnsFallbackNode) OnReady() error {
	if n.slots.TakeDeferredReview() {
		if err := n.reviewInputPorts(nodekit.SlotPortReview{EnsureFree: true}); err != nil {
			return err
		}
	}
	n.ready = true
	n.requestInputs()
	if len(n.upstreams) > 0 || !n.slots.HasLinks() {
		n.sendAll()
	}
	return n.updateLabel()
}

func (n *dnsFallbackNode) OnStop() {
	n.outputs.CloseAll()
	for slot := range n.upstreams {
		n.detachSlot(slot)
	}
	if n.fallback != nil {
		_ = n.fallback.Close()
		n.fallback = nil
	}
	n.ready = false
	clear(n.inputLinks)
}

func (n *dnsFallbackNode) PreLinkAdd(port uint64, linkType, portDirection string) error {
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

func (n *dnsFallbackNode) OnLinkAdd(link, port uint64, linkType, portDirection string) error {
	if linkType != TypeDNS {
		return nil
	}
	switch {
	case port == n.out && portDirection == "left":
		if n.ready {
			n.sendFallbackToLink(link)
		}
	case n.slots.Slot(port) != 0 && portDirection == "right":
		n.inputLinks[n.slots.Slot(port)] = link
		if n.ready {
			std.RequestLocked(n.W, n.ID, link)
		}
		if err := n.reviewInputPorts(nodekit.SlotPortReview{EnsureFree: true}); err != nil {
			return err
		}
	}
	return n.updateLabel()
}

func (n *dnsFallbackNode) OnLinkRemoved(link uint64, port uint64, linkType, portDirection string) error {
	if linkType != TypeDNS {
		return nil
	}
	switch {
	case port == n.out && portDirection == "left":
		n.outputs.Close(link)
	case n.slots.Slot(port) != 0 && portDirection == "right":
		slot := n.slots.Slot(port)
		if n.inputLinks[slot] != link {
			return nil
		}
		delete(n.inputLinks, slot)
		n.detachSlot(slot)
		if err := n.reviewInputPorts(nodekit.SlotPortReview{EnsureFree: true}); err != nil {
			return err
		}
	}
	return n.updateLabel()
}

func (n *dnsFallbackNode) OnPortRemoved(port uint64, direction string) error {
	if direction != "right" {
		return nil
	}
	if slot := n.slots.Remove(port); slot != 0 {
		delete(n.inputLinks, slot)
		n.detachSlot(slot)
	}
	return n.updateLabel()
}

func (n *dnsFallbackNode) OnEvent(event pasta.Event, linkType string, _ []string, receiverPortDirection string) error {
	if linkType != TypeDNS {
		return nil
	}
	if event.ReceiverPort == n.out && receiverPortDirection == "left" && std.IsRequest(event.Payload) {
		n.refreshFallbackToLink(event.Link)
		return nil
	}
	slot := n.slots.Slot(event.ReceiverPort)
	if slot == 0 || receiverPortDirection != "right" || event.Link != n.inputLinks[slot] || std.IsRequest(event.Payload) {
		return nil
	}
	payload, ok := event.Payload.(DNSPayload)
	if !ok || !DNSIsReady(payload.DNS) || SameDNSInstance(n.upstreams[slot], payload.DNS) {
		return nil
	}

	n.detachSlot(slot)
	if err := n.fallback.Attach(slot, payload.DNS); err != nil {
		CloseDNS(payload.DNS)
		return fmt.Errorf("attach DNS fallback %s: %w", nodekit.SlotPortName(slot), err)
	}
	BindDNSResource(n.W, n.ID, event.Link, payload.DNS)
	n.upstreams[slot] = payload.DNS
	if n.ready {
		n.sendAll()
	}
	return n.updateLabel()
}

func (n *dnsFallbackNode) OnFormularMsg(message any) error {
	switch msg := message.(type) {
	case formular.FormApplyMessage:
		if msg.MenuID != pasta.NodeMenuID(n.ID) || msg.BlockID != DNSFallbackConfigBlockID {
			return nil
		}
		return n.applyConfigForm(msg.Values)
	case formular.FieldValidateMessage:
		if msg.MenuID != pasta.NodeMenuID(n.ID) || msg.Field.BlockID != DNSFallbackConfigBlockID || msg.Field.FieldID != DNSFallbackAttemptTimeoutMSField || len(msg.Field.ElementPath) > 0 {
			return nil
		}
		n.sendTimeoutFieldStatus(msg)
	}
	return nil
}

func (n *dnsFallbackNode) OnSave(cfg configer.Config) error {
	if err := pasta.DeleteNodeOwnedConfigKeys(cfg); err != nil {
		return err
	}
	return configer.SetMap(cfg, map[string]any{
		DNSFallbackAttemptTimeoutMSField: n.attemptTimeoutMS,
	})
}

func (n *dnsFallbackNode) applyConfigForm(values map[string]any) error {
	next := n.attemptTimeoutMS
	if value, ok := formular.IntValue(values[DNSFallbackAttemptTimeoutMSField]); ok {
		next = value
	}
	if err := validateDNSTimeoutMilliseconds(next); err != nil {
		n.formError = err.Error()
		n.sendForcedMenuSnapshot()
		return n.updateLabel()
	}
	changed := next != n.attemptTimeoutMS || n.formError != ""
	n.attemptTimeoutMS = next
	n.formError = ""
	n.sendForcedMenuSnapshot()
	if changed {
		n.rebuildFallback()
	}
	return n.updateLabel()
}

func (n *dnsFallbackNode) sendTimeoutFieldStatus(msg formular.FieldValidateMessage) {
	nodekit.SendNodeFieldStatus(n.W, n.ID, msg.Field, validateDNSTimeoutField(msg.Value))
}

func (n *dnsFallbackNode) reviewInputPorts(opts nodekit.SlotPortReview) error {
	desired := make(map[int]struct{}, len(n.inputLinks))
	for slot := range n.inputLinks {
		desired[slot] = struct{}{}
	}
	if err := n.slots.Review(desired, opts); err != nil {
		return err
	}
	var typeErr error
	n.slots.ForEach(func(_ int, port uint64) {
		if typeErr == nil {
			typeErr = n.W.SetPortTypesLocked(port, []string{TypeDNS})
		}
	})
	return typeErr
}

func (n *dnsFallbackNode) requestInputs() {
	n.slots.ForEach(func(_ int, port uint64) {
		n.RequestPort(port)
	})
}

func (n *dnsFallbackNode) sendAll() {
	n.outputs.SendPortLocked(n.W, n.ID, n.out, n.fallback)
}

func (n *dnsFallbackNode) sendFallbackToLink(link uint64) {
	n.outputs.SendLocked(n.W, n.ID, link, n.fallback)
}

func (n *dnsFallbackNode) refreshFallbackToLink(link uint64) {
	n.outputs.RefreshLocked(n.W, n.ID, link, n.fallback)
}

func (n *dnsFallbackNode) detachSlot(slot int) {
	if slot < 1 || slot > dnsFallbackMaxSlots {
		return
	}
	old := n.upstreams[slot]
	if old == nil {
		return
	}
	if n.fallback != nil {
		_ = n.fallback.Detach(slot)
	}
	CloseDNS(old)
	delete(n.upstreams, slot)
}

// rebuildFallback replaces only the fallback runtime. Attached DNS wrappers
// stay owned by their input links and are attached to the replacement before
// it is published to consumers.
func (n *dnsFallbackNode) rebuildFallback() {
	n.outputs.CloseAll()
	old := n.fallback
	n.fallback = n.newFallback()
	for slot := 1; slot <= dnsFallbackMaxSlots; slot++ {
		if upstream := n.upstreams[slot]; upstream != nil {
			_ = n.fallback.Attach(slot, upstream)
		}
	}
	BindDNSResource(n.W, n.ID, 0, n.fallback)
	if old != nil {
		_ = old.Close()
	}
	if n.ready {
		n.sendAll()
	}
}

func (n *dnsFallbackNode) newFallback() *dns.Fallback {
	return dnsFallbackNewFallback(dns.FallbackOptions{
		AttemptTimeout: dnsTimeoutDuration(n.attemptTimeoutMS),
	}, n.dnsSpawner())
}

func (n *dnsFallbackNode) dnsSpawner() gonnect.Spawner {
	if n.WorkerSpawner == nil {
		return nil
	}
	return n.WorkerSpawner
}

func (n *dnsFallbackNode) updateLabel() error {
	if n.formError != "" {
		return n.SetLabel("invalid config")
	}
	switch len(n.upstreams) {
	case 0:
		return n.SetLabel("waiting for DNS")
	case 1:
		return n.SetLabel("fallback DNS")
	default:
		return n.SetLabel(fmt.Sprintf("fallback across %d DNS backends", len(n.upstreams)))
	}
}

func (n *dnsFallbackNode) sendMenuSnapshot() {
	nodekit.SendNodeMenuSnapshot(n.W, n.ID, false, n.configBlock())
}

func (n *dnsFallbackNode) sendForcedMenuSnapshot() {
	nodekit.SendNodeMenuSnapshot(n.W, n.ID, true, n.configBlock())
}

func (n *dnsFallbackNode) configBlock() formular.Block {
	return formular.Block{ID: DNSFallbackConfigBlockID, Order: 10, Generation: 1, Form: true, Items: []formular.Item{
		dnsTimeoutField(
			DNSFallbackAttemptTimeoutMSField,
			"Attempt timeout (ms)",
			n.attemptTimeoutMS,
			"Maximum time to wait for one upstream before trying the next slot.",
			n.formError,
		),
	}}
}
