package amnesianodes

import (
	"strings"

	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/formular"

	"github.com/asciimoth/almagest/lib/nodekit"
)

// managedAmnesiaConfig contains only settings that belong to the combined
// Amnezia client and WireGuard device. WireGuard keys and device settings are
// deliberately absent because they are runtime-only profile data.
type managedAmnesiaConfig struct {
	enabled                bool
	connectionSecret       string
	serverCountryCode      string
	tlsConfigName          string
	gatewayURL             string
	gatewayPublicKeySecret string
	storageID              string
}

func defaultManagedAmnesiaConfig() managedAmnesiaConfig {
	return managedAmnesiaConfig{enabled: true}
}

func readManagedAmnesiaConfig(cfg configer.Config) managedAmnesiaConfig {
	out := defaultManagedAmnesiaConfig()
	if cfg == nil {
		return out
	}
	out.enabled = configer.Bool(cfg, configer.Path{fieldEnabled}, out.enabled)
	out.connectionSecret = configer.String(cfg, configer.Path{fieldConnectionSecret}, "")
	out.serverCountryCode = configer.String(cfg, configer.Path{fieldServerCountryCode}, "")
	out.tlsConfigName = nodekit.ReadTLSConfigName(cfg)
	out.gatewayURL = configer.String(cfg, configer.Path{fieldGatewayURL}, "")
	out.gatewayPublicKeySecret = configer.String(cfg, configer.Path{fieldGatewayPublicKeySecret}, "")
	out.storageID = configer.String(cfg, configer.Path{fieldStorageID}, "")
	out.normalize()
	return out
}

func (c *managedAmnesiaConfig) normalize() {
	c.connectionSecret = strings.TrimSpace(c.connectionSecret)
	c.serverCountryCode = strings.ToLower(strings.TrimSpace(c.serverCountryCode))
	c.tlsConfigName = nodekit.NormalizeTLSConfigName(c.tlsConfigName)
	c.gatewayURL = strings.TrimSpace(c.gatewayURL)
	c.gatewayPublicKeySecret = strings.TrimSpace(c.gatewayPublicKeySecret)
	c.storageID = strings.TrimSpace(c.storageID)
}

func (c managedAmnesiaConfig) commonConfig() amnesiaConfig {
	return amnesiaConfig{
		enabled:                c.enabled,
		connectionSecret:       c.connectionSecret,
		serverCountryCode:      c.serverCountryCode,
		tlsConfigName:          c.tlsConfigName,
		gatewayURL:             c.gatewayURL,
		gatewayPublicKeySecret: c.gatewayPublicKeySecret,
		storageID:              c.storageID,
	}
}

func applyManagedAmnesiaConfigValues(c *managedAmnesiaConfig, values map[string]any) {
	if value, ok := formular.BoolValue(values[fieldEnabled]); ok {
		c.enabled = value
	}
	if value, ok := formular.StringValue(values[fieldConnectionSecret]); ok {
		c.connectionSecret = value
	}
	if value, ok := formular.StringValue(values[fieldServerCountryCode]); ok {
		c.serverCountryCode = value
	}
	if value, ok := nodekit.TLSConfigValueFromAny(values[nodekit.TLSConfigFieldID]); ok {
		c.tlsConfigName = value
	}
	if value, ok := formular.StringValue(values[fieldGatewayURL]); ok {
		c.gatewayURL = value
	}
	if value, ok := formular.StringValue(values[fieldGatewayPublicKeySecret]); ok {
		c.gatewayPublicKeySecret = value
	}
	c.normalize()
}

func validateManagedAmnesiaConfig(c managedAmnesiaConfig) error {
	return validateAmnesiaConfig(c.commonConfig())
}

func validateManagedAmnesiaConfigField(c managedAmnesiaConfig, fieldID string) error {
	return validateAmnesiaConfigField(c.commonConfig(), fieldID)
}
