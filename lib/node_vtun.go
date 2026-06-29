package almagest

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strings"

	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/formular"
	"github.com/asciimoth/gonnect"
	netstackhelpers "github.com/asciimoth/gonnect-netstack/helpers"
	netstackvtun "github.com/asciimoth/gonnect-netstack/vtun"
	"github.com/asciimoth/gonnect/tun"
	"github.com/asciimoth/pasta/pasta"
	"github.com/asciimoth/pasta/pasta/std"
)

func init() {
	regClass(func(e *engine) pasta.NodeClass {
		return vtunClass{engine: e}
	}, false)
}

const (
	// NodeTypeVTun is the node class name for a virtual network-to-TUN adapter.
	NodeTypeVTun = "almagest/VTun"

	defaultVTunName       = "vtun"
	defaultVTunMTU        = 1500
	defaultVTunEndpointCh = 1024
	defaultVTunEventCh    = 10
)

type vtunClass struct {
	engine *engine
}

func (vtunClass) ClassName() string        { return NodeTypeVTun }
func (vtunClass) ShortDescription() string { return "Virtual TUN network" }
func (vtunClass) LongDescription() string {
	return "Creates one virtual netstack instance and exposes it as both an almagest/network and an almagest/tun endpoint."
}
func (vtunClass) DefaultNodeParams() pasta.NodeClassParams {
	return pasta.NodeClassParams{PrimaryType: TypeTun, InitialPorts: []pasta.Port{
		NetworkPort("left", "Network"),
		TunPort("right", "Tun"),
	}}
}
func (c vtunClass) NewNode(cfg configer.Config, _ ...*pasta.NodeClassState) (pasta.Node, error) {
	config := defaultVTunConfig()
	config.read(cfg)
	return &vtunNode{engine: c.engine, config: config}, nil
}

type vtunConfig struct {
	name       string
	localAddrs string
	dnsServers string
	mtu        int
	endpointCh int
	eventCh    int
	// mwo            int
	// mro            int
	noLoopbackAddr bool
	disableSACK    bool
}

func defaultVTunConfig() vtunConfig {
	return vtunConfig{
		name:       defaultVTunName,
		mtu:        defaultVTunMTU,
		endpointCh: defaultVTunEndpointCh,
		eventCh:    defaultVTunEventCh,
	}
}

func (c *vtunConfig) read(cfg configer.Config) {
	if cfg == nil {
		return
	}
	c.name = configer.String(cfg, configer.Path{"name"}, c.name)
	c.localAddrs = configer.String(cfg, configer.Path{"localAddrs"}, c.localAddrs)
	c.dnsServers = configer.String(cfg, configer.Path{"dnsServers"}, c.dnsServers)
	c.mtu = configer.Int(cfg, configer.Path{"mtu"}, c.mtu)
	c.endpointCh = configer.Int(cfg, configer.Path{"endpointCh"}, c.endpointCh)
	c.eventCh = configer.Int(cfg, configer.Path{"eventCh"}, c.eventCh)
	// c.mwo = configer.Int(cfg, configer.Path{"mwo"}, c.mwo)
	// c.mro = configer.Int(cfg, configer.Path{"mro"}, c.mro)
	c.noLoopbackAddr = configer.Bool(cfg, configer.Path{"noLoopbackAddr"}, c.noLoopbackAddr)
	c.disableSACK = configer.Bool(cfg, configer.Path{"disableSACK"}, c.disableSACK)
	c.normalize()
}

func (c *vtunConfig) normalize() {
	c.name = strings.TrimSpace(c.name)
	if c.name == "" {
		c.name = defaultVTunName
	}
	c.localAddrs = normalizeListText(c.localAddrs)
	c.dnsServers = normalizeListText(c.dnsServers)
	if c.mtu <= 0 {
		c.mtu = defaultVTunMTU
	}
	if c.endpointCh <= 0 {
		c.endpointCh = defaultVTunEndpointCh
	}
	if c.eventCh <= 0 {
		c.eventCh = defaultVTunEventCh
	}
}

