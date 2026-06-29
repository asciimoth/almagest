package almagest

import (
	"fmt"
	"strings"

	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/formular"
	wgdevice "github.com/asciimoth/wgo/device"
)

type wireGuardAmneziaConfig struct {
	junkCount         int
	junkMin           int
	junkMax           int
	initHeader        string
	responseHeader    string
	cookieHeader      string
	transportHeader   string
	initPadding       int
	responsePadding   int
	cookiePadding     int
	transportPadding  int
	initiationPackets [wireGuardAmneziaInitiationPacketCount]string
}

const wireGuardAmneziaInitiationPacketCount = 5

type wireGuardAmneziaIntField struct {
	formID   string
	configID string
	label    string
	value    func(wireGuardAmneziaConfig) int
	set      func(*wireGuardAmneziaConfig, int)
}
type wireGuardAmneziaStringField struct {
	formID   string
	configID string
	label    string
	value    func(wireGuardAmneziaConfig) string
	set      func(*wireGuardAmneziaConfig, string)
}

var wireGuardAmneziaIntFieldSpecs = []wireGuardAmneziaIntField{
	{
		formID:   "amneziaJunkCount",
		configID: "junkCount",
		label:    "AWG Jc",
		value:    func(c wireGuardAmneziaConfig) int { return c.junkCount },
		set:      func(c *wireGuardAmneziaConfig, value int) { c.junkCount = value },
	},
	{
		formID:   "amneziaJunkMin",
		configID: "junkMin",
		label:    "AWG Jmin",
		value:    func(c wireGuardAmneziaConfig) int { return c.junkMin },
		set:      func(c *wireGuardAmneziaConfig, value int) { c.junkMin = value },
	},
	{
		formID:   "amneziaJunkMax",
		configID: "junkMax",
		label:    "AWG Jmax",
		value:    func(c wireGuardAmneziaConfig) int { return c.junkMax },
		set:      func(c *wireGuardAmneziaConfig, value int) { c.junkMax = value },
	},
	{
		formID:   "amneziaInitPadding",
		configID: "initPadding",
		label:    "AWG S1",
		value:    func(c wireGuardAmneziaConfig) int { return c.initPadding },
		set:      func(c *wireGuardAmneziaConfig, value int) { c.initPadding = value },
	},
	{
		formID:   "amneziaResponsePadding",
		configID: "responsePadding",
		label:    "AWG S2",
		value:    func(c wireGuardAmneziaConfig) int { return c.responsePadding },
		set:      func(c *wireGuardAmneziaConfig, value int) { c.responsePadding = value },
	},
	{
		formID:   "amneziaCookiePadding",
		configID: "cookiePadding",
		label:    "AWG S3",
		value:    func(c wireGuardAmneziaConfig) int { return c.cookiePadding },
		set:      func(c *wireGuardAmneziaConfig, value int) { c.cookiePadding = value },
	},
	{
		formID:   "amneziaTransportPadding",
		configID: "transportPadding",
		label:    "AWG S4",
		value:    func(c wireGuardAmneziaConfig) int { return c.transportPadding },
		set:      func(c *wireGuardAmneziaConfig, value int) { c.transportPadding = value },
	},
}
var wireGuardAmneziaStringFieldSpecs = []wireGuardAmneziaStringField{
	{
		formID:   "amneziaInitHeader",
		configID: "initHeader",
		label:    "AWG H1",
		value:    func(c wireGuardAmneziaConfig) string { return c.initHeader },
		set:      func(c *wireGuardAmneziaConfig, value string) { c.initHeader = value },
	},
	{
		formID:   "amneziaResponseHeader",
		configID: "responseHeader",
		label:    "AWG H2",
		value:    func(c wireGuardAmneziaConfig) string { return c.responseHeader },
		set:      func(c *wireGuardAmneziaConfig, value string) { c.responseHeader = value },
	},
	{
		formID:   "amneziaCookieHeader",
		configID: "cookieHeader",
		label:    "AWG H3",
		value:    func(c wireGuardAmneziaConfig) string { return c.cookieHeader },
		set:      func(c *wireGuardAmneziaConfig, value string) { c.cookieHeader = value },
	},
	{
		formID:   "amneziaTransportHeader",
		configID: "transportHeader",
		label:    "AWG H4",
		value:    func(c wireGuardAmneziaConfig) string { return c.transportHeader },
		set:      func(c *wireGuardAmneziaConfig, value string) { c.transportHeader = value },
	},
	wireGuardAmneziaPacketField(0, "amneziaInitiationPacket1", "AWG I1"),
	wireGuardAmneziaPacketField(1, "amneziaInitiationPacket2", "AWG I2"),
	wireGuardAmneziaPacketField(2, "amneziaInitiationPacket3", "AWG I3"),
	wireGuardAmneziaPacketField(3, "amneziaInitiationPacket4", "AWG I4"),
	wireGuardAmneziaPacketField(4, "amneziaInitiationPacket5", "AWG I5"),
}

