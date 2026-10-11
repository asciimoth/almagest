//go:build linux

// Package nativebackend defines native backend settings shared by direct and
// isolated startup. It contains no daemon, node, config engine, or IPC code.
package nativebackend

import (
	"encoding/json"
	"fmt"
	"net/netip"
	"strconv"
	"strings"
	"time"

	sysnetlinux "github.com/asciimoth/sysnet-linux"
	sysnetlinuxrouting "github.com/asciimoth/sysnet-linux/routing"
)

const LinuxDefaultUserMark uint32 = 0x414c4d01

type LinuxFileConfig struct {
	sysnetlinux.SystemConfig

	DNS           linuxDNSConfig   `json:"DNS"`
	Pmark         linuxPmarkConfig `json:"Pmark"`
	AppBypassMark linuxUint32      `json:"AppBypassMark"`
	AppBypassMask linuxUint32      `json:"AppBypassMask"`
	UserMark      linuxUint32      `json:"UserMark"`
	UserMarkMask  linuxUint32      `json:"UserMarkMask"`
	Logf          json.RawMessage  `json:"Logf,omitempty"`
	Callbacks     json.RawMessage
}

type linuxDNSConfig struct {
	Mode                   sysnetlinux.DNSMode `json:"Mode"`
	ResolvconfInterface    string              `json:"ResolvconfInterface"`
	ResolvedInterfaceIndex int                 `json:"ResolvedInterfaceIndex"`
	FallbackServers        []linuxAddrPort     `json:"FallbackServers"`
}

type linuxPmarkConfig struct {
	PinPath              string        `json:"PinPath"`
	TombCollectionEvents uint64        `json:"TombCollectionEvents"`
	TombTTL              time.Duration `json:"TombTTL"`
	Priority             int           `json:"Priority"`
}

type linuxAddrPort struct {
	netip.AddrPort
}

type linuxUint32 uint32

// NewLinuxFileConfig preserves daemon defaults before a protected config overlay.
func NewLinuxFileConfig() LinuxFileConfig {
	config := defaultLinuxSystemConfig()
	return LinuxFileConfig{
		SystemConfig: config, DNS: linuxDNSConfigFromSystem(config.DNS), Pmark: linuxPmarkConfigFromSystem(config.Pmark),
		AppBypassMark: linuxUint32(config.AppBypassMark), AppBypassMask: linuxUint32(config.AppBypassMask),
		UserMark: linuxUint32(config.UserMark), UserMarkMask: linuxUint32(config.UserMarkMask),
	}
}

// ParseLinuxFileConfig decodes the already selected System.Linux JSON object.
// Trusted bootstrap owns HuJSON normalization and duplicate-member checks.
// This does not create a backend or execute callbacks.
func ParseLinuxFileConfig(data []byte) (LinuxFileConfig, error) {
	config := NewLinuxFileConfig()
	if err := json.Unmarshal(data, &config); err != nil {
		return LinuxFileConfig{}, fmt.Errorf("system linux config: %w", err)
	}
	return config, nil
}

// Native resolves wire scalar types. Executable callbacks and logging are
// installed only by trusted callers; JSON cannot supply function values.
func (c LinuxFileConfig) Native(logf func(string, ...any)) sysnetlinux.SystemConfig {
	config := c.SystemConfig
	config.DNS = c.DNS.systemConfig()
	config.Pmark = c.Pmark.systemConfig()
	config.AppBypassMark, config.AppBypassMask = uint32(c.AppBypassMark), uint32(c.AppBypassMask)
	config.UserMark, config.UserMarkMask = uint32(c.UserMark), uint32(c.UserMarkMask)
	config.Logf = logf
	config.Callbacks = sysnetlinux.Callbacks{}
	return config
}

func defaultLinuxSystemConfig() sysnetlinux.SystemConfig {
	return sysnetlinux.SystemConfig{
		DNS: sysnetlinux.DNSConfig{
			FallbackServers: []netip.AddrPort{
				netip.MustParseAddrPort("8.8.8.8:53"),
				netip.MustParseAddrPort("1.1.1.1:53"),
			},
		},
		Pmark: sysnetlinux.PmarkConfig{
			PinPath: "/sys/fs/bpf/almagest",
		},
		AppBypassMark:          sysnetlinuxrouting.DefaultAppBypassMark,
		AppBypassMask:          sysnetlinuxrouting.DefaultMarkMask,
		UserMark:               LinuxDefaultUserMark,
		UserMarkMask:           sysnetlinuxrouting.DefaultMarkMask,
		KillswitchAllowExclude: true,
		Logf:                   nil,
	}
}