// vtunNode owns one netstack VTun and publishes detached wrappers for each
// connected network or TUN link. The underlying VTun is lazy: links create it,
// options replace it, and the last detached link closes it.
type vtunNode struct {
	pasta.BasicNode

	engine *engine
	logger pasta.Logger

	w       *pasta.Workspace
	id      uint64
	network uint64
	tunp    uint64

	config vtunConfig

	tunConfig TunConfig

	generatedAddrs []netip.Addr
	vtun           *netstackvtun.VTun
	sentNetworks   map[uint64]NetworkCloser
	sentTuns       map[uint64]tun.Tun
	sentTunWait    []tun.Tun
	formError      string
}

func (n *vtunNode) OnInit(w *pasta.Workspace, logger pasta.Logger, id uint64, _ string, restored *pasta.NodeInitData, _, _, _, _ bool) error {
	n.w = w
	n.id = id
	n.logger = logger
	n.config.normalize()
	if restored != nil {
		if len(restored.LeftPorts) > 0 {
			n.network = restored.LeftPorts[0]
		}
		if len(restored.RightPorts) > 0 {
			n.tunp = restored.RightPorts[0]
		}
	}
	if err := n.w.SetNodePrimary(n.id, TypeTun); err != nil {
		return err
	}
	_ = n.updateLabel()
	n.sendMenuSnapshot()
	return nil
}

func (n *vtunNode) OnReady() error {
	if err := n.ensureVTun(); err != nil {
		return nil
	}
	n.sendAll()
	return nil
}

func (n *vtunNode) OnStop() {
	n.dropVTun()
	n.freeGeneratedAddrs()
}

func (n *vtunNode) PreLinkAdd(port uint64, linkType, portDirection string) error {
	switch port {
	case n.network:
		if portDirection != "left" || linkType != TypeNetwork {
			return pasta.LinkTypeErr(linkType)
		}
	case n.tunp:
		if portDirection != "right" {
			return pasta.LinkTypeErr(linkType)
		}
		return TunPreLinkAdd(n.w, port, linkType)
	default:
		return pasta.LinkTypeErr(linkType)
	}
	return nil
}

func (n *vtunNode) OnLinkAdd(link, port uint64, _, _ string) error {
	if port != n.network && port != n.tunp {
		return nil
	}
	if n.vtun == nil {
		if err := n.rebuildVTunActive(); err != nil {
			return nil
		}
	} else if err := n.ensureVTun(); err != nil {
		return nil
	}
	switch port {
	case n.network:
		n.sendNetworkToLink(link)
	case n.tunp:
		n.sendTunToLink(link)
	}
	return nil
}

func (n *vtunNode) OnLinkRemoved(link uint64, port uint64, _, _ string) error {
	switch port {
	case n.network:
		n.closeSentNetwork(link)
	case n.tunp:
		n.closeSentTun(link)
		n.tunConfig = TunConfig{}
		if n.vtun != nil && n.usesTunConfig() {
			if err := n.rebuildVTun(); err != nil {
				return nil
			}
		}
	}
	if !n.hasAnyLink() {
		n.dropVTun()
		_ = n.updateLabel()
	}
	return nil
}

func (n *vtunNode) OnEvent(event pasta.Event, linkType string, _ []string, receiverPortDirection string) error {
	if event.ReceiverPort == n.network && receiverPortDirection == "left" && linkType == TypeNetwork && std.IsRequest(event.Payload) {
		n.sendNetworkToLink(event.Link)
		return nil
	}
	if event.ReceiverPort == n.tunp && receiverPortDirection == "right" && linkType == TypeTun {
		if std.IsRequest(event.Payload) {
			n.sendTunToLink(event.Link)
			return nil
		}
		if cfg, ok := event.Payload.(TunConfig); ok {
			n.tunConfig = cfg.Clone()
			if n.usesTunConfig() {
				if err := n.rebuildVTun(); err != nil {
					return nil
				}
			} else {
				n.sendEffectiveBlock()
			}
		}
	}
	return nil
}

func (n *vtunNode) OnFormularMsg(message any) error {
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
		n.sendConfigFieldStatus(msg)
	}
	return nil
}

