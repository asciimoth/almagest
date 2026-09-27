package tunnodes

import (
	"errors"
	"slices"
	"strings"

	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/formular"
	"github.com/asciimoth/gonnect/sysnet"

	"github.com/asciimoth/almagest/lib/nodekit"
)

type defaultTunConfig struct {
	name      string
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
	c.name = configer.String(cfg, configer.Path{"name"}, c.name)
	c.tunAddrs = configer.String(cfg, configer.Path{"tunAddrs"}, c.tunAddrs)
	c.tunRoutes = configer.String(cfg, configer.Path{"tunRoutes"}, c.tunRoutes)
	c.mtu = configer.Int(cfg, configer.Path{"mtu"}, c.mtu)
	c.dnsIP = configer.String(cfg, configer.Path{"dnsIP"}, c.dnsIP)
	c.strict = configer.Bool(cfg, configer.Path{"strict"}, c.strict)
	c.exclude = readDefaultTunRulesConfig(cfg, "exclude")
	c.include = readDefaultTunRulesConfig(cfg, "include")
	c.normalize()
}

func (c *defaultTunConfig) normalize() {
	c.name = strings.TrimSpace(c.name)
	c.tunAddrs = normalizeListText(c.tunAddrs)
	c.tunRoutes = normalizeListText(c.tunRoutes)
	c.dnsIP = strings.TrimSpace(c.dnsIP)
	c.exclude = normalizeDefaultTunRules(c.exclude)
	c.include = normalizeDefaultTunRules(c.include)
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

func splitDefaultTunList(value string) []string {
	return nodekit.SplitListText(value)
}

func defaultTunStringListFromAny(raw any) (string, bool) {
	if value, ok := formular.StringValue(raw); ok {
		return value, true
	}
	switch values := raw.(type) {
	case []formular.ArrayElementValue:
		return strings.Join(defaultTunStringListFromArray(values), "\n"), true
	case []any:
		array := make([]formular.ArrayElementValue, 0, len(values))
		for _, rawElement := range values {
			element, ok := formular.ArrayElementValueFromAny(rawElement)
			if !ok {
				continue
			}
			array = append(array, element)
		}
		return strings.Join(defaultTunStringListFromArray(array), "\n"), true
	default:
		return "", false
	}
}

func defaultTunStringListFromArray(values []formular.ArrayElementValue) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		item, _ := formular.StringValue(value.Values["value"])
		item = strings.TrimSpace(item)
		if item != "" {
			out = append(out, item)
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

func (c defaultTunConfig) defaultTunOpts() sysnet.DefaultTunOpts {
	return sysnet.DefaultTunOpts{
		Name:      c.name,
		TunAddrs:  splitDefaultTunList(c.tunAddrs),
		TunRoutes: splitDefaultTunList(c.tunRoutes),
		MTU:       c.mtu,
		DnsIP:     c.dnsIP,
		Strict:    c.strict,
		Exclude:   slices.Clone(c.exclude),
		Include:   slices.Clone(c.include),
	}
}

func defaultTunOptsDynamicallyCompatible(a, b sysnet.DefaultTunOpts) bool {
	return slices.Equal(a.TunRoutes, b.TunRoutes) &&
		slices.Equal(a.SourceRoutes, b.SourceRoutes) &&
		a.DnsIP == b.DnsIP &&
		a.Strict == b.Strict &&
		slices.Equal(a.Exclude, b.Exclude) &&
		slices.Equal(a.Include, b.Include)
}

func defaultTunOptsEqual(a, b sysnet.DefaultTunOpts) bool {
	return defaultTunOptsDynamicallyCompatible(a, b) &&
		a.Name == b.Name &&
		slices.Equal(a.TunAddrs, b.TunAddrs) &&
		a.MTU == b.MTU
}

func defaultTunRulesFromAny(raw any) ([]sysnet.Rule, bool) {
	switch values := raw.(type) {
	case []formular.ArrayElementValue:
		return defaultTunRulesFromArray(values), true
	case []any:
		array := make([]formular.ArrayElementValue, 0, len(values))
		for _, rawElement := range values {
			element, ok := formular.ArrayElementValueFromAny(rawElement)
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
		ruleType, _ := formular.StringValue(value.Values["type"])
		ruleValue, _ := formular.StringValue(value.Values["rule"])
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
			ruleType, _ := formular.StringValue(value["type"])
			ruleValue, _ := formular.StringValue(value["rule"])
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
				ruleType, _ := formular.StringValue(value["type"])
				ruleValue, _ := formular.StringValue(value["rule"])
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
