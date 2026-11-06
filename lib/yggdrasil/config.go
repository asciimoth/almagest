// Package yggdrasilnodes provides embedded Yggdrasil mesh nodes.
package yggdrasilnodes

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/asciimoth/almagest/lib/nodekit"
	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/formular"
	"github.com/asciimoth/pasta/pasta"
	"github.com/asciimoth/ygg/ygglib/transport"
)

type configuration struct {
	enabled                         bool
	secret, peers, listen, networks string
	multicastInterfaces             string
	autoPeerEnabled                 bool
	autoPeerSources                 string
	autoPeerCountries               string
	autoPeerSchemes                 string
	autoPeerFetchInterval           string
	autoPeerCheckInterval           string
	autoPeerMinimumConnected        int
	autoPeerMinimumFromFetch        int
	jumperEnabled                   bool
	jumperAddresses                 string
	jumperCheckInterval             string
	jumperLinkTimeout               string
	nodeInfo                        string
	nodeInfoPrivacy                 bool
}

func readConfig(cfg configer.Config) configuration {
	c := configuration{enabled: true, autoPeerFetchInterval: "1h", autoPeerCheckInterval: "1m", autoPeerMinimumConnected: 1, autoPeerMinimumFromFetch: 1, jumperCheckInterval: "10s", jumperLinkTimeout: "30s"}
	if cfg != nil {
		c.enabled = configer.Bool(cfg, configer.Path{"enabled"}, true)
		c.secret = configer.String(cfg, configer.Path{"privateKeySecret"}, "")
		c.peers = configer.String(cfg, configer.Path{"peers"}, "")
		c.listen = configer.String(cfg, configer.Path{"listen"}, "")
		c.networks = configer.String(cfg, configer.Path{"networks"}, "")
		c.multicastInterfaces = configer.String(cfg, configer.Path{"multicastInterfaces"}, "")
		c.autoPeerEnabled = configer.Bool(cfg, configer.Path{"autoPeerEnabled"}, false)
		c.autoPeerSources = configer.String(cfg, configer.Path{"autoPeerSources"}, "")
		c.autoPeerCountries = configer.String(cfg, configer.Path{"autoPeerCountries"}, "")
		c.autoPeerSchemes = configer.String(cfg, configer.Path{"autoPeerSchemes"}, "")
		c.autoPeerFetchInterval = configer.String(cfg, configer.Path{"autoPeerFetchInterval"}, c.autoPeerFetchInterval)
		c.autoPeerCheckInterval = configer.String(cfg, configer.Path{"autoPeerCheckInterval"}, c.autoPeerCheckInterval)
		c.autoPeerMinimumConnected = configer.Int(cfg, configer.Path{"autoPeerMinimumConnected"}, c.autoPeerMinimumConnected)
		c.autoPeerMinimumFromFetch = configer.Int(cfg, configer.Path{"autoPeerMinimumFromFetch"}, c.autoPeerMinimumFromFetch)
		c.jumperEnabled = configer.Bool(cfg, configer.Path{"jumperEnabled"}, false)
		c.jumperAddresses = configer.String(cfg, configer.Path{"jumperAddresses"}, "")
		c.jumperCheckInterval = configer.String(cfg, configer.Path{"jumperCheckInterval"}, c.jumperCheckInterval)
		c.jumperLinkTimeout = configer.String(cfg, configer.Path{"jumperLinkTimeout"}, c.jumperLinkTimeout)
		c.nodeInfo = configer.String(cfg, configer.Path{"nodeInfo"}, "")
		c.nodeInfoPrivacy = configer.Bool(cfg, configer.Path{"nodeInfoPrivacy"}, false)
	}
	return c.normalized()
}
func (c configuration) normalized() configuration {
	c.secret = strings.TrimSpace(c.secret)
	c.peers = normalizeLines(c.peers)
	c.listen = normalizeLines(c.listen)
	entries := strings.Fields(c.networks)
	for i, entry := range entries {
		if name, pattern, ok := strings.Cut(entry, "="); ok {
			entries[i] = name + "=" + strings.ToLower(pattern)
		}
	}
	c.networks = normalizeLines(strings.Join(entries, "\n"))
	c.autoPeerSources = normalizeLines(c.autoPeerSources)
	c.autoPeerCountries = strings.ToLower(normalizeLines(c.autoPeerCountries))
	c.autoPeerSchemes = strings.ToLower(normalizeLines(c.autoPeerSchemes))
	c.jumperAddresses = normalizeLines(c.jumperAddresses)
	c.autoPeerFetchInterval = strings.TrimSpace(c.autoPeerFetchInterval)
	c.autoPeerCheckInterval = strings.TrimSpace(c.autoPeerCheckInterval)
	c.jumperCheckInterval = strings.TrimSpace(c.jumperCheckInterval)
	c.jumperLinkTimeout = strings.TrimSpace(c.jumperLinkTimeout)
	c.multicastInterfaces = normalizeJSON(c.multicastInterfaces)
	c.nodeInfo = normalizeJSON(c.nodeInfo)
	return c
}
func normalizeJSON(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	var parsed any
	if err := json.Unmarshal([]byte(value), &parsed); err != nil {
		return value
	}
	data, _ := json.Marshal(parsed)
	return string(data)
}
func normalizeLines(value string) string {
	var out []string
	for _, line := range strings.Fields(value) {
		if !slices.Contains(out, line) {
			out = append(out, line)
		}
	}
	return strings.Join(out, "\n")
}

