package tunnodes

import (
	"errors"
	"fmt"
	"net/netip"
	"strings"

	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/formular"
)

type vtunConfig struct {
	name       string
	localAddrs string
	dnsServers string
	mtu        int
	endpointCh int
	eventCh    int
	// mwo            int
	// mro            int
	noLoopbackAddr bool
	disableSACK    bool
}

func defaultVTunConfig() vtunConfig {
	return vtunConfig{
		name:       defaultVTunName,
		mtu:        0,
		endpointCh: defaultVTunEndpointCh,
		eventCh:    defaultVTunEventCh,
	}
}

func (c *vtunConfig) read(cfg configer.Config) {
	if cfg == nil {
		return
	}
	c.name = configer.String(cfg, configer.Path{"name"}, c.name)
	c.localAddrs = configer.String(cfg, configer.Path{"localAddrs"}, c.localAddrs)
	c.dnsServers = configer.String(cfg, configer.Path{"dnsServers"}, c.dnsServers)
	c.mtu = configer.Int(cfg, configer.Path{"mtu"}, c.mtu)
	c.endpointCh = configer.Int(cfg, configer.Path{"endpointCh"}, c.endpointCh)
	c.eventCh = configer.Int(cfg, configer.Path{"eventCh"}, c.eventCh)
	// c.mwo = configer.Int(cfg, configer.Path{"mwo"}, c.mwo)
	// c.mro = configer.Int(cfg, configer.Path{"mro"}, c.mro)
	c.noLoopbackAddr = configer.Bool(cfg, configer.Path{"noLoopbackAddr"}, c.noLoopbackAddr)
	c.disableSACK = configer.Bool(cfg, configer.Path{"disableSACK"}, c.disableSACK)
	c.normalize()
}

func (c *vtunConfig) normalize() {
	c.name = strings.TrimSpace(c.name)
	if c.name == "" {
		c.name = defaultVTunName
	}
	c.localAddrs = normalizeListText(c.localAddrs)
	c.dnsServers = normalizeListText(c.dnsServers)
	if c.endpointCh <= 0 {
		c.endpointCh = defaultVTunEndpointCh
	}
	if c.eventCh <= 0 {
		c.eventCh = defaultVTunEventCh
	}
}

func validateVTunConfig(config vtunConfig) error {
	if _, err := parseAddrList(config.localAddrs, true); err != nil {
		return fmt.Errorf("local addrs: %w", err)
	}
	if _, err := parseAddrList(config.dnsServers, false); err != nil {
		return fmt.Errorf("DNS servers: %w", err)
	}
	if config.mtu < 0 {
		return errors.New("MTU must not be negative")
	}
	if config.endpointCh <= 0 {
		return errors.New("endpoint channel size must be positive")
	}
	if config.eventCh <= 0 {
		return errors.New("event channel size must be positive")
	}
	return nil
}

func (n *vtunNode) sendConfigFieldStatus(msg formular.FieldValidateMessage) {
	sendNodeFieldStatus(n.W, n.ID, msg.Field, validateVTunConfigField(msg))
}

func validateVTunConfigField(msg formular.FieldValidateMessage) error {
	switch msg.Field.FieldID {
	case "name":
		return nil
	case "localAddrs":
		value, _ := formular.StringValue(msg.Value)
		if _, err := parseAddrList(value, true); err != nil {
			return fmt.Errorf("local addrs: %w", err)
		}
	case "dnsServers":
		value, _ := formular.StringValue(msg.Value)
		if _, err := parseAddrList(value, false); err != nil {
			return fmt.Errorf("DNS servers: %w", err)
		}
	case "mtu":
		value, ok := formular.IntValue(msg.Value)
		if !ok || value < 0 {
			return errors.New("MTU must not be negative")
		}
	case "endpointCh":
		value, ok := formular.IntValue(msg.Value)
		if !ok || value <= 0 {
			return errors.New("endpoint channel size must be positive")
		}
	case "eventCh":
		value, ok := formular.IntValue(msg.Value)
		if !ok || value <= 0 {
			return errors.New("event channel size must be positive")
		}
	default:
		return nil
	}
	return nil
}

func tunConfigAddrs(cfg TunConfig) []netip.Addr {
	out := make([]netip.Addr, 0, len(cfg.Addrs))
	for _, value := range cfg.Addrs {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if prefix, err := netip.ParsePrefix(value); err == nil {
			out = append(out, prefix.Addr())
			continue
		}
		if addr, err := netip.ParseAddr(value); err == nil {
			out = append(out, addr)
		}
	}
	return out
}
