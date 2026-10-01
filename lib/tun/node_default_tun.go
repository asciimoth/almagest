package tunnodes

import (
	"github.com/asciimoth/almagest/lib/nodekit"
	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/formular"
	"github.com/asciimoth/gonnect/dns"
	"github.com/asciimoth/gonnect/sysnet"
	"github.com/asciimoth/pasta/pasta"
	"github.com/asciimoth/pasta/pasta/std"

	dnsnodes "github.com/asciimoth/almagest/lib/dns"
)

const (
	// NodeTypeDefaultTun is the node class name for the system default TUN.
	NodeTypeDefaultTun = "almagest/DefaultTun"
)

type DefaultTunClass struct {
	Runtime Runtime
}

func (DefaultTunClass) ClassName() string        { return NodeTypeDefaultTun }
func (DefaultTunClass) ShortDescription() string { return "System default TUN" }
func (DefaultTunClass) LongDescription() string {
	return "Builds one sysnet.DefaultTun, exposes it as a right-directed almagest/tun endpoint, and uses an optional right-directed almagest/dns input as the system DNS provider. With no DNS link, the sysnet DefaultTun DNS server is configured to drop DNS requests. The node is available only when the active sysnet.System reports DefaultTun support."
}
func (DefaultTunClass) DefaultNodeParams() pasta.NodeClassParams {
	return pasta.NodeClassParams{Unique: true, PrimaryType: TypeTun, InitialPorts: []pasta.Port{
		TunPort("right", "Tun"),
		dnsnodes.DNSPort("right", "DNS"),
	}}
}
func (c DefaultTunClass) NewNode(cfg configer.Config, _ ...*pasta.NodeClassState) (pasta.Node, error) {
	config := defaultDefaultTunConfig()
	config.read(cfg)
	return &defaultTunNode{NodeBase: NodeBase{Runtime: c.Runtime}, config: config}, nil
}

// defaultTunNode owns the current sysnet.DefaultTun selected by the node. DNS
// service is configured only through its almagest/dns port, so DNS addresses in
// TunConfig are discarded.
type defaultTunNode struct {
	NodeBase

	tunp uint64
	dnsp uint64

	dnsLink     uint64
	dnsProvider dns.Interface

	config  defaultTunConfig
	portCfg TunConfigByLink

	effective       sysnet.DefaultTunOpts
	hasEffective    bool
	dtun            sysnet.DefaultTun
	outputTuns      TunOutputCache
	warningPopupIDs []uint64
	formError       string
	runtimeError    string
	ruleDraftType   map[string]string
}

func (n *defaultTunNode) OnInit(w *pasta.Workspace, logger pasta.Logger, id uint64, _ string, restored *pasta.NodeInitData, _, _, _, _ bool) error {
	n.InitBaseNode(w, logger, id)
	n.config.normalize()
	if n.portCfg == nil {
		n.portCfg = TunConfigByLink{}
	}
	n.tunp = restoredRight(restored, 0)
	n.dnsp = restoredRight(restored, 1)
	if err := n.SetPrimary(TypeTun); err != nil {
		return err
	}
	_ = n.updateLabel()
	n.sendMenuSnapshot()
	return nil
}

func (n *defaultTunNode) OnReady() error {
	if err := n.configureDefaultTun(); err != nil {
		return nil
	}
	n.RequestPort(n.dnsp)
	n.sendAll()
	return nil
}

func (n *defaultTunNode) OnStop() {
	if n.dtun != nil {
		if err := n.dtun.SetDNS(nil); err != nil && n.Logger != nil {
			n.Logger.Errf("failed to stop DefaultTun DNS: %v", err)
		}
	}
	dnsnodes.CloseDNS(n.dnsProvider)
	n.dnsProvider = nil
	n.dnsLink = 0
	n.outputTuns.CloseAll()
	if n.dtun != nil {
		_ = n.dtun.Close()
	}
	n.outputTuns.Wait()
	n.dtun = nil
	n.hasEffective = false
	n.warningPopupIDs = nil
	n.portCfg = TunConfigByLink{}
}

func (n *defaultTunNode) PreLinkAdd(port uint64, linkType, portDirection string) error {
	switch port {
	case n.tunp:
		if portDirection != "right" {
			return pasta.LinkTypeErr(linkType)
		}
		return TunPreLinkAdd(n.W, port, linkType)
	case n.dnsp:
		if portDirection != "right" || linkType != dnsnodes.TypeDNS {
			return pasta.LinkTypeErr(linkType)
		}
		if n.PortHasLinks(port) {
			return pasta.ErrLinkDup
		}
		return nil
	default:
		return pasta.LinkTypeErr(linkType)
	}
}

func (n *defaultTunNode) OnLinkAdd(link, port uint64, linkType, portDirection string) error {
	switch {
	case port == n.tunp && portDirection == "right" && linkType == TypeTun:
		if n.Logger != nil {
			n.Logger.Debugf("DefaultTun TUN link added: link=%d", link)
		}
		if err := n.configureDefaultTun(); err != nil {
			return nil
		}
		n.sendTunToLink(link)
	case port == n.dnsp && portDirection == "right" && linkType == dnsnodes.TypeDNS:
		if n.Logger != nil {
			n.Logger.Debugf("DefaultTun DNS link added: link=%d", link)
		}
		n.dnsLink = link
		std.RequestLocked(n.W, n.ID, link)
	}
	return nil
}

