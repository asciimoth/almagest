package networknodes

import (
	"errors"
	"net"

	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/formular"
	"github.com/asciimoth/gonnect"
	"github.com/asciimoth/gonnect/dns"
	"github.com/asciimoth/gonnect/routing"
	"github.com/asciimoth/pasta/pasta"
	"github.com/asciimoth/pasta/pasta/std"

	dnsnodes "github.com/asciimoth/almagest/lib/dns"
	"github.com/asciimoth/almagest/lib/links"
	"github.com/asciimoth/almagest/lib/storage"
)

const (
	NodeTypeNetworkRouter = "almagest/NetworkRouter"

	networkRouterMaxSlots                 = 16
	NetworkRouterConfigBlockID            = "config"
	NetworkRouterRulesField               = "rules"
	NetworkRouterDNSCacheStorageNameField = "dnsCacheStorageName"

	// NetworkRouter reserves the "slot N" name pattern for generated backend
	// slot ports. Static right-directed ports, including DNS, must use names
	// outside that pattern so restore, copy/paste, and slot review can
	// distinguish fixed inputs from dynamic backend slots.
	NetworkRouterDNSPortName = "DNS"
)

type NetworkRouterClass struct {
	Runtime Runtime
}

func (NetworkRouterClass) ClassName() string        { return NodeTypeNetworkRouter }
func (NetworkRouterClass) ShortDescription() string { return "Network router" }
func (NetworkRouterClass) LongDescription() string {
	return "Routes network operations to backend network slots using bytecode routing rules."
}
func (NetworkRouterClass) DefaultNodeParams() pasta.NodeClassParams {
	return pasta.NodeClassParams{PrimaryType: TypeNetwork, InitialPorts: []pasta.Port{
		NetworkPort("left", "Network"),
		NetworkPort("right", slotPortName(1)),
		dnsnodes.DNSPort("right", NetworkRouterDNSPortName),
	}}
}
func (c NetworkRouterClass) NewNode(cfg configer.Config, _ ...*pasta.NodeClassState) (pasta.Node, error) {
	return &networkRouterNode{
		NodeBase:            NodeBase{Runtime: c.Runtime},
		rulesText:           configer.String(cfg, configer.Path{NetworkRouterRulesField}, ""),
		dnsCacheStorageName: dnsnodes.NormalizeOptionalCacheStorageName(configer.String(cfg, configer.Path{NetworkRouterDNSCacheStorageNameField}, "")),
		slots: newSlotPorts(
			networkRouterMaxSlots,
			"right",
			func(slot int) pasta.Port { return NetworkPort("right", slotPortName(slot)) },
			slotPortName,
			func(name string) (int, bool) { return slotPortFromName(name, networkRouterMaxSlots) },
		),
		backends:        map[int]NetworkCloser{},
		backendPayloads: map[int]links.NetworkPayload{},
	}, nil
}

type networkRouterNode struct {
	NodeBase

	in    uint64
	dnsIn uint64
	slots slotPorts

	router          *gonnect.Router
	routerNetwork   gonnect.Network
	mtuProvider     *links.MinimumDatagramMTUProvider
	cfg             routing.RouterCfg
	rulesText       string
	formError       string
	ready           bool
	backends        map[int]NetworkCloser
	backendPayloads map[int]links.NetworkPayload
	outputs         NetworkOutputCache

	// dnsCacheStorageName optionally selects a storage.DnsCacheManager storage whose
	// synthetic PTR records are used by routing string and regexp address
	// matchers. The DNS resolver input below is independent and only resolves
	// LookupHost calls made through the router network.
	dnsCacheStorageName  string
	dnsCacheStorageError string

	// The right-directed DNS input installs an optional Resolver on router.
	// DNS wrappers are kept separate from the received DNS value so replacing
	// or detaching the input can close only the resolver generation installed
	// on this router instance.
	dnsLink     uint64
	dns         dns.Interface
	resolverDNS dns.Interface
	resolver    *dns.Resolver
}

