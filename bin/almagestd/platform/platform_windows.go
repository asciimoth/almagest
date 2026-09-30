//go:build windows

package platform

import (
	_ "embed"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/asciimoth/almagest/bin/almagestd/args"
	cmdrunner "github.com/asciimoth/almagest/bin/almagestd/command"
	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/gonnect/sysnet"
	sysnetwindows "github.com/asciimoth/sysnet-windows"
	"github.com/kataras/golog"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
)

//go:embed default_windows_config.hujson
var DefaultConfigText string

//go:embed default_windows_protected_config.hujson
var DefaultProtectedConfigText string

var _ Agent = &windowsAgent{}

const windowsApplicationDirectory = "Almagest"

var (
	windowsKnownFolderPath = windows.KnownFolderPath
	windowsServiceStatus   = svc.IsWindowsService
)

// SetupFS prepares Windows file storage. Services use the machine data folder,
// and interactive processes use the current identity's local application data
// folder. An explicit path takes precedence over both defaults.
func SetupFS(storagePath string, tmp bool) (fs string, cleanup FSCleanup, err error) {
	cleanup = noopFSCleanup
	if tmp {
		localData, folderErr := windowsKnownFolderPath(windows.FOLDERID_LocalAppData, windows.KF_FLAG_DEFAULT)
		if folderErr != nil {
			return "", nil, fmt.Errorf("resolve LocalAppData for temporary storage: %w", folderErr)
		}
		parent := filepath.Join(localData, windowsApplicationDirectory, "tmp")
		if err := os.MkdirAll(parent, 0o700); err != nil {
			return "", nil, fmt.Errorf("temporary file storage parent: %w", err)
		}
		fs, err = os.MkdirTemp(parent, "alm-store-")
		if err != nil {
			return "", nil, fmt.Errorf("temporary file storage setup: %w", err)
		}
		temporaryPath := fs
		return fs, func() error { return os.RemoveAll(temporaryPath) }, nil
	}
	if storagePath == "" {
		isService, serviceErr := windowsServiceStatus()
		if serviceErr != nil {
			return "", nil, fmt.Errorf("detect Windows service execution: %w", serviceErr)
		}
		folderID := windows.FOLDERID_LocalAppData
		folderName := "LocalAppData"
		if isService {
			folderID = windows.FOLDERID_ProgramData
			folderName = "ProgramData"
		}
		dataPath, folderErr := windowsKnownFolderPath(folderID, windows.KF_FLAG_DEFAULT)
		if folderErr != nil {
			return "", nil, fmt.Errorf("resolve %s for file storage: %w", folderName, folderErr)
		}
		storagePath = filepath.Join(dataPath, windowsApplicationDirectory, "store")
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
	ReadOnly    bool

	ConfigPath    string
	ProtectedPath string
}

func (a *windowsAgent) ConfRO() bool                    { return a.ReadOnly }
func (a *windowsAgent) ConfigFilePath() string          { return a.ConfigPath }
func (a *windowsAgent) ProtectedConfigFilePath() string { return a.ProtectedPath }

func (a *windowsAgent) WriteConfig(data []byte) error {
	if a.ReadOnly {
		return ErrConfigReadOnly
	}
	info, err := os.Stat(a.ConfigPath)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("config path is not a regular file: %s", a.ConfigPath)
	}
	temporary, err := os.CreateTemp(filepath.Dir(a.ConfigPath), "."+filepath.Base(a.ConfigPath)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create temporary config file: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath) //nolint:errcheck
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write temporary config file: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("sync temporary config file: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temporary config file: %w", err)
	}
	if err := os.Rename(temporaryPath, a.ConfigPath); err != nil {
		return fmt.Errorf("replace config file: %w", err)
	}
	return nil
}

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
	configData, err := os.ReadFile(a.ConfigPath)
	if err != nil {
		return nil, nil, fmt.Errorf("config read: %s: %w", a.ConfigPath, err)
	}
	protectedData, err := os.ReadFile(a.ProtectedPath)
	if err != nil {
		return nil, nil, fmt.Errorf("protected config read: %s: %w", a.ProtectedPath, err)
	}
	return configData, protectedData, nil
}

func RunAgent(args args.Args) Agent {
	agent, err := newWindowsAgent(args)
	if err != nil {
		golog.Fatal("Failed to prepare config files: ", err)
	}
	return agent
}

func newWindowsAgent(arguments args.Args) (*windowsAgent, error) {
	agent := &windowsAgent{StdInConfig: arguments.StdInConfig, ReadOnly: arguments.StdInConfig}
	if arguments.StdInConfig {
		return agent, nil
	}

	configPath := arguments.Config
	if configPath == "" {
		programData, err := windowsKnownFolderPath(windows.FOLDERID_ProgramData, windows.KF_FLAG_DEFAULT)
		if err != nil {
			return nil, fmt.Errorf("resolve ProgramData for config: %w", err)
		}
		configPath = filepath.Join(programData, windowsApplicationDirectory, "alm.hujson")
	}
	configPath, err := filepath.Abs(configPath)
	if err != nil {
		return nil, fmt.Errorf("config path %q: %w", configPath, err)
	}
	if info, statErr := os.Stat(configPath); statErr == nil && info.IsDir() {
		configPath = filepath.Join(configPath, "alm.hujson")
	} else if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		return nil, fmt.Errorf("config path %s: %w", configPath, statErr)
	}

	protectedPath := arguments.ProtectedConfig
	if protectedPath == "" {
		protectedPath = filepath.Join(filepath.Dir(configPath), "protected_alm.hujson")
	} else {
		protectedPath, err = filepath.Abs(protectedPath)
		if err != nil {
			return nil, fmt.Errorf("protected config path %q: %w", protectedPath, err)
		}
	}
	if err := makeSureWindowsConfigFileExists(configPath, []byte(DefaultConfigText)); err != nil {
		return nil, fmt.Errorf("config file %s: %w", configPath, err)
	}
	if err := makeSureWindowsConfigFileExists(protectedPath, []byte(DefaultProtectedConfigText)); err != nil {
		return nil, fmt.Errorf("protected config file %s: %w", protectedPath, err)
	}
	agent.ConfigPath = configPath
	agent.ProtectedPath = protectedPath
	return agent, nil
}

func makeSureWindowsConfigFileExists(path string, initial []byte) error {
	if info, err := os.Stat(path); err == nil {
		if !info.Mode().IsRegular() {
			return fmt.Errorf("not a regular file")
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := file.Write(initial); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return err
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		return err
	}
	return nil
}

// ValidateArgs rejects Unix administration flags before startup changes any
// filesystem state. Windows named-pipe administration is added in Step 12.
func ValidateArgs(arguments *args.Args) error {
	if arguments == nil {
		return nil
	}
	if arguments.UnixAdmin != nil || arguments.TmpUnixAdmin ||
		len(arguments.UnixAdminFilterUIDs) != 0 || len(arguments.UnixAdminFilterGIDs) != 0 ||
		len(arguments.UnixAdminFilterUsernames) != 0 {
		return fmt.Errorf("unix admin flags are not supported on Windows")
	}
	return nil
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
