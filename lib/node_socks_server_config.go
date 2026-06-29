package almagest

import (
	"errors"
	"fmt"
	"maps"
	"net"
	"strconv"
	"strings"

	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/formular"
	"github.com/asciimoth/pasta/pasta"
	"github.com/asciimoth/socksgo/protocol"
)

type socksServerConfig struct {
	socksHost string
	socksPort int
	wsHost    string
	wsPort    int
	wsPath    string
	users     []socksServerUser
	commands  map[protocol.Cmd]bool
}

type socksServerUser struct {
	id       string
	username string
	password string
}

func defaultSocksServerConfig() socksServerConfig {
	return socksServerConfig{
		socksHost: DefaultSocksServerHost,
		socksPort: DefaultSocksServerPort,
		wsPath:    DefaultSocksServerWSPath,
		commands:  defaultSocksServerCommands(),
	}
}

func defaultSocksServerCommands() map[protocol.Cmd]bool {
	return map[protocol.Cmd]bool{
		protocol.CmdConnect:       true,
		protocol.CmdBind:          true,
		protocol.CmdUDPAssoc:      true,
		protocol.CmdTorResolve:    true,
		protocol.CmdTorResolvePtr: true,
		protocol.CmdGostMuxBind:   true,
		protocol.CmdGostUDPTun:    true,
	}
}

func (c *socksServerConfig) read(cfg configer.Config) {
	if cfg == nil {
		return
	}
	c.socksHost = configer.String(cfg, configer.Path{"socksHost"}, c.socksHost)
	c.socksPort = configer.Int(cfg, configer.Path{"socksPort"}, c.socksPort)
	c.wsHost = configer.String(cfg, configer.Path{"wsHost"}, c.wsHost)
	c.wsPort = configer.Int(cfg, configer.Path{"wsPort"}, c.wsPort)
	c.wsPath = normalizeSocksServerPath(configer.String(cfg, configer.Path{"wsPath"}, c.wsPath))
	if c.wsPath == "" {
		c.wsPath = DefaultSocksServerWSPath
	}
	if users, ok := readSocksServerUsers(cfg); ok {
		c.users = users
	}
	if commands, ok := readSocksServerCommands(cfg); ok {
		c.commands = commands
	}
}

func (c socksServerConfig) validate() error {
	if c.socksPort < 0 || c.socksPort > 65535 {
		return errors.New("SOCKS port must be between 0 and 65535")
	}
	if c.wsPort < 0 || c.wsPort > 65535 {
		return errors.New("SOCKS+WS port must be between 0 and 65535")
	}
	if c.sameListenAddress() {
		return errors.New("SOCKS and SOCKS+WS listeners must not use the same host and port")
	}
	return nil
}

func (c socksServerConfig) sameListenAddress() bool {
	return c.socksPort > 0 && c.wsPort > 0 && c.socksPort == c.wsPort && hostOrDefault(c.socksHost) == hostOrDefault(c.wsHost)
}

func (c socksServerConfig) hasListener() bool {
	return c.socksPort > 0 || c.wsPort > 0
}

func saveSocksServerConfig(cfg configer.Config, config socksServerConfig) error {
	if err := pasta.DeleteNodeOwnedConfigKeys(cfg); err != nil {
		return err
	}
	return configer.SetMap(cfg, map[string]any{
		"socksHost": config.socksHost,
		"socksPort": config.socksPort,
		"wsHost":    config.wsHost,
		"wsPort":    config.wsPort,
		"wsPath":    config.wsPath,
		"users":     socksServerUsersConfig(config.users),
		"commands":  socksServerCommandsConfig(config.commands),
	})
}

func socksServerConfigWithValues(current socksServerConfig, values map[string]any) socksServerConfig {
	next := current
	next.commands = maps.Clone(current.commands)
	if value, ok := formular.StringValue(values["socksHost"]); ok {
		next.socksHost = strings.TrimSpace(value)
	}
	if value, ok := formular.IntValue(values["socksPort"]); ok {
		next.socksPort = validPort(value)
	}
	if value, ok := formular.StringValue(values["wsHost"]); ok {
		next.wsHost = strings.TrimSpace(value)
	}
	if value, ok := formular.IntValue(values["wsPort"]); ok {
		next.wsPort = validPort(value)
	}
	if value, ok := formular.StringValue(values["wsPath"]); ok {
		next.wsPath = normalizeSocksServerPath(value)
	}
	if users, ok := parseSocksServerUsers(values["users"]); ok {
		next.users = users
	}
	for cmd, field := range socksServerCommandFields() {
		if value, ok := formular.BoolValue(values[field]); ok {
			next.commands[cmd] = value
		}
	}
	if next.wsPath == "" {
		next.wsPath = DefaultSocksServerWSPath
	}
	return next
}

func socksServerConfigWithFieldValue(current socksServerConfig, msg formular.FieldValidateMessage) (socksServerConfig, bool) {
	next := current
	switch msg.Field.FieldID {
	case "socksHost":
		if value, ok := formular.StringValue(msg.Value); ok {
			next.socksHost = strings.TrimSpace(value)
		}
	case "socksPort":
		if value, ok := formular.IntValue(msg.Value); ok {
			next.socksPort = value
		}
	case "wsHost":
		if value, ok := formular.StringValue(msg.Value); ok {
			next.wsHost = strings.TrimSpace(value)
		}
	case "wsPort":
		if value, ok := formular.IntValue(msg.Value); ok {
			next.wsPort = value
		}
	case "wsPath":
		if value, ok := formular.StringValue(msg.Value); ok {
			next.wsPath = normalizeSocksServerPath(value)
		}
	default:
		return current, false
	}
	if next.wsPath == "" {
		next.wsPath = DefaultSocksServerWSPath
	}
	return next, true
}

