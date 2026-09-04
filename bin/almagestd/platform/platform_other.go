//go:build !linux

package platform

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"

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

// SetupFS prepares stub file storage for unsupported platforms.
//
// A caller-provided persistent path is created and checked as a private
// directory where the platform's permission model exposes Unix-like permission
// bits. When no path is provided, or when tmp is true, the stub creates a
// random directory prefixed with "alm-store-" in the current working directory.
func SetupFS(storagePath string, tmp bool) (fs string, cleanup FSCleanup, err error) {
	cleanup = noopFSCleanup

	if tmp || storagePath == "" {
		fs, err = os.MkdirTemp(".", "alm-store-")
		if err != nil {
			return "", nil, fmt.Errorf("file storage setup: %w", err)
		}

		tmpPath := fs
		fs, err = filepath.Abs(tmpPath)
		if err != nil {
			_ = os.RemoveAll(tmpPath)
			return "", nil, fmt.Errorf("file storage path: %w", err)
		}

		if err := checkOtherPrivateDir(fs); err != nil {
			_ = os.RemoveAll(fs)
			return "", nil, fmt.Errorf("file storage setup: %w", err)
		}

		if !tmp {
			return fs, cleanup, nil
		}
		return fs, func() error {
			return os.RemoveAll(fs)
		}, nil
	}

	fs, err = filepath.Abs(storagePath)
	if err != nil {
		return "", nil, fmt.Errorf("file storage path %q: %w", storagePath, err)
	}
	if err := os.MkdirAll(fs, 0o700); err != nil {
		return "", nil, fmt.Errorf("file storage setup: %w", err)
	}
	if err := checkOtherPrivateDir(fs); err != nil {
		return "", nil, fmt.Errorf("file storage setup: %w", err)
	}
	return fs, cleanup, nil
}

func checkOtherPrivateDir(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("%s: not a directory", path)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("%s: storage directory mode %#o, want private", path, info.Mode().Perm())
	}
	return nil
}

// TmpAdminUnixSocketPath returns a random temporary path for the local admin
// Unix socket on non-Linux platforms. The regular Unix admin listener remains
// responsible for platform support, bind-time setup, permissions, and cleanup.
func TmpAdminUnixSocketPath() (string, error) {
	return tmpAdminUnixSocketPathIn(os.TempDir())
}

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

func SystemIntegration(cfg configer.Config, runner cmdrunner.Dispatcher, randomSeed ...int64) (sysnet.System, error) {
	_ = runner
	_ = randomSeed
	golog.Warn("No system integration sor this platform; Using stub one.")
	return &sysnetdebug.System{}, nil
}