func (n *networkRouterNode) OnInit(w *pasta.Workspace, _ pasta.Logger, id uint64, _ string, restored *pasta.NodeInitData, _, isPlaceholderReplacement, _, isRestored bool) error {
	n.InitBaseNode(w, nil, id)
	if n.router == nil {
		// Nil spawner is intentional here; see the Gonnect spawner note in helpers.go.
		n.router = gonnect.NewRouter(nil)
	}
	if n.mtuProvider == nil {
		n.mtuProvider = links.NewMinimumDatagramMTUProvider()
	}
	if n.routerNetwork == nil {
		n.routerNetwork = links.AttachDatagramMTUProvider(n.router, n.mtuProvider)
	}
	if n.backends == nil {
		n.backends = map[int]NetworkCloser{}
	}
	if n.backendPayloads == nil {
		n.backendPayloads = map[int]links.NetworkPayload{}
	}
	if n.slots.Max() == 0 {
		n.slots = newSlotPorts(
			networkRouterMaxSlots,
			"right",
			func(slot int) pasta.Port { return NetworkPort("right", slotPortName(slot)) },
			slotPortName,
			func(name string) (int, bool) { return slotPortFromName(name, networkRouterMaxSlots) },
		)
	}
	n.in = restoredLeft(restored, 0)
	var rightSlotPorts []uint64
	n.dnsIn, rightSlotPorts = n.restoredRightPorts(restored)
	n.slots.Init(w, id, rightSlotPorts)
	if err := n.SetPrimary(TypeNetwork); err != nil {
		return err
	}
	_ = n.SetLabel("router")
	BindNetworkResource(n.W, n.ID, 0, n.router)

	routingStorageName := n.dnsCacheStorageName
	if err := dnsnodes.ValidateOptionalCacheStorageName(routingStorageName); err != nil {
		n.dnsCacheStorageError = err.Error()
		routingStorageName = ""
	}
	cfg, err := n.compileRules(n.rulesText, routingStorageName)
	if err != nil {
		n.formError = err.Error()
	} else {
		n.cfg = cfg
		n.router.SetCfg(cfg)
	}
	if err := n.reviewOutputPorts(slotPortReview{DefaultSlot: 1, ExtraOrder: []uint64{n.dnsIn}, PreserveExisting: isRestored || isPlaceholderReplacement}); err != nil {
		return err
	}
	n.sendMenuSnapshot()
	return nil
}

func (n *networkRouterNode) OnStop() {
	n.outputs.CloseAll()
	n.detachDNS()
	links.DetachDatagramMTUProvider(n.routerNetwork)
	n.mtuProvider.Close()
	if n.router != nil {
		_ = n.router.Close()
	}
}

