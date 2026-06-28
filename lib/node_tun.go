package almagest

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/formular"
	"github.com/asciimoth/gonnect/sysnet"
	"github.com/asciimoth/gonnect/tun"
	"github.com/asciimoth/pasta/pasta"
	"github.com/asciimoth/pasta/pasta/std"
)

func init() {
	regClass(func(e *engine) pasta.NodeClass {
		return tunClass{engine: e}
	}, false)
}

const (
	// NodeTypeTun is the node class name for a regular system TUN device.
	NodeTypeTun = "almagest/Tun"
)

type tunClass struct {
	engine *engine
}

func (tunClass) ClassName() string        { return NodeTypeTun }
func (tunClass) ShortDescription() string { return "System TUN" }
func (tunClass) LongDescription() string {
	return "Builds one regular sysnet Tun and exposes it as a right-directed almagest/tun endpoint. The node is available only when the active sysnet.System reports regular Tun support."
}
func (tunClass) DefaultNodeParams() pasta.NodeClassParams {
	return pasta.NodeClassParams{PrimaryType: TypeTun, InitialPorts: []pasta.Port{
		TunPort("right", "Tun"),
	}}
}
func (c tunClass) NewNode(cfg configer.Config, _ ...*pasta.NodeClassState) (pasta.Node, error) {
	config := defaultTunNodeConfig()
	config.read(cfg)
	return &tunNode{engine: c.engine, config: config}, nil
}

type tunNodeConfig struct {
	name      string
	tunAddrs  string
	tunRoutes string
	mtu       int
}

func defaultTunNodeConfig() tunNodeConfig {
	return tunNodeConfig{}
}

func (c *tunNodeConfig) read(cfg configer.Config) {
	if cfg == nil {
		return
	}
	c.name = readConfigString(cfg, "name", c.name)
	c.tunAddrs = readConfigString(cfg, "tunAddrs", c.tunAddrs)
	c.tunRoutes = readConfigString(cfg, "tunRoutes", c.tunRoutes)
	c.mtu = readConfigInt(cfg, "mtu", c.mtu)
	c.normalize()
}

func (c *tunNodeConfig) normalize() {
	c.name = strings.TrimSpace(c.name)
	c.tunAddrs = normalizeListText(c.tunAddrs)
	c.tunRoutes = normalizeListText(c.tunRoutes)
}

func (c tunNodeConfig) opts() sysnet.TunOpts {
	return sysnet.TunOpts{
		TunAddrs:  splitDefaultTunList(c.tunAddrs),
		TunRoutes: splitDefaultTunList(c.tunRoutes),
		MTU:       c.mtu,
	}
}

// tunNode owns one regular sysnet Tun and publishes detached wrappers for each
// connected TypeTun link. Downstream TunConfig suggestions can alter only the
// generic regular-TUN settings: interface addresses and MTU.
type tunNode struct {
	pasta.BasicNode

	engine *engine
	logger pasta.Logger

	w    *pasta.Workspace
	id   uint64
	tunp uint64

	config  tunNodeConfig
	portCfg TunConfigByLink

	dynTun        bool
	tunNames      bool
	effective     sysnet.TunOpts
	effectiveName string
	hasEffective  bool
	tun           tun.Tun
	sentTuns      map[uint64]tun.Tun
	sentTunWait   []tun.Tun
	formError     string
}

func (n *tunNode) OnInit(w *pasta.Workspace, logger pasta.Logger, id uint64, _ string, restored *pasta.NodeInitData, _, _, _, _ bool) error {
	n.w = w
	n.id = id
	n.logger = logger
	n.config.normalize()
	if n.engine != nil && n.engine.sys != nil {
		features := n.engine.sys.Features()
		n.dynTun = features.DynTun
		n.tunNames = features.TunNames
	}
	if n.portCfg == nil {
		n.portCfg = TunConfigByLink{}
	}
	if restored != nil && len(restored.RightPorts) > 0 {
		n.tunp = restored.RightPorts[0]
	}
	if err := n.w.SetNodePrimary(n.id, TypeTun); err != nil {
		return err
	}
	_ = n.updateLabel()
	n.sendMenuSnapshot()
	return nil
}

func (n *tunNode) OnReady() error {
	if err := n.configureTun(); err != nil {
		return nil
	}
	n.sendAll()
	return nil
}

func (n *tunNode) OnStop() {
	n.closeTun()
	n.hasEffective = false
	n.effectiveName = ""
	n.portCfg = TunConfigByLink{}
}

