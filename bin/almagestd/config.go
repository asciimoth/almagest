package main

import (
	"errors"
	"fmt"
	"github.com/asciimoth/almagest/lib/storage"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/asciimoth/almagest/bin/almagestd/args"
	cmdrunner "github.com/asciimoth/almagest/bin/almagestd/command"
	"github.com/asciimoth/almagest/bin/api"
	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/configer/hujson"
	"github.com/kataras/golog"
	tailscalehujson "github.com/tailscale/hujson"
)

var (
	ErrConfigRO = errors.New("config is read-only")
)

type Config struct {
	mu sync.RWMutex

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
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.mainToBytesLocked()
}

func (c *Config) mainToBytesLocked() ([]byte, error) {
	if c == nil || c.Main == nil {
		return nil, ErrConfigRO
	}
	return tailscalehujson.Format(c.Main.Pack())
}

func (c *Config) LogCache() LogCacheConfig {
	c.mu.RLock()
	defer c.mu.RUnlock()
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
	c.mu.RLock()
	defer c.mu.RUnlock()
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

type tlsBuiltinCAConfig struct {
	Issuer       string `json:"Issuer,omitempty"`
	ValidityDays int    `json:"ValidityDays,omitempty"`
	KeyAlgorithm string `json:"KeyAlgorithm,omitempty"`
	Curve        string `json:"Curve,omitempty"`
	KeyBits      int    `json:"KeyBits,omitempty"`
}

func (c *Config) TLSBuiltinCAOptions() (api.TLSBuiltinCAOptions, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	cfg := tlsBuiltinCAConfigFromAPI(defaultTLSBuiltinCAOptions())
	if c != nil && c.Main != nil {
		view := c.Main.View(configer.Path{"Config", "TLS", "BuiltinCA"})
		if err := configer.Unmarshal(view, &cfg); err != nil {
			return api.TLSBuiltinCAOptions{}, fmt.Errorf("%w: read builtin TLS CA config: %v", storage.ErrTlsConfigInvalidValue, err)
		}
	}
	return normalizeTLSBuiltinCAOptions(api.TLSBuiltinCAOptions{
		Issuer:       cfg.Issuer,
		ValidityDays: cfg.ValidityDays,
		KeyAlgorithm: cfg.KeyAlgorithm,
		Curve:        cfg.Curve,
		KeyBits:      cfg.KeyBits,
	})
}

func (c *Config) SetTLSBuiltinCAOptions(options api.TLSBuiltinCAOptions) (api.TLSBuiltinCAOptions, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c == nil || c.Main == nil {
		return api.TLSBuiltinCAOptions{}, ErrConfigRO
	}
	options, err := normalizeTLSBuiltinCAOptions(options)
	if err != nil {
		return api.TLSBuiltinCAOptions{}, err
	}
	if err := configer.Marshal(
		c.Main.View(configer.Path{"Config", "TLS", "BuiltinCA"}),
		tlsBuiltinCAConfigFromAPI(options),
	); err != nil {
		return api.TLSBuiltinCAOptions{}, fmt.Errorf("%w: update builtin TLS CA config: %v", storage.ErrTlsConfigInvalidValue, err)
	}
	return options, nil
}

func tlsBuiltinCAConfigFromAPI(options api.TLSBuiltinCAOptions) tlsBuiltinCAConfig {
	return tlsBuiltinCAConfig{
		Issuer:       options.Issuer,
		ValidityDays: options.ValidityDays,
		KeyAlgorithm: options.KeyAlgorithm,
		Curve:        options.Curve,
		KeyBits:      options.KeyBits,
	}
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
	TLS           ProtectedTLSConfig
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

type ProtectedTLSConfig struct {
	Certificates map[string]ProtectedTLSCertificateConfig `json:"Certificates"`
	Policies     map[string]api.TLSPolicySpec             `json:"Policies"`
	BuiltinCA    ProtectedTLSBuiltinCAConfig              `json:"BuiltinCA"`
}

// ProtectedTLSCertificateConfig defines one immutable certificate authority.
// PEM stores the literal certificate. File names a local PEM file read once at
// daemon startup. Exactly one of PEM or File may be set.
type ProtectedTLSCertificateConfig struct {
	PEM  *string `json:"PEM"`
	File string  `json:"File"`
}

// ProtectedTLSBuiltinCAConfig defines an immutable built-in CA keypair. PEM
// stores one certificate and one private key in the same PEM text. File names a
// local file with the same combined PEM content.
type ProtectedTLSBuiltinCAConfig struct {
	PEM  *string `json:"PEM"`
	File string  `json:"File"`
}

func (p *ProtectedConfig) ImmutableSecrets() (map[string]immutableSecret, error) {
	if len(p.Secrets) == 0 {
		return nil, nil
	}
	out := make(map[string]immutableSecret, len(p.Secrets))
	for key, cfg := range p.Secrets {
		if !validSecretName(key) {
			return nil, fmt.Errorf("%w: invalid protected secret key %q", storage.ErrSecretInvalidName, key)
		}
		if cfg.Value != nil && cfg.Env != "" {
			return nil, fmt.Errorf("%w: protected secret %q sets both Value and Env", storage.ErrSecretInvalidValue, key)
		}
		if cfg.Value == nil && cfg.Env == "" {
			return nil, fmt.Errorf("%w: protected secret %q must set Value or Env", storage.ErrSecretInvalidValue, key)
		}
		for _, node := range cfg.Nodes {
			if !validSecretNodeName(node) {
				return nil, fmt.Errorf("%w: invalid protected secret node %q", storage.ErrSecretInvalidName, node)
			}
		}
		value := ""
		source := api.SecretSourceProtected
		if cfg.Value != nil {
			golog.Debugf("secrets storage: loading protected secret %q from inline value", key)
			value = *cfg.Value
		}
		if cfg.Env != "" {
			golog.Debugf("secrets storage: loading protected secret %q from environment variable %q", key, cfg.Env)
			envValue, ok := os.LookupEnv(cfg.Env)
			if !ok {
				return nil, fmt.Errorf("%w: environment variable %q for protected secret %q is not set", storage.ErrSecretInvalidValue, cfg.Env, key)
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
	golog.Debugf("secrets storage: loaded %d protected secrets", len(out))
	return out, nil
}

func (p *ProtectedConfig) ImmutableTLS() (immutableTLSData, error) {
	var out immutableTLSData
	if len(p.TLS.Certificates) > 0 {
		out.Certs = make(map[string]immutableTLSCert, len(p.TLS.Certificates))
		for name, cfg := range p.TLS.Certificates {
			if !validMutableTLSCertName(name) {
				return immutableTLSData{}, fmt.Errorf("%w: invalid protected TLS certificate name %q", storage.ErrTlsConfigInvalidName, name)
			}
			if cfg.PEM != nil && strings.TrimSpace(cfg.File) != "" {
				return immutableTLSData{}, fmt.Errorf("%w: protected TLS certificate %q sets both PEM and File", storage.ErrTlsConfigInvalidValue, name)
			}
			if cfg.PEM == nil && strings.TrimSpace(cfg.File) == "" {
				return immutableTLSData{}, fmt.Errorf("%w: protected TLS certificate %q must set PEM or File", storage.ErrTlsConfigInvalidValue, name)
			}
			pemText := ""
			if cfg.PEM != nil {
				golog.Debugf("TLS certificates: loading protected certificate %q from inline PEM", name)
				pemText = *cfg.PEM
			} else {
				path := strings.TrimSpace(cfg.File)
				golog.Debugf("TLS certificates: loading protected certificate %q from file %s", name, path)
				data, err := os.ReadFile(path)
				if err != nil {
					return immutableTLSData{}, fmt.Errorf("%w: read protected TLS certificate %q file %q: %v", storage.ErrTlsConfigInvalidValue, name, cfg.File, err)
				}
				pemText = string(data)
			}
			if _, _, err := parseTLSAuthorityPEM(pemText); err != nil {
				return immutableTLSData{}, fmt.Errorf("%w: protected TLS certificate %q: %v", storage.ErrTlsConfigInvalidValue, name, err)
			}
			out.Certs[name] = immutableTLSCert{
				tlsCertificateRecord: tlsCertificateRecord{PEM: pemText},
				source:               api.TLSSourceProtected,
			}
		}
		golog.Debugf("TLS certificates: loaded %d protected certificates", len(out.Certs))
	}
	if len(p.TLS.Policies) > 0 {
		out.Policies = make(map[string]immutableTLSPolicy, len(p.TLS.Policies))
		for name, spec := range p.TLS.Policies {
			if !validMutableTLSPolicyName(name) {
				return immutableTLSData{}, fmt.Errorf("%w: invalid protected TLS policy name %q", storage.ErrTlsConfigInvalidName, name)
			}
			if err := validateTLSPolicySpecScopes(spec); err != nil {
				return immutableTLSData{}, fmt.Errorf("%w: protected TLS policy %q: %v", storage.ErrTlsConfigInvalidValue, name, err)
			}
			out.Policies[name] = immutableTLSPolicy{
				tlsPolicyRecord: tlsPolicyRecord{Spec: normalizeTLSPolicySpec(spec)},
				source:          api.TLSSourceProtected,
			}
		}
		golog.Debugf("TLS policies: loaded %d protected policies", len(out.Policies))
	}
	return out, nil
}

func (p *ProtectedConfig) TLSBuiltinCA() (*tlsBuiltinCA, error) {
	cfg := p.TLS.BuiltinCA
	if cfg.PEM == nil && strings.TrimSpace(cfg.File) == "" {
		return nil, nil
	}
	if cfg.PEM != nil && strings.TrimSpace(cfg.File) != "" {
		return nil, fmt.Errorf("%w: protected builtin TLS CA sets both PEM and File", storage.ErrTlsConfigInvalidValue)
	}
	pemText := ""
	if cfg.PEM != nil {
		golog.Debug("builtin TLS CA: reading immutable certificate from protected inline PEM")
		pemText = *cfg.PEM
	} else {
		path := strings.TrimSpace(cfg.File)
		golog.Debugf("builtin TLS CA: reading immutable certificate from protected file %s", path)
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("%w: read protected builtin TLS CA file %q: %v", storage.ErrTlsConfigInvalidValue, cfg.File, err)
		}
		pemText = string(data)
	}
	ca, err := parseProtectedTLSBuiltinCA(pemText)
	if err != nil {
		return nil, fmt.Errorf("%w: protected builtin TLS CA: %v", storage.ErrTlsConfigInvalidValue, err)
	}
	golog.Debugf("builtin TLS CA: using immutable protected certificate; runtime generation is disabled (%s)", describeTLSBuiltinCACert(ca))
	return ca, nil
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
	if args.WebAdminAllowCrossOrigin {
		p.Admin.AllowCrossOrigin = true
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
	Web              uint16
	WebFilters       AdminPeerFiltersConfig
	WebPProf         bool
	AllowCrossOrigin bool
	Unix             string
	UnixFilters      AdminPeerFiltersConfig
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
