//go:build linux

package platform

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/netip"
	"os"
	"os/user"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/asciimoth/almagest/bin/almagestd/args"
	cmdrunner "github.com/asciimoth/almagest/bin/almagestd/command"
	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/gonnect/sysnet"
	sysnetlinux "github.com/asciimoth/sysnet-linux"
	sysnetlinuxrouting "github.com/asciimoth/sysnet-linux/routing"
	"github.com/kataras/golog"
	"golang.org/x/sys/unix"
)

//go:embed default_linux_config.hujson
var DefaultConfigText string

//go:embed default_linux_protected_config.hujson
var DefaultProtectedConfigText string

const DefaultConfig = "/etc/almagest/alm.hujson"

const linuxDefaultUserMark uint32 = 0x414c4d01
const linuxFallbackFileStorage = "/var/lib/almagest/store"

var (
	_ Agent = &agent{}
)

// SetupFS prepares almagestd file storage and returns its absolute path.
//
// Persistent storage uses storagePath when it is not empty. Otherwise it uses a
// per-user data directory under the current user's home. When almagestd is
// running as root, or when no home directory is available, it falls back to
// /var/lib/almagest/store. Persistent cleanup is a no-op.
//
// Temporary storage ignores storagePath and creates a random 0700 directory
// prefixed with "alm-store-" under the platform temporary data parent. Temporary
// cleanup removes that directory.
//
// The final storage directory is always checked as a private 0700 directory
// owned by root or the current effective UID. Persistent storage also walks all
// parent directories with openat(), rejecting symlinks and writable untrusted
// parents before creating missing components.
func SetupFS(storagePath string, tmp bool) (fs string, cleanup FSCleanup, err error) {
	cleanup = noopFSCleanup

	if tmp {
		return setupTempFS(linuxTempFileStorageParent())
	}

	if storagePath == "" {
		storagePath = linuxDefaultFileStoragePath()
	}

	fs, err = filepath.Abs(storagePath)
	if err != nil {
		return "", nil, fmt.Errorf("file storage path %q: %w", storagePath, err)
	}

	if err := makeSurePrivateDirExists(fs); err != nil {
		return "", nil, fmt.Errorf("file storage setup: %w", err)
	}

	return fs, cleanup, nil
}

func linuxDefaultFileStoragePath() string {
	return linuxDefaultFileStoragePathFor(os.Geteuid(), currentUserHomeDir())
}

func linuxDefaultFileStoragePathFor(euid int, home string) string {
	if euid == 0 || home == "" {
		return linuxFallbackFileStorage
	}
	return filepath.Join(home, ".local", "share", "almagest", "store")
}

func currentUserHomeDir() string {
	if home, err := os.UserHomeDir(); err == nil && home != "" && filepath.IsAbs(home) {
		return home
	}
	if current, err := user.Current(); err == nil && current.HomeDir != "" && filepath.IsAbs(current.HomeDir) {
		return current.HomeDir
	}
	return ""
}

func linuxTempFileStorageParent() string {
	if runtimeDir := os.Getenv("XDG_RUNTIME_DIR"); runtimeDir != "" && filepath.IsAbs(runtimeDir) {
		if info, err := os.Stat(runtimeDir); err == nil && info.IsDir() {
			return runtimeDir
		}
	}
	return os.TempDir()
}

// TmpAdminUnixSocketPath returns a random temporary path for the local admin
// Unix socket. Linux prefers XDG_RUNTIME_DIR when it is an absolute existing
// directory, matching temporary file storage; otherwise it falls back to the
// process temporary directory.
func TmpAdminUnixSocketPath() (string, error) {
	return tmpAdminUnixSocketPathIn(linuxTempFileStorageParent())
}

func setupTempFS(parent string) (fs string, cleanup FSCleanup, err error) {
	parent, err = filepath.Abs(parent)
	if err != nil {
		return "", nil, fmt.Errorf("temporary file storage parent: %w", err)
	}

	fs, err = os.MkdirTemp(parent, "alm-store-")
	if err != nil {
		return "", nil, fmt.Errorf("temporary file storage setup: %w", err)
	}

	tmpPath := fs
	fs, err = filepath.Abs(tmpPath)
	if err != nil {
		_ = os.RemoveAll(tmpPath)
		return "", nil, fmt.Errorf("temporary file storage path: %w", err)
	}

	if err := checkPrivateDirPath(fs); err != nil {
		_ = os.RemoveAll(fs)
		return "", nil, fmt.Errorf("temporary file storage setup: %w", err)
	}

	return fs, func() error {
		return os.RemoveAll(fs)
	}, nil
}

