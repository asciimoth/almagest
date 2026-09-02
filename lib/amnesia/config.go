package amnesianodes

import (
	"errors"
	"net/url"
	"strings"

	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/formular"

	"github.com/asciimoth/almagest/lib/nodekit"
)

const (
	fieldEnabled                = "enabled"
	fieldConnectionSecret       = "connectionSecret"
	fieldServerCountryCode      = "serverCountryCode"
	fieldConnectEvent           = "connectEvent"
	fieldGatewayURL             = "gatewayURL"
	fieldGatewayPublicKeySecret = "gatewayPublicKeySecret"
	fieldStorageID              = "storageID"
)

type amnesiaConfig struct {
	enabled                bool
	connectionSecret       string
	serverCountryCode      string
	connectEvent           bool
	tlsConfigName          string
	gatewayURL             string
	gatewayPublicKeySecret string
	storageID              string
}

func defaultAmnesiaConfig() amnesiaConfig {
	return amnesiaConfig{enabled: true}
}

func readAmnesiaConfig(cfg configer.Config) amnesiaConfig {
	out := defaultAmnesiaConfig()
	if cfg == nil {
		return out
	}
	out.enabled = configer.Bool(cfg, configer.Path{fieldEnabled}, out.enabled)
	out.connectionSecret = configer.String(cfg, configer.Path{fieldConnectionSecret}, "")
	out.serverCountryCode = configer.String(cfg, configer.Path{fieldServerCountryCode}, "")
	out.connectEvent = configer.Bool(cfg, configer.Path{fieldConnectEvent}, false)
	out.tlsConfigName = nodekit.ReadTLSConfigName(cfg)
	out.gatewayURL = configer.String(cfg, configer.Path{fieldGatewayURL}, "")
	out.gatewayPublicKeySecret = configer.String(cfg, configer.Path{fieldGatewayPublicKeySecret}, "")
	out.storageID = configer.String(cfg, configer.Path{fieldStorageID}, "")
	out.normalize()
	return out
}

func (c *amnesiaConfig) normalize() {
	c.connectionSecret = strings.TrimSpace(c.connectionSecret)
	c.serverCountryCode = strings.ToLower(strings.TrimSpace(c.serverCountryCode))
	c.tlsConfigName = nodekit.NormalizeTLSConfigName(c.tlsConfigName)
	c.gatewayURL = strings.TrimSpace(c.gatewayURL)
	c.gatewayPublicKeySecret = strings.TrimSpace(c.gatewayPublicKeySecret)
	c.storageID = strings.TrimSpace(c.storageID)
}

func (c amnesiaConfig) equal(other amnesiaConfig) bool {
	return c == other
}

func applyAmnesiaConfigValues(c *amnesiaConfig, values map[string]any) {
	if value, ok := formular.BoolValue(values[fieldEnabled]); ok {
		c.enabled = value
	}
	if value, ok := formular.StringValue(values[fieldConnectionSecret]); ok {
		c.connectionSecret = value
	}
	if value, ok := formular.StringValue(values[fieldServerCountryCode]); ok {
		c.serverCountryCode = value
	}
	if value, ok := formular.BoolValue(values[fieldConnectEvent]); ok {
		c.connectEvent = value
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

func validateAmnesiaConfig(c amnesiaConfig) error {
	for _, fieldID := range []string{fieldConnectionSecret, fieldServerCountryCode, fieldGatewayURL} {
		if err := validateAmnesiaConfigField(c, fieldID); err != nil {
			return err
		}
	}
	return nil
}

func validateAmnesiaConfigField(c amnesiaConfig, fieldID string) error {
	switch fieldID {
	case fieldConnectionSecret:
		if c.connectionSecret == "" {
			return errors.New("connection secret is required")
		}
	case fieldServerCountryCode:
		if c.serverCountryCode != "" && (len(c.serverCountryCode) != 2 || !asciiLetters(c.serverCountryCode)) {
			return errors.New("server country must be an ISO 3166-1 alpha-2 code")
		}
	case fieldGatewayURL:
		if c.gatewayURL == "" {
			return nil
		}
		parsed, err := url.Parse(c.gatewayURL)
		if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
			return errors.New("gateway URL must have an http or https scheme and host")
		}
	}
	return nil
}

func asciiLetters(value string) bool {
	for _, char := range value {
		if (char < 'a' || char > 'z') && (char < 'A' || char > 'Z') {
			return false
		}
	}
	return true
}
