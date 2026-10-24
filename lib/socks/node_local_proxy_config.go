package socksnodes

import (
	"errors"
	"slices"
	"strings"

	"github.com/asciimoth/almagest/lib/nodekit"
	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/formular"
	"github.com/asciimoth/gonnect/sysnet"
	"github.com/asciimoth/pasta/pasta"
)

type localProxyConfig struct {
	server  socksServerConfig
	exclude []sysnet.Rule
	include []sysnet.Rule
}

func defaultLocalProxyConfig() localProxyConfig {
	return localProxyConfig{server: defaultSocksServerConfig()}
}

func (c *localProxyConfig) read(cfg configer.Config) {
	c.server.read(cfg)
	c.exclude = readLocalProxyRulesConfig(cfg, "exclude")
	c.include = readLocalProxyRulesConfig(cfg, "include")
	c.normalize()
}

func (c *localProxyConfig) normalize() {
	c.exclude = normalizeLocalProxyRules(c.exclude)
	c.include = normalizeLocalProxyRules(c.include)
}

func (c localProxyConfig) validate() error {
	if err := c.server.validate(); err != nil {
		return err
	}
	if len(c.exclude) > 0 && len(c.include) > 0 {
		return errors.New("exclude and include client rules are mutually exclusive")
	}
	return nil
}

func saveLocalProxyConfig(cfg configer.Config, config localProxyConfig) error {
	if err := pasta.DeleteNodeOwnedConfigKeys(cfg); err != nil {
		return err
	}
	values := socksServerConfigValues(config.server)
	values["exclude"] = localProxyRulesConfig(config.exclude)
	values["include"] = localProxyRulesConfig(config.include)
	return nodekit.SetConfigMap(cfg, values)
}

func localProxyConfigWithValues(current localProxyConfig, values map[string]any) localProxyConfig {
	next := current
	next.server = socksServerConfigWithValues(current.server, values)
	if rules, ok := localProxyRulesFromAny(values["exclude"]); ok {
		next.exclude = rules
	}
	if rules, ok := localProxyRulesFromAny(values["include"]); ok {
		next.include = rules
	}
	next.normalize()
	return next
}

func localProxyRulesFromAny(raw any) ([]sysnet.Rule, bool) {
	switch values := raw.(type) {
	case []formular.ArrayElementValue:
		return localProxyRulesFromArray(values), true
	case []any:
		array := make([]formular.ArrayElementValue, 0, len(values))
		for _, rawElement := range values {
			element, ok := formular.ArrayElementValueFromAny(rawElement)
			if ok {
				array = append(array, element)
			}
		}
		return localProxyRulesFromArray(array), true
	default:
		return nil, false
	}
}

func localProxyRulesFromArray(values []formular.ArrayElementValue) []sysnet.Rule {
	out := make([]sysnet.Rule, 0, len(values))
	for _, value := range values {
		ruleType, _ := formular.StringValue(value.Values["type"])
		ruleValue, _ := formular.StringValue(value.Values["rule"])
		rule := sysnet.Rule{Type: strings.TrimSpace(ruleType), Rule: strings.TrimSpace(ruleValue)}
		if rule.Type != "" || rule.Rule != "" {
			out = append(out, rule)
		}
	}
	return out
}

func readLocalProxyRulesConfig(cfg configer.Config, key string) []sysnet.Rule {
	if cfg == nil {
		return nil
	}
	raw, err := cfg.Get(configer.Path{key})
	if err != nil {
		return nil
	}
	switch values := raw.(type) {
	case []sysnet.Rule:
		return normalizeLocalProxyRules(slices.Clone(values))
	case []map[string]string:
		out := make([]sysnet.Rule, 0, len(values))
		for _, value := range values {
			out = append(out, sysnet.Rule{Type: value["type"], Rule: value["rule"]})
		}
		return normalizeLocalProxyRules(out)
	case []map[string]any:
		out := make([]sysnet.Rule, 0, len(values))
		for _, value := range values {
			ruleType, _ := formular.StringValue(value["type"])
			ruleValue, _ := formular.StringValue(value["rule"])
			out = append(out, sysnet.Rule{Type: ruleType, Rule: ruleValue})
		}
		return normalizeLocalProxyRules(out)
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
		return normalizeLocalProxyRules(out)
	default:
		return nil
	}
}

func normalizeLocalProxyRules(rules []sysnet.Rule) []sysnet.Rule {
	out := make([]sysnet.Rule, 0, len(rules))
	for _, rule := range rules {
		rule.Type = strings.TrimSpace(rule.Type)
		rule.Rule = strings.TrimSpace(rule.Rule)
		if rule.Type != "" || rule.Rule != "" {
			out = append(out, rule)
		}
	}
	return out
}

func localProxyRulesConfig(rules []sysnet.Rule) []map[string]string {
	out := make([]map[string]string, 0, len(rules))
	for _, rule := range rules {
		out = append(out, map[string]string{"type": rule.Type, "rule": rule.Rule})
	}
	return out
}
