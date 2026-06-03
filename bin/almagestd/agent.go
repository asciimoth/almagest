package main

import (
	"errors"
	"io"
	"os"
	"path"
	"path/filepath"

	"github.com/asciimoth/almagest/bin/platforms"
	"github.com/asciimoth/configer/configer"
	"github.com/kataras/golog"
	tailscalehujson "github.com/tailscale/hujson"
)

// Agent runs in background before privileges drop and give limited access
// to some operations like config rewriting.
type Agent struct {
	args Args

	configReadOnly bool
	configPath     string
	protectedPath  string
}

func (a *Agent) ConfiRO() bool {
	return a.configReadOnly
}

func (a *Agent) ReadConfig() (*Config, error) {
	var err error

	var (
		configData, protectedData []byte
	)

	if a.args.StdInConfig {
		golog.Info("Loading config from stdin")
		configData, err = readStdinIfPiped([]byte(platforms.DefaultProtectedConfigText))
		if err != nil {
			golog.Fatal(err)
		}
	} else {
		configData, err = platforms.ReadSafeFile(a.configPath)
		if err != nil {
			golog.Fatal("Failed to read config", a.configPath, err)
		}

		protectedData, err = platforms.ReadSafeFile(a.protectedPath)
		if err != nil {
			golog.Fatal("Failed to read config", a.protectedPath, err)
		}
	}

	config, err := ParseConfig(configData, protectedData)
	if err != nil {
		golog.Fatal(err)
	}

	config.Protected.Merge(&a.args)

	return config, nil
}

func (a *Agent) WriteConfig(c configer.Config) error {
	if c == nil {
		return nil
	}

	if a.configReadOnly {
		return nil
	}

	packer, ok := c.(interface {
		Pack() []byte
	})
	if !ok {
		return errors.New("config does not support HuJSON packing")
	}

	data, err := tailscalehujson.Format(packer.Pack())
	if err != nil {
		return err
	}

	info, err := os.Stat(a.configPath)
	if err != nil {
		return err
	}

	return os.WriteFile(a.configPath, data, info.Mode().Perm())
}

func RunAgent(args Args) *Agent {
	agent := &Agent{
		args: args,
	}

	if args.StdInConfig {
		agent.configReadOnly = true
	} else {
		const ConfFail = "Failed to check config file"

		agent.configPath = args.Config
		agent.protectedPath = args.ProtectedConfig

		if agent.configPath == "" {
			agent.configPath = platforms.DefaultConfig()
		}

		configPath, err := filepath.Abs(agent.configPath)
		if err != nil {
			golog.Fatal(ConfFail, agent.configPath, err)
		}
		agent.configPath = configPath

		isdir, err := platforms.IsDir(agent.configPath)
		if err != nil {
			golog.Fatal(ConfFail, agent.configPath, err)
		}

		if isdir {
			agent.configPath = path.Join(agent.configPath, "alm.hujson")
		}

		if err := platforms.MakeSureSafeFileExists(
			agent.configPath, []byte(platforms.DefaultConfigText),
		); err != nil {
			golog.Fatal(ConfFail, agent.configPath, err)
		}

		if agent.protectedPath == "" {
			dir := filepath.Dir(agent.configPath)
			agent.protectedPath = path.Join(dir, "protected_alm.hujson")
		}

		protectedPath, err := filepath.Abs(agent.protectedPath)
		if err != nil {
			golog.Fatal(ConfFail, agent.protectedPath, err)
		}
		agent.protectedPath = protectedPath

		if err := platforms.MakeSureSafeFileExists(
			agent.protectedPath, []byte(platforms.DefaultProtectedConfigText),
		); err != nil {
			golog.Fatal(ConfFail, agent.protectedPath, err)
		}
	}

	if agent.configReadOnly {
		golog.Debug("Config is read-only")
	}

	// TODO: Really launch agent in background process
	return agent
}

// ReadStdinIfPiped returns fallback immediately if stdin is a terminal.
// Otherwise, reads all piped/file input until EOF.
func readStdinIfPiped(fallback []byte) ([]byte, error) {
	stat, err := os.Stdin.Stat()
	if err != nil {
		return nil, err
	}

	if stat.Mode()&os.ModeCharDevice != 0 {
		return fallback, nil
	}

	// Stdin is a pipe or redirected file → read until EOF
	return io.ReadAll(os.Stdin)
}