func (n *vtunNode) OnSave(cfg configer.Config) error {
	if err := pasta.DeleteNodeOwnedConfigKeys(cfg); err != nil {
		return err
	}
	return configer.SetMap(cfg, map[string]any{
		"name":       n.config.name,
		"localAddrs": n.config.localAddrs,
		"dnsServers": n.config.dnsServers,
		"mtu":        n.config.mtu,
		"endpointCh": n.config.endpointCh,
		"eventCh":    n.config.eventCh,
		// "mwo":            n.config.mwo,
		// "mro":            n.config.mro,
		"noLoopbackAddr": n.config.noLoopbackAddr,
		"disableSACK":    n.config.disableSACK,
	})
}

func (n *vtunNode) applyConfigForm(values map[string]any) error {
	next := n.config
	if value, ok := formular.StringValue(values["name"]); ok {
		next.name = value
	}
	if value, ok := formular.StringValue(values["localAddrs"]); ok {
		next.localAddrs = value
	}
	if value, ok := formular.StringValue(values["dnsServers"]); ok {
		next.dnsServers = value
	}
	if value, ok := formular.IntValue(values["mtu"]); ok {
		next.mtu = value
	}
	if value, ok := formular.IntValue(values["endpointCh"]); ok {
		next.endpointCh = value
	}
	if value, ok := formular.IntValue(values["eventCh"]); ok {
		next.eventCh = value
	}
	// if value, ok := formular.IntValue(values["mwo"]); ok {
	// 	next.mwo = value
	// }
	// if value, ok := formular.IntValue(values["mro"]); ok {
	// 	next.mro = value
	// }
	if value, ok := formular.BoolValue(values["noLoopbackAddr"]); ok {
		next.noLoopbackAddr = value
	}
	if value, ok := formular.BoolValue(values["disableSACK"]); ok {
		next.disableSACK = value
	}
	next.normalize()
	if err := validateVTunConfig(next); err != nil {
		n.formError = err.Error()
		n.sendForcedMenuSnapshot()
		_ = n.updateLabel()
		return nil
	}
	n.formError = ""
	n.config = next
	n.sendForcedMenuSnapshot()
	if err := n.rebuildVTun(); err != nil {
		return nil
	}
	return nil
}

func validateVTunConfig(config vtunConfig) error {
	if _, err := parseAddrList(config.localAddrs, true); err != nil {
		return fmt.Errorf("local addrs: %w", err)
	}
	if _, err := parseAddrList(config.dnsServers, false); err != nil {
		return fmt.Errorf("DNS servers: %w", err)
	}
	if config.mtu <= 0 {
		return errors.New("MTU must be positive")
	}
	if config.endpointCh <= 0 {
		return errors.New("endpoint channel size must be positive")
	}
	if config.eventCh <= 0 {
		return errors.New("event channel size must be positive")
	}
	return nil
}

func (n *vtunNode) sendConfigFieldStatus(msg formular.FieldValidateMessage) {
	n.w.SendNodeMenuMsg(n.id, formular.FieldStatusFromError(pasta.NodeMenuID(n.id), 1, 1, msg.Field, validateVTunConfigField(msg)))
}

func validateVTunConfigField(msg formular.FieldValidateMessage) error {
	switch msg.Field.FieldID {
	case "name":
		return nil
	case "localAddrs":
		value, _ := formular.StringValue(msg.Value)
		if _, err := parseAddrList(value, true); err != nil {
			return fmt.Errorf("local addrs: %w", err)
		}
	case "dnsServers":
		value, _ := formular.StringValue(msg.Value)
		if _, err := parseAddrList(value, false); err != nil {
			return fmt.Errorf("DNS servers: %w", err)
		}
	case "mtu":
		value, ok := formular.IntValue(msg.Value)
		if !ok || value <= 0 {
			return errors.New("MTU must be positive")
		}
	case "endpointCh":
		value, ok := formular.IntValue(msg.Value)
		if !ok || value <= 0 {
			return errors.New("endpoint channel size must be positive")
		}
	case "eventCh":
		value, ok := formular.IntValue(msg.Value)
		if !ok || value <= 0 {
			return errors.New("event channel size must be positive")
		}
	default:
		return nil
	}
	return nil
}