func (n *defaultTunNode) OnLinkRemoved(link uint64, port uint64, linkType, portDirection string) error {
	switch {
	case port == n.tunp && portDirection == "right" && linkType == TypeTun:
		if n.Logger != nil {
			n.Logger.Debugf("DefaultTun TUN link removed: link=%d", link)
		}
		n.outputTuns.Close(link)
		n.portCfg.Delete(link)
		if err := n.configureDefaultTun(); err != nil {
			return nil
		}
	case port == n.dnsp && portDirection == "right" && linkType == dnsnodes.TypeDNS && link == n.dnsLink:
		if n.Logger != nil {
			n.Logger.Debugf("DefaultTun DNS link removed: link=%d", link)
		}
		old := n.dnsProvider
		n.dnsProvider = nil
		n.dnsLink = 0
		_ = n.applyDefaultTunDNS()
		dnsnodes.CloseDNS(old)
	}
	return nil
}

func (n *defaultTunNode) OnEvent(event pasta.Event, linkType string, _ []string, receiverPortDirection string) error {
	if event.ReceiverPort == n.tunp && receiverPortDirection == "right" && linkType == TypeTun {
		if !tunEventIsCurrent(n.W, event, n.tunp) {
			return nil
		}
		if std.IsRequest(event.Payload) {
			n.refreshTunToLink(event.Link)
			return nil
		}
		cfg, ok := event.Payload.(TunConfig)
		if !ok {
			return nil
		}
		if n.portCfg == nil {
			n.portCfg = TunConfigByLink{}
		}
		n.portCfg.Set(event.Link, cfg.WithoutDNS())
		if n.Logger != nil {
			n.Logger.Debugf(
				"DefaultTun config received: link=%d addrs=%v routes=%v source_routes=%v mtu=%d",
				event.Link,
				cfg.Addrs,
				cfg.Routes,
				cfg.SourceRoutes,
				cfg.MTU,
			)
		}
		if err := n.configureDefaultTun(); err != nil {
			return nil
		}
		return nil
	}
	if event.ReceiverPort == n.dnsp && receiverPortDirection == "right" && linkType == dnsnodes.TypeDNS {
		if event.Link != n.dnsLink {
			return nil
		}
		next, changed := dnsnodes.AcceptDNSPayload(n.W, n.ID, event, n.dnsProvider)
		if !changed {
			return nil
		}
		n.dnsProvider = next
		_ = n.applyDefaultTunDNS()
	}
	return nil
}

func (n *defaultTunNode) OnFormularMsg(message any) error {
	switch msg := message.(type) {
	case formular.FormApplyMessage:
		if msg.MenuID != pasta.NodeMenuID(n.ID) || msg.BlockID != "config" {
			return nil
		}
		return n.applyConfigForm(msg.Values)
	case formular.FieldValidateMessage:
		if msg.MenuID != pasta.NodeMenuID(n.ID) || msg.Field.BlockID != "config" {
			return nil
		}
		n.sendConfigFieldStatus(msg)
	case formular.AutocompleteRequestMessage:
		if msg.MenuID != pasta.NodeMenuID(n.ID) || msg.Field.BlockID != "config" {
			return nil
		}
		n.sendConfigAutocompleteHints(msg)
	}
	return nil
}

func (n *defaultTunNode) OnSave(cfg configer.Config) error {
	if err := pasta.DeleteNodeOwnedConfigKeys(cfg); err != nil {
		return err
	}
	return nodekit.SetConfigMap(cfg, map[string]any{
		"name":      n.config.name,
		"tunAddrs":  n.config.tunAddrs,
		"tunRoutes": n.config.tunRoutes,
		"mtu":       n.config.mtu,
		"dnsIP":     n.config.dnsIP,
		"strict":    n.config.strict,
		"exclude":   defaultTunRulesConfig(n.config.exclude),
		"include":   defaultTunRulesConfig(n.config.include),
	})
}

func (n *defaultTunNode) applyConfigForm(values map[string]any) error {
	next := n.config
	if value, ok := formular.StringValue(values["name"]); ok {
		next.name = value
	}
	if value, ok := defaultTunStringListFromAny(values["tunAddrs"]); ok {
		next.tunAddrs = value
	}
	if value, ok := defaultTunStringListFromAny(values["tunRoutes"]); ok {
		next.tunRoutes = value
	}
	if value, ok := formular.IntValue(values["mtu"]); ok {
		next.mtu = value
	}
	if value, ok := formular.StringValue(values["dnsIP"]); ok {
		next.dnsIP = value
	}
	if value, ok := formular.BoolValue(values["strict"]); ok {
		next.strict = value
	}
	if rules, ok := defaultTunRulesFromAny(values["exclude"]); ok {
		next.exclude = rules
	}
	if rules, ok := defaultTunRulesFromAny(values["include"]); ok {
		next.include = rules
	}
	next.normalize()
	if err := n.validateConfig(next); err != nil {
		// Keep rejected desired values in the form and saved config so an
		// unsupported platform-specific rule remains visible for repair. The
		// effective runtime stays unchanged until the complete desired config
		// passes validation.
		n.config = next
		n.ruleDraftType = nil
		n.formError = err.Error()
		n.sendForcedMenuSnapshot()
		_ = n.updateLabel()
		return nil
	}
	n.formError = ""
	n.config = next
	n.ruleDraftType = nil
	n.sendForcedMenuSnapshot()
	if err := n.configureDefaultTun(); err != nil {
		return nil
	}
	return nil
}
