package main

import (
	"errors"
	"fmt"

	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/configer/hujson"
)

var (
	ErrConfigRO = errors.New("config is read-only")
)

type Config struct {
	Main      configer.Config
	Protected ProtectedConfig
}

type LogCacheConfig struct {
	Debug int `json:"Debug"`
	Info  int `json:"Info"`
	Warn  int `json:"Warn"`
	Error int `json:"Error"`
	Fatal int `json:"Fatal"`
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

func ParseConfig(cfg, prt []byte) (*Config, error) {
	config, err := hujson.Parse(cfg)
	if err != nil {
		return nil, fmt.Errorf("config parsing: %w", err)
	}

	protected := ProtectedConfig{}
	protectedCfg, err := hujson.Parse(prt)
	if err != nil {
		return nil, fmt.Errorf("protected config parsing: %w", err)
	}
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
	Admin AdminAPIConfig
}

func (p *ProtectedConfig) Merge(args *Args) {
	if args.WebAdmin != nil {
		p.Admin.Web = *args.WebAdmin
	}
}

type AdminAPIConfig struct {
	Web uint16
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