type agent struct {
	StdInConfig  bool
	ReadOnly     bool
	UnsafeConfig bool

	ConfigPath    string
	ProtectedPath string
}

func (a *agent) ConfRO() bool {
	return a.ReadOnly
}

func (a *agent) ConfigFilePath() string {
	return a.ConfigPath
}

func (a *agent) ProtectedConfigFilePath() string {
	return a.ProtectedPath
}

func (a *agent) ReadConfig() ([]byte, []byte, error) {
	var err error

	var (
		configData, protectedData []byte
	)

	readfile := ReadSafeFile
	if a.UnsafeConfig {
		readfile = os.ReadFile
	}

	if a.StdInConfig {
		golog.Info("Loading config from stdin")
		configData, err = readStdinIfPiped([]byte(DefaultConfigText))
		if err != nil {
			return nil, nil, err
		}
		configData, protectedData = splitEmbeddedProtectedConfig(
			configData,
			[]byte(DefaultProtectedConfigText),
		)
	} else {
		configData, err = readfile(a.ConfigPath)
		if err != nil {
			return nil, nil, fmt.Errorf("config read: %s: %w", a.ConfigPath, err)
		}

		protectedData, err = readfile(a.ProtectedPath)
		if err != nil {
			return nil, nil, fmt.Errorf("config read: %s: %w", a.ConfigPath, err)
		}
	}

	return configData, protectedData, nil
}

func (a *agent) WriteConfig(data []byte) error {
	target, err := filepath.EvalSymlinks(a.ConfigPath)
	if err != nil {
		return err
	}
	info, err := os.Stat(target)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("config path is not a regular file: %s", a.ConfigPath)
	}

	temporary, err := os.CreateTemp(filepath.Dir(target), "."+filepath.Base(target)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create temporary config file: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath) //nolint:errcheck
	closeTemporary := func() {
		_ = temporary.Close()
	}

	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		if err := temporary.Chown(int(stat.Uid), int(stat.Gid)); err != nil {
			closeTemporary()
			return fmt.Errorf("preserve config ownership: %w", err)
		}
	}
	if err := temporary.Chmod(info.Mode().Perm()); err != nil {
		closeTemporary()
		return fmt.Errorf("preserve config permissions: %w", err)
	}
	if _, err := temporary.Write(data); err != nil {
		closeTemporary()
		return fmt.Errorf("write temporary config file: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		closeTemporary()
		return fmt.Errorf("sync temporary config file: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temporary config file: %w", err)
	}
	if err := os.Rename(temporaryPath, target); err != nil {
		return fmt.Errorf("replace config file: %w", err)
	}

	return nil
}

func RunAgent(args args.Args) Agent {
	agent := &agent{
		StdInConfig:  args.StdInConfig,
		UnsafeConfig: args.UnsafeConfig,
	}

	if args.StdInConfig {
		agent.ReadOnly = true
	} else {
		const ConfFail = "Failed to check config file"

		agent.ConfigPath = args.Config
		agent.ProtectedPath = args.ProtectedConfig

		if agent.ConfigPath == "" {
			agent.ConfigPath = DefaultConfig
		}

		configPath, err := filepath.Abs(agent.ConfigPath)
		if err != nil {
			golog.Fatal(ConfFail, agent.ConfigPath, err)
		}
		agent.ConfigPath = configPath

		isdir, err := IsDir(agent.ConfigPath)
		if err != nil {
			golog.Fatal(ConfFail, agent.ConfigPath, err)
		}

		if isdir {
			agent.ConfigPath = path.Join(agent.ConfigPath, "alm.hujson")
		}

		if !args.UnsafeConfig {
			if err := MakeSureSafeFileExists(
				agent.ConfigPath, []byte(DefaultConfigText),
			); err != nil {
				golog.Fatal(ConfFail, agent.ConfigPath, err)
			}
		}

		if agent.ProtectedPath == "" {
			dir := filepath.Dir(agent.ConfigPath)
			agent.ProtectedPath = path.Join(dir, "protected_alm.hujson")
		}

		protectedPath, err := filepath.Abs(agent.ProtectedPath)
		if err != nil {
			golog.Fatal(ConfFail, agent.ProtectedPath, err)
		}
		agent.ProtectedPath = protectedPath

		if !args.UnsafeConfig {
			if err := MakeSureSafeFileExists(
				agent.ProtectedPath, []byte(DefaultProtectedConfigText),
			); err != nil {
				golog.Fatal(ConfFail, agent.ProtectedPath, err)
			}
		}
	}

	if agent.ReadOnly {
		golog.Debug("Config is read-only")
	}

	return agent
}