func (n *vtunNode) ensureVTun() error {
	if n.vtun != nil || !n.hasAnyLink() {
		return nil
	}
	return n.rebuildVTun()
}

func (n *vtunNode) rebuildVTun() error {
	return n.rebuildVTunWithActive(n.hasAnyLink())
}

func (n *vtunNode) rebuildVTunActive() error {
	return n.rebuildVTunWithActive(true)
}

func (n *vtunNode) rebuildVTunWithActive(active bool) error {
	n.dropVTun()
	if !active {
		_ = n.updateLabel()
		return nil
	}
	opts, err := n.buildOpts()
	if err != nil {
		n.formError = err.Error()
		n.sendForcedMenuSnapshot()
		_ = n.updateLabel()
		return err
	}
	vt, err := opts.Build()
	if err != nil {
		n.formError = err.Error()
		n.sendForcedMenuSnapshot()
		_ = n.updateLabel()
		return err
	}
	n.formError = ""
	n.vtun = vt
	_ = n.w.AddNodeResource(n.id, vt)
	_ = n.updateLabel()
	n.sendEffectiveBlock()
	n.sendAll()
	return nil
}

func (n *vtunNode) buildOpts() (*netstackvtun.Opts, error) {
	localAddrs, err := n.effectiveLocalAddrs()
	if err != nil {
		return nil, err
	}
	dnsServers, err := parseAddrList(n.config.dnsServers, false)
	if err != nil {
		return nil, err
	}
	mtu := n.config.mtu
	if mtu <= 0 && n.tunConfig.MTU > 0 {
		mtu = n.tunConfig.MTU
	}
	if mtu <= 0 {
		mtu = defaultVTunMTU
	}
	return &netstackvtun.Opts{
		EndpointCh:     n.config.endpointCh,
		EventCh:        n.config.eventCh,
		LocalAddrs:     localAddrs,
		DnsServers:     dnsServers,
		Name:           n.config.name,
		NoLoopbackAddr: n.config.noLoopbackAddr,
		NetStackOpts: &netstackhelpers.Opts{
			MTU:         mtu,
			DisableSACK: n.config.disableSACK,
		},
		// MWO: n.config.mwo,
		// MRO: n.config.mro,
	}, nil
}

func (n *vtunNode) effectiveLocalAddrs() ([]netip.Addr, error) {
	if strings.TrimSpace(n.config.localAddrs) != "" {
		return parseAddrList(n.config.localAddrs, true)
	}
	if addrs := tunConfigAddrs(n.tunConfig); len(addrs) > 0 {
		return addrs, nil
	}
	if len(n.generatedAddrs) == 0 {
		n.generatedAddrs = n.allocLocalAddrs()
	}
	return append([]netip.Addr(nil), n.generatedAddrs...), nil
}

func (n *vtunNode) usesTunConfig() bool {
	return strings.TrimSpace(n.config.localAddrs) == "" || n.config.mtu <= 0
}

func (n *vtunNode) allocLocalAddrs() []netip.Addr {
	if n.engine == nil || n.engine.sys == nil || n.engine.AllocIP() == nil {
		return nil
	}
	alloc := n.engine.AllocIP()
	out := make([]netip.Addr, 0, 2)
	if ip, _ := alloc.AllocIP4(); ip != nil {
		if addr, ok := netip.AddrFromSlice(ip.To4()); ok {
			out = append(out, addr)
		}
	}
	if ip, _ := alloc.AllocIP6(); ip != nil {
		if addr, ok := netip.AddrFromSlice(ip.To16()); ok {
			out = append(out, addr)
		}
	}
	return out
}

func (n *vtunNode) freeGeneratedAddrs() {
	if len(n.generatedAddrs) == 0 || n.engine == nil || n.engine.sys == nil || n.engine.AllocIP() == nil {
		n.generatedAddrs = nil
		return
	}
	alloc := n.engine.AllocIP()
	for _, addr := range n.generatedAddrs {
		alloc.FreeIP(net.IP(addr.AsSlice()))
	}
	n.generatedAddrs = nil
}

