package debugnodes

import (
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/asciimoth/almagest/lib/nodekit"
	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/pasta/pasta"
	"github.com/asciimoth/pasta/pasta/std"
)

type udpPingConfig struct {
	network    string
	socketMode string
	localHost  string
	localPort  int
	remoteHost string
	remotePort int
	payload    string
	timeout    time.Duration
}

type udpPingConfigPorts struct {
	network    uint64
	socketMode uint64
	localHost  uint64
	localPort  uint64
	remoteHost uint64
	remotePort uint64
	payload    uint64
	timeout    uint64
}

func readUDPPingConfig(cfg configer.Config, server bool) udpPingConfig {
	localPort := defaultUDPPingClientPort
	remotePort := defaultUDPPingPort
	if server {
		localPort = defaultUDPPingPort
		remotePort = defaultUDPPingClientPort
	}
	out := udpPingConfig{
		network:    normalizeUDPPingNetwork(configer.String(cfg, configer.Path{"network"}, defaultUDPPingNetwork)),
		socketMode: normalizeUDPPingSocketMode(configer.String(cfg, configer.Path{"socket_mode"}, udpSocketListen)),
		localHost:  configer.String(cfg, configer.Path{"local_host"}, defaultUDPPingHost),
		localPort:  configer.Int(cfg, configer.Path{"local_port"}, localPort),
		remoteHost: configer.String(cfg, configer.Path{"remote_host"}, defaultUDPPingHost),
		remotePort: configer.Int(cfg, configer.Path{"remote_port"}, remotePort),
		payload:    configer.String(cfg, configer.Path{"payload"}, defaultUDPPingPayload),
		timeout:    time.Duration(configer.Int(cfg, configer.Path{"timeout_ms"}, defaultUDPPingTimeoutMS)) * time.Millisecond,
	}
	out.normalize(server)
	return out
}

func (c *udpPingConfig) normalize(server bool) {
	c.network = normalizeUDPPingNetwork(c.network)
	c.socketMode = normalizeUDPPingSocketMode(c.socketMode)
	if c.localHost == "" {
		c.localHost = hostForUDPNetwork(c.network)
	}
	if c.remoteHost == "" {
		c.remoteHost = hostForUDPNetwork(c.network)
	}
	if c.localPort < 0 || c.localPort > 65535 {
		if server {
			c.localPort = defaultUDPPingPort
		} else {
			c.localPort = defaultUDPPingClientPort
		}
	}
	if c.remotePort < 0 || c.remotePort > 65535 {
		c.remotePort = defaultUDPPingPort
	}
	if c.payload == "" {
		c.payload = defaultUDPPingPayload
	}
	if c.timeout <= 0 {
		c.timeout = defaultUDPPingTimeoutMS * time.Millisecond
	}
}

func normalizeUDPPingNetwork(value string) string {
	switch strings.TrimSpace(value) {
	case "udp", "udp4", "udp6":
		return strings.TrimSpace(value)
	default:
		return defaultUDPPingNetwork
	}
}

func normalizeUDPPingSocketMode(value string) string {
	switch strings.TrimSpace(value) {
	case udpSocketDial:
		return udpSocketDial
	default:
		return udpSocketListen
	}
}

func hostForUDPNetwork(network string) string {
	if network == "udp6" {
		return "::1"
	}
	return defaultUDPPingHost
}

func (c udpPingConfig) localAddr() string {
	return net.JoinHostPort(c.localHost, strconv.Itoa(c.localPort))
}

func (c udpPingConfig) remoteAddr() string {
	return net.JoinHostPort(c.remoteHost, strconv.Itoa(c.remotePort))
}

func (c udpPingConfig) label() string {
	return c.network + "/" + c.socketMode + " " + c.localAddr()
}

func (n *udpPingServerNode) requestConfig() {
	n.RequestPort(n.netp)
	for _, port := range n.configPorts() {
		n.RequestPort(port)
	}
}

func (n *udpPingClientNode) requestConfig() {
	n.RequestPort(n.netp)
	for _, port := range n.configPorts() {
		n.RequestPort(port)
	}
}

