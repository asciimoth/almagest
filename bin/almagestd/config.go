package main

import (
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/asciimoth/almagest/bin/almagestd/args"
	cmdrunner "github.com/asciimoth/almagest/bin/almagestd/command"
	"github.com/asciimoth/almagest/bin/api"
	almagest "github.com/asciimoth/almagest/lib"
	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/configer/hujson"
	tailscalehujson "github.com/tailscale/hujson"
)

var (
	ErrConfigRO = errors.New("config is read-only")
)

type Config struct {
	Main      *hujson.Config
	Protected ProtectedConfig
}

type saveConfig struct {
	IdleMilliseconds int64 `json:"IdleMilliseconds"`
}

type LogCacheConfig struct {
	Debug int `json:"Debug"`
	Info  int `json:"Info"`
	Warn  int `json:"Warn"`
	Error int `json:"Error"`
	Fatal int `json:"Fatal"`
}

func (c *Config) MainToBytes() ([]byte, error) {
	return tailscalehujson.Format(c.Main.Pack())
}

func (c *Config) LogCache() LogCacheConfig {
	cfg := LogCacheConfig{
		Debug: 32,
		Info:  64,
		Warn:  128,
		Error: 256,
		Fatal: 64,
	}
	view := c.Main.View(configer.Path{"Logs", "Cache"})
	if err := configer.Unmarshal(view, &cfg); err != nil {
		return cfg
	}
	cfg.Debug = max(cfg.Debug, 0)
	cfg.Info = max(cfg.Info, 0)
	cfg.Warn = max(cfg.Warn, 0)
	cfg.Error = max(cfg.Error, 0)
	cfg.Fatal = max(cfg.Fatal, 0)
	return cfg
}

func (cfg LogCacheConfig) MergeArgs(a *args.Args) LogCacheConfig {
	if a == nil {
		return cfg
	}
	if a.LogCacheDebug != nil {
		cfg.Debug = max(*a.LogCacheDebug, 0)
	}
	if a.LogCacheInfo != nil {
		cfg.Info = max(*a.LogCacheInfo, 0)
	}
	if a.LogCacheWarn != nil {
		cfg.Warn = max(*a.LogCacheWarn, 0)
	}
	if a.LogCacheError != nil {
		cfg.Error = max(*a.LogCacheError, 0)
	}
	if a.LogCacheFatal != nil {
		cfg.Fatal = max(*a.LogCacheFatal, 0)
	}
	return cfg
}

func (c *Config) SaveIdle() time.Duration {
	cfg := saveConfig{
		IdleMilliseconds: 500,
	}
	view := c.Main.View(configer.Path{"Config", "Save"})
	if err := configer.Unmarshal(view, &cfg); err != nil {
		return time.Duration(cfg.IdleMilliseconds) * time.Millisecond
	}
	if cfg.IdleMilliseconds <= 0 {
		cfg.IdleMilliseconds = 500
	}
	return time.Duration(cfg.IdleMilliseconds) * time.Millisecond
}

func ParseConfig(cfg, prt []byte) (*Config, error) {
	config, err := hujson.Parse(cfg)
	if err != nil {
		return nil, fmt.Errorf("config parsing: %w", err)
	}

	protected := ProtectedConfig{
		Admin: defaultAdminAPIConfig(),
	}
	protectedCfg, err := hujson.Parse(prt)
	if err != nil {
		return nil, fmt.Errorf("protected config parsing: %w", err)
	}
	protected.System = protectedCfg.View(configer.Path{"System"}).ReadOnly()
	err = configer.Unmarshal(protectedCfg, &protected)
	if err != nil {
		return nil, fmt.Errorf("protected config unmarshaling: %w", err)
	}

	return &Config{
		Main:      config,
		Protected: protected,
	}, nil
}

// ProtectedConfig is intended for "protected" readonly options that should not
// be editable via admin API.
type ProtectedConfig struct {
	Admin         AdminAPIConfig
	CommandRunner cmdrunner.Config
	Secrets       map[string]ProtectedSecretConfig
	System        configer.Config `json:"-"`
}

