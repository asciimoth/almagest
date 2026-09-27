package tunnodes

import (
	"errors"
	"slices"
	"strings"

	"github.com/asciimoth/almagest/lib/nodekit"
	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/formular"
	"github.com/asciimoth/gonnect/sysnet"
	"github.com/asciimoth/gonnect/tun"
	"github.com/asciimoth/pasta/pasta"
	"github.com/asciimoth/pasta/pasta/std"
)

const (
	// NodeTypeTun is the node class name for a regular system TUN device.
	NodeTypeTun = "almagest/Tun"
)

type TunClass struct {
	Runtime Runtime
}

func (TunClass) ClassName() string        { return NodeTypeTun }
func (TunClass) ShortDescription() string { return "System TUN" }
func (TunClass) LongDescription() string {
	return "Builds one regular sysnet Tun and exposes it as a right-directed almagest/tun endpoint. The node is available only when the active sysnet.System reports regular Tun support."
}
func (TunClass) DefaultNodeParams() pasta.NodeClassParams {
	return pasta.NodeClassParams{PrimaryType: TypeTun, InitialPorts: []pasta.Port{
		TunPort("right", "Tun"),
	}}
}
func (c TunClass) NewNode(cfg configer.Config, _ ...*pasta.NodeClassState) (pasta.Node, error) {
	config := defaultTunNodeConfig()
	config.read(cfg)
	return &tunNode{NodeBase: NodeBase{Runtime: c.Runtime}, config: config}, nil
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
	c.name = configer.String(cfg, configer.Path{"name"}, c.name)
	c.tunAddrs = configer.String(cfg, configer.Path{"tunAddrs"}, c.tunAddrs)
	c.tunRoutes = configer.String(cfg, configer.Path{"tunRoutes"}, c.tunRoutes)
	c.mtu = configer.Int(cfg, configer.Path{"mtu"}, c.mtu)
	c.normalize()
}

func (c *tunNodeConfig) normalize() {
	c.name = strings.TrimSpace(c.name)
	c.tunAddrs = normalizeListText(c.tunAddrs)
	c.tunRoutes = normalizeListText(c.tunRoutes)
}

func (c tunNodeConfig) opts() sysnet.TunOpts {
	return sysnet.TunOpts{
		Name:      c.name,
		TunAddrs:  splitDefaultTunList(c.tunAddrs),
		TunRoutes: splitDefaultTunList(c.tunRoutes),
		MTU:       c.mtu,
	}
}

// tunNode owns one regular sysnet Tun and publishes detached wrappers for each
// connected TypeTun link. Downstream TunConfig suggestions can alter only the
// generic regular-TUN settings: interface addresses, routes, and MTU. The node
// discards DNS suggestions because system TUN DNS configuration belongs to DNS
// links and DefaultTun integration.
type tunNode struct {
	NodeBase

	tunp uint64

	config  tunNodeConfig
	portCfg TunConfigByLink

	effective    sysnet.TunOpts
	hasEffective bool
	tun          tun.Tun
	outputTuns   TunOutputCache
	formError    string
}

func (n *tunNode) OnInit(w *pasta.Workspace, logger pasta.Logger, id uint64, _ string, restored *pasta.NodeInitData, _, _, _, _ bool) error {
	n.InitBaseNode(w, logger, id)
	n.config.normalize()
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

func (n *tunNode) OnReady() error {
	if err := n.configureTun(); err != nil {
		return nil
	}
	n.sendAll()
	return nil
}

func (n *tunNode) OnStop() {
	n.closeTun(false)
	n.hasEffective = false
	n.portCfg = TunConfigByLink{}
}

func (n *tunNode) PreLinkAdd(port uint64, linkType, portDirection string) error {
	if port != n.tunp || portDirection != "right" {
		return pasta.LinkTypeErr(linkType)
	}
	return TunPreLinkAdd(n.W, port, linkType)
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
	n.outputTuns.Close(link)
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
	if err := n.configureTun(); err != nil {
		return nil
	}
	return nil
}

func (n *tunNode) OnFormularMsg(message any) error {
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
	}
	return nil
}

func (n *tunNode) OnSave(cfg configer.Config) error {
	if err := pasta.DeleteNodeOwnedConfigKeys(cfg); err != nil {
		return err
	}
	return nodekit.SetConfigMap(cfg, map[string]any{
		"name":      n.config.name,
		"tunAddrs":  n.config.tunAddrs,
		"tunRoutes": n.config.tunRoutes,
		"mtu":       n.config.mtu,
	})
}

func (n *tunNode) applyConfigForm(values map[string]any) error {
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
	sendNodeFieldStatus(n.W, n.ID, msg.Field, n.validateConfigField(msg))
}

