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
	return &defaultTunNode{engine: c.engine, config: config}, nil
}

type defaultTunConfig struct {
	tunAddrs  string
	tunRoutes string
	mtu       int
	dnsIP     string
	strict    bool
	exclude   []sysnet.Rule
	include   []sysnet.Rule
}

func defaultDefaultTunConfig() defaultTunConfig {
	return defaultTunConfig{}
}

func (c *defaultTunConfig) read(cfg configer.Config) {
	if cfg == nil {
		return
	}
	c.tunAddrs = readConfigString(cfg, "tunAddrs", c.tunAddrs)
	c.tunRoutes = readConfigString(cfg, "tunRoutes", c.tunRoutes)
	c.mtu = readConfigInt(cfg, "mtu", c.mtu)
	c.dnsIP = readConfigString(cfg, "dnsIP", c.dnsIP)
	c.strict = readConfigBool(cfg, "strict", c.strict)
	c.exclude = readDefaultTunRulesConfig(cfg, "exclude")
	c.include = readDefaultTunRulesConfig(cfg, "include")
	c.normalize()
}

func (c *defaultTunConfig) normalize() {
	c.tunAddrs = normalizeListText(c.tunAddrs)
	c.tunRoutes = normalizeListText(c.tunRoutes)
	c.dnsIP = strings.TrimSpace(c.dnsIP)
	c.exclude = normalizeDefaultTunRules(c.exclude)
	c.include = normalizeDefaultTunRules(c.include)
}

// defaultTunNode owns the current sysnet.DefaultTun selected by the node.
type defaultTunNode struct {
	pasta.BasicNode

	engine *engine
	logger pasta.Logger

	w    *pasta.Workspace
	id   uint64
	tunp uint64

	config  defaultTunConfig
	portCfg TunConfig

	dynDefaultTun bool
	effective     sysnet.DefaultTunOpts
	hasEffective  bool
	dtun          sysnet.DefaultTun
	sentTuns      map[uint64]tun.Tun
	formError     string
}

