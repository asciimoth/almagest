package tlsnodes

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/formular"
	"github.com/asciimoth/gonnect"
	gonnecttls "github.com/asciimoth/gonnect/tls"
	"github.com/asciimoth/pasta/pasta"
	"github.com/asciimoth/pasta/pasta/std"

	"github.com/asciimoth/almagest/lib/links"
	"github.com/asciimoth/almagest/lib/nodekit"
)

const (
	TypeNetwork = nodekit.TypeNetwork

	configBlockID = "config"

	fieldTLSConfig         = nodekit.TLSConfigFieldID
	fieldClientServerName  = "clientServerName"
	fieldClientALPN        = "clientAlpn"
	fieldServerName        = "serverName"
	fieldServerALPN        = "serverAlpn"
	fieldLeafTTLSeconds    = "leafTTLSeconds"
	fieldSniffBufferSize   = "sniffBufferSize"
	fieldFilterMode        = "filterMode"
	fieldFilterRules       = "filterRules"
	fieldUseSNIHostname    = "useSNIHostname"
	fieldDestinationRemaps = "destinationRemaps"
)

const (
	defaultLeafTTLSeconds = 0
	defaultFilterRules    = "[]"
	defaultRemaps         = "[]"
)

const (
	placeholderALPN              = "h2\nhttp/1.1"
	placeholderClientServerName  = "origin.example.test"
	placeholderDestinationRemaps = "[\n  {\"sni_hosts\":[\"example.test\"],\"dst\":\"127.0.0.1:8443\"}\n]"
	placeholderFilterMode        = "off"
	placeholderFilterRules       = "[\n  {\"sni_hosts\":[\"example.test\"],\"alpns\":[\"h2\"]}\n]"
	placeholderLeafTTLSeconds    = "0"
	placeholderServerName        = "example.test"
	placeholderSniffBufferBytes  = "0"
)

type NetworkCloser = nodekit.NetworkCloser
type NetworkOutputCache = nodekit.NetworkOutputCache
type NodeBase = nodekit.Base
type Runtime = nodekit.Runtime

type TlsConfigChanged = nodekit.TlsConfigChanged
type TlsLocalCAChanged = nodekit.TlsLocalCAChanged

func NetworkPort(direction, name string) pasta.Port {
	return nodekit.NetworkPort(direction, name)
}

func restoredLeft(restored *pasta.NodeInitData, index int) uint64 {
	return nodekit.RestoredLeft(restored, index)
}

func restoredRight(restored *pasta.NodeInitData, index int) uint64 {
	return nodekit.RestoredRight(restored, index)
}

func acceptNetworkPayload(w *pasta.Workspace, node uint64, event pasta.Event, current NetworkCloser) (NetworkCloser, bool) {
	return nodekit.AcceptNetworkPayload(w, node, event, current)
}

func closeNetwork(n NetworkCloser) error {
	return nodekit.CloseNetwork(n)
}

func networkIsReady(n NetworkCloser) bool {
	return nodekit.NetworkIsReady(n)
}

func sendNodeMenuSnapshot(w *pasta.Workspace, node uint64, force bool, blocks ...formular.Block) {
	nodekit.SendNodeMenuSnapshot(w, node, force, blocks...)
}

func sendNodeFieldStatus(w *pasta.Workspace, node uint64, field formular.FieldRef, err error) {
	nodekit.SendNodeFieldStatus(w, node, field, err)
}

func tlsConfigSelector(base nodekit.NodeContext, selectedName, errText string) formular.Item {
	return nodekit.TLSConfigSelectorItem(base, selectedName, errText)
}

func readTLSConfigName(cfg configer.Config) string {
	return nodekit.ReadTLSConfigName(cfg)
}

func tlsConfigValueFromAny(raw any) (string, bool) {
	return nodekit.TLSConfigValueFromAny(raw)
}

func handleTLSConfigChanged(selectedName, effectiveName string, msg TlsConfigChanged) (string, bool) {
	return nodekit.HandleTLSConfigChanged(selectedName, effectiveName, msg)
}

func resolveTLSClientConfig(base nodekit.NodeContext, selectedName string) (nodekit.TLSConfigResult, error) {
	return nodekit.ResolveTLSClientConfig(base, selectedName)
}

func resolveTLSLocalCA(base nodekit.NodeContext) (tls.Certificate, error) {
	return nodekit.ResolveTLSLocalCA(base)
}

func cloneTLSClientConfig(cfg *tls.Config) *tls.Config {
	return nodekit.CloneTLSClientConfig(cfg)
}