func socksServerConfigLabel(config socksServerConfig) string {
	if config.socksPort > 0 {
		return net.JoinHostPort(hostOrDefault(config.socksHost), strconv.Itoa(config.socksPort))
	}
	if config.wsPort > 0 {
		return "ws " + net.JoinHostPort(hostOrDefault(config.wsHost), strconv.Itoa(config.wsPort))
	}
	return "disabled"
}

func socksServerCommandFields() map[protocol.Cmd]string {
	return map[protocol.Cmd]string{
		protocol.CmdConnect:       "cmdConnect",
		protocol.CmdBind:          "cmdBind",
		protocol.CmdUDPAssoc:      "cmdUDPAssoc",
		protocol.CmdTorResolve:    "cmdTorResolve",
		protocol.CmdTorResolvePtr: "cmdTorResolvePtr",
		protocol.CmdGostMuxBind:   "cmdGostMuxBind",
		protocol.CmdGostUDPTun:    "cmdGostUDPTun",
	}
}

func parseSocksServerUsers(raw any) ([]socksServerUser, bool) {
	switch values := raw.(type) {
	case []formular.ArrayElementValue:
		return socksServerUsersFromArray(values), true
	case []any:
		array := make([]formular.ArrayElementValue, 0, len(values))
		for _, rawElement := range values {
			element, ok := formular.ArrayElementValueFromAny(rawElement)
			if !ok {
				continue
			}
			array = append(array, element)
		}
		return socksServerUsersFromArray(array), true
	default:
		return nil, false
	}
}

func socksServerUsersFromArray(values []formular.ArrayElementValue) []socksServerUser {
	out := make([]socksServerUser, 0, len(values))
	seen := map[string]struct{}{}
	for i, value := range values {
		if value.Template != "" && value.Template != "user" {
			continue
		}
		username, _ := formular.StringValue(value.Values["username"])
		password, _ := formular.StringValue(value.Values["password"])
		username = strings.TrimSpace(username)
		if username == "" {
			continue
		}
		id := value.ID
		if id == "" {
			id = fmt.Sprintf("user-%d", i+1)
		}
		if _, ok := seen[id]; ok {
			id = fmt.Sprintf("%s-%d", id, i+1)
		}
		seen[id] = struct{}{}
		out = append(out, socksServerUser{id: id, username: username, password: password})
	}
	return out
}

func socksServerUsersConfig(users []socksServerUser) []map[string]any {
	out := make([]map[string]any, 0, len(users))
	for _, user := range users {
		out = append(out, map[string]any{"id": user.id, "username": user.username, "password": user.password})
	}
	return out
}

func readSocksServerUsers(cfg configer.Config) ([]socksServerUser, bool) {
	raw, err := cfg.Get(configer.Path{"users"})
	if err != nil {
		return nil, false
	}
	switch values := raw.(type) {
	case []map[string]any:
		out := make([]socksServerUser, 0, len(values))
		for i, value := range values {
			out = append(out, socksServerUserFromConfigMap(i, value))
		}
		return compactSocksServerUsers(out), true
	case []any:
		out := make([]socksServerUser, 0, len(values))
		for i, rawValue := range values {
			if value, ok := rawValue.(map[string]any); ok {
				out = append(out, socksServerUserFromConfigMap(i, value))
			}
		}
		return compactSocksServerUsers(out), true
	default:
		return nil, false
	}
}

func socksServerUserFromConfigMap(index int, value map[string]any) socksServerUser {
	id, _ := formular.StringValue(value["id"])
	username, _ := formular.StringValue(value["username"])
	password, _ := formular.StringValue(value["password"])
	if id == "" {
		id = fmt.Sprintf("user-%d", index+1)
	}
	return socksServerUser{id: id, username: strings.TrimSpace(username), password: password}
}

func compactSocksServerUsers(users []socksServerUser) []socksServerUser {
	out := users[:0]
	for _, user := range users {
		if user.username != "" {
			out = append(out, user)
		}
	}
	return out
}

func socksServerCommandsConfig(commands map[protocol.Cmd]bool) map[string]bool {
	out := make(map[string]bool, len(commands))
	for cmd, field := range socksServerCommandFields() {
		out[field] = commands[cmd]
	}
	return out
}

func readSocksServerCommands(cfg configer.Config) (map[protocol.Cmd]bool, bool) {
	raw, err := cfg.Get(configer.Path{"commands"})
	if err != nil {
		return nil, false
	}
	values, ok := raw.(map[string]any)
	if ok {
		out := defaultSocksServerCommands()
		for cmd, field := range socksServerCommandFields() {
			if value, ok := formular.BoolValue(values[field]); ok {
				out[cmd] = value
			}
		}
		return out, true
	}
	boolValues, ok := raw.(map[string]bool)
	if !ok {
		return nil, false
	}
	out := defaultSocksServerCommands()
	for cmd, field := range socksServerCommandFields() {
		if value, ok := boolValues[field]; ok {
			out[cmd] = value
		}
	}
	return out, true
}

func normalizeSocksServerPath(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if !strings.HasPrefix(value, "/") {
		value = "/" + value
	}
	return value
}

func hostOrDefault(host string) string {
	host = strings.TrimSpace(host)
	if host == "" {
		return DefaultSocksServerHost
	}
	return host
}

func validPort(value int) int {
	if value < 0 || value > 65535 {
		return 0
	}
	return value
}

func socksAuthSummary(info protocol.AuthInfo) string {
	switch info.Code {
	case protocol.NoAuthCode:
		return "none"
	case protocol.PassAuthCode:
		if user, ok := info.Info["user"].(string); ok {
			return "user:" + user
		}
		return "userpass"
	default:
		return strconv.Itoa(int(info.Code))
	}
}