// PrepareAgent keeps the legacy Linux config preparation behavior while
// providing the lifecycle entry point used by the daemon.
func PrepareAgent(arguments args.Args) (Agent, error) {
	return RunAgent(arguments), nil
}

func SystemIntegration(cfg configer.Config, runner cmdrunner.Dispatcher, randomSeed ...int64) (sysnet.System, error) {
	config, err := readLinuxSystemConfig(cfg, runner)
	if err != nil {
		return nil, err
	}
	config.DefaultTunBaseName = "alm"
	if len(randomSeed) > 0 {
		config.Allocator.Rng = rand.New(rand.NewSource(randomSeed[0])) //nolint:gosec // The daemon supplies a crypto-random seed.
	}
	return sysnetlinux.New(config)
}

type linuxSystemConfigFile struct {
	sysnetlinux.SystemConfig

	DNS           linuxDNSConfig   `json:"DNS"`
	Pmark         linuxPmarkConfig `json:"Pmark"`
	AppBypassMark linuxUint32      `json:"AppBypassMark"`
	AppBypassMask linuxUint32      `json:"AppBypassMask"`
	UserMark      linuxUint32      `json:"UserMark"`
	UserMarkMask  linuxUint32      `json:"UserMarkMask"`
	Logf          json.RawMessage  `json:"Logf,omitempty"`
	Callbacks     linuxCallbacksConfig
}

type linuxDNSConfig struct {
	Mode                   sysnetlinux.DNSMode `json:"Mode"`
	ResolvconfInterface    string              `json:"ResolvconfInterface"`
	ResolvedInterfaceIndex int                 `json:"ResolvedInterfaceIndex"`
	FallbackServers        []linuxAddrPort     `json:"FallbackServers"`
}

type linuxPmarkConfig struct {
	PinPath              string        `json:"PinPath"`
	TombCollectionEvents uint64        `json:"TombCollectionEvents"`
	TombTTL              time.Duration `json:"TombTTL"`
	Priority             int           `json:"Priority"`
}

type linuxAddrPort struct {
	netip.AddrPort
}

type linuxUint32 uint32

func readLinuxSystemConfig(
	cfg configer.Config,
	runner cmdrunner.Dispatcher,
) (sysnetlinux.SystemConfig, error) {
	config := defaultLinuxSystemConfig()
	fileConfig := linuxSystemConfigFile{
		SystemConfig:  config,
		DNS:           linuxDNSConfigFromSystem(config.DNS),
		Pmark:         linuxPmarkConfigFromSystem(config.Pmark),
		AppBypassMark: linuxUint32(config.AppBypassMark),
		AppBypassMask: linuxUint32(config.AppBypassMask),
		UserMark:      linuxUint32(config.UserMark),
		UserMarkMask:  linuxUint32(config.UserMarkMask),
	}
	if cfg != nil {
		view := cfg.View(configer.Path{"Linux"})
		if err := configer.Unmarshal(view, &fileConfig); err != nil {
			return config, fmt.Errorf("system linux config: %w", err)
		}
	}
	config = fileConfig.SystemConfig
	config.DNS = fileConfig.DNS.systemConfig()
	config.Pmark = fileConfig.Pmark.systemConfig()
	config.AppBypassMark = uint32(fileConfig.AppBypassMark)
	config.AppBypassMask = uint32(fileConfig.AppBypassMask)
	config.UserMark = uint32(fileConfig.UserMark)
	config.UserMarkMask = uint32(fileConfig.UserMarkMask)
	config.Logf = golog.Debugf
	if fileConfig.Callbacks.configured() {
		if runner == nil {
			return config, fmt.Errorf("system linux callbacks: command runner is unavailable")
		}
		config.Callbacks = fileConfig.Callbacks.sysnetCallbacks(runner)
	} else {
		config.Callbacks = sysnetlinux.Callbacks{}
	}
	return config, nil
}

