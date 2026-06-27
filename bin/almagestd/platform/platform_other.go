//go:build !linux

package platform

import (
	_ "embed"

	"github.com/asciimoth/almagest/bin/almagestd/args"
	cmdrunner "github.com/asciimoth/almagest/bin/almagestd/command"
	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/gonnect/sysnet"
	sysnetdebug "github.com/asciimoth/gonnect/sysnet/debug"
	"github.com/kataras/golog"
)

//go:embed default_config.hujson
var DefaultConfigText string

//go:embed default_protected_config.hujson
var DefaultProtectedConfigText string

var (
	_ Agent = &agent{}
)

type agent struct {
	StdInConfig bool
}

func (a *agent) ConfRO() bool {
	return true
}

func (a *agent) ReadConfig() (cfg, prt []byte, err error) {
	if a.StdInConfig {
		golog.Info("Loading config from stdin")
		configData, err := readStdinIfPiped([]byte(DefaultConfigText))
		if err != nil {
			return nil, nil, err
		}
		mainData, protectedData := splitEmbeddedProtectedConfig(
			configData,
			[]byte(DefaultProtectedConfigText),
		)
		return mainData, protectedData, nil
	}
	return []byte(DefaultConfigText), []byte(DefaultProtectedConfigText), nil
}

func (a *agent) WriteConfig(cfg []byte) error {
	return nil
}

func RunAgent(args args.Args) Agent {
	golog.Warn("No system agent for this latform; Using stub one.")
	return &agent{
		StdInConfig: args.StdInConfig,
	}
}

func SystemIntegration(cfg configer.Config, runner cmdrunner.Dispatcher) (sysnet.System, error) {
	_ = runner
	golog.Warn("No system integration sor this platform; Using stub one.")
	return &sysnetdebug.System{}, nil
}
