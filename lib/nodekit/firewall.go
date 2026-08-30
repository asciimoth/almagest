package nodekit

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/gonnect"
	"github.com/asciimoth/pasta/pasta/std"
)

// FirewallRulesPlaceholder is an example policy for firewall JSON fields.
// Field names use lower camel case. responseTTL accepts a Go duration string.
const FirewallRulesPlaceholder = `{
  "exclude": [
    {"network": "tcp", "hosts": ["example.test"], "ports": [443]}
  ],
  "include": [
    {"network": "udp", "hosts": ["192.0.2.0/24"], "ports": [53]}
  ],
  "responseTTL": "2m"
}`

type firewallConfigJSON struct {
	Exclude     []firewallRuleJSON `json:"exclude,omitempty"`
	Include     []firewallRuleJSON `json:"include,omitempty"`
	ResponseTTL string             `json:"responseTTL,omitempty"`
}

type firewallRuleJSON struct {
	Network    string                  `json:"network,omitempty"`
	Hosts      []string                `json:"hosts,omitempty"`
	LocalHosts []string                `json:"localHosts,omitempty"`
	Ports      []uint16                `json:"ports,omitempty"`
	PortRanges []firewallPortRangeJSON `json:"portRanges,omitempty"`
}

type firewallPortRangeJSON struct {
	First uint16 `json:"first"`
	Last  uint16 `json:"last"`
}

// ParseFirewallConfigJSON parses one user-facing firewall policy. Unknown
// fields and trailing JSON values are errors so misspelled rules do not fail
// open without a visible validation error.
func ParseFirewallConfigJSON(text string) (*gonnect.FirewallConfig, error) {
	if strings.TrimSpace(text) == "" {
		return nil, errors.New("firewall config is empty")
	}
	var raw firewallConfigJSON
	decoder := json.NewDecoder(strings.NewReader(text))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&raw); err != nil {
		return nil, fmt.Errorf("parse firewall config: %w", err)
	}
	if err := requireJSONEOF(decoder); err != nil {
		return nil, err
	}

	ttl := time.Duration(0)
	if raw.ResponseTTL != "" {
		var err error
		ttl, err = time.ParseDuration(raw.ResponseTTL)
		if err != nil {
			return nil, fmt.Errorf("parse firewall config responseTTL: %w", err)
		}
		if ttl <= 0 {
			return nil, errors.New("firewall config responseTTL must be positive")
		}
	}
	return (&gonnect.FirewallConfig{
		Exclude:     firewallRulesFromJSON(raw.Exclude),
		Include:     firewallRulesFromJSON(raw.Include),
		ResponseTTL: ttl,
	}).Optimize(), nil
}

func requireJSONEOF(decoder *json.Decoder) error {
	var trailing any
	err := decoder.Decode(&trailing)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("parse firewall config: %w", err)
	}
	return errors.New("parse firewall config: multiple JSON values")
}

// FirewallConfigFromObject converts an immutable pasta/object value to a
// firewall policy by using the same strict JSON contract as the menu field.
func FirewallConfigFromObject(object std.Object) (*gonnect.FirewallConfig, error) {
	data, err := object.JSON()
	if err != nil {
		return nil, fmt.Errorf("encode firewall config object: %w", err)
	}
	return ParseFirewallConfigJSON(string(data))
}

// FirewallConfigTextFromConfig reads a structured policy from a config
// subgraph and returns JSON for the node menu. String values remain supported
// so existing workspaces can load and migrate to structured data on save.
func FirewallConfigTextFromConfig(cfg configer.Config, path configer.Path) string {
	if cfg == nil {
		return "{}"
	}
	value, err := cfg.Get(path)
	if err != nil {
		return "{}"
	}
	if text, ok := value.(string); ok {
		return text
	}
	data, err := json.Marshal(value)
	if err != nil {
		// An empty string is invalid firewall JSON. OnInit will expose the
		// validation error instead of silently accepting damaged config data.
		return ""
	}
	return string(data)
}

// FirewallConfigValue returns native maps and arrays for workspace config
// persistence. The result is a rules subgraph, not a serialized JSON string.
func FirewallConfigValue(cfg *gonnect.FirewallConfig) any {
	var value any
	if err := json.Unmarshal([]byte(FormatFirewallConfigJSON(cfg)), &value); err != nil {
		return map[string]any{}
	}
	return value
}

// FormatFirewallConfigJSON returns the canonical policy as indented JSON for
// read-only effective-config fields. It excludes runtime-only DNS cache state.
func FormatFirewallConfigJSON(cfg *gonnect.FirewallConfig) string {
	optimized := cfg.Optimize()
	raw := firewallConfigJSON{
		Exclude: firewallRulesToJSON(optimized.Exclude),
		Include: firewallRulesToJSON(optimized.Include),
	}
	if optimized.ResponseTTL > 0 {
		raw.ResponseTTL = optimized.ResponseTTL.String()
	}
	data, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return "{}"
	}
	return string(bytes.TrimSpace(data))
}

// MergeFirewallConfigMap merges base with policies in ascending link-ID
// order. Sorting makes the choice deterministic when policies have equal
// effective response TTL values.
func MergeFirewallConfigMap(base *gonnect.FirewallConfig, byLink map[uint64]*gonnect.FirewallConfig) *gonnect.FirewallConfig {
	links := make([]uint64, 0, len(byLink))
	for link := range byLink {
		links = append(links, link)
	}
	slices.Sort(links)
	configs := make([]*gonnect.FirewallConfig, 0, len(links)+1)
	configs = append(configs, base)
	for _, link := range links {
		configs = append(configs, byLink[link])
	}
	return gonnect.MergeFirewallConfigs(configs...)
}

func firewallRulesFromJSON(rules []firewallRuleJSON) []gonnect.FirewallRule {
	out := make([]gonnect.FirewallRule, len(rules))
	for i, rule := range rules {
		out[i] = gonnect.FirewallRule{
			Network:    rule.Network,
			Hosts:      slices.Clone(rule.Hosts),
			LocalHosts: slices.Clone(rule.LocalHosts),
			Ports:      slices.Clone(rule.Ports),
			PortRanges: make([]gonnect.FirewallPortRange, len(rule.PortRanges)),
		}
		for j, portRange := range rule.PortRanges {
			out[i].PortRanges[j] = gonnect.FirewallPortRange(portRange)
		}
	}
	return out
}

func firewallRulesToJSON(rules []gonnect.FirewallRule) []firewallRuleJSON {
	if len(rules) == 0 {
		return nil
	}
	out := make([]firewallRuleJSON, len(rules))
	for i, rule := range rules {
		out[i] = firewallRuleJSON{
			Network:    rule.Network,
			Hosts:      slices.Clone(rule.Hosts),
			LocalHosts: slices.Clone(rule.LocalHosts),
			Ports:      slices.Clone(rule.Ports),
			PortRanges: make([]firewallPortRangeJSON, len(rule.PortRanges)),
		}
		for j, portRange := range rule.PortRanges {
			out[i].PortRanges[j] = firewallPortRangeJSON(portRange)
		}
	}
	return out
}