func cloneTLSCertificate(cert tls.Certificate) tls.Certificate {
	return nodekit.CloneTLSCertificate(cert)
}

func configTextField(id, label, value string, statusErr bool, statusText string, multiline bool) formular.Item {
	return nodekit.ConfigTextField(id, label, value, statusErr, statusText, multiline)
}

func configTextFieldPlaceholder(id, label, value, placeholder string, statusErr bool, statusText string, multiline bool) formular.Item {
	item := configTextField(id, label, value, statusErr, statusText, multiline)
	if item.Field != nil {
		item.Placeholder = placeholder
	}
	return item
}

func configIntField(id, label string, value int, statusErr bool, statusText string) formular.Item {
	return nodekit.ConfigIntField(id, label, value, statusErr, statusText)
}

func configIntFieldPlaceholder(id, label string, value int, placeholder string, statusErr bool, statusText string) formular.Item {
	item := configIntField(id, label, value, statusErr, statusText)
	if item.Field != nil {
		item.Placeholder = placeholder
	}
	return item
}

func configCheckbox(id, label string, value bool) formular.Item {
	return nodekit.ConfigCheckbox(id, label, value)
}

func readString(cfg configer.Config, key, fallback string) string {
	if cfg == nil {
		return fallback
	}
	return configer.String(cfg, configer.Path{key}, fallback)
}

func readInt(cfg configer.Config, key string, fallback int) int {
	if cfg == nil {
		return fallback
	}
	return configer.Int(cfg, configer.Path{key}, fallback)
}

func readBool(cfg configer.Config, key string, fallback bool) bool {
	if cfg == nil {
		return fallback
	}
	return configer.Bool(cfg, configer.Path{key}, fallback)
}

func splitListText(value string) []string {
	return nodekit.SplitListText(value)
}

func normalizeListText(value string) string {
	return nodekit.NormalizeListText(value)
}

func durationFromSeconds(seconds int) (time.Duration, error) {
	if seconds < 0 {
		return 0, fmt.Errorf("value cannot be negative")
	}
	if seconds == 0 {
		return 0, nil
	}
	return time.Duration(seconds) * time.Second, nil
}

func parseJSONList[T any](text string, fallback string) ([]T, error) {
	if strings.TrimSpace(text) == "" {
		text = fallback
	}
	var out []T
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		return nil, err
	}
	return out, nil
}

func modeAllowedValues() []any {
	return []any{"off", "inclusive", "exclusive"}
}

func parseInterceptionMode(text string) (gonnecttls.InterceptionFilterMode, error) {
	switch strings.ToLower(strings.TrimSpace(text)) {
	case "", "off":
		return gonnecttls.InterceptionFilterOff, nil
	case "inclusive":
		return gonnecttls.InterceptionFilterInclusive, nil
	case "exclusive":
		return gonnecttls.InterceptionFilterExclusive, nil
	default:
		return 0, fmt.Errorf("unknown filter mode %q", text)
	}
}

func formatInterceptionMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "inclusive", "exclusive":
		return strings.ToLower(strings.TrimSpace(mode))
	default:
		return "off"
	}
}

type interceptionRuleJSON struct {
	Networks []string `json:"networks,omitempty"`
	ConnSrcs []string `json:"conn_srcs,omitempty"`
	ConnDsts []string `json:"conn_dsts,omitempty"`
	SNIHosts []string `json:"sni_hosts,omitempty"`
	ALPNs    []string `json:"alpns,omitempty"`

	SNIAvailable string `json:"sni_available,omitempty"`
	SNIEncrypted string `json:"sni_encrypted,omitempty"`

	TLSVersions []uint16 `json:"tls_versions,omitempty"`
}

func parseInterceptionRules(text string) ([]gonnecttls.InterceptionRule, error) {
	raw, err := parseJSONList[interceptionRuleJSON](text, defaultFilterRules)
	if err != nil {
		return nil, err
	}
	out := make([]gonnecttls.InterceptionRule, len(raw))
	for i, rule := range raw {
		available, err := parseInterceptionFlag(rule.SNIAvailable)
		if err != nil {
			return nil, fmt.Errorf("rule %d sni_available: %w", i, err)
		}
		encrypted, err := parseInterceptionFlag(rule.SNIEncrypted)
		if err != nil {
			return nil, fmt.Errorf("rule %d sni_encrypted: %w", i, err)
		}
		out[i] = gonnecttls.InterceptionRule{
			Networks:     append([]string(nil), rule.Networks...),
			ConnSrcs:     append([]string(nil), rule.ConnSrcs...),
			ConnDsts:     append([]string(nil), rule.ConnDsts...),
			SNIHosts:     append([]string(nil), rule.SNIHosts...),
			ALPNs:        append([]string(nil), rule.ALPNs...),
			SNIAvailable: available,
			SNIEncrypted: encrypted,
			TLSVersions:  append([]uint16(nil), rule.TLSVersions...),
		}
	}
	return out, nil
}