// ProtectedSecretConfig defines one immutable secret available to selected
// workspace nodes. Value stores the literal secret. Env names an environment
// variable whose value is read once at daemon startup. Exactly one of Value or
// Env may be set.
type ProtectedSecretConfig struct {
	Value *string  `json:"Value"`
	Env   string   `json:"Env"`
	Nodes []string `json:"Nodes"`
}

func (p *ProtectedConfig) ImmutableSecrets() (map[string]immutableSecret, error) {
	if len(p.Secrets) == 0 {
		return nil, nil
	}
	out := make(map[string]immutableSecret, len(p.Secrets))
	for key, cfg := range p.Secrets {
		if !validSecretName(key) {
			return nil, fmt.Errorf("%w: invalid protected secret key %q", almagest.ErrSecretInvalidName, key)
		}
		if cfg.Value != nil && cfg.Env != "" {
			return nil, fmt.Errorf("%w: protected secret %q sets both Value and Env", almagest.ErrSecretInvalidValue, key)
		}
		if cfg.Value == nil && cfg.Env == "" {
			return nil, fmt.Errorf("%w: protected secret %q must set Value or Env", almagest.ErrSecretInvalidValue, key)
		}
		for _, node := range cfg.Nodes {
			if !validSecretNodeName(node) {
				return nil, fmt.Errorf("%w: invalid protected secret node %q", almagest.ErrSecretInvalidName, node)
			}
		}
		value := ""
		source := api.SecretSourceProtected
		if cfg.Value != nil {
			value = *cfg.Value
		}
		if cfg.Env != "" {
			envValue, ok := os.LookupEnv(cfg.Env)
			if !ok {
				return nil, fmt.Errorf("%w: environment variable %q for protected secret %q is not set", almagest.ErrSecretInvalidValue, cfg.Env, key)
			}
			value = envValue
			source = api.SecretSourceEnv
		}
		out[key] = immutableSecret{
			secretRecord: secretRecord{
				Value: value,
				Nodes: normalizeSecretNodes(cfg.Nodes),
			},
			source: source,
		}
	}
	return out, nil
}

func (p *ProtectedConfig) Merge(args *args.Args) {
	if args.WebAdmin != nil {
		p.Admin.Web = *args.WebAdmin
	}
	if len(args.WebAdminFilterUIDs) > 0 {
		p.Admin.WebFilters.UIDs = args.WebAdminFilterUIDs
	}
	if len(args.WebAdminFilterGIDs) > 0 {
		p.Admin.WebFilters.GIDs = args.WebAdminFilterGIDs
	}
	if len(args.WebAdminFilterUsernames) > 0 {
		p.Admin.WebFilters.Usernames = args.WebAdminFilterUsernames
	}
	if args.WebAdminPProf {
		p.Admin.WebPProf = true
	}
	if args.UnixAdmin != nil {
		p.Admin.Unix = *args.UnixAdmin
	}
	if len(args.UnixAdminFilterUIDs) > 0 {
		p.Admin.UnixFilters.UIDs = args.UnixAdminFilterUIDs
	}
	if len(args.UnixAdminFilterGIDs) > 0 {
		p.Admin.UnixFilters.GIDs = args.UnixAdminFilterGIDs
	}
	if len(args.UnixAdminFilterUsernames) > 0 {
		p.Admin.UnixFilters.Usernames = args.UnixAdminFilterUsernames
	}
}

type AdminAPIConfig struct {
	Web         uint16
	WebFilters  AdminPeerFiltersConfig
	WebPProf    bool
	Unix        string
	UnixFilters AdminPeerFiltersConfig
}

type AdminPeerFiltersConfig struct {
	UIDs      []uint32
	GIDs      []uint32
	Usernames []string
}

func (c AdminPeerFiltersConfig) Enabled() bool {
	return len(c.UIDs) > 0 || len(c.GIDs) > 0 || len(c.Usernames) > 0
}

// type SystemIntegrationConfig struct {
// 	Type string
// }
//
// func (c *Config) SystemIntegration() SystemIntegrationConfig {
// 	si := SystemIntegrationConfig{
// 		Type: "debug",
// 	}
// 	return si
// }