func wireGuardAmneziaPacketField(index int, formID, label string) wireGuardAmneziaStringField {
	return wireGuardAmneziaStringField{
		formID: formID,
		label:  label,
		value: func(c wireGuardAmneziaConfig) string {
			return c.initiationPackets[index]
		},
		set: func(c *wireGuardAmneziaConfig, value string) {
			c.initiationPackets[index] = value
		},
	}
}
func defaultWireGuardAmneziaConfig() wireGuardAmneziaConfig {
	return wireGuardAmneziaConfigFromDevice(wgdevice.DefaultAmneziaWGConfig())
}
func wireGuardAmneziaConfigFromDevice(cfg wgdevice.AmneziaWGConfig) wireGuardAmneziaConfig {
	out := wireGuardAmneziaConfig{
		junkCount:        cfg.JunkCount,
		junkMin:          cfg.JunkMin,
		junkMax:          cfg.JunkMax,
		initHeader:       cfg.InitHeader.Spec(),
		responseHeader:   cfg.ResponseHeader.Spec(),
		cookieHeader:     cfg.CookieHeader.Spec(),
		transportHeader:  cfg.TransportHeader.Spec(),
		initPadding:      cfg.InitPadding,
		responsePadding:  cfg.ResponsePadding,
		cookiePadding:    cfg.CookiePadding,
		transportPadding: cfg.TransportPadding,
	}
	copy(out.initiationPackets[:], cfg.InitiationPackets[:])
	return out
}
func (c *wireGuardAmneziaConfig) normalize() {
	defaults := defaultWireGuardAmneziaConfig()
	c.initHeader = normalizeWireGuardAmneziaHeader(c.initHeader, defaults.initHeader)
	c.responseHeader = normalizeWireGuardAmneziaHeader(c.responseHeader, defaults.responseHeader)
	c.cookieHeader = normalizeWireGuardAmneziaHeader(c.cookieHeader, defaults.cookieHeader)
	c.transportHeader = normalizeWireGuardAmneziaHeader(c.transportHeader, defaults.transportHeader)
	for i := range c.initiationPackets {
		c.initiationPackets[i] = strings.TrimSpace(c.initiationPackets[i])
	}
}
func normalizeWireGuardAmneziaHeader(value, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	return value
}
func (c wireGuardAmneziaConfig) deviceConfig() (wgdevice.AmneziaWGConfig, error) {
	defaults := wgdevice.DefaultAmneziaWGConfig()
	cfg := defaults
	cfg.JunkCount = c.junkCount
	cfg.JunkMin = c.junkMin
	cfg.JunkMax = c.junkMax
	cfg.InitPadding = c.initPadding
	cfg.ResponsePadding = c.responsePadding
	cfg.CookiePadding = c.cookiePadding
	cfg.TransportPadding = c.transportPadding

	var err error
	if cfg.InitHeader, err = parseWireGuardAmneziaHeader(c.initHeader, defaults.InitHeader); err != nil {
		return wgdevice.AmneziaWGConfig{}, fmt.Errorf("init header: %w", err)
	}
	if cfg.ResponseHeader, err = parseWireGuardAmneziaHeader(c.responseHeader, defaults.ResponseHeader); err != nil {
		return wgdevice.AmneziaWGConfig{}, fmt.Errorf("response header: %w", err)
	}
	if cfg.CookieHeader, err = parseWireGuardAmneziaHeader(c.cookieHeader, defaults.CookieHeader); err != nil {
		return wgdevice.AmneziaWGConfig{}, fmt.Errorf("cookie header: %w", err)
	}
	if cfg.TransportHeader, err = parseWireGuardAmneziaHeader(c.transportHeader, defaults.TransportHeader); err != nil {
		return wgdevice.AmneziaWGConfig{}, fmt.Errorf("transport header: %w", err)
	}
	copy(cfg.InitiationPackets[:], c.initiationPackets[:])
	return cfg, nil
}
func parseWireGuardAmneziaHeader(value string, fallback wgdevice.AmneziaWGHeaderRange) (wgdevice.AmneziaWGHeaderRange, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback, nil
	}
	return wgdevice.ParseAmneziaWGHeaderRange(value)
}
func applyWireGuardAmneziaValues(config *wireGuardAmneziaConfig, values map[string]any) {
	for _, spec := range wireGuardAmneziaIntFieldSpecs {
		if value, ok := formular.IntValue(values[spec.formID]); ok {
			spec.set(config, value)
		}
	}
	for _, spec := range wireGuardAmneziaStringFieldSpecs {
		if value, ok := formular.StringValue(values[spec.formID]); ok {
			spec.set(config, value)
		}
	}
}
func applyWireGuardAmneziaValue(config *wireGuardAmneziaConfig, fieldID string, raw any) bool {
	for _, spec := range wireGuardAmneziaIntFieldSpecs {
		if fieldID != spec.formID {
			continue
		}
		if value, ok := formular.IntValue(raw); ok {
			spec.set(config, value)
		}
		return true
	}
	for _, spec := range wireGuardAmneziaStringFieldSpecs {
		if fieldID != spec.formID {
			continue
		}
		if value, ok := formular.StringValue(raw); ok {
			spec.set(config, value)
		}
		return true
	}
	return false
}
func wireGuardAmneziaConfigMap(config wireGuardAmneziaConfig) map[string]any {
	values := make(map[string]any, len(wireGuardAmneziaIntFieldSpecs)+len(wireGuardAmneziaStringFieldSpecs))
	for _, spec := range wireGuardAmneziaIntFieldSpecs {
		values[spec.configID] = spec.value(config)
	}
	for _, spec := range wireGuardAmneziaStringFieldSpecs {
		if spec.configID != "" {
			values[spec.configID] = spec.value(config)
		}
	}
	values["initiationPackets"] = wireGuardAmneziaInitiationPacketsConfig(config.initiationPackets)
	return values
}
func wireGuardAmneziaInitiationPacketsConfig(packets [wireGuardAmneziaInitiationPacketCount]string) []string {
	out := make([]string, len(packets))
	copy(out, packets[:])
	return out
}
func readWireGuardAmneziaConfig(cfg configer.Config, fallback wireGuardAmneziaConfig) wireGuardAmneziaConfig {
	raw, err := cfg.Get(configer.Path{"amneziaWG"})
	if err != nil {
		return fallback
	}
	return readWireGuardAmneziaConfigMap(raw, fallback)
}
func readWireGuardAmneziaConfigMap(raw any, fallback wireGuardAmneziaConfig) wireGuardAmneziaConfig {
	values, ok := configer.MapStringAny(raw)
	if !ok {
		return fallback
	}
	out := fallback
	for _, spec := range wireGuardAmneziaIntFieldSpecs {
		if value, ok := formular.IntValue(values[spec.configID]); ok {
			spec.set(&out, value)
		}
	}
	for _, spec := range wireGuardAmneziaStringFieldSpecs {
		if spec.configID == "" {
			continue
		}
		if value, ok := formular.StringValue(values[spec.configID]); ok {
			spec.set(&out, value)
		}
	}
	if packets, ok := wireGuardAmneziaInitiationPacketsFromAny(values["initiationPackets"]); ok {
		out.initiationPackets = packets
	}
	out.normalize()
	return out
}
func wireGuardAmneziaInitiationPacketsFromAny(raw any) ([wireGuardAmneziaInitiationPacketCount]string, bool) {
	var out [wireGuardAmneziaInitiationPacketCount]string
	switch values := raw.(type) {
	case []string:
		for i, value := range values {
			if i >= len(out) {
				break
			}
			out[i] = value
		}
		return out, true
	case []any:
		for i, rawValue := range values {
			if i >= len(out) {
				break
			}
			out[i], _ = formular.StringValue(rawValue)
		}
		return out, true
	default:
		return out, false
	}
}