func (n *tunNode) validateConfigField(msg formular.FieldValidateMessage) error {
	next := n.config
	switch msg.Field.FieldID {
	case "name":
		if value, ok := formular.StringValue(msg.Value); ok {
			next.name = value
		}
	case "mtu":
		if value, ok := formular.IntValue(msg.Value); ok {
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
	return n.verifyTunOpts(config.opts())
}

func (n *tunNode) configureTun() error {
	opts := n.effectiveTunOpts()
	if n.tun != nil && n.hasEffective && tunOptsEqual(opts, n.effective) {
		_ = n.updateLabel()
		return nil
	}
	if n.tun != nil && n.hasEffective && n.canApplyDynamicTunConfig(opts) {
		if err := n.applyDynamicTunConfig(opts); err == nil {
			n.effective = copyTunOpts(opts)
			n.hasEffective = true
			n.formError = ""
			_ = n.updateLabel()
			n.sendEffectiveBlock()
			return nil
		}
	}
	if err := n.rebuildTun(opts); err != nil {
		n.logConfigureTunError(err)
		return err
	}
	return nil
}

func (n *tunNode) logConfigureTunError(err error) {
	if err == nil || n.Logger == nil {
		return
	}
	n.Logger.Errf("configure TUN failed: %v", err)
}

func (n *tunNode) rebuildTun(opts sysnet.TunOpts) error {
	if n.System() == nil {
		return errors.New("no sysnet system")
	}
	if err := n.verifyTunOpts(opts); err != nil {
		n.formError = err.Error()
		n.sendForcedMenuSnapshot()
		_ = n.updateLabel()
		return err
	}
	n.closeTun(true)
	nextTun, err := n.System().BuildTun(copyTunOpts(opts))
	if err != nil {
		n.formError = err.Error()
		n.sendForcedMenuSnapshot()
		_ = n.updateLabel()
		return err
	}
	n.formError = ""
	n.tun = nextTun
	n.effective = copyTunOpts(opts)
	n.hasEffective = true
	_ = n.W.AddNodeResourceLocked(n.ID, nextTun)
	_ = n.updateLabel()
	n.sendEffectiveBlock()
	n.sendAll()
	return nil
}

func (n *tunNode) canApplyDynamicTunConfig(opts sysnet.TunOpts) bool {
	if n.System() == nil || n.tun == nil {
		return false
	}
	fallbackFamily := tunOptsFamily(n.effective)
	if opts.Name != n.effective.Name {
		if opts.Name == "" || !tunOperationAllowsAttempt(
			n.System(), n.tun, sysnet.TargetTun, sysnet.OpRename, sysnet.FamilyNone,
		) {
			return false
		}
	}
	if !slices.Equal(n.effective.TunAddrs, opts.TunAddrs) {
		family := optionAddressFamily(opts.TunAddrs, nil, fallbackFamily)
		if !tunOperationAllowsAttempt(n.System(), n.tun, sysnet.TargetTun, sysnet.OpSetAddresses, family) {
			return false
		}
	}
	if !slices.Equal(n.effective.TunRoutes, opts.TunRoutes) {
		family := optionAddressFamily(nil, opts.TunRoutes, fallbackFamily)
		if !tunOperationAllowsAttempt(n.System(), n.tun, sysnet.TargetTun, sysnet.OpSetRoutes, family) {
			return false
		}
	}
	return n.effective.MTU == opts.MTU || tunOperationAllowsAttempt(
		n.System(), n.tun, sysnet.TargetTun, sysnet.OpSetMTU, sysnet.FamilyNone,
	)
}

func (n *tunNode) applyDynamicTunConfig(opts sysnet.TunOpts) error {
	if n.System() == nil || n.tun == nil {
		return errors.New("no tun")
	}
	if opts.Name != n.effective.Name {
		if err := n.System().SetTunName(n.tun, opts.Name); err != nil {
			return err
		}
	}
	if !slices.Equal(n.effective.TunAddrs, opts.TunAddrs) {
		if err := n.System().SetTunAddrs(n.tun, slices.Clone(opts.TunAddrs)); err != nil {
			return err
		}
	}
	if !slices.Equal(n.effective.TunRoutes, opts.TunRoutes) {
		if err := n.System().SetTunRoutes(n.tun, slices.Clone(opts.TunRoutes)); err != nil {
			return err
		}
	}
	if n.effective.MTU != opts.MTU {
		if err := n.System().SetTunMTU(n.tun, opts.MTU); err != nil {
			return err
		}
	}
	return nil
}

func (n *tunNode) effectiveTunOpts() sysnet.TunOpts {
	opts := n.config.opts()
	portCfg := n.portCfg.Merge()
	opts.TunAddrs = mergeDefaultTunStringLists(opts.TunAddrs, portCfg.Addrs)
	opts.TunRoutes = mergeDefaultTunStringLists(opts.TunRoutes, portCfg.Routes)
	opts.MTU = mergeDefaultTunMTU(opts.MTU, portCfg.MTU)
	return opts
}

func (n *tunNode) verifyTunOpts(opts sysnet.TunOpts) error {
	if n.System() == nil {
		return errors.New("no sysnet system")
	}
	return validationErrorForAttempt(n.System().CheckTunOpts(copyTunOpts(opts)))
}

func (n *tunNode) sendAll() {
	if n.tun == nil {
		return
	}
	n.ForEachPortLink(n.tunp, n.sendTunToLink)
}

func (n *tunNode) sendTunToLink(link uint64) {
	if n.tun == nil {
		return
	}
	n.outputTuns.SendLocked(n.W, n.ID, link, n.tun, enginePool(n.Runtime))
}

func (n *tunNode) refreshTunToLink(link uint64) {
	if n.tun == nil {
		return
	}
	n.outputTuns.RefreshLocked(n.W, n.ID, link, n.tun, enginePool(n.Runtime))
}

func (n *tunNode) closeTun(notify bool) {
	if notify {
		n.outputTuns.InvalidateAllLocked(n.W, n.ID)
	} else {
		n.outputTuns.CloseAll()
	}
	if n.tun != nil {
		_ = n.tun.Close()
	}
	n.outputTuns.Wait()
	n.tun = nil
}

func (n *tunNode) updateLabel() error {
	if n.formError != "" {
		return n.SetLabel("invalid config")
	}
	if n.tun == nil {
		return n.SetLabel("idle")
	}
	if n.effective.Name != "" {
		return n.SetLabel(n.effective.Name)
	}
	return n.SetLabel("ready")
}

func (n *tunNode) sendMenuSnapshot() {
	sendNodeMenuSnapshot(n.W, n.ID, false, n.menuBlocks()...)
}

func (n *tunNode) sendForcedMenuSnapshot() {
	sendNodeMenuSnapshot(n.W, n.ID, true, n.menuBlocks()...)
}

func (n *tunNode) menuBlocks() []formular.Block {
	return []formular.Block{n.effectiveBlock(), n.configBlock()}
}

func (n *tunNode) sendEffectiveBlock() {
	sendNodeBlockSnapshot(n.W, n.ID, n.effectiveBlock())
}

func (n *tunNode) effectiveBlock() formular.Block {
	opts := n.effectiveTunOpts()
	if n.hasEffective {
		opts = copyTunOpts(n.effective)
	}
	return formular.Block{ID: "effective", Order: 0, Generation: 1, Items: []formular.Item{
		readonlyTextField("name", "Name", opts.Name),
		readonlyStringListField("tunAddrs", "TUN addrs", opts.TunAddrs),
		readonlyStringListField("tunRoutes", "TUN routes", opts.TunRoutes),
		readonlyIntField("mtu", "MTU", opts.MTU),
	}}
}

func (n *tunNode) configBlock() formular.Block {
	statusErr := n.formError != ""
	name := configTextField("name", "Name", n.config.name, statusErr, n.formError, false)
	if n.System() != nil {
		opts := n.effectiveTunOpts()
		name = applyCapabilityToMenuItem(name, systemOperation(
			n.System().Capabilities(), sysnet.TargetTun, sysnet.OpCreateNamed, tunOptsFamily(opts),
		))
	}
	return formular.Block{ID: "config", Order: 10, Generation: 1, Form: true, Items: []formular.Item{
		name,
		defaultTunStringListField("tunAddrs", "TUN addrs", splitDefaultTunList(n.config.tunAddrs)),
		defaultTunStringListField("tunRoutes", "TUN routes", splitDefaultTunList(n.config.tunRoutes)),
		configIntField("mtu", "MTU", n.config.mtu, statusErr, n.formError),
	}}
}

func copyTunOpts(opts sysnet.TunOpts) sysnet.TunOpts {
	return sysnet.TunOpts{
		Name:      opts.Name,
		TunAddrs:  slices.Clone(opts.TunAddrs),
		TunRoutes: slices.Clone(opts.TunRoutes),
		MTU:       opts.MTU,
	}
}

func tunOptsEqual(a, b sysnet.TunOpts) bool {
	return a.Name == b.Name &&
		slices.Equal(a.TunAddrs, b.TunAddrs) &&
		slices.Equal(a.TunRoutes, b.TunRoutes) &&
		a.MTU == b.MTU
}