func (n *defaultTunNode) OnInit(w *pasta.Workspace, logger pasta.Logger, id uint64, _ string, restored *pasta.NodeInitData, _, _, _, _ bool) error {
	n.w = w
	n.id = id
	n.logger = logger
	n.config.normalize()
	if n.engine != nil && n.engine.sys != nil {
		n.dynDefaultTun = n.engine.sys.Features().DynDefaultTun
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

func (n *defaultTunNode) OnReady() error {
	if err := n.configureDefaultTun(); err != nil {
		return nil
	}
	n.sendAll()
	return nil
}

func (n *defaultTunNode) OnStop() {
	n.closeSentTuns()
	if n.dtun != nil {
		_ = n.dtun.Close()
	}
	n.dtun = nil
	n.hasEffective = false
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
	n.closeSentTun(link)
	n.portCfg = TunConfig{}
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
		n.sendTunToLink(event.Link)
		return nil
	}
	cfg, ok := event.Payload.(TunConfig)
	if !ok {
		return nil
	}
	n.portCfg = cfg.Clone()
	if err := n.configureDefaultTun(); err != nil {
		return nil
	}
	return nil
}

func (n *defaultTunNode) OnFormularMsg(message any) error {
	msg, ok := message.(formular.FormApplyMessage)
	if !ok || msg.MenuID != pasta.NodeMenuID(n.id) || msg.BlockID != "config" {
		return nil
	}
	return n.applyConfigForm(msg.Values)
}

func (n *defaultTunNode) OnSave(cfg configer.Config) error {
	if err := pasta.DeleteNodeOwnedConfigKeys(cfg); err != nil {
		return err
	}
	values := map[string]any{
		"tunAddrs":  n.config.tunAddrs,
		"tunRoutes": n.config.tunRoutes,
		"mtu":       n.config.mtu,
		"dnsIP":     n.config.dnsIP,
		"strict":    n.config.strict,
		"exclude":   defaultTunRulesConfig(n.config.exclude),
		"include":   defaultTunRulesConfig(n.config.include),
	}
	for key, value := range values {
		if err := cfg.Set(configer.Path{key}, value); err != nil {
			return err
		}
	}
	return nil
}

func (n *defaultTunNode) applyConfigForm(values map[string]any) error {
	next := n.config
	if value, ok := stringFromAny(values["tunAddrs"]); ok {
		next.tunAddrs = value
	}
	if value, ok := stringFromAny(values["tunRoutes"]); ok {
		next.tunRoutes = value
	}
	if value, ok := intFromAny(values["mtu"]); ok {
		next.mtu = value
	}
	if value, ok := stringFromAny(values["dnsIP"]); ok {
		next.dnsIP = value
	}
	if value, ok := boolFromAny(values["strict"]); ok {
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
	n.sendForcedMenuSnapshot()
	if err := n.configureDefaultTun(); err != nil {
		return nil
	}
	return nil
}

func (n *defaultTunNode) validateConfig(config defaultTunConfig) error {
	if config.mtu < 0 {
		return errors.New("MTU must not be negative")
	}
	if len(config.exclude) > 0 && len(config.include) > 0 {
		return errors.New("exclude and include rules are mutually exclusive")
	}
	opts := config.defaultTunOpts()
	return n.verifyDefaultTunOpts(opts)
}

func (n *defaultTunNode) configureDefaultTun() error {
	opts := n.effectiveDefaultTunOpts()
	if n.dtun != nil && n.hasEffective && defaultTunOptsEqual(opts, n.effective) {
		_ = n.updateLabel()
		return nil
	}
	if n.dtun != nil && n.dynDefaultTun && n.hasEffective && defaultTunOptsDynamicallyCompatible(n.effective, opts) {
		if err := n.applyDynamicDefaultTunOpts(opts); err == nil {
			n.effective = opts.Copy()
			n.hasEffective = true
			n.formError = ""
			_ = n.updateLabel()
			return nil
		}
	}
	if err := n.rebuildDefaultTun(opts); err != nil {
		n.logConfigureDefaultTunError(err)
		return err
	}
	return nil
}

func (n *defaultTunNode) logConfigureDefaultTunError(err error) {
	if err == nil || n.logger == nil {
		return
	}
	n.logger.Errf("configure default TUN failed: %v", err)
}

func (n *defaultTunNode) rebuildDefaultTun(opts sysnet.DefaultTunOpts) error {
	if n.engine == nil || n.engine.sys == nil {
		return errors.New("no sysnet system")
	}
	if err := n.verifyDefaultTunOpts(opts); err != nil {
		n.formError = err.Error()
		n.sendForcedMenuSnapshot()
		_ = n.updateLabel()
		return err
	}
	previous := n.dtun
	dtun, err := n.engine.sys.BuildDefaultTun(opts.Copy())
	if err != nil {
		n.formError = err.Error()
		n.sendForcedMenuSnapshot()
		_ = n.updateLabel()
		return err
	}
	if outDNS := n.engine.sys.OutDNS(); outDNS != nil {
		dtun.SetDns(outDNS)
	}
	n.formError = ""
	n.dtun = dtun
	n.effective = opts.Copy()
	n.hasEffective = true
	_ = n.w.AddNodeResource(n.id, dtun)
	_ = n.updateLabel()
	n.sendAll()
	if previous != nil {
		_ = previous.Close()
	}
	return nil
}

func (n *defaultTunNode) applyDynamicDefaultTunOpts(opts sysnet.DefaultTunOpts) error {
	if n.engine == nil || n.engine.sys == nil || n.dtun == nil {
		return errors.New("no default tun")
	}
	if !slices.Equal(n.effective.TunAddrs, opts.TunAddrs) {
		if err := n.engine.sys.SetTunAddrs(n.dtun, slices.Clone(opts.TunAddrs)); err != nil {
			return err
		}
	}
	if n.effective.MTU != opts.MTU {
		if err := n.engine.sys.SetTunMTU(n.dtun, opts.MTU); err != nil {
			return err
		}
	}
	return nil
}

func (n *defaultTunNode) effectiveDefaultTunOpts() sysnet.DefaultTunOpts {
	opts := n.config.defaultTunOpts()
	opts.TunAddrs = mergeDefaultTunStringLists(opts.TunAddrs, n.portCfg.Addrs)
	opts.MTU = mergeDefaultTunMTU(opts.MTU, n.portCfg.MTU)
	return opts
}

func (c defaultTunConfig) defaultTunOpts() sysnet.DefaultTunOpts {
	return sysnet.DefaultTunOpts{
		TunAddrs:  splitDefaultTunList(c.tunAddrs),
		TunRoutes: splitDefaultTunList(c.tunRoutes),
		MTU:       c.mtu,
		DnsIP:     c.dnsIP,
		Strict:    c.strict,
		Exclude:   slices.Clone(c.exclude),
		Include:   slices.Clone(c.include),
	}
}

func (n *defaultTunNode) verifyDefaultTunOpts(opts sysnet.DefaultTunOpts) error {
	if n.engine == nil || n.engine.sys == nil {
		return errors.New("no sysnet system")
	}
	return n.engine.sys.VerifyDefaultTunOpts(opts.Copy())
}

func (n *defaultTunNode) sendAll() {
	if n.dtun == nil {
		return
	}
	if port, ok := n.w.PortSnapshot(n.tunp); ok {
		for _, link := range port.Links {
			n.sendTunToLink(link)
		}
	}
}

func (n *defaultTunNode) sendTunToLink(link uint64) {
	if n.dtun == nil {
		return
	}
	n.closeSentTun(link)
	wrapper := DetachTunForLink(n.w, n.id, link, n.dtun)
	if wrapper == nil {
		return
	}
	if n.sentTuns == nil {
		n.sentTuns = make(map[uint64]tun.Tun)
	}
	n.sentTuns[link] = wrapper
	n.w.EmitEvent(n.id, link, TunPayload{Tun: wrapper})
}

func (n *defaultTunNode) closeSentTun(link uint64) {
	if n.sentTuns == nil {
		return
	}
	t := n.sentTuns[link]
	delete(n.sentTuns, link)
	if t != nil {
		_ = t.Close()
	}
}

func (n *defaultTunNode) closeSentTuns() {
	for link, t := range n.sentTuns {
		delete(n.sentTuns, link)
		if t != nil {
			_ = t.Close()
		}
	}
}

func (n *defaultTunNode) updateLabel() error {
	if n.formError != "" {
		return n.w.SetNodeLabel(n.id, "invalid config")
	}
	if n.dtun == nil {
		return n.w.SetNodeLabel(n.id, "idle")
	}
	return n.w.SetNodeLabel(n.id, "ready")
}

func (n *defaultTunNode) sendMenuSnapshot() {
	n.w.SendNodeMenuMsg(n.id, formular.MenuSnapshotMessage{
		MessageBase: formular.MessageBase{Type: formular.MessageMenuSnapshot, MenuID: pasta.NodeMenuID(n.id), MenuGeneration: 1},
		Blocks:      []formular.Block{n.configBlock()},
	})
}

func (n *defaultTunNode) sendForcedMenuSnapshot() {
	if n.w == nil || n.id == 0 {
		return
	}
	n.w.SendNodeMenuMsg(n.id, formular.MenuSnapshotMessage{
		MessageBase: formular.MessageBase{Type: formular.MessageMenuSnapshot, MenuID: pasta.NodeMenuID(n.id), MenuGeneration: 1},
		Force:       true,
		Blocks:      []formular.Block{n.configBlock()},
	})
}

func (n *defaultTunNode) configBlock() formular.Block {
	statusErr := n.formError != ""
	return formular.Block{ID: "config", Order: 10, Generation: 1, Form: true, Items: []formular.Item{
		configTextField("tunAddrs", "TUN addrs", n.config.tunAddrs, statusErr, n.formError, true),
		configTextField("tunRoutes", "TUN routes", n.config.tunRoutes, statusErr, n.formError, true),
		configIntField("mtu", "MTU", n.config.mtu, statusErr, n.formError),
		configTextField("dnsIP", "DNS IP", n.config.dnsIP, statusErr, n.formError, false),
		configCheckbox("strict", "Strict routing", n.config.strict),
		defaultTunRulesField("exclude", "Exclude", n.config.exclude),
		defaultTunRulesField("include", "Include", n.config.include),
	}}
}

func splitDefaultTunList(value string) []string {
	fields := strings.FieldsFunc(value, func(r rune) bool {
		return r == ',' || r == ';' || r == '\n' || r == '\r' || r == '\t' || r == ' '
	})
	out := make([]string, 0, len(fields))
	for _, field := range fields {
		field = strings.TrimSpace(field)
		if field != "" {
			out = append(out, field)
		}
	}
	return out
}

func mergeDefaultTunStringLists(a, b []string) []string {
	out := make([]string, 0, len(a)+len(b))
	seen := make(map[string]struct{}, len(a)+len(b))
	for _, values := range [][]string{a, b} {
		for _, value := range values {
			value = strings.TrimSpace(value)
			if value == "" {
				continue
			}
			if _, ok := seen[value]; ok {
				continue
			}
			seen[value] = struct{}{}
			out = append(out, value)
		}
	}
	return out
}

func mergeDefaultTunMTU(a, b int) int {
	if a <= 0 {
		return b
	}
	if b <= 0 {
		return a
	}
	return min(a, b)
}

func defaultTunOptsDynamicallyCompatible(a, b sysnet.DefaultTunOpts) bool {
	return slices.Equal(a.TunRoutes, b.TunRoutes) &&
		a.DnsIP == b.DnsIP &&
		a.Strict == b.Strict &&
		slices.Equal(a.Exclude, b.Exclude) &&
		slices.Equal(a.Include, b.Include)
}

func defaultTunOptsEqual(a, b sysnet.DefaultTunOpts) bool {
	return defaultTunOptsDynamicallyCompatible(a, b) &&
		slices.Equal(a.TunAddrs, b.TunAddrs) &&
		a.MTU == b.MTU
}

func defaultTunRulesField(id, label string, rules []sysnet.Rule) formular.Item {
	return formular.Item{Type: formular.ItemField, ID: id, Label: label, Field: &formular.Field{
		Kind:      formular.FieldArray,
		Templates: []formular.ArrayTemplate{defaultTunRuleTemplate()},
		Elements:  defaultTunRuleElements(rules),
	}}
}

func defaultTunRuleTemplate() formular.ArrayTemplate {
	return formular.ArrayTemplate{
		Name:  "rule",
		Label: "Rule",
		Items: []formular.Item{
			{Type: formular.ItemField, ID: "type", Label: "Type", Field: &formular.Field{Kind: formular.FieldText}},
			{Type: formular.ItemField, ID: "rule", Label: "Rule", Field: &formular.Field{Kind: formular.FieldText}},
		},
	}
}

func defaultTunRuleElements(rules []sysnet.Rule) []formular.ArrayElement {
	out := make([]formular.ArrayElement, 0, len(rules))
	for i, rule := range rules {
		out = append(out, formular.ArrayElement{
			ID:       fmt.Sprintf("rule-%d", i),
			Template: "rule",
			Items: []formular.Item{
				{Type: formular.ItemField, ID: "type", Label: "Type", Field: &formular.Field{Kind: formular.FieldText, Value: rule.Type}},
				{Type: formular.ItemField, ID: "rule", Label: "Rule", Field: &formular.Field{Kind: formular.FieldText, Value: rule.Rule}},
			},
		})
	}
	return out
}

func defaultTunRulesFromAny(raw any) ([]sysnet.Rule, bool) {
	switch values := raw.(type) {
	case []formular.ArrayElementValue:
		return defaultTunRulesFromArray(values), true
	case []any:
		array := make([]formular.ArrayElementValue, 0, len(values))
		for _, rawElement := range values {
			element, ok := arrayElementValueFromAny(rawElement)
			if !ok {
				continue
			}
			array = append(array, element)
		}
		return defaultTunRulesFromArray(array), true
	default:
		return nil, false
	}
}

func defaultTunRulesFromArray(values []formular.ArrayElementValue) []sysnet.Rule {
	out := make([]sysnet.Rule, 0, len(values))
	for _, value := range values {
		ruleType, _ := stringFromAny(value.Values["type"])
		ruleValue, _ := stringFromAny(value.Values["rule"])
		rule := sysnet.Rule{Type: strings.TrimSpace(ruleType), Rule: strings.TrimSpace(ruleValue)}
		if rule.Type == "" && rule.Rule == "" {
			continue
		}
		out = append(out, rule)
	}
	return out
}

func readDefaultTunRulesConfig(cfg configer.Config, key string) []sysnet.Rule {
	if cfg == nil {
		return nil
	}
	raw, err := cfg.Get(configer.Path{key})
	if err != nil {
		return nil
	}
	return defaultTunRulesFromConfigValue(raw)
}

func defaultTunRulesFromConfigValue(raw any) []sysnet.Rule {
	switch values := raw.(type) {
	case []sysnet.Rule:
		return normalizeDefaultTunRules(slices.Clone(values))
	case []map[string]string:
		out := make([]sysnet.Rule, 0, len(values))
		for _, value := range values {
			out = append(out, sysnet.Rule{Type: value["type"], Rule: value["rule"]})
		}
		return normalizeDefaultTunRules(out)
	case []map[string]any:
		out := make([]sysnet.Rule, 0, len(values))
		for _, value := range values {
			ruleType, _ := stringFromAny(value["type"])
			ruleValue, _ := stringFromAny(value["rule"])
			out = append(out, sysnet.Rule{Type: ruleType, Rule: ruleValue})
		}
		return normalizeDefaultTunRules(out)
	case []any:
		out := make([]sysnet.Rule, 0, len(values))
		for _, rawRule := range values {
			switch value := rawRule.(type) {
			case sysnet.Rule:
				out = append(out, value)
			case map[string]string:
				out = append(out, sysnet.Rule{Type: value["type"], Rule: value["rule"]})
			case map[string]any:
				ruleType, _ := stringFromAny(value["type"])
				ruleValue, _ := stringFromAny(value["rule"])
				out = append(out, sysnet.Rule{Type: ruleType, Rule: ruleValue})
			}
		}
		return normalizeDefaultTunRules(out)
	default:
		return nil
	}
}

func normalizeDefaultTunRules(rules []sysnet.Rule) []sysnet.Rule {
	out := make([]sysnet.Rule, 0, len(rules))
	for _, rule := range rules {
		rule.Type = strings.TrimSpace(rule.Type)
		rule.Rule = strings.TrimSpace(rule.Rule)
		if rule.Type == "" && rule.Rule == "" {
			continue
		}
		out = append(out, rule)
	}
	return out
}

func defaultTunRulesConfig(rules []sysnet.Rule) []map[string]string {
	out := make([]map[string]string, 0, len(rules))
	for _, rule := range rules {
		out = append(out, map[string]string{"type": rule.Type, "rule": rule.Rule})
	}
	return out
}