func defaultLinuxSystemConfig() sysnetlinux.SystemConfig {
	return sysnetlinux.SystemConfig{
		DNS: sysnetlinux.DNSConfig{
			FallbackServers: []netip.AddrPort{
				netip.MustParseAddrPort("8.8.8.8:53"),
				netip.MustParseAddrPort("1.1.1.1:53"),
			},
		},
		Pmark: sysnetlinux.PmarkConfig{
			PinPath: "/sys/fs/bpf/almagest",
		},
		AppBypassMark:          sysnetlinuxrouting.DefaultAppBypassMark,
		AppBypassMask:          sysnetlinuxrouting.DefaultMarkMask,
		UserMark:               linuxDefaultUserMark,
		UserMarkMask:           sysnetlinuxrouting.DefaultMarkMask,
		KillswitchAllowExclude: true,
		Logf:                   golog.Debugf,
	}
}

func linuxDNSConfigFromSystem(config sysnetlinux.DNSConfig) linuxDNSConfig {
	return linuxDNSConfig{
		Mode:                   config.Mode,
		ResolvconfInterface:    config.ResolvconfInterface,
		ResolvedInterfaceIndex: config.ResolvedInterfaceIndex,
		FallbackServers:        linuxAddrPortsFromSystem(config.FallbackServers),
	}
}

func linuxPmarkConfigFromSystem(config sysnetlinux.PmarkConfig) linuxPmarkConfig {
	return linuxPmarkConfig{
		PinPath:              config.PinPath,
		TombCollectionEvents: config.TombCollectionEvents,
		TombTTL:              config.TombTTL,
		Priority:             config.Priority,
	}
}

func (c linuxPmarkConfig) systemConfig() sysnetlinux.PmarkConfig {
	return sysnetlinux.PmarkConfig{
		PinPath:              c.PinPath,
		TombCollectionEvents: c.TombCollectionEvents,
		TombTTL:              c.TombTTL,
		Priority:             c.Priority,
	}
}

func linuxAddrPortsFromSystem(values []netip.AddrPort) []linuxAddrPort {
	out := make([]linuxAddrPort, 0, len(values))
	for _, value := range values {
		out = append(out, linuxAddrPort{AddrPort: value})
	}
	return out
}

func (c linuxDNSConfig) systemConfig() sysnetlinux.DNSConfig {
	return sysnetlinux.DNSConfig{
		Mode:                   c.Mode,
		ResolvconfInterface:    c.ResolvconfInterface,
		ResolvedInterfaceIndex: c.ResolvedInterfaceIndex,
		FallbackServers:        linuxAddrPortsToSystem(c.FallbackServers),
	}
}

func linuxAddrPortsToSystem(values []linuxAddrPort) []netip.AddrPort {
	out := make([]netip.AddrPort, 0, len(values))
	for _, value := range values {
		out = append(out, value.AddrPort)
	}
	return out
}

func (a *linuxAddrPort) UnmarshalJSON(data []byte) error {
	var value string
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	addrPort, err := parseLinuxAddrPort(value)
	if err != nil {
		return err
	}
	a.AddrPort = addrPort
	return nil
}

func parseLinuxAddrPort(value string) (netip.AddrPort, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return netip.AddrPort{}, fmt.Errorf("empty DNS fallback server")
	}
	if addrPort, err := netip.ParseAddrPort(value); err == nil {
		return addrPort, nil
	}
	addr, err := netip.ParseAddr(value)
	if err != nil {
		return netip.AddrPort{}, fmt.Errorf("DNS fallback server %q: %w", value, err)
	}
	return netip.AddrPortFrom(addr, 53), nil
}