type multicastInterfaceConfig struct {
	Regex    string `json:"regex"`
	Beacon   bool   `json:"beacon"`
	Listen   bool   `json:"listen"`
	Port     int    `json:"port,omitempty"`
	Priority int    `json:"priority,omitempty"`
}

func (c configuration) multicastOptions() ([]multicastInterfaceConfig, error) {
	if c.multicastInterfaces == "" {
		return nil, nil
	}
	decoder := json.NewDecoder(bytes.NewBufferString(c.multicastInterfaces))
	decoder.DisallowUnknownFields()
	var interfaces []multicastInterfaceConfig
	if err := decoder.Decode(&interfaces); err != nil || interfaces == nil {
		return nil, fmt.Errorf("multicast interfaces must be a JSON array: %v", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, fmt.Errorf("multicast interfaces contain extra JSON data")
	}
	for _, intf := range interfaces {
		if intf.Regex == "" {
			return nil, fmt.Errorf("multicast interface regex is empty")
		}
		if _, err := regexp.Compile(intf.Regex); err != nil {
			return nil, fmt.Errorf("invalid multicast interface regex %q: %w", intf.Regex, err)
		}
		if intf.Port < 0 || intf.Port > 65535 || intf.Priority < 0 || intf.Priority > 255 {
			return nil, fmt.Errorf("multicast port or priority is out of range")
		}
	}
	return interfaces, nil
}
func (c configuration) nodeInfoMap() (map[string]interface{}, error) {
	if c.nodeInfo == "" {
		return map[string]interface{}{}, nil
	}
	var value map[string]interface{}
	if err := json.Unmarshal([]byte(c.nodeInfo), &value); err != nil || value == nil {
		return nil, fmt.Errorf("NodeInfo must be a JSON object: %v", err)
	}
	if len(c.nodeInfo) > 16_000 {
		return nil, fmt.Errorf("NodeInfo is too large")
	}
	return value, nil
}
func validateURL(raw string, allowPortZero bool) error {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" || u.Port() == "" || (u.Scheme != "tcp" && u.Scheme != "tls") {
		return fmt.Errorf("invalid TCP or TLS URL: %s", raw)
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil || port < 0 || port > 65535 || (!allowPortZero && port == 0) {
		return fmt.Errorf("invalid port in URL: %s", raw)
	}
	return nil
}
func (c configuration) validate() error {
	for index, list := range []string{c.peers, c.listen} {
		for _, raw := range strings.Fields(list) {
			if err := validateURL(raw, index == 1); err != nil {
				return err
			}
		}
	}
	m := transport.NewManager(nil)
	defer func() { _ = m.Close() }()
	seenNames, seenPatterns := map[string]bool{}, map[string]bool{}
	for _, entry := range strings.Fields(c.networks) {
		name, pattern, ok := strings.Cut(entry, "=")
		if !ok || name == "default" || pasta.ValidatePortName(name) != nil {
			return fmt.Errorf("upstream must be name=host-pattern with a valid port name: %s", entry)
		}
		if seenNames[name] || seenPatterns[pattern] {
			return fmt.Errorf("duplicate upstream name or pattern: %s", entry)
		}
		seenNames[name] = true
		seenPatterns[pattern] = true
		if err := m.MapNetwork(pattern, nil); err != nil {
			return err
		}
	}
	if _, err := c.multicastOptions(); err != nil {
		return err
	}
	if c.autoPeerMinimumConnected < 0 || c.autoPeerMinimumFromFetch < 0 {
		return fmt.Errorf("auto-peer minimum connected counts must be non-negative")
	}
	for _, value := range []struct{ name, text string }{{"auto-peer fetch interval", c.autoPeerFetchInterval}, {"auto-peer check interval", c.autoPeerCheckInterval}, {"jumper check interval", c.jumperCheckInterval}, {"jumper link timeout", c.jumperLinkTimeout}} {
		if value.text == "" {
			continue
		}
		d, err := time.ParseDuration(value.text)
		if err != nil || d <= 0 {
			return fmt.Errorf("invalid %s: %q", value.name, value.text)
		}
	}
	for _, source := range strings.Fields(c.autoPeerSources) {
		if source == "BUILTIN" {
			continue
		}
		u, err := url.Parse(source)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
			return fmt.Errorf("invalid public-peer source: %q", source)
		}
	}
	for _, scheme := range strings.Fields(c.autoPeerSchemes) {
		if scheme != "tcp" && scheme != "tls" {
			return fmt.Errorf("unsupported public-peer transport scheme: %q", scheme)
		}
	}
	for _, raw := range strings.Fields(c.jumperAddresses) {
		if err := validateURL(raw, false); err != nil {
			return fmt.Errorf("invalid jumper address: %w", err)
		}
	}
	if _, err := c.nodeInfoMap(); err != nil {
		return err
	}

	return nil
}
func (c configuration) values() map[string]any {
	return map[string]any{
		"enabled": c.enabled, "privateKeySecret": c.secret, "peers": c.peers, "listen": c.listen, "networks": c.networks,
		"multicastInterfaces": c.multicastInterfaces,
		"autoPeerEnabled":     c.autoPeerEnabled, "autoPeerSources": c.autoPeerSources, "autoPeerCountries": c.autoPeerCountries,
		"autoPeerSchemes": c.autoPeerSchemes, "autoPeerFetchInterval": c.autoPeerFetchInterval,
		"autoPeerCheckInterval": c.autoPeerCheckInterval, "autoPeerMinimumConnected": c.autoPeerMinimumConnected,
		"autoPeerMinimumFromFetch": c.autoPeerMinimumFromFetch,
		"jumperEnabled":            c.jumperEnabled, "jumperAddresses": c.jumperAddresses,
		"jumperCheckInterval": c.jumperCheckInterval, "jumperLinkTimeout": c.jumperLinkTimeout,
		"nodeInfo": c.nodeInfo, "nodeInfoPrivacy": c.nodeInfoPrivacy,
	}
}
func (c configuration) apply(values map[string]any) configuration {
	for key, target := range map[string]*bool{"enabled": &c.enabled, "autoPeerEnabled": &c.autoPeerEnabled, "jumperEnabled": &c.jumperEnabled, "nodeInfoPrivacy": &c.nodeInfoPrivacy} {
		if v, ok := formular.BoolValue(values[key]); ok {
			*target = v
		}
	}
	for key, target := range map[string]*int{"autoPeerMinimumConnected": &c.autoPeerMinimumConnected, "autoPeerMinimumFromFetch": &c.autoPeerMinimumFromFetch} {
		if v, ok := formular.IntValue(values[key]); ok {
			*target = v
		}
	}
	for key, target := range map[string]*string{
		"privateKeySecret": &c.secret, "peers": &c.peers, "listen": &c.listen, "networks": &c.networks,
		"multicastInterfaces": &c.multicastInterfaces, "autoPeerSources": &c.autoPeerSources,
		"autoPeerCountries": &c.autoPeerCountries, "autoPeerSchemes": &c.autoPeerSchemes,
		"autoPeerFetchInterval": &c.autoPeerFetchInterval, "autoPeerCheckInterval": &c.autoPeerCheckInterval,
		"jumperAddresses": &c.jumperAddresses, "jumperCheckInterval": &c.jumperCheckInterval,
		"jumperLinkTimeout": &c.jumperLinkTimeout, "nodeInfo": &c.nodeInfo,
	} {
		if v, ok := formular.StringValue(values[key]); ok {
			*target = v
		}
	}
	return c.normalized()
}
func (c configuration) block(problem string) formular.Block {
	return formular.Block{ID: "config", Order: 10, Generation: 1, Form: true, Items: []formular.Item{
		formular.CheckboxField("enabled", "Enabled", c.enabled),
		nodekit.ConfigSecretNameField("privateKeySecret", "Identity key secret", c.secret, problem != "", problem),
		nodekit.ConfigTextField("peers", "Peer URLs", c.peers, problem != "", problem, true),
		nodekit.ConfigTextField("listen", "Listener URLs", c.listen, problem != "", problem, true),
		nodekit.ConfigTextField("networks", "Upstreams (name=host-pattern)", c.networks, problem != "", problem, true),
		nodekit.ConfigTextField("multicastInterfaces", "Link-local interfaces (JSON)", c.multicastInterfaces, problem != "", problem, true),
		formular.CheckboxField("autoPeerEnabled", "Public-peer autopeering", c.autoPeerEnabled),
		nodekit.ConfigTextField("autoPeerSources", "Public-peer sources", c.autoPeerSources, problem != "", problem, true),
		nodekit.ConfigTextField("autoPeerCountries", "Public-peer countries", c.autoPeerCountries, problem != "", problem, true),
		nodekit.ConfigTextField("autoPeerSchemes", "Public-peer schemes", c.autoPeerSchemes, problem != "", problem, true),
		nodekit.ConfigTextField("autoPeerFetchInterval", "Peer-list fetch interval", c.autoPeerFetchInterval, problem != "", problem, false),
		nodekit.ConfigTextField("autoPeerCheckInterval", "Autopeer check interval", c.autoPeerCheckInterval, problem != "", problem, false),
		nodekit.ConfigIntField("autoPeerMinimumConnected", "Minimum connected peers", c.autoPeerMinimumConnected, problem != "", problem),
		nodekit.ConfigIntField("autoPeerMinimumFromFetch", "Minimum peers from sources", c.autoPeerMinimumFromFetch, problem != "", problem),
		formular.CheckboxField("jumperEnabled", "NodeInfo jumper", c.jumperEnabled),
		nodekit.ConfigTextField("jumperAddresses", "Jumper addresses", c.jumperAddresses, problem != "", problem, true),
		nodekit.ConfigTextField("jumperCheckInterval", "Jumper check interval", c.jumperCheckInterval, problem != "", problem, false),
		nodekit.ConfigTextField("jumperLinkTimeout", "Jumper link timeout", c.jumperLinkTimeout, problem != "", problem, false),
		nodekit.ConfigTextField("nodeInfo", "NodeInfo (JSON)", c.nodeInfo, problem != "", problem, true),
		formular.CheckboxField("nodeInfoPrivacy", "NodeInfo privacy", c.nodeInfoPrivacy),
	}}
}

func (c configuration) mappings() map[string]string {
	out := map[string]string{"default": "default"}
	for _, entry := range strings.Fields(c.networks) {
		name, pattern, _ := strings.Cut(entry, "=")
		out[name] = pattern
	}
	return out
}
