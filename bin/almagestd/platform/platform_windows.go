//go:build windows

package platform

import (
	_ "embed"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/asciimoth/almagest/bin/almagestd/args"
	"github.com/asciimoth/almagest/bin/almagestd/daemonservices"
	"github.com/asciimoth/almagest/bin/internal/durablefile"
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
		if err := makeSurePrivateWindowsDirExists(parent); err != nil {
			return "", nil, fmt.Errorf("temporary file storage parent: %w", err)
		}
		fs, err = os.MkdirTemp(parent, "alm-store-")
		if err != nil {
			return "", nil, fmt.Errorf("temporary file storage setup: %w", err)
		}
		if err := checkWindowsDirectory(fs, windowsPathPrivateDirectory); err != nil {
			_ = os.RemoveAll(fs)
			return "", nil, fmt.Errorf("temporary file storage setup: %w", err)
		}
		temporaryPath := fs
		return fs, func() error { return cleanupWindowsTemporaryStorage(temporaryPath) }, nil
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
	if err := makeSurePrivateWindowsDirExists(fs); err != nil {
		return "", nil, fmt.Errorf("file storage setup: %w", err)
	}
	return fs, cleanup, nil
}

func cleanupWindowsTemporaryStorage(storage string) error {
	workingDirectory, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("get current directory before temporary storage cleanup: %w", err)
	}
	relative, err := filepath.Rel(storage, workingDirectory)
	if err != nil {
		return fmt.Errorf("compare current directory with temporary storage: %w", err)
	}
	if relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))) {
		if err := os.Chdir(filepath.Dir(storage)); err != nil {
			return fmt.Errorf("leave temporary storage before cleanup: %w", err)
		}
	}
	return os.RemoveAll(storage)
}

func TmpAdminUnixSocketPath() (string, error) {
	return tmpAdminUnixSocketPathIn(os.TempDir())
}

type windowsAgent struct {
	StdInConfig  bool
	ReadOnly     bool
	UnsafeConfig bool

	ConfigPath    string
	ProtectedPath string
	writeMu       sync.Mutex
}

func (a *windowsAgent) ConfRO() bool                    { return a.ReadOnly }
func (a *windowsAgent) ConfigFilePath() string          { return a.ConfigPath }
func (a *windowsAgent) ProtectedConfigFilePath() string { return a.ProtectedPath }

func (a *windowsAgent) WriteConfig(data []byte) error {
	a.writeMu.Lock()
	defer a.writeMu.Unlock()
	if a.ReadOnly {
		return ErrConfigReadOnly
	}
	var err error
	if a.UnsafeConfig {
		info, statErr := os.Stat(a.ConfigPath)
		if statErr != nil {
			return statErr
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("config path is not a regular file: %s", a.ConfigPath)
		}
	} else if _, err := readPrivateWindowsFile(a.ConfigPath); err != nil {
		return err
	}
	var temporary *os.File
	if a.UnsafeConfig {
		temporary, err = os.CreateTemp(filepath.Dir(a.ConfigPath), "."+filepath.Base(a.ConfigPath)+".tmp-*")
	} else {
		temporary, err = createPrivateWindowsTempFile(
			filepath.Dir(a.ConfigPath), "."+filepath.Base(a.ConfigPath)+".tmp-",
		)
	}
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
	if err := durablefile.Replace(a.ConfigPath, temporaryPath); err != nil {
		return fmt.Errorf("replace config file: %w", err)
	}
	if !a.UnsafeConfig {
		if _, err := readPrivateWindowsFile(a.ConfigPath); err != nil {
			return fmt.Errorf("validate replaced config file: %w", err)
		}
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
	readFile := readPrivateWindowsFile
	if a.UnsafeConfig {
		readFile = os.ReadFile
	}
	configData, err := readFile(a.ConfigPath)
	if err != nil {
		return nil, nil, fmt.Errorf("config read: %s: %w", a.ConfigPath, err)
	}
	protectedData, err := readFile(a.ProtectedPath)
	if err != nil {
		return nil, nil, fmt.Errorf("protected config read: %s: %w", a.ProtectedPath, err)
	}
	return configData, protectedData, nil
}

func (a *windowsAgent) ReadRegularConfig() ([]byte, error) {
	if a.UnsafeConfig {
		return os.ReadFile(a.ConfigPath)
	}
	return readPrivateWindowsFile(a.ConfigPath)
}

func RunAgent(args args.Args) *windowsAgent {
	agent, err := prepareAgent(args)
	if err != nil {
		golog.Fatal("Failed to prepare config files: ", err)
	}
	return agent
}

// prepareAgent prepares Windows config ownership without terminating the
// process. Service startup uses this form so the SCM receives the failure.
func prepareAgent(arguments args.Args) (*windowsAgent, error) {
	return newWindowsAgent(arguments)
}

func newWindowsAgent(arguments args.Args) (*windowsAgent, error) {
	agent := &windowsAgent{
		StdInConfig:  arguments.StdInConfig,
		ReadOnly:     arguments.StdInConfig,
		UnsafeConfig: arguments.UnsafeConfig,
	}
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
	if !arguments.UnsafeConfig {
		if err := makeSureWindowsConfigFileExists(configPath, []byte(DefaultConfigText)); err != nil {
			return nil, fmt.Errorf("config file %s: %w", configPath, err)
		}
		if err := makeSureWindowsConfigFileExists(protectedPath, []byte(DefaultProtectedConfigText)); err != nil {
			return nil, fmt.Errorf("protected config file %s: %w", protectedPath, err)
		}
	}
	agent.ConfigPath = configPath
	agent.ProtectedPath = protectedPath
	return agent, nil
}

func makeSureWindowsConfigFileExists(path string, initial []byte) error {
	if _, err := readPrivateWindowsFile(path); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) && !errors.Is(err, windows.ERROR_FILE_NOT_FOUND) &&
		!errors.Is(err, windows.ERROR_PATH_NOT_FOUND) {
		return err
	}
	return createPrivateWindowsFile(path, initial)
}

// ValidateArgs rejects Unix administration flags before startup changes any
// filesystem state.
func ValidateArgs(arguments *args.Args) error {
	if arguments == nil {
		return nil
	}
	if arguments.UnixAdmin != nil || arguments.TmpUnixAdmin ||
		len(arguments.UnixAdminFilterUIDs) != 0 || len(arguments.UnixAdminFilterGIDs) != 0 ||
		len(arguments.UnixAdminFilterUsernames) != 0 {
		return fmt.Errorf("unix admin flags are not supported on Windows")
	}
	if len(arguments.WebAdminFilterUIDs) != 0 || len(arguments.WebAdminFilterGIDs) != 0 {
		return fmt.Errorf("web admin UID and GID filters are not supported on windows; use --web-filter-user")
	}
	return nil
}

func SystemIntegration(cfg configer.Config, runner daemonservices.BackendCallbackDispatcher, randomSeed ...int64) (sysnet.System, error) {
	_ = runner
	_ = randomSeed
	config, err := readWindowsSystemConfig(cfg)
	if err != nil {
		return nil, err
	}
	return sysnetwindows.New(config)
}