func (v *linuxUint32) UnmarshalJSON(data []byte) error {
	if string(data) == "null" {
		return nil
	}
	var number uint32
	if len(data) == 0 || data[0] != '"' {
		if err := json.Unmarshal(data, &number); err != nil {
			return err
		}
		*v = linuxUint32(number)
		return nil
	}

	var value string
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	parsed, err := parseLinuxHexUint32(value)
	if err != nil {
		return err
	}
	*v = linuxUint32(parsed)
	return nil
}

func parseLinuxHexUint32(value string) (uint32, error) {
	value = strings.TrimSpace(value)
	value = strings.TrimPrefix(value, "0x")
	value = strings.TrimPrefix(value, "0X")
	if value == "" {
		return 0, fmt.Errorf("empty uint32 hex value")
	}
	parsed, err := strconv.ParseUint(value, 16, 32)
	if err != nil {
		return 0, fmt.Errorf("uint32 hex value %q: %w", value, err)
	}
	return uint32(parsed), nil
}

// IsDir reports if path is a dir.
// If path not exists, reports false.
func IsDir(path string) (bool, error) {
	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		} else {
			return false, err
		}
	}
	return info.IsDir(), nil
}

func trustedUID(uid uint32) bool {
	return uid == 0 || uid == uint32(unix.Geteuid())
}

// checkSafeFD verifies that fd refers to a safe filesystem object.
//
// It checks the already-opened fd with fstat(). This is important: checking a
// pathname with stat() and then opening it later would be racy.
//
// The object is considered safe when:
//
//   - it has the expected filesystem type;
//   - it is owned by root or by the current effective UID;
//   - it is not writable by group or others.
func checkSafeFD(fd int, wantType uint32, label string) error {
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return fmt.Errorf("%s: fstat: %w", label, err)
	}

	if st.Mode&unix.S_IFMT != wantType {
		return fmt.Errorf("%s: unexpected file type: mode %#o", label, st.Mode)
	}

	if !trustedUID(st.Uid) {
		return fmt.Errorf("%s: owner uid %d is not trusted", label, st.Uid)
	}

	if st.Mode&0o022 != 0 {
		return fmt.Errorf("%s: writable by group or others: mode %#o", label, st.Mode&0o7777)
	}

	return nil
}

// checkPrivateDirFD verifies that fd is the final storage directory.
//
// Parent directories only need to be non-writable by untrusted users so common
// roots such as /, /home, and /var can be shared. The storage directory itself
// is stricter: it must be exactly 0700 so future files can contain secrets
// without exposing names or contents to group/other users.
func checkPrivateDirFD(fd int, label string) error {
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return fmt.Errorf("%s: fstat: %w", label, err)
	}

	if st.Mode&unix.S_IFMT != unix.S_IFDIR {
		return fmt.Errorf("%s: unexpected file type: mode %#o", label, st.Mode)
	}

	if !trustedUID(st.Uid) {
		return fmt.Errorf("%s: owner uid %d is not trusted", label, st.Uid)
	}

	if st.Mode&0o7777 != 0o700 {
		return fmt.Errorf("%s: storage directory mode %#o, want 0700", label, st.Mode&0o7777)
	}

	return nil
}

func checkPrivateDirPath(path string) error {
	fd, err := unix.Open(
		path,
		unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC,
		0,
	)
	if err != nil {
		return fmt.Errorf("%s: open: %w", path, err)
	}
	defer unix.Close(fd) //nolint:errcheck

	return checkPrivateDirFD(fd, path)
}

// splitAbsPath splits an absolute path into path components.
//
// The function requires an absolute path because safety checks for relative
// paths depend on the current working directory, which may be attacker-controlled
// or may change during program execution.
//
// The function rejects ".." before cleaning the path. This avoids surprising
// behavior around symlinks and parent-directory traversal.
func splitAbsPath(path string) ([]string, error) {
	if path == "" {
		return nil, fmt.Errorf("empty path")
	}

	if !filepath.IsAbs(path) {
		return nil, fmt.Errorf("path must be absolute: %q", path)
	}

	if slices.Contains(strings.Split(path, "/"), "..") {
		return nil, fmt.Errorf("path must not contain '..': %q", path)
	}

	clean := filepath.Clean(path)
	if clean == "/" {
		return nil, nil
	}

	parts := strings.Split(strings.TrimPrefix(clean, "/"), "/")
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return nil, fmt.Errorf("bad path component %q in %q", part, path)
		}
	}

	return parts, nil
}

