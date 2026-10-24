package tailscalenodes

import (
	"errors"
	"net/url"
	"strings"

	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/formular"
	wgotailscale "github.com/asciimoth/wgo-tailscale"

	"github.com/asciimoth/almagest/lib/nodekit"
)

const (
	fieldEnabled          = "enabled"
	fieldControlURL       = "controlURL"
	fieldHostname         = "hostname"
	fieldAuthKeySecret    = "authKeySecret"
	fieldEphemeral        = "ephemeral"
	fieldConfirmPeers     = "confirmPeers"
	fieldListenPort       = "listenPort"
	fieldDisableDERP      = "disableDERP"
	fieldDisableDiscovery = "disableDiscovery"
	fieldStorageID        = "storageID"
)

type tailscaleConfig struct {
	enabled          bool
	controlURL       string
	hostname         string
	authKeySecret    string
	ephemeral        bool
	confirmPeers     bool
	listenPort       int
	disableDERP      bool
	disableDiscovery bool
	tlsConfigName    string
	storageID        string
}

func defaultTailscaleConfig() tailscaleConfig {
	return tailscaleConfig{
		enabled:    true,
		controlURL: wgotailscale.DefaultControlURL,
	}
}

func readTailscaleConfig(cfg configer.Config) tailscaleConfig {
	out := defaultTailscaleConfig()
	if cfg == nil {
		return out
	}
	out.enabled = configer.Bool(cfg, configer.Path{fieldEnabled}, out.enabled)
	out.controlURL = configer.String(cfg, configer.Path{fieldControlURL}, out.controlURL)
	out.hostname = configer.String(cfg, configer.Path{fieldHostname}, "")
	out.authKeySecret = configer.String(cfg, configer.Path{fieldAuthKeySecret}, "")
	out.ephemeral = configer.Bool(cfg, configer.Path{fieldEphemeral}, false)
	out.confirmPeers = configer.Bool(cfg, configer.Path{fieldConfirmPeers}, false)
	out.listenPort = configer.Int(cfg, configer.Path{fieldListenPort}, 0)
	out.disableDERP = configer.Bool(cfg, configer.Path{fieldDisableDERP}, false)
	out.disableDiscovery = configer.Bool(cfg, configer.Path{fieldDisableDiscovery}, false)
	out.tlsConfigName = nodekit.ReadTLSConfigName(cfg)
	out.storageID = configer.String(cfg, configer.Path{fieldStorageID}, "")
	out.normalize()
	return out
}

func (c *tailscaleConfig) normalize() {
	c.controlURL = strings.TrimRight(strings.TrimSpace(c.controlURL), "/")
	c.hostname = strings.TrimSpace(c.hostname)
	c.authKeySecret = strings.TrimSpace(c.authKeySecret)
	c.tlsConfigName = nodekit.NormalizeTLSConfigName(c.tlsConfigName)
	c.storageID = strings.TrimSpace(c.storageID)
}

func applyTailscaleConfigValues(c *tailscaleConfig, values map[string]any) {
	if value, ok := formular.BoolValue(values[fieldEnabled]); ok {
		c.enabled = value
	}
	if value, ok := formular.StringValue(values[fieldControlURL]); ok {
		c.controlURL = value
	}
	if value, ok := formular.StringValue(values[fieldHostname]); ok {
		c.hostname = value
	}
	if value, ok := formular.StringValue(values[fieldAuthKeySecret]); ok {
		c.authKeySecret = value
	}
	if value, ok := formular.BoolValue(values[fieldEphemeral]); ok {
		c.ephemeral = value
	}
	if value, ok := formular.BoolValue(values[fieldConfirmPeers]); ok {
		c.confirmPeers = value
	}
	if value, ok := formular.IntValue(values[fieldListenPort]); ok {
		c.listenPort = value
	}
	if value, ok := formular.BoolValue(values[fieldDisableDERP]); ok {
		c.disableDERP = value
	}
	if value, ok := formular.BoolValue(values[fieldDisableDiscovery]); ok {
		c.disableDiscovery = value
	}
	if value, ok := nodekit.TLSConfigValueFromAny(values[nodekit.TLSConfigFieldID]); ok {
		c.tlsConfigName = value
	}
	c.normalize()
}

func validateTailscaleConfig(c tailscaleConfig) error {
	for _, field := range []string{fieldControlURL, fieldListenPort} {
		if err := validateTailscaleConfigField(c, field); err != nil {
			return err
		}
	}
	return nil
}

func validateTailscaleConfigField(c tailscaleConfig, field string) error {
	switch field {
	case fieldControlURL:
		parsed, err := url.Parse(c.controlURL)
		if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
			return errors.New("control URL must have an http or https scheme and host")
		}
	case fieldListenPort:
		if c.listenPort < 0 || c.listenPort > 65535 {
			return errors.New("listen port must be between 0 and 65535")
		}
	}
	return nil
}