func (n *vtunNode) dropVTun() {
	n.closeSentNetworks()
	n.closeSentTuns()
	if n.vtun != nil {
		_ = n.vtun.Close()
		n.vtun = nil
	}
	waitTuns(n.sentTunWait)
	n.sentTunWait = nil
}

func (n *vtunNode) sendAll() {
	if n.vtun == nil {
		return
	}
	if port, ok := n.w.PortSnapshot(n.network); ok {
		for _, link := range port.Links {
			n.sendNetworkToLink(link)
		}
	}
	if port, ok := n.w.PortSnapshot(n.tunp); ok {
		for _, link := range port.Links {
			n.sendTunToLink(link)
		}
	}
}

func (n *vtunNode) sendNetworkToLink(link uint64) {
	if n.vtun == nil {
		return
	}
	if n.sentNetworks == nil {
		n.sentNetworks = make(map[uint64]NetworkCloser)
	}
	if wrapper := n.sentNetworks[link]; NetworkIsReady(wrapper) {
		n.w.EmitEvent(n.id, link, NetworkPayload{Network: wrapper})
		return
	}
	n.closeSentNetwork(link)
	wrapper := gonnect.DetachNetwork(n.vtun, nil)
	n.sentNetworks[link] = wrapper
	BindNetworkResource(n.w, n.id, link, wrapper)
	n.w.EmitEvent(n.id, link, NetworkPayload{Network: wrapper})
}

func (n *vtunNode) sendTunToLink(link uint64) {
	if n.vtun == nil {
		return
	}
	if n.sentTuns != nil {
		if wrapper := n.sentTuns[link]; TunIsReady(wrapper) {
			n.w.EmitEvent(n.id, link, TunPayload{Tun: wrapper})
			return
		}
	}
	n.closeSentTun(link)
	wrapper := DetachTunForLink(n.w, n.id, link, n.vtun, enginePool(n.engine))
	if wrapper == nil {
		return
	}
	if n.sentTuns == nil {
		n.sentTuns = make(map[uint64]tun.Tun)
	}
	n.sentTuns[link] = wrapper
	n.w.EmitEvent(n.id, link, TunPayload{Tun: wrapper})
}

func (n *vtunNode) closeSentNetwork(link uint64) {
	if n.sentNetworks == nil {
		return
	}
	network := n.sentNetworks[link]
	delete(n.sentNetworks, link)
	if network != nil {
		_ = network.Close()
	}
}

func (n *vtunNode) closeSentNetworks() {
	for link, network := range n.sentNetworks {
		delete(n.sentNetworks, link)
		if network != nil {
			_ = network.Close()
		}
	}
}

func (n *vtunNode) closeSentTun(link uint64) {
	if n.sentTuns == nil {
		return
	}
	t := n.sentTuns[link]
	delete(n.sentTuns, link)
	closeTunForLaterWait(&n.sentTunWait, t)
}

func (n *vtunNode) closeSentTuns() {
	for link, t := range n.sentTuns {
		delete(n.sentTuns, link)
		closeTunForLaterWait(&n.sentTunWait, t)
	}
}

func (n *vtunNode) hasAnyLink() bool {
	return portHasLinks(n.w, n.network) || portHasLinks(n.w, n.tunp)
}

func (n *vtunNode) updateLabel() error {
	if n.formError != "" {
		return n.w.SetNodeLabel(n.id, "invalid config")
	}
	if n.vtun == nil {
		return n.w.SetNodeLabel(n.id, "idle")
	}
	return n.w.SetNodeLabel(n.id, n.config.name)
}

func (n *vtunNode) sendMenuSnapshot() {
	n.w.SendNodeMenuMsg(n.id, formular.MenuSnapshot(pasta.NodeMenuID(n.id), 1, n.menuBlocks()...))
}