// splitAbsFilePath splits an absolute file path into parent directory components
// and the final file name.
//
// For example:
//
//	/etc/myprog/config -> []string{"etc", "myprog"}, "config"
//
// A trailing slash is rejected because this function is for regular files, not
// directories.
func splitAbsFilePath(path string) (parents []string, base string, err error) {
	if strings.HasSuffix(path, "/") {
		return nil, "", fmt.Errorf("file path must not end with slash: %q", path)
	}

	parts, err := splitAbsPath(path)
	if err != nil {
		return nil, "", err
	}

	if len(parts) == 0 {
		return nil, "", fmt.Errorf("root directory is not a file path")
	}

	return parts[:len(parts)-1], parts[len(parts)-1], nil
}

// openSafeDirComponents opens a directory path component-by-component.
//
// It starts at "/" and then opens each component with openat(). Every opened
// directory is immediately checked with fstat().
//
// Symlinks are rejected because every component is opened with O_NOFOLLOW.
// Untrusted writable parent directories are rejected because every directory
// must pass checkSafeFD().
//
// If createMissing is true, missing directories are created with mode 0700.
// Existing directories are never chmodded here; if an existing directory is
// unsafe, this function returns an error.
func openSafeDirComponents(parts []string, createMissing bool) (int, error) {
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, fmt.Errorf("open /: %w", err)
	}

	ok := false
	defer func() {
		if !ok {
			_ = unix.Close(fd)
		}
	}()

	if err := checkSafeFD(fd, unix.S_IFDIR, "/"); err != nil {
		return -1, err
	}

	current := "/"

	for _, part := range parts {
		nextPath := filepath.Join(current, part)

		nextFD, err := unix.Openat(
			fd,
			part,
			unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC,
			0,
		)

		if err == unix.ENOENT && createMissing {
			if err := unix.Mkdirat(fd, part, 0o700); err != nil && err != unix.EEXIST {
				return -1, fmt.Errorf("%s: mkdirat: %w", nextPath, err)
			}

			nextFD, err = unix.Openat(
				fd,
				part,
				unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC,
				0,
			)
		}

		if err != nil {
			return -1, fmt.Errorf("%s: openat: %w", nextPath, err)
		}

		if err := checkSafeFD(nextFD, unix.S_IFDIR, nextPath); err != nil {
			_ = unix.Close(nextFD)
			return -1, err
		}

		_ = unix.Close(fd)
		fd = nextFD
		current = nextPath
	}

	ok = true
	return fd, nil
}

// MakeSureDirExists creates path and all missing parents with strict directory
// permissions.
//
// Missing directories are created with mode 0700.
//
// Existing directories are not modified. Instead, every existing directory in
// the path must already be safe:
//
//   - owned by root or by the current effective UID;
//   - not writable by group or others;
//   - not a symlink.
func MakeSureDirExists(path string) error {
	parts, err := splitAbsPath(path)
	if err != nil {
		return err
	}

	fd, err := openSafeDirComponents(parts, true)
	if err != nil {
		return err
	}

	return unix.Close(fd)
}

// makeSurePrivateDirExists creates path and missing parents, then verifies that
// path itself is a private 0700 storage directory.
//
// Existing parent directories use the same non-symlink, trusted-owner, and
// non-writable-by-group/other checks as config directories. Existing storage
// directories are not chmodded; an unsafe mode is reported as a setup error so
// the operator can inspect the path before secrets are written there.
func makeSurePrivateDirExists(path string) error {
	parts, err := splitAbsPath(path)
	if err != nil {
		return err
	}

	fd, err := openSafeDirComponents(parts, true)
	if err != nil {
		return err
	}
	defer unix.Close(fd) //nolint:errcheck

	return checkPrivateDirFD(fd, path)
}

