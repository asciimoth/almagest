package almagest

import (
	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/formular"
	"github.com/asciimoth/gonnect/sysnet"
	"github.com/asciimoth/pasta/pasta"
	"github.com/asciimoth/pasta/pasta/std"
)

func init() {
	regClass(func(e *engine) pasta.NodeClass {
		return defaultTunClass{engine: e}
	}, false)
}

const (
	// NodeTypeDefaultTun is the node class name for the system default TUN.
	NodeTypeDefaultTun = "almagest/DefaultTun"
)

type defaultTunClass struct {
	engine *engine
}

func (defaultTunClass) ClassName() string        { return NodeTypeDefaultTun }
func (defaultTunClass) ShortDescription() string { return "System default TUN" }
func (defaultTunClass) LongDescription() string {
	return "Builds one sysnet.DefaultTun and exposes it as a right-directed almagest/tun endpoint. The node is available only when the active sysnet.System reports DefaultTun support."
}
func (defaultTunClass) DefaultNodeParams() pasta.NodeClassParams {
	return pasta.NodeClassParams{Unique: true, PrimaryType: TypeTun, InitialPorts: []pasta.Port{
		TunPort("right", "Tun"),
	}}
}
func (c defaultTunClass) NewNode(cfg configer.Config, _ ...*pasta.NodeClassState) (pasta.Node, error) {
	config := defaultDefaultTunConfig()
	config.read(cfg)
	return &defaultTunNode{NodeBase: NodeBase{engine: c.engine}, config: config}, nil
}

// defaultTunNode owns the current sysnet.DefaultTun selected by the node.
type defaultTunNode struct {
	NodeBase

	tunp uint64

	config  defaultTunConfig
	portCfg TunConfigByLink

	dynDefaultTun bool
	effective     sysnet.DefaultTunOpts
	hasEffective  bool
	dtun          sysnet.DefaultTun
	outputTuns    TunOutputCache
	formError     string
	ruleDraftType map[string]string
}

func (n *defaultTunNode) OnInit(w *pasta.Workspace, logger pasta.Logger, id uint64, _ string, restored *pasta.NodeInitData, _, _, _, _ bool) error {
	n.InitBaseNode(w, logger, id)
	n.config.normalize()
	if n.engine != nil && n.engine.sys != nil {
		n.dynDefaultTun = n.engine.sys.Features().DynDefaultTun
	}
	if n.portCfg == nil {
		n.portCfg = TunConfigByLink{}
	}
	n.tunp = restoredRight(restored, 0)
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
	n.sendAll()
	return nil
}

func (n *defaultTunNode) OnStop() {
	n.outputTuns.CloseAll()
	if n.dtun != nil {
		_ = n.dtun.Close()
	}
	n.outputTuns.Wait()
	n.dtun = nil
	n.hasEffective = false
	n.portCfg = TunConfigByLink{}
}

func (n *defaultTunNode) PreLinkAdd(port uint64, linkType, portDirection string) error {
	if port != n.tunp || portDirection != "right" {
		return pasta.LinkTypeErr(linkType)
	}
	return TunPreLinkAdd(n.w, port, linkType)
}

func (n *defaultTunNode) OnLinkAdd(link, port uint64, _, _ string) error {
	if port != n.tunp {
		return nil
	}
	if err := n.configureDefaultTun(); err != nil {
		return nil
	}
	n.sendTunToLink(link)
	return nil
}

func (n *defaultTunNode) OnLinkRemoved(link uint64, port uint64, _, _ string) error {
	if port != n.tunp {
		return nil
	}
	n.outputTuns.Close(link)
	n.portCfg.Delete(link)
	if err := n.configureDefaultTun(); err != nil {
		return nil
	}
	return nil
}

func (n *defaultTunNode) OnEvent(event pasta.Event, linkType string, _ []string, receiverPortDirection string) error {
	if event.ReceiverPort != n.tunp || receiverPortDirection != "right" || linkType != TypeTun {
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
	n.portCfg.Set(event.Link, cfg)
	if err := n.configureDefaultTun(); err != nil {
		return nil
	}
	return nil
}

func (n *defaultTunNode) OnFormularMsg(message any) error {
	switch msg := message.(type) {
	case formular.FormApplyMessage:
		if msg.MenuID != pasta.NodeMenuID(n.id) || msg.BlockID != "config" {
			return nil
		}
		return n.applyConfigForm(msg.Values)
	case formular.FieldValidateMessage:
		if msg.MenuID != pasta.NodeMenuID(n.id) || msg.Field.BlockID != "config" {
			return nil
		}
		n.sendConfigFieldStatus(msg)
	case formular.AutocompleteRequestMessage:
		if msg.MenuID != pasta.NodeMenuID(n.id) || msg.Field.BlockID != "config" {
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
	return configer.SetMap(cfg, map[string]any{
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