func (n *vtunNode) sendForcedMenuSnapshot() {
	if n.w == nil || n.id == 0 {
		return
	}
	n.w.SendNodeMenuMsg(n.id, formular.ForcedMenuSnapshot(pasta.NodeMenuID(n.id), 1, n.menuBlocks()...))
}

func (n *vtunNode) menuBlocks() []formular.Block {
	return []formular.Block{n.effectiveBlock(), n.configBlock()}
}

func (n *vtunNode) sendEffectiveBlock() {
	if n.w == nil || n.id == 0 {
		return
	}
	n.w.SendNodeMenuMsg(n.id, formular.BlockSnapshot(pasta.NodeMenuID(n.id), 1, 1, n.effectiveBlock()))
}

func (n *vtunNode) effectiveBlock() formular.Block {
	localAddrs := n.effectiveLocalAddrsForMenu()
	dnsServers, _ := parseAddrList(n.config.dnsServers, false)
	mtu := n.config.mtu
	if mtu <= 0 && n.tunConfig.MTU > 0 {
		mtu = n.tunConfig.MTU
	}
	if mtu <= 0 {
		mtu = defaultVTunMTU
	}
	return formular.Block{ID: "effective", Order: 0, Generation: 1, Items: []formular.Item{
		readonlyTextField("name", "Name", n.config.name),
		readonlyStringListField("localAddrs", "Local addrs", netipAddrsText(localAddrs)),
		readonlyStringListField("dnsServers", "DNS servers", netipAddrsText(dnsServers)),
		readonlyIntField("mtu", "MTU", mtu),
		readonlyIntField("endpointCh", "Endpoint channel", n.config.endpointCh),
		readonlyIntField("eventCh", "Event channel", n.config.eventCh),
		readonlyBoolField("noLoopbackAddr", "No loopback addrs", n.config.noLoopbackAddr),
		readonlyBoolField("disableSACK", "Disable TCP SACK", n.config.disableSACK),
	}}
}

func (n *vtunNode) effectiveLocalAddrsForMenu() []netip.Addr {
	if strings.TrimSpace(n.config.localAddrs) != "" {
		addrs, _ := parseAddrList(n.config.localAddrs, true)
		return addrs
	}
	if addrs := tunConfigAddrs(n.tunConfig); len(addrs) > 0 {
		return addrs
	}
	return append([]netip.Addr(nil), n.generatedAddrs...)
}

func (n *vtunNode) configBlock() formular.Block {
	statusErr := n.formError != ""
	return formular.Block{ID: "config", Order: 10, Generation: 1, Form: true, Items: []formular.Item{
		configTextField("name", "Name", n.config.name, statusErr, n.formError, false),
		configTextField("localAddrs", "Local addrs", n.config.localAddrs, statusErr, n.formError, true),
		configTextField("dnsServers", "DNS servers", n.config.dnsServers, statusErr, n.formError, true),
		configIntField("mtu", "MTU", n.config.mtu, statusErr, n.formError),
		configIntField("endpointCh", "Endpoint channel", n.config.endpointCh, statusErr, n.formError),
		configIntField("eventCh", "Event channel", n.config.eventCh, statusErr, n.formError),
		// configIntField("mwo", "Max write offset", n.config.mwo, false, ""),
		// configIntField("mro", "Max read offset", n.config.mro, false, ""),
		configCheckbox("noLoopbackAddr", "No loopback addrs", n.config.noLoopbackAddr),
		configCheckbox("disableSACK", "Disable TCP SACK", n.config.disableSACK),
	}}
}

func tunConfigAddrs(cfg TunConfig) []netip.Addr {
	out := make([]netip.Addr, 0, len(cfg.Addrs))
	for _, value := range cfg.Addrs {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if prefix, err := netip.ParsePrefix(value); err == nil {
			out = append(out, prefix.Addr())
			continue
		}
		if addr, err := netip.ParseAddr(value); err == nil {
			out = append(out, addr)
		}
	}
	return out
}

func netipAddrsText(addrs []netip.Addr) []string {
	out := make([]string, 0, len(addrs))
	for _, addr := range addrs {
		if addr.IsValid() {
			out = append(out, addr.String())
		}
	}
	return out
}