// openSafeFileAt opens a file inside an already-opened safe parent directory.
//
// The final path component is opened with O_NOFOLLOW, so a final symlink is
// rejected. The opened fd is then checked with fstat(), so the check applies to
// exactly the file that will be read.
func openSafeFileAt(parentFD int, name string, fullPath string) (*os.File, error) {
	fd, err := unix.Openat(
		parentFD,
		name,
		unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC,
		0,
	)
	if err != nil {
		return nil, fmt.Errorf("%s: openat: %w", fullPath, err)
	}

	if err := checkSafeFD(fd, unix.S_IFREG, fullPath); err != nil {
		_ = unix.Close(fd)
		return nil, err
	}

	return os.NewFile(uintptr(fd), fullPath), nil
}

// MakeSureSafeFileExists creates a config file and all missing parent
// directories if they do not exist.
//
// Missing parent directories are created with mode 0700.
// A missing file is created with mode 0600 and filled with initial.
//
// Existing files are not modified, but they must already be safe:
//
//   - regular file;
//   - owned by root or by the current effective UID;
//   - not writable by group or others;
//   - not a symlink.
//
// Existing parent directories must also be safe by the same ownership/write
// rules, and symlinks in the path are rejected.
//
// This function is suitable for startup initialization. It is not enough for
// reloads by itself; every reload should still use OpenSafeFile or ReadSafeFile.
func MakeSureSafeFileExists(path string, initial []byte) error {
	parents, base, err := splitAbsFilePath(path)
	if err != nil {
		return err
	}

	parentFD, err := openSafeDirComponents(parents, true)
	if err != nil {
		return err
	}
	defer unix.Close(parentFD) //nolint:errcheck

	fd, err := unix.Openat(
		parentFD,
		base,
		unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC,
		0o600,
	)

	if err == unix.EEXIST {
		f, err := openSafeFileAt(parentFD, base, path)
		if err != nil {
			return err
		}
		return f.Close()
	}

	if err != nil {
		return fmt.Errorf("%s: create: %w", path, err)
	}

	if err := checkSafeFD(fd, unix.S_IFREG, path); err != nil {
		_ = unix.Close(fd)
		return err
	}

	f := os.NewFile(uintptr(fd), path)

	if len(initial) > 0 {
		if _, err := f.Write(initial); err != nil {
			_ = f.Close()
			return fmt.Errorf("%s: write initial config: %w", path, err)
		}
	}

	if err := f.Sync(); err != nil {
		_ = f.Close()
		return fmt.Errorf("%s: sync: %w", path, err)
	}

	return f.Close()
}

// OpenSafeFile opens path only if the file and all parent directories are safe.
//
// Use this every time the config is loaded or reloaded.
//
// The safety check is performed by walking the path with openat(), rejecting
// symlinks, and checking every opened directory/file with fstat(). This avoids
// classic time-of-check/time-of-use races where an attacker swaps the path
// between stat() and open().
func OpenSafeFile(path string) (*os.File, error) {
	parents, base, err := splitAbsFilePath(path)
	if err != nil {
		return nil, err
	}

	parentFD, err := openSafeDirComponents(parents, false)
	if err != nil {
		return nil, err
	}
	defer unix.Close(parentFD) //nolint:errcheck

	return openSafeFileAt(parentFD, base, path)
}

// ReadSafeFile safely opens and reads a config file.
//
// This should be used for every config reload. The file is opened, checked, and
// read through the same file descriptor.
func ReadSafeFile(path string) ([]byte, error) {
	f, err := OpenSafeFile(path)
	if err != nil {
		return nil, err
	}
	defer f.Close() //nolint:errcheck

	return io.ReadAll(f)
}

// import (
//
//	_ "embed"
//	"errors"
//	"fmt"
//	"io"
//	"os"
//	"path/filepath"
//	"slices"
//	"strings"
//
//	"github.com/asciimoth/configer/configer"
//	"github.com/asciimoth/gonnect/sysnet"
//	"golang.org/x/sys/unix"
//
// )
//
// //go:embed default_linux_config.hujson
// var DefaultConfigText string
//
// //go:embed default_linux_protected_config.hujson
// var DefaultProtectedConfigText string
//
//	func DefaultConfig() string {
//		return "/etc/almagest/alm.hujson"
//	}
//
