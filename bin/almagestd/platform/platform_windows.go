//go:build windows

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
	sysnetwindows "github.com/asciimoth/sysnet-windows"
	"github.com/kataras/golog"
)

//go:embed default_windows_config.hujson
var DefaultConfigText string

//go:embed default_windows_protected_config.hujson
var DefaultProtectedConfigText string

var _ Agent = &windowsAgent{}

// SetupFS keeps the pre-integration storage behavior until the Windows ACL and
// known-folder work in Step 9. An explicit directory remains available for the
// native backend tests added by this milestone.
func SetupFS(storagePath string, tmp bool) (fs string, cleanup FSCleanup, err error) {
	cleanup = noopFSCleanup
	if tmp || storagePath == "" {
		fs, err = os.MkdirTemp(".", "alm-store-")
		if err != nil {
			return "", nil, fmt.Errorf("file storage setup: %w", err)
		}
		fs, err = filepath.Abs(fs)
		if err != nil {
			return "", nil, fmt.Errorf("file storage path: %w", err)
		}
		if tmp {
			return fs, func() error { return os.RemoveAll(fs) }, nil
		}
		return fs, cleanup, nil
	}
	fs, err = filepath.Abs(storagePath)
	if err != nil {
		return "", nil, fmt.Errorf("file storage path %q: %w", storagePath, err)
	}
	if err := os.MkdirAll(fs, 0o700); err != nil {
		return "", nil, fmt.Errorf("file storage setup: %w", err)
	}
	return fs, cleanup, nil
}

func TmpAdminUnixSocketPath() (string, error) {
	return tmpAdminUnixSocketPathIn(os.TempDir())
}

type windowsAgent struct {
	StdInConfig bool
}

func (a *windowsAgent) ConfRO() bool                    { return true }
func (a *windowsAgent) ConfigFilePath() string          { return "" }
func (a *windowsAgent) ProtectedConfigFilePath() string { return "" }
func (a *windowsAgent) WriteConfig([]byte) error        { return nil }
func (a *windowsAgent) ReadConfig() ([]byte, []byte, error) {
	if a.StdInConfig {
		golog.Info("Loading config from stdin")
		configData, err := readStdinIfPiped([]byte(DefaultConfigText))
		if err != nil {
			return nil, nil, err
		}
		mainData, protectedData := splitEmbeddedProtectedConfig(configData, []byte(DefaultProtectedConfigText))
		return mainData, protectedData, nil
	}
	return []byte(DefaultConfigText), []byte(DefaultProtectedConfigText), nil
}

func RunAgent(args args.Args) Agent {
	return &windowsAgent{StdInConfig: args.StdInConfig}
}

func SystemIntegration(cfg configer.Config, runner cmdrunner.Dispatcher, randomSeed ...int64) (sysnet.System, error) {
	_ = runner
	_ = randomSeed
	config, err := readWindowsSystemConfig(cfg)
	if err != nil {
		return nil, err
	}
	return sysnetwindows.New(config)
}
