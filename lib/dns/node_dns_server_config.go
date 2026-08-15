package dnsnodes

import (
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"

	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/formular"
	"github.com/asciimoth/pasta/pasta"

	"github.com/asciimoth/almagest/lib/nodekit"
)

const (
	DefaultDNSServerNetwork = "udp"
	DefaultDNSServerHost    = "127.0.0.1"
	DefaultDNSServerPort    = 5353

	DNSServerConfigBlockID = "listen"
	DNSServerNetworkField  = "network"
	DNSServerHostField     = "host"
	DNSServerPortField     = "port"
)

type dnsServerConfig struct {
	network string
	host    string
	port    int
}

func defaultDNSServerConfig() dnsServerConfig {
	return dnsServerConfig{
		network: DefaultDNSServerNetwork,
		host:    DefaultDNSServerHost,
		port:    DefaultDNSServerPort,
	}
}

func (c *dnsServerConfig) read(cfg configer.Config) {
	if cfg == nil {
		return
	}
	c.network = configer.String(cfg, configer.Path{DNSServerNetworkField}, c.network)
	c.host = configer.String(cfg, configer.Path{DNSServerHostField}, c.host)
	c.port = configer.Int(cfg, configer.Path{DNSServerPortField}, c.port)
	c.normalize()
}

func (c *dnsServerConfig) normalize() {
	c.network = NormalizeDNSServerNetwork(c.network)
	c.host = strings.TrimSpace(c.host)
	if c.host == "" {
		c.host = defaultDNSServerHostForNetwork(c.network)
	}
	if c.port <= 0 || c.port > 65535 {
		c.port = DefaultDNSServerPort
	}
}

func (c dnsServerConfig) valid() error {
	if NormalizeDNSServerNetwork(c.network) != c.network {
		return fmt.Errorf("unsupported DNS server network %q", c.network)
	}
	if strings.TrimSpace(c.host) == "" {
		return errors.New("host must not be empty")
	}
	if strings.ContainsAny(c.host, " \t\r\n") {
		return errors.New("host must not contain whitespace")
	}
	if c.port <= 0 || c.port > 65535 {
		return errors.New("port must be between 1 and 65535")
	}
	return nil
}

func (c dnsServerConfig) addr() string {
	return net.JoinHostPort(c.host, strconv.Itoa(c.port))
}

func (c dnsServerConfig) copy() dnsServerConfig {
	return c
}

// NormalizeDNSServerNetwork normalizes a DNS server network name.
func NormalizeDNSServerNetwork(value string) string {
	switch strings.TrimSpace(value) {
	case "udp", "udp4", "udp6":
		return strings.TrimSpace(value)
	default:
		return DefaultDNSServerNetwork
	}
}

func defaultDNSServerHostForNetwork(network string) string {
	if network == "udp6" {
		return "::1"
	}
	return DefaultDNSServerHost
}

func (n *dnsServerNode) OnSave(cfg configer.Config) error {
	if err := pasta.DeleteNodeOwnedConfigKeys(cfg); err != nil {
		return err
	}
	return configer.SetMap(cfg, map[string]any{
		DNSServerNetworkField: n.config.network,
		DNSServerHostField:    n.config.host,
		DNSServerPortField:    n.config.port,
	})
}

func (n *dnsServerNode) applyConfigForm(values map[string]any) error {
	next := n.config.copy()
	if value, ok := formular.StringValue(values[DNSServerNetworkField]); ok {
		next.network = value
	}
	if value, ok := formular.StringValue(values[DNSServerHostField]); ok {
		next.host = value
	}
	if value, ok := formular.IntValue(values[DNSServerPortField]); ok {
		next.port = value
	}
	next.normalize()
	if err := next.valid(); err != nil {
		n.formError = err.Error()
		n.sendForcedMenuSnapshot()
		_ = n.updateLabel()
		return nil
	}
	changed := n.config != next || n.formError != ""
	n.config = next
	n.formError = ""
	n.sendForcedMenuSnapshot()
	if changed {
		n.restartServer()
	}
	return n.updateLabel()
}

func (n *dnsServerNode) sendConfigFieldStatus(msg formular.FieldValidateMessage) {
	nodekit.SendNodeFieldStatus(n.W, n.ID, msg.Field, validateDNSServerConfigField(msg))
}

func validateDNSServerConfigField(msg formular.FieldValidateMessage) error {
	switch msg.Field.FieldID {
	case DNSServerNetworkField:
		value, ok := formular.StringValue(msg.Value)
		if !ok {
			return nil
		}
		if NormalizeDNSServerNetwork(value) != strings.TrimSpace(value) {
			return fmt.Errorf("unsupported DNS server network %q", value)
		}
	case DNSServerHostField:
		value, ok := formular.StringValue(msg.Value)
		if !ok {
			return nil
		}
		value = strings.TrimSpace(value)
		if value == "" {
			return errors.New("host must not be empty")
		}
		if strings.ContainsAny(value, " \t\r\n") {
			return errors.New("host must not contain whitespace")
		}
	case DNSServerPortField:
		value, ok := formular.IntValue(msg.Value)
		if !ok {
			return nil
		}
		if value <= 0 || value > 65535 {
			return errors.New("port must be between 1 and 65535")
		}
	}
	return nil
}