func parseInterceptionFlag(text string) (gonnecttls.InterceptionFlag, error) {
	switch strings.ToLower(strings.TrimSpace(text)) {
	case "", "any":
		return gonnecttls.InterceptionFlagAny, nil
	case "required", "true", "yes":
		return gonnecttls.InterceptionFlagRequired, nil
	case "forbidden", "false", "no":
		return gonnecttls.InterceptionFlagForbidden, nil
	default:
		return 0, fmt.Errorf("unknown flag value %q", text)
	}
}

type terminatorRemapJSON struct {
	OriginalDsts []string `json:"original_dsts,omitempty"`
	SNIHosts     []string `json:"sni_hosts,omitempty"`
	ALPNs        []string `json:"alpns,omitempty"`
	Dst          string   `json:"dst"`
}

func parseTerminatorRemaps(text string) ([]gonnecttls.TerminatorDestinationRemap, error) {
	raw, err := parseJSONList[terminatorRemapJSON](text, defaultRemaps)
	if err != nil {
		return nil, err
	}
	out := make([]gonnecttls.TerminatorDestinationRemap, len(raw))
	for i, remap := range raw {
		out[i] = gonnecttls.TerminatorDestinationRemap{
			OriginalDsts: append([]string(nil), remap.OriginalDsts...),
			SNIHosts:     append([]string(nil), remap.SNIHosts...),
			ALPNs:        append([]string(nil), remap.ALPNs...),
			Dst:          strings.TrimSpace(remap.Dst),
		}
	}
	return out, nil
}

func formApplyString(values map[string]any, key, fallback string) string {
	if value, ok := formular.StringValue(values[key]); ok {
		return value
	}
	if value, ok := std.StringFromPayload(values[key]); ok {
		return value
	}
	return fallback
}

func formApplyInt(values map[string]any, key string, fallback int) int {
	if value, ok := formular.IntValue(values[key]); ok {
		return value
	}
	if value, ok := std.IntFromPayload(values[key]); ok {
		return value
	}
	return fallback
}

func formApplyBool(values map[string]any, key string, fallback bool) bool {
	if value, ok := formular.BoolValue(values[key]); ok {
		return value
	}
	return fallback
}

func saveNodeConfig(cfg configer.Config, values map[string]any) error {
	if err := pasta.DeleteNodeOwnedConfigKeys(cfg); err != nil {
		return err
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	for _, key := range keys {
		if err := cfg.Set(configer.Path{key}, values[key]); err != nil {
			return err
		}
	}
	return nil
}

func emitWrappedNetwork(
	cache *NetworkOutputCache,
	w *pasta.Workspace,
	node uint64,
	link uint64,
	source gonnect.Network,
	wrap func(NetworkCloser) (NetworkCloser, error),
) bool {
	if wrap == nil {
		return cache.SendLocked(w, node, link, source)
	}
	return cache.SendWrappedLocked(w, node, link, source, func(input NetworkCloser) NetworkCloser {
		output, err := wrap(input)
		if err != nil {
			_ = input.Close()
			return nil
		}
		return output
	})
}

func refreshWrappedNetwork(
	cache *NetworkOutputCache,
	w *pasta.Workspace,
	node uint64,
	link uint64,
	source gonnect.Network,
	wrap func(NetworkCloser) (NetworkCloser, error),
) bool {
	if wrap == nil {
		return cache.RefreshLocked(w, node, link, source)
	}
	return cache.RefreshWrappedLocked(w, node, link, source, func(input NetworkCloser) NetworkCloser {
		output, err := wrap(input)
		if err != nil {
			_ = input.Close()
			return nil
		}
		return output
	})
}

func broadcastWrappedNetwork(
	cache *NetworkOutputCache,
	w *pasta.Workspace,
	node uint64,
	port uint64,
	source gonnect.Network,
	wrap func(NetworkCloser) (NetworkCloser, error),
) {
	links.BroadcastNetworkPort(w, node, port, func(link uint64) {
		emitWrappedNetwork(cache, w, node, link, source, wrap)
	})
}