func (n *networkRouterNode) OnReady() error {
	if n.slots.TakeDeferredReview() {
		if err := n.reviewOutputPorts(slotPortReview{DefaultSlot: 1, ExtraOrder: []uint64{n.dnsIn}}); err != nil {
			return err
		}
	}
	n.ready = true
	n.requestDNS()
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
	if port == n.dnsIn {
		if portDirection != "right" || linkType != dnsnodes.TypeDNS {
			return pasta.LinkTypeErr(linkType)
		}
		if n.PortHasLinks(port) {
			return pasta.ErrLinkDup
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
	if port == n.dnsIn && portDirection == "right" && linkType == dnsnodes.TypeDNS {
		n.dnsLink = link
		if n.ready {
			std.RequestLocked(n.W, n.ID, link)
		}
		return nil
	}
	if n.slots.Slot(port) != 0 && portDirection == "right" && linkType == TypeNetwork {
		if n.ready {
			std.RequestLocked(n.W, n.ID, link)
		}
	}
	return nil
}

func (n *networkRouterNode) OnLinkRemoved(link, port uint64, linkType, portDirection string) error {
	if port == n.in && portDirection == "left" && linkType == TypeNetwork {
		n.outputs.Close(link)
		return nil
	}
	if port == n.dnsIn && portDirection == "right" && linkType == dnsnodes.TypeDNS && link == n.dnsLink {
		n.detachDNS()
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
	if port == n.dnsIn {
		n.detachDNS()
		n.dnsIn = 0
		return nil
	}
	if slot := n.slots.Remove(port); slot != 0 {
		n.detachSlot(slot)
	}
	return nil
}

func (n *networkRouterNode) OnEvent(event pasta.Event, linkType string, _ []string, receiverPortDirection string) error {
	if event.ReceiverPort == n.in && receiverPortDirection == "left" && linkType == TypeNetwork && std.IsRequest(event.Payload) {
		n.refreshRouterToLink(event.Link)
		return nil
	}
	if event.ReceiverPort == n.dnsIn && receiverPortDirection == "right" && linkType == dnsnodes.TypeDNS && event.Link == n.dnsLink {
		next, changed := dnsnodes.AcceptDNSPayload(n.W, n.ID, event, n.dns)
		if !changed {
			return nil
		}
		n.clearResolver()
		n.dns = next
		n.installResolver(next)
		if n.ready {
			n.sendAll()
		}
		return nil
	}
	slot := n.slots.Slot(event.ReceiverPort)
	if slot == 0 || receiverPortDirection != "right" || linkType != TypeNetwork {
		return nil
	}
	nextPayload, changed := links.AcceptNetworkPayloadUpdate(event, n.backendPayloads[slot])
	if !changed {
		return nil
	}
	if SameNetworkInstance(n.backends[slot], nextPayload.Network) {
		n.backendPayloads[slot] = nextPayload
		n.mtuProvider.SetProvider(uint64(slot), nextPayload.DatagramMTU)
		if n.ready {
			n.sendAll()
		}
		return nil
	}
	n.detachSlot(slot)
	err := n.router.Attach(slot, nextPayload.Network)
	BindNetworkResource(n.W, n.ID, event.Link, nextPayload.Network)
	n.backends[slot] = nextPayload.Network
	n.backendPayloads[slot] = nextPayload
	n.mtuProvider.SetProvider(uint64(slot), nextPayload.DatagramMTU)
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
		if msg.MenuID != pasta.NodeMenuID(n.ID) || msg.BlockID != NetworkRouterConfigBlockID {
			return nil
		}
		return n.applyConfigForm(msg.Values)
	case formular.FieldValidateMessage:
		if msg.MenuID != pasta.NodeMenuID(n.ID) || msg.Field.BlockID != NetworkRouterConfigBlockID || len(msg.Field.ElementPath) > 0 {
			return nil
		}
		switch msg.Field.FieldID {
		case NetworkRouterRulesField:
			n.sendRulesFieldStatus(msg)
		case NetworkRouterDNSCacheStorageNameField:
			n.sendDNSCacheStorageFieldStatus(msg)
		}
	}
	return nil
}

func (n *networkRouterNode) OnSave(cfg configer.Config) error {
	if err := pasta.DeleteNodeOwnedConfigKeys(cfg); err != nil {
		return err
	}
	return configer.SetMap(cfg, map[string]any{
		NetworkRouterRulesField:               n.rulesText,
		NetworkRouterDNSCacheStorageNameField: n.dnsCacheStorageName,
	})
}

func (n *networkRouterNode) applyConfigForm(values map[string]any) error {
	text := n.rulesText
	if value, ok := formular.StringValue(values[NetworkRouterRulesField]); ok {
		text = value
	}
	storageName := n.dnsCacheStorageName
	if value, ok := formular.StringValue(values[NetworkRouterDNSCacheStorageNameField]); ok {
		storageName = dnsnodes.NormalizeOptionalCacheStorageName(value)
	}
	if err := dnsnodes.ValidateOptionalCacheStorageName(storageName); err != nil {
		n.dnsCacheStorageError = err.Error()
		if _, rulesErr := n.compileRules(text, ""); rulesErr != nil {
			n.formError = rulesErr.Error()
		} else {
			n.formError = ""
		}
		n.sendConfigBlock()
		return nil
	}
	cfg, err := n.compileRules(text, storageName)
	if err != nil {
		n.formError = err.Error()
		n.sendConfigBlock()
		return nil
	}
	n.rulesText = text
	n.dnsCacheStorageName = storageName
	n.formError = ""
	n.dnsCacheStorageError = ""
	n.cfg = cfg
	n.router.SetCfg(cfg)
	if err := n.reviewOutputPorts(slotPortReview{DefaultSlot: 1, ExtraOrder: []uint64{n.dnsIn}}); err != nil {
		return err
	}
	n.sendForcedMenuSnapshot()
	return nil
}

func (n *networkRouterNode) sendRulesFieldStatus(msg formular.FieldValidateMessage) {
	text, _ := formular.StringValue(msg.Value)
	if _, err := n.compileRules(text, ""); err != nil {
		sendNodeFieldStatus(n.W, n.ID, msg.Field, err)
		return
	}
	sendNodeFieldStatus(n.W, n.ID, msg.Field, nil)
}

func (n *networkRouterNode) sendDNSCacheStorageFieldStatus(msg formular.FieldValidateMessage) {
	value, _ := formular.StringValue(msg.Value)
	value = dnsnodes.NormalizeOptionalCacheStorageName(value)
	sendNodeFieldStatus(n.W, n.ID, msg.Field, dnsnodes.ValidateOptionalCacheStorageName(value))
}

func (n *networkRouterNode) compileRules(text, dnsCacheStorageName string) (routing.RouterCfg, error) {
	dnsCacheStorageName = dnsnodes.NormalizeOptionalCacheStorageName(dnsCacheStorageName)
	if err := dnsnodes.ValidateOptionalCacheStorageName(dnsCacheStorageName); err != nil {
		return nil, err
	}
	rules, err := routing.NewBytecodeRulesProgram(text)
	if err != nil {
		return nil, err
	}
	rules.DNSCacheStorage = dnsnodes.CacheStorageForRouting(n.cacheManager(), dnsCacheStorageName)
	return routing.NewBytecodeRouterCfg(rules)
}

func (n *networkRouterNode) cacheManager() storage.DnsCacheManager {
	return runtimeDNSCacheManager(n.Runtime)
}

func (n *networkRouterNode) reviewOutputPorts(opts slotPortReview) error {
	desired := map[int]struct{}{}
	if n.cfg != nil {
		for _, slot := range n.cfg.MentionedSlots() {
			if slot >= 1 && slot <= networkRouterMaxSlots {
				desired[slot] = struct{}{}
			}
		}
	}
	return n.slots.Review(desired, opts)
}

func (n *networkRouterNode) sendAll() {
	n.outputs.SendPortLocked(n.W, n.ID, n.in, n.routerNetwork)
}

func (n *networkRouterNode) requestSlots() {
	n.slots.ForEach(func(_ int, portID uint64) {
		n.RequestPort(portID)
	})
}

func (n *networkRouterNode) requestDNS() {
	n.RequestPort(n.dnsIn)
}

func (n *networkRouterNode) sendRouterToLink(link uint64) {
	n.outputs.SendLocked(n.W, n.ID, link, n.routerNetwork)
}

func (n *networkRouterNode) refreshRouterToLink(link uint64) {
	n.outputs.RefreshLocked(n.W, n.ID, link, n.routerNetwork)
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
	delete(n.backendPayloads, slot)
	n.mtuProvider.RemoveProvider(uint64(slot))
}

func (n *networkRouterNode) restoredRightPorts(restored *pasta.NodeInitData) (uint64, []uint64) {
	var dnsPort uint64
	var slotPorts []uint64
	for _, port := range restoredRightPorts(restored) {
		snapshot, ok := n.W.PortSnapshotLocked(port)
		if !ok {
			continue
		}
		// Only ports named with the reserved "slot N" pattern are dynamic
		// backend slots. Static right-directed ports such as DNS are excluded
		// so slot review cannot rename or remove them as routing rules change.
		if snapshot.Name == NetworkRouterDNSPortName {
			dnsPort = port
			continue
		}
		if _, ok := slotPortFromName(snapshot.Name, networkRouterMaxSlots); ok {
			slotPorts = append(slotPorts, port)
		}
	}
	return dnsPort, slotPorts
}

func (n *networkRouterNode) installResolver(input dns.Interface) {
	if n.router == nil || input == nil {
		return
	}
	n.resolverDNS = n.newResolverDNS(input)
	n.resolver = dns.NewResolver(n.resolverDNS)
	n.router.SetResolver(n.resolver)
}

func (n *networkRouterNode) newResolverDNS(input dns.Interface) dns.Interface {
	if source, ok := dnsnodes.DNSSource(input).(dns.Interface); ok && source != nil {
		return dnsnodes.NewDetachedDNS(source)
	}
	return dnsnodes.NewDetachedDNS(input)
}

func (n *networkRouterNode) clearResolver() {
	if n.router != nil {
		n.router.SetResolver(nil)
	}
	dnsnodes.CloseDNS(n.resolverDNS)
	n.resolverDNS = nil
	n.resolver = nil
}

func (n *networkRouterNode) detachDNS() {
	n.clearResolver()
	dnsnodes.CloseDNS(n.dns)
	n.dns = nil
	n.dnsLink = 0
}

func (n *networkRouterNode) hasSlotLinks() bool {
	return n.slots.HasLinks()
}

func (n *networkRouterNode) sendMenuSnapshot() {
	sendNodeMenuSnapshot(n.W, n.ID, false, n.configBlock())
}

func (n *networkRouterNode) sendForcedMenuSnapshot() {
	sendNodeMenuSnapshot(n.W, n.ID, true, n.configBlock())
}

func (n *networkRouterNode) sendConfigBlock() {
	sendNodeBlockSnapshot(n.W, n.ID, n.configBlock())
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
	return formular.Block{ID: NetworkRouterConfigBlockID, Order: 10, Generation: 1, Form: true, Items: []formular.Item{
		{Type: formular.ItemField, ID: NetworkRouterRulesField, Label: "Routing rules", Field: field},
		dnsnodes.CacheStorageSelectorField(
			NetworkRouterDNSCacheStorageNameField,
			"DNS cache storage",
			n.dnsCacheStorageName,
			n.cacheManager(),
			n.dnsCacheStorageError != "",
			n.dnsCacheStorageError,
		),
	}}
}
