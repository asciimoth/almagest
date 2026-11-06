package socksnodes

import (
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"

	"github.com/asciimoth/almagest/lib/nodekit"
	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/formular"
	"github.com/asciimoth/pasta/pasta"
	ss "golang.getoutline.org/sdk/transport/shadowsocks"
)

const (
	NodeTypeShadowsocksClient = "almagest/ShadowsocksClient"
	NodeTypeShadowsocksServer = "almagest/ShadowsocksServer"
	DefaultShadowsocksMethod  = "chacha20-ietf-poly1305"
	DefaultShadowsocksAddress = "127.0.0.1:8388"
)

type shadowsocksConfig struct {
	address, method, password, passwordSecret string
	tcp, udp                                  bool
}

func defaultShadowsocksConfig() shadowsocksConfig {
	return shadowsocksConfig{address: DefaultShadowsocksAddress, method: DefaultShadowsocksMethod, tcp: true, udp: true}
}
func readShadowsocksConfig(cfg configer.Config) shadowsocksConfig {
	c := defaultShadowsocksConfig()
	c.address = configer.String(cfg, configer.Path{"address"}, c.address)
	c.method = configer.String(cfg, configer.Path{"method"}, c.method)
	c.password = configer.String(cfg, configer.Path{"password"}, "")
	c.passwordSecret = configer.String(cfg, configer.Path{"passwordSecret"}, "")
	c.tcp = configer.Bool(cfg, configer.Path{"tcp"}, c.tcp)
	c.udp = configer.Bool(cfg, configer.Path{"udp"}, c.udp)
	return c
}
func (c shadowsocksConfig) validate() error {
	if _, err := validateShadowsocksAddress(c.address); err != nil {
		return err
	}
	if strings.TrimSpace(c.method) == "" {
		return errors.New("cipher is required")
	}
	if _, err := ss.NewEncryptionKey(c.method, "test"); err != nil {
		return fmt.Errorf("cipher: %w", err)
	}
	if c.password == "" && c.passwordSecret == "" {
		return errors.New("password or password secret is required")
	}
	if !c.tcp && !c.udp {
		return errors.New("enable TCP or UDP")
	}
	return nil
}
func (c shadowsocksConfig) resolved(base *NodeBase) shadowsocksConfig {
	if secret, ok := socksSecretValue(base, c.passwordSecret); ok {
		c.password = secret
	}
	return c
}
func (c shadowsocksConfig) values() map[string]any {
	return map[string]any{"address": c.address, "method": c.method, "password": c.password, "passwordSecret": c.passwordSecret, "tcp": c.tcp, "udp": c.udp}
}
func (c shadowsocksConfig) withValues(values map[string]any) shadowsocksConfig {
	if s, ok := formular.StringValue(values["address"]); ok {
		c.address = strings.TrimSpace(s)
	}
	if s, ok := formular.StringValue(values["method"]); ok {
		c.method = strings.TrimSpace(s)
	}
	if s, ok := formular.StringValue(values["password"]); ok {
		c.password = s
	}
	if s, ok := formular.StringValue(values["passwordSecret"]); ok {
		c.passwordSecret = strings.TrimSpace(s)
	}
	if b, ok := formular.BoolValue(values["tcp"]); ok {
		c.tcp = b
	}
	if b, ok := formular.BoolValue(values["udp"]); ok {
		c.udp = b
	}
	return c
}
func (c shadowsocksConfig) configBlock(errText string, server bool) formular.Block {
	label := "Server address"
	if server {
		label = "Listen address"
	}
	status := formular.StatusOK
	if errText != "" {
		status = formular.StatusError
	}
	address := formular.TextField("address", label, c.address, formular.Validation, formular.Status(status), formular.StatusText(errText))
	method := formular.TextField("method", "Cipher", c.method, formular.AllowedValues("chacha20-ietf-poly1305", "aes-128-gcm", "aes-192-gcm", "aes-256-gcm"))
	return formular.Block{ID: "config", Order: 10, Generation: 1, Form: true, Items: []formular.Item{
		address, method,
		formular.SecretTextField("password", "Password", c.password),
		nodekit.ConfigSecretNameField("passwordSecret", "Password secret", c.passwordSecret, false, ""),
		formular.CheckboxField("tcp", "TCP", c.tcp),
		formular.CheckboxField("udp", "UDP", c.udp),
	}}
}
func saveShadowsocksConfig(cfg configer.Config, c shadowsocksConfig) error {
	if err := pasta.DeleteNodeOwnedConfigKeys(cfg); err != nil {
		return err
	}
	return nodekit.SetConfigMap(cfg, c.values())
}
func (c shadowsocksConfig) listenAddress() string {
	host, port, err := net.SplitHostPort(c.address)
	if err != nil {
		return c.address
	}
	number, err := strconv.Atoi(port)
	if err != nil {
		return c.address
	}
	return net.JoinHostPort(host, strconv.Itoa(number))
}
func shadowsocksErrorText(formError, runtimeError string) string {
	if formError != "" {
		return formError
	}
	return runtimeError
}