func (n *tunNode) PreLinkAdd(port uint64, linkType, portDirection string) error {
	if port != n.tunp || portDirection != "right" {
		return pasta.LinkTypeErr(linkType)
	}
	return TunPreLinkAdd(n.w, port, linkType)
}

func (n *tunNode) OnLinkAdd(link, port uint64, _, _ string) error {
	if port != n.tunp {
		return nil
	}
	if err := n.configureTun(); err != nil {
		return nil
	}
	n.sendTunToLink(link)
	return nil
}

func (n *tunNode) OnLinkRemoved(link uint64, port uint64, _, _ string) error {
	if port != n.tunp {
		return nil
	}
	n.closeSentTun(link)
	n.portCfg.Delete(link)
	if err := n.configureTun(); err != nil {
		return nil
	}
	return nil
}

func (n *tunNode) OnEvent(event pasta.Event, linkType string, _ []string, receiverPortDirection string) error {
	if event.ReceiverPort != n.tunp || receiverPortDirection != "right" || linkType != TypeTun {
		return nil
	}
	if std.IsRequest(event.Payload) {
		n.sendTunToLink(event.Link)
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
	if err := n.configureTun(); err != nil {
		return nil
	}
	return nil
}

func (n *tunNode) OnFormularMsg(message any) error {
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
	}
	return nil
}

func (n *tunNode) OnSave(cfg configer.Config) error {
	if err := pasta.DeleteNodeOwnedConfigKeys(cfg); err != nil {
		return err
	}
	values := map[string]any{
		"name":      n.config.name,
		"tunAddrs":  n.config.tunAddrs,
		"tunRoutes": n.config.tunRoutes,
		"mtu":       n.config.mtu,
	}
	for key, value := range values {
		if err := cfg.Set(configer.Path{key}, value); err != nil {
			return err
		}
	}
	return nil
}