func linuxDNSConfigFromSystem(config sysnetlinux.DNSConfig) linuxDNSConfig {
	return linuxDNSConfig{
		Mode:                   config.Mode,
		ResolvconfInterface:    config.ResolvconfInterface,
		ResolvedInterfaceIndex: config.ResolvedInterfaceIndex,
		FallbackServers:        linuxAddrPortsFromSystem(config.FallbackServers),
	}
}

func linuxPmarkConfigFromSystem(config sysnetlinux.PmarkConfig) linuxPmarkConfig {
	return linuxPmarkConfig{
		PinPath:              config.PinPath,
		TombCollectionEvents: config.TombCollectionEvents,
		TombTTL:              config.TombTTL,
		Priority:             config.Priority,
	}
}

func (c linuxPmarkConfig) systemConfig() sysnetlinux.PmarkConfig {
	return sysnetlinux.PmarkConfig{
		PinPath:              c.PinPath,
		TombCollectionEvents: c.TombCollectionEvents,
		TombTTL:              c.TombTTL,
		Priority:             c.Priority,
	}
}

func linuxAddrPortsFromSystem(values []netip.AddrPort) []linuxAddrPort {
	out := make([]linuxAddrPort, 0, len(values))
	for _, value := range values {
		out = append(out, linuxAddrPort{AddrPort: value})
	}
	return out
}

func (c linuxDNSConfig) systemConfig() sysnetlinux.DNSConfig {
	return sysnetlinux.DNSConfig{
		Mode:                   c.Mode,
		ResolvconfInterface:    c.ResolvconfInterface,
		ResolvedInterfaceIndex: c.ResolvedInterfaceIndex,
		FallbackServers:        linuxAddrPortsToSystem(c.FallbackServers),
	}
}

func linuxAddrPortsToSystem(values []linuxAddrPort) []netip.AddrPort {
	out := make([]netip.AddrPort, 0, len(values))
	for _, value := range values {
		out = append(out, value.AddrPort)
	}
	return out
}

func (a *linuxAddrPort) UnmarshalJSON(data []byte) error {
	var value string
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	addrPort, err := parseLinuxAddrPort(value)
	if err != nil {
		return err
	}
	a.AddrPort = addrPort
	return nil
}

func parseLinuxAddrPort(value string) (netip.AddrPort, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return netip.AddrPort{}, fmt.Errorf("empty DNS fallback server")
	}
	if addrPort, err := netip.ParseAddrPort(value); err == nil {
		return addrPort, nil
	}
	addr, err := netip.ParseAddr(value)
	if err != nil {
		return netip.AddrPort{}, fmt.Errorf("DNS fallback server %q: %w", value, err)
	}
	return netip.AddrPortFrom(addr, 53), nil
}

func (v *linuxUint32) UnmarshalJSON(data []byte) error {
	if string(data) == "null" {
		return nil
	}
	var number uint32
	if len(data) == 0 || data[0] != '"' {
		if err := json.Unmarshal(data, &number); err != nil {
			return err
		}
		*v = linuxUint32(number)
		return nil
	}

	var value string
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	parsed, err := parseLinuxHexUint32(value)
	if err != nil {
		return err
	}
	*v = linuxUint32(parsed)
	return nil
}

func parseLinuxHexUint32(value string) (uint32, error) {
	value = strings.TrimSpace(value)
	value = strings.TrimPrefix(value, "0x")
	value = strings.TrimPrefix(value, "0X")
	if value == "" {
		return 0, fmt.Errorf("empty uint32 hex value")
	}
	parsed, err := strconv.ParseUint(value, 16, 32)
	if err != nil {
		return 0, fmt.Errorf("uint32 hex value %q: %w", value, err)
	}
	return uint32(parsed), nil
}
