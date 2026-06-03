package main

import (
	"errors"

	"github.com/asciimoth/almagest/bin/platforms"
	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/gonnect/sysnet"
	sysnetdebug "github.com/asciimoth/sysnet-debug"
	"github.com/kataras/golog"
)

var (
	ErrSysType = errors.New("system integration type")
)

func SystemIntegration(cfg *Config) (sysnet.SysBuilder, error) {
	view := cfg.Main.View(configer.Path{"System"})
	typ, err := view.Get(configer.Path{"Type"})
	if err == nil {
		if typ, ok := typ.(string); ok {
			switch typ {
			case "debug":
				golog.Warn("Using debug system integration")
				return BuildDebugIntegration(cfg)
			case "native":
				// noop
			default:
				return nil, ErrSysType
			}
		}
	}
	return platforms.SystemIntegration(view)
}

func BuildDebugIntegration(cfg *Config) (sysnet.SysBuilder, error) {
	// TODO: Custom debug integration
	// TODO: Separated debug admin API for IP packets on Tun emulation and so on
	return sysnetdebug.NewDebugSysBuilder(nil), nil
}