func (n *tunNode) applyConfigForm(values map[string]any) error {
	next := n.config
	if value, ok := stringFromAny(values["name"]); ok {
		next.name = value
	}
	if value, ok := defaultTunStringListFromAny(values["tunAddrs"]); ok {
		next.tunAddrs = value
	}
	if value, ok := defaultTunStringListFromAny(values["tunRoutes"]); ok {
		next.tunRoutes = value
	}
	if value, ok := intFromAny(values["mtu"]); ok {
		next.mtu = value
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
	n.sendForcedMenuSnapshot()
	if err := n.configureTun(); err != nil {
		return nil
	}
	return nil
}

func (n *tunNode) sendConfigFieldStatus(msg formular.FieldValidateMessage) {
	status := formular.StatusOK
	statusText := ""
	if err := n.validateConfigField(msg); err != nil {
		status = formular.StatusError
		statusText = err.Error()
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

func (n *tunNode) validateConfigField(msg formular.FieldValidateMessage) error {
	next := n.config
	switch msg.Field.FieldID {
	case "name":
		if value, ok := stringFromAny(msg.Value); ok {
			next.name = value
		}
	case "mtu":
		if value, ok := intFromAny(msg.Value); ok {
			next.mtu = value
		}
	default:
		return nil
	}
	next.normalize()
	return n.validateConfig(next)
}

func (n *tunNode) validateConfig(config tunNodeConfig) error {
	if config.mtu < 0 {
		return errors.New("MTU must not be negative")
	}
	if err := n.verifyTunOpts(config.opts()); err != nil {
		return err
	}
	return n.verifyTunName(config.name)
}

func (n *tunNode) configureTun() error {
	opts := n.effectiveTunOpts()
	name := n.config.name
	if n.tun != nil && n.hasEffective && tunOptsEqual(opts, n.effective) && name == n.effectiveName {
		_ = n.updateLabel()
		return nil
	}
	if n.tun != nil && n.dynTun && n.hasEffective {
		if err := n.applyDynamicTunConfig(opts, name); err == nil {
			n.effective = copyTunOpts(opts)
			n.effectiveName = name
			n.hasEffective = true
			n.formError = ""
			_ = n.updateLabel()
			n.sendEffectiveBlock()
			return nil
		}
	}
	if err := n.rebuildTun(opts, name); err != nil {
		n.logConfigureTunError(err)
		return err
	}
	return nil
}

func (n *tunNode) logConfigureTunError(err error) {
	if err == nil || n.logger == nil {
		return
	}
	n.logger.Errf("configure TUN failed: %v", err)
}

func (n *tunNode) rebuildTun(opts sysnet.TunOpts, name string) error {
	if n.engine == nil || n.engine.sys == nil {
		return errors.New("no sysnet system")
	}
	if err := n.verifyTunOpts(opts); err != nil {
		n.formError = err.Error()
		n.sendForcedMenuSnapshot()
		_ = n.updateLabel()
		return err
	}
	if err := n.verifyTunName(name); err != nil {
		n.formError = err.Error()
		n.sendForcedMenuSnapshot()
		_ = n.updateLabel()
		return err
	}

	n.closeTun()
	nextTun, err := n.engine.sys.BuildTun(copyTunOpts(opts))
	if err != nil {
		n.formError = err.Error()
		n.sendForcedMenuSnapshot()
		_ = n.updateLabel()
		return err
	}
	if name != "" {
		if _, err := n.engine.sys.SetTunName(nextTun, name); err != nil {
			_ = nextTun.Close()
			n.formError = err.Error()
			n.sendForcedMenuSnapshot()
			_ = n.updateLabel()
			return err
		}
	}

	n.formError = ""
	n.tun = nextTun
	n.effective = copyTunOpts(opts)
	n.effectiveName = name
	n.hasEffective = true
	_ = n.w.AddNodeResource(n.id, nextTun)
	_ = n.updateLabel()
	n.sendEffectiveBlock()
	n.sendAll()
	return nil
}

func (n *tunNode) applyDynamicTunConfig(opts sysnet.TunOpts, name string) error {
	if n.engine == nil || n.engine.sys == nil || n.tun == nil {
		return errors.New("no tun")
	}
	if name != n.effectiveName {
		if err := n.verifyTunName(name); err != nil {
			return err
		}
		if name == "" {
			return sysnet.ErrNotSupported
		}
		if !n.tunNames {
			return sysnet.ErrNotSupported
		}
		if _, err := n.engine.sys.SetTunName(n.tun, name); err != nil {
			return err
		}
	}
	if !slices.Equal(n.effective.TunAddrs, opts.TunAddrs) {
		if err := n.engine.sys.SetTunAddrs(n.tun, slices.Clone(opts.TunAddrs)); err != nil {
			return err
		}
	}
	if !slices.Equal(n.effective.TunRoutes, opts.TunRoutes) {
		if err := n.engine.sys.SetTunRoutes(n.tun, slices.Clone(opts.TunRoutes)); err != nil {
			return err
		}
	}
	if n.effective.MTU != opts.MTU {
		if err := n.engine.sys.SetTunMTU(n.tun, opts.MTU); err != nil {
			return err
		}
	}
	return nil
}

func (n *tunNode) effectiveTunOpts() sysnet.TunOpts {
	opts := n.config.opts()
	portCfg := n.portCfg.Merge()
	opts.TunAddrs = mergeDefaultTunStringLists(opts.TunAddrs, portCfg.Addrs)
	opts.MTU = mergeDefaultTunMTU(opts.MTU, portCfg.MTU)
	return opts
}

func (n *tunNode) verifyTunOpts(opts sysnet.TunOpts) error {
	if n.engine == nil || n.engine.sys == nil {
		return errors.New("no sysnet system")
	}
	return n.engine.sys.VerifyTunOpts(copyTunOpts(opts))
}

func (n *tunNode) verifyTunName(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil
	}
	if n.engine == nil || n.engine.sys == nil {
		return errors.New("no sysnet system")
	}
	if !n.tunNames {
		return errors.New("custom TUN names are not supported")
	}
	valid, free := n.engine.sys.TunNameVerify(name)
	if !valid {
		return fmt.Errorf("invalid TUN name %q", name)
	}
	if !free && name != n.effectiveName {
		return fmt.Errorf("TUN name %q is already in use", name)
	}
	return nil
}

func (n *tunNode) sendAll() {
	if n.tun == nil {
		return
	}
	if port, ok := n.w.PortSnapshot(n.tunp); ok {
		for _, link := range port.Links {
			n.sendTunToLink(link)
		}
	}
}

func (n *tunNode) sendTunToLink(link uint64) {
	if n.tun == nil {
		return
	}
	if n.sentTuns != nil {
		if wrapper := n.sentTuns[link]; TunIsReady(wrapper) {
			n.w.EmitEvent(n.id, link, TunPayload{Tun: wrapper})
			return
		}
	}
	n.closeSentTun(link)
	wrapper := DetachTunForLink(n.w, n.id, link, n.tun, enginePool(n.engine))
	if wrapper == nil {
		return
	}
	if n.sentTuns == nil {
		n.sentTuns = make(map[uint64]tun.Tun)
	}
	n.sentTuns[link] = wrapper
	n.w.EmitEvent(n.id, link, TunPayload{Tun: wrapper})
}

func (n *tunNode) closeSentTun(link uint64) {
	if n.sentTuns == nil {
		return
	}
	t := n.sentTuns[link]
	delete(n.sentTuns, link)
	closeTunForLaterWait(&n.sentTunWait, t)
}

func (n *tunNode) closeSentTuns() {
	for link, t := range n.sentTuns {
		delete(n.sentTuns, link)
		closeTunForLaterWait(&n.sentTunWait, t)
	}
}

func (n *tunNode) closeTun() {
	n.closeSentTuns()
	if n.tun != nil {
		_ = n.tun.Close()
	}
	waitTuns(n.sentTunWait)
	n.sentTunWait = nil
	n.tun = nil
}

func (n *tunNode) updateLabel() error {
	if n.formError != "" {
		return n.w.SetNodeLabel(n.id, "invalid config")
	}
	if n.tun == nil {
		return n.w.SetNodeLabel(n.id, "idle")
	}
	if n.effectiveName != "" {
		return n.w.SetNodeLabel(n.id, n.effectiveName)
	}
	return n.w.SetNodeLabel(n.id, "ready")
}

func (n *tunNode) sendMenuSnapshot() {
	n.w.SendNodeMenuMsg(n.id, formular.MenuSnapshotMessage{
		MessageBase: formular.MessageBase{Type: formular.MessageMenuSnapshot, MenuID: pasta.NodeMenuID(n.id), MenuGeneration: 1},
		Blocks:      n.menuBlocks(),
	})
}

func (n *tunNode) sendForcedMenuSnapshot() {
	if n.w == nil || n.id == 0 {
		return
	}
	n.w.SendNodeMenuMsg(n.id, formular.MenuSnapshotMessage{
		MessageBase: formular.MessageBase{Type: formular.MessageMenuSnapshot, MenuID: pasta.NodeMenuID(n.id), MenuGeneration: 1},
		Force:       true,
		Blocks:      n.menuBlocks(),
	})
}

func (n *tunNode) menuBlocks() []formular.Block {
	return []formular.Block{n.effectiveBlock(), n.configBlock()}
}

func (n *tunNode) sendEffectiveBlock() {
	if n.w == nil || n.id == 0 {
		return
	}
	n.w.SendNodeMenuMsg(n.id, formular.BlockSnapshotMessage{
		MessageBase: formular.MessageBase{Type: formular.MessageBlockSnapshot, MenuID: pasta.NodeMenuID(n.id), MenuGeneration: 1, BlockGeneration: 1},
		Block:       n.effectiveBlock(),
	})
}

func (n *tunNode) effectiveBlock() formular.Block {
	opts := n.effectiveTunOpts()
	name := n.config.name
	if n.hasEffective {
		opts = copyTunOpts(n.effective)
		name = n.effectiveName
	}
	return formular.Block{ID: "effective", Order: 0, Generation: 1, Items: []formular.Item{
		readonlyTextField("name", "Name", name),
		readonlyStringListField("tunAddrs", "TUN addrs", opts.TunAddrs),
		readonlyStringListField("tunRoutes", "TUN routes", opts.TunRoutes),
		readonlyIntField("mtu", "MTU", opts.MTU),
	}}
}

func (n *tunNode) configBlock() formular.Block {
	statusErr := n.formError != ""
	return formular.Block{ID: "config", Order: 10, Generation: 1, Form: true, Items: []formular.Item{
		configTextField("name", "Name", n.config.name, statusErr, n.formError, false),
		defaultTunStringListField("tunAddrs", "TUN addrs", splitDefaultTunList(n.config.tunAddrs)),
		defaultTunStringListField("tunRoutes", "TUN routes", splitDefaultTunList(n.config.tunRoutes)),
		configIntField("mtu", "MTU", n.config.mtu, statusErr, n.formError),
	}}
}

func copyTunOpts(opts sysnet.TunOpts) sysnet.TunOpts {
	return sysnet.TunOpts{
		TunAddrs:  slices.Clone(opts.TunAddrs),
		TunRoutes: slices.Clone(opts.TunRoutes),
		MTU:       opts.MTU,
	}
}

func tunOptsEqual(a, b sysnet.TunOpts) bool {
	return slices.Equal(a.TunAddrs, b.TunAddrs) &&
		slices.Equal(a.TunRoutes, b.TunRoutes) &&
		a.MTU == b.MTU
}