func (n *udpPingServerNode) configPorts() []uint64 {
	return []uint64{n.cfgp.network, n.cfgp.socketMode, n.cfgp.localHost, n.cfgp.localPort, n.cfgp.remoteHost, n.cfgp.remotePort}
}

func (n *udpPingClientNode) configPorts() []uint64 {
	return []uint64{n.cfgp.network, n.cfgp.socketMode, n.cfgp.localHost, n.cfgp.localPort, n.cfgp.remoteHost, n.cfgp.remotePort, n.cfgp.payload, n.cfgp.timeout}
}

func (n *udpPingServerNode) isConfigPort(port uint64) bool {
	for _, candidate := range n.configPorts() {
		if port == candidate {
			return true
		}
	}
	return false
}

func (n *udpPingClientNode) isConfigPort(port uint64) bool {
	for _, candidate := range n.configPorts() {
		if port == candidate {
			return true
		}
	}
	return false
}

func (n *udpPingServerNode) applyConfigString(port uint64, value string) bool {
	return applyUDPPingConfigString(&n.cfg, n.cfgp, port, value, true)
}

func (n *udpPingClientNode) applyConfigString(port uint64, value string) bool {
	return applyUDPPingConfigString(&n.cfg, n.cfgp, port, value, false)
}

func applyUDPPingConfigString(cfg *udpPingConfig, ports udpPingConfigPorts, port uint64, value string, server bool) bool {
	before := *cfg
	switch port {
	case ports.network:
		cfg.network = value
	case ports.socketMode:
		cfg.socketMode = value
	case ports.localHost:
		cfg.localHost = value
	case ports.localPort:
		cfg.localPort = parsePort(value, cfg.localPort)
	case ports.remoteHost:
		cfg.remoteHost = value
	case ports.remotePort:
		cfg.remotePort = parsePort(value, cfg.remotePort)
	case ports.payload:
		cfg.payload = value
	case ports.timeout:
		ms := parsePort(value, int(cfg.timeout/time.Millisecond))
		if ms > 0 {
			cfg.timeout = time.Duration(ms) * time.Millisecond
		}
	}
	cfg.normalize(server)
	return before != *cfg
}

func applyUDPPingForm(cfg *udpPingConfig, values map[string]any, server bool) bool {
	before := *cfg
	if value, ok := std.StringFromPayload(values["network"]); ok {
		cfg.network = value
	}
	if value, ok := std.StringFromPayload(values["socket_mode"]); ok {
		cfg.socketMode = value
	}
	if value, ok := std.StringFromPayload(values["local_host"]); ok {
		cfg.localHost = value
	}
	if value, ok := std.IntFromPayload(values["local_port"]); ok {
		cfg.localPort = value
	}
	if value, ok := std.StringFromPayload(values["remote_host"]); ok {
		cfg.remoteHost = value
	}
	if value, ok := std.IntFromPayload(values["remote_port"]); ok {
		cfg.remotePort = value
	}
	if value, ok := std.StringFromPayload(values["payload"]); ok {
		cfg.payload = value
	}
	if value, ok := std.IntFromPayload(values["timeout_ms"]); ok && value > 0 {
		cfg.timeout = time.Duration(value) * time.Millisecond
	}
	cfg.normalize(server)
	return before != *cfg
}

func saveUDPPingConfig(cfg configer.Config, value udpPingConfig) error {
	if err := pasta.DeleteNodeOwnedConfigKeys(cfg); err != nil {
		return err
	}
	return nodekit.SetConfigMap(cfg, map[string]any{
		"network":     value.network,
		"socket_mode": value.socketMode,
		"local_host":  value.localHost,
		"local_port":  value.localPort,
		"remote_host": value.remoteHost,
		"remote_port": value.remotePort,
		"payload":     value.payload,
		"timeout_ms":  int(value.timeout / time.Millisecond),
	})
}

func (n *udpPingServerNode) updateLabel() error {
	return n.SetLabel(n.cfg.label())
}

func (n *udpPingClientNode) updateLabel() error {
	return n.SetLabel(n.statusText)
}
