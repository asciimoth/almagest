//go:build linux && amd64

package main

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"

	"github.com/alexflint/go-arg"
	"github.com/asciimoth/almagest/bin/internal/daemonargs"
	"github.com/asciimoth/almagest/bin/internal/isolation/bootstrap"
	"github.com/asciimoth/almagest/bin/internal/isolation/linuxprofile"
	"github.com/asciimoth/almagest/bin/internal/isolation/networkrole"
	"github.com/asciimoth/almagest/bin/internal/isolation/protocol"
	"github.com/asciimoth/almagest/bin/internal/isolation/selection"
	"golang.org/x/sys/unix"
)

func linuxUserProduction(arguments daemonargs.Args, bin string) (networkrole.LinuxProduction, error) {
	uid, gid := uint32(os.Geteuid()), uint32(os.Getegid())
	ruid, euid, suid := unix.Getresuid()
	rgid, egid, sgid := unix.Getresgid()
	fsuid, fsuidErr := unix.SetfsuidRetUid(-1)
	fsgid, fsgidErr := unix.SetfsgidRetGid(-1)
	if uid == 0 || fsuidErr != nil || fsgidErr != nil || ruid != euid || euid != suid || euid != fsuid ||
		rgid != egid || egid != sgid || egid != fsgid {
		return networkrole.LinuxProduction{}, errors.New("linux-user-v1 requires stable non-root real and effective credentials")
	}
	root := arguments.IsolationStateDir
	if root == "" {
		account, err := user.LookupId(strconv.FormatUint(uint64(uid), 10))
		if err != nil || account.HomeDir == "" || !filepath.IsAbs(account.HomeDir) {
			return networkrole.LinuxProduction{}, errors.New("linux-user-v1 requires an account home or --isolation-state-dir")
		}
		root = filepath.Join(account.HomeDir, ".local", "share", "almagest", "isolation")
	}
	if !filepath.IsAbs(root) || filepath.Clean(root) != root {
		return networkrole.LinuxProduction{}, errors.New("--isolation-state-dir must be absolute and clean")
	}
	probePath := root
	for {
		if _, err := os.Stat(probePath); err == nil {
			break
		} else if !errors.Is(err, os.ErrNotExist) {
			return networkrole.LinuxProduction{}, fmt.Errorf("inspect user isolation state filesystem: %w", err)
		}
		parent := filepath.Dir(probePath)
		if parent == probePath {
			return networkrole.LinuxProduction{}, errors.New("user isolation state has no existing filesystem ancestor")
		}
		probePath = parent
	}
	var filesystem unix.Statfs_t
	if err := unix.Statfs(probePath, &filesystem); err != nil {
		return networkrole.LinuxProduction{}, fmt.Errorf("inspect user isolation state filesystem: %w", err)
	}
	if filesystem.Flags&unix.ST_NOEXEC != 0 {
		return networkrole.LinuxProduction{}, errors.New("user isolation state filesystem is mounted noexec")
	}
	makePrivate := func(path string) error {
		if err := bootstrap.EnsurePrivateDirectory(path, uid, gid); err != nil {
			return err
		}
		file, err := bootstrap.OpenPrivateDirectory(path, uid, gid)
		if file != nil {
			_ = file.Close()
		}
		return err
	}
	for _, path := range []string{root, filepath.Join(root, "runtime"), filepath.Join(root, "broker"),
		filepath.Join(root, "config"), filepath.Join(root, "protected")} {
		if err := makePrivate(path); err != nil {
			return networkrole.LinuxProduction{}, fmt.Errorf("prepare user isolation directory %s: %w", path, err)
		}
	}
	createDefault := func(path string) error {
		file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if errors.Is(err, os.ErrExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if _, err = file.WriteString("{}\n"); err == nil {
			err = file.Sync()
		}
		return errors.Join(err, file.Close())
	}
	configPath := arguments.Config
	if configPath == "" {
		configPath = filepath.Join(root, "config", "alm.hujson")
		if err := createDefault(configPath); err != nil {
			return networkrole.LinuxProduction{}, err
		}
	} else {
		var err error
		configPath, err = filepath.Abs(configPath)
		if err != nil || filepath.Clean(configPath) != configPath {
			return networkrole.LinuxProduction{}, errors.New("config path must resolve to an absolute clean path")
		}
	}
	protectedPath := arguments.ProtectedConfig
	if protectedPath == "" {
		protectedPath = filepath.Join(root, "protected", "protected_alm.hujson")
		if err := createDefault(protectedPath); err != nil {
			return networkrole.LinuxProduction{}, err
		}
	} else {
		var err error
		protectedPath, err = filepath.Abs(protectedPath)
		if err != nil || filepath.Clean(protectedPath) != protectedPath {
			return networkrole.LinuxProduction{}, errors.New("protected config path must resolve to an absolute clean path")
		}
	}
	runtimePath := arguments.FileStorage
	if runtimePath == "" {
		runtimePath = filepath.Join(root, "runtime")
	} else {
		var err error
		runtimePath, err = filepath.Abs(runtimePath)
		if err != nil || filepath.Clean(runtimePath) != runtimePath {
			return networkrole.LinuxProduction{}, errors.New("file storage path must be absolute and clean")
		}
	}
	runDirectory := filepath.Join(root, "run")
	if xdg := os.Getenv("XDG_RUNTIME_DIR"); filepath.IsAbs(xdg) && filepath.Clean(xdg) == xdg {
		if directory, err := bootstrap.OpenPrivateDirectory(xdg, uid, gid); err == nil {
			_ = directory.Close()
			digest := sha256.Sum256([]byte(root))
			runDirectory = filepath.Join(xdg, fmt.Sprintf("almagest-%x", digest[:8]))
		}
	}
	identity := networkrole.LinuxIdentity{UID: uid, GID: gid}
	arguments.Config, arguments.ProtectedConfig, arguments.FileStorage = configPath, protectedPath, runtimePath
	brokerPath := filepath.Join(root, "broker")
	configDirectory := filepath.Dir(configPath)
	writable := []string{runtimePath, configDirectory, brokerPath}
	for left := range writable {
		for right := left + 1; right < len(writable); right++ {
			if pathsOverlap(writable[left], writable[right]) {
				return networkrole.LinuxProduction{}, errors.New("user isolation runtime, config, and broker directories must be disjoint")
			}
		}
	}
	for _, directory := range writable {
		if pathsOverlap(runDirectory, directory) {
			return networkrole.LinuxProduction{}, errors.New("user isolation admin directory must be disjoint from role-writable directories")
		}
	}
	trustedInputs := []string{protectedPath, filepath.Join(bin, "almagestd"), filepath.Join(bin, "almagest-linux-role-launcher"), filepath.Join(bin, "almagest-config-writer-role")}
	for _, protected := range trustedInputs {
		for _, directory := range writable {
			if pathInside(protected, directory) {
				return networkrole.LinuxProduction{}, fmt.Errorf("trusted input %s is inside a user-profile writable directory", protected)
			}
		}
		if pathInside(protected, runDirectory) {
			return networkrole.LinuxProduction{}, fmt.Errorf("trusted input %s is inside the user-profile admin directory", protected)
		}
	}
	if err := makePrivate(runDirectory); err != nil {
		return networkrole.LinuxProduction{}, fmt.Errorf("prepare user runtime directory: %w", err)
	}
	if err := makePrivate(runtimePath); err != nil {
		return networkrole.LinuxProduction{}, err
	}
	domains := []string{runtimePath, configDirectory, brokerPath, runDirectory}
	for left := range domains {
		for right := left + 1; right < len(domains); right++ {
			aliased, err := directoryDomainsOverlap(domains[left], domains[right])
			if err != nil {
				return networkrole.LinuxProduction{}, fmt.Errorf("compare user isolation directory identities: %w", err)
			}
			if aliased {
				return networkrole.LinuxProduction{}, errors.New("user isolation directory identities overlap through a filesystem alias")
			}
		}
	}
	for _, domain := range domains {
		for _, trusted := range trustedInputs {
			aliased, err := directoryDomainsOverlap(domain, filepath.Dir(trusted))
			if err != nil {
				return networkrole.LinuxProduction{}, fmt.Errorf("compare trusted input and writable directory identities: %w", err)
			}
			if aliased {
				return networkrole.LinuxProduction{}, errors.New("trusted input and user isolation directory identities overlap through a filesystem alias")
			}
		}
	}
	for _, directory := range []string{runtimePath, configDirectory} {
		if err := bootstrap.ValidatePrivateTree(directory, uid, gid); err != nil {
			return networkrole.LinuxProduction{}, fmt.Errorf("validate user isolation tree %s: %w", directory, err)
		}
	}
	entries, err := os.ReadDir(configDirectory)
	if err != nil {
		return networkrole.LinuxProduction{}, err
	}
	for _, entry := range entries {
		if entry.Name() == ".tmp" && entry.IsDir() {
			continue
		}
		if entry.Name() != filepath.Base(configPath) {
			return networkrole.LinuxProduction{}, fmt.Errorf("user-profile config directory must be dedicated to %s; found %s", filepath.Base(configPath), entry.Name())
		}
	}
	return networkrole.LinuxProduction{Profile: linuxprofile.ProfileUser, Arguments: arguments,
		Sources: bootstrap.ConfigSources{RegularPath: configPath, ProtectedPath: protectedPath,
			CombinedStdin: arguments.StdInConfig, DefaultRegular: []byte("{}"), DefaultProtected: []byte("{}")},
		Identities: networkrole.LinuxProductionIdentities{Runtime: identity, Writer: identity, Helper: identity},
		Stdin:      os.Stdin, LookupEnv: os.LookupEnv, Output: os.Stderr,
		Paths: networkrole.LinuxProductionPaths{
			Launcher: filepath.Join(bin, "almagest-linux-role-launcher"), RuntimeExecutable: filepath.Join(bin, "almagestd"),
			WriterExecutable: filepath.Join(bin, "almagest-config-writer-role"), HelperExecutable: filepath.Join(bin, "almagest-command-role"),
			RuntimeDirectory: runtimePath, WriterDirectory: filepath.Dir(configPath), BrokerDirectory: brokerPath,
			AdminUnixDefault: filepath.Join(runDirectory, "admin.sock"),
			Layout: bootstrap.StorageLayout{Legacy: filepath.Join(root, "legacy-store"), Runtime: runtimePath, Broker: brokerPath,
				Backup: filepath.Join(root, "legacy-store.pre-isolation"), State: filepath.Join(root, "isolation-migration.json")},
		}}, nil
}

func pathInside(path, directory string) bool {
	relative, err := filepath.Rel(directory, path)
	return err == nil && relative != ".." && relative != "." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func pathsOverlap(left, right string) bool {
	return left == right || pathInside(left, right) || pathInside(right, left)
}

type directoryIdentity struct {
	device uint64
	inode  uint64
}

func directoryIdentityAt(path string) (directoryIdentity, error) {
	var stat unix.Stat_t
	if err := unix.Stat(path, &stat); err != nil {
		return directoryIdentity{}, err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFDIR {
		return directoryIdentity{}, errors.New("isolation domain is not a directory")
	}
	return directoryIdentity{device: uint64(stat.Dev), inode: stat.Ino}, nil
}

func directoryHasAncestorIdentity(path string, expected directoryIdentity) (bool, error) {
	for {
		identity, err := directoryIdentityAt(path)
		if err != nil {
			return false, err
		}
		if identity == expected {
			return true, nil
		}
		parent := filepath.Dir(path)
		if parent == path {
			return false, nil
		}
		path = parent
	}
}

func directoryDomainsOverlap(left, right string) (bool, error) {
	leftIdentity, err := directoryIdentityAt(left)
	if err != nil {
		return false, err
	}
	rightIdentity, err := directoryIdentityAt(right)
	if err != nil {
		return false, err
	}
	leftContainsRight, err := directoryHasAncestorIdentity(right, leftIdentity)
	if err != nil || leftContainsRight {
		return leftContainsRight, err
	}
	return directoryHasAncestorIdentity(left, rightIdentity)
}

const (
	defaultConfigPath    = "/var/lib/almagest/config/alm.hujson"
	defaultProtectedPath = "/etc/almagest/protected_alm.hujson"
	defaultRuntimePath   = "/var/lib/almagest/runtime"
	defaultHelperPath    = "/var/lib/almagest/helper"
	defaultBrokerPath    = "/var/lib/almagest/broker"
	developmentRootEnv   = "ALMAGEST_ISOLATION_DEVELOPMENT_ROOT"
	developmentIDsEnv    = "ALMAGEST_ISOLATION_DEVELOPMENT_IDENTITIES"
)

func runNetworkRole() error {
	var arguments daemonargs.Args
	if err := arg.Parse(&arguments); err != nil {
		return err
	}
	if arguments.NoIsolation {
		return errors.New("almagest-network-role cannot run direct mode; invoke almagestd --no-isolation")
	}
	if arguments.Experimental {
		return errors.New("--experimental requires explicit --no-isolation")
	}
	if arguments.TmpFileStorage || arguments.TmpUnixAdmin {
		return errors.New("temporary storage and admin paths are not qualified for isolated system launch; use fixed protected paths or explicit --no-isolation")
	}
	policy, err := selection.ResolvePolicy(arguments, selection.Platform{OS: runtime.GOOS, Arch: runtime.GOARCH})
	if err != nil {
		return err
	}
	self, err := currentExecutable()
	if err != nil {
		return err
	}
	userAvailable := false
	if os.Geteuid() != 0 && policy == selection.PolicyAuto {
		paths := []string{"almagest-linux-role-launcher", "almagestd", "almagest-config-writer-role"}
		files := make([]*os.File, 0, len(paths))
		for _, name := range paths {
			file, openErr := bootstrap.OpenTrustedFile(filepath.Join(filepath.Dir(self), name))
			if openErr != nil {
				for _, opened := range files {
					_ = opened.Close()
				}
				return fmt.Errorf("user-profile executable preflight: %w", openErr)
			}
			files = append(files, file)
		}
		probeErr := linuxprofile.ProbeUserPrerequisites(files[0], files[1:]...)
		var closeErr error
		for _, file := range files {
			closeErr = errors.Join(closeErr, file.Close())
		}
		if probeErr != nil || closeErr != nil {
			return errors.Join(probeErr, closeErr)
		}
		userAvailable = true
	}
	selected, err := selection.SelectProfile(selection.ProfileRequest{Policy: policy,
		Platform: selection.Platform{OS: runtime.GOOS, Arch: runtime.GOARCH}, UID: uint32(os.Geteuid()),
		FullAvailable: os.Geteuid() == 0, UserAvailable: userAvailable,
		UserProcessLimitsAvailable: true, IsolationStateDir: arguments.IsolationStateDir,
		Resources: selection.Resources(arguments)})
	if err != nil {
		return err
	}
	profile := linuxprofile.Profile(selected)
	developmentRoot := os.Getenv(developmentRootEnv)
	if developmentRoot != "" && os.Geteuid() != 0 {
		return errors.New("isolated development root requires a root network role")
	}
	production, err := linuxProduction(arguments, profile, func() (string, error) { return self, nil }, user.Lookup, developmentRoot, os.Getenv(developmentIDsEnv))
	if err != nil {
		return err
	}
	if value, set := os.LookupEnv("ALMAGEST_LOCALES_DIR"); set {
		if !filepath.IsAbs(value) || filepath.Clean(value) != value {
			return errors.New("ALMAGEST_LOCALES_DIR must be an absolute clean path")
		}
		production.LocalesDirectory = value
	} else if info, statErr := os.Stat("/usr/share/almagest/locales"); statErr == nil && info.IsDir() {
		production.LocalesDirectory = "/usr/share/almagest/locales"
	}
	if err := unix.Prctl(unix.PR_SET_DUMPABLE, 0, 0, 0, 0); err != nil {
		return fmt.Errorf("disable network broker dumps: %w", err)
	}
	production.SystemRandomSeed, err = systemRandomSeedOverride()
	if err != nil {
		return err
	}
	production.E2ETailscaleRejectAfterSeconds, err = tailscaleRejectAfterOverride()
	if err != nil {
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	err = networkrole.RunLinuxProduction(ctx, production, func() {
		_, _ = fmt.Fprintf(os.Stderr, "Execution mode: isolation; protocol=%d; runtime=linux-namespaces+landlock+seccomp; broker=trusted-native\n", protocol.Version)
	})
	if ctx.Err() != nil && errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}

func currentExecutable() (string, error) {
	path, err := os.Readlink("/proc/self/exe")
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(path) || strings.HasSuffix(path, " (deleted)") {
		return "", errors.New("network role executable has no stable installed path")
	}
	return filepath.Clean(path), nil
}

func linuxProduction(arguments daemonargs.Args, profile linuxprofile.Profile, executable func() (string, error), lookup func(string) (*user.User, error), developmentRoot, developmentIDs string) (networkrole.LinuxProduction, error) {
	if executable == nil || developmentRoot == "" && lookup == nil {
		return networkrole.LinuxProduction{}, errors.New("missing Linux bootstrap dependency")
	}
	self, err := executable()
	if err != nil {
		return networkrole.LinuxProduction{}, err
	}
	self, err = filepath.Abs(self)
	if err != nil {
		return networkrole.LinuxProduction{}, err
	}
	bin := filepath.Dir(self)
	if profile == linuxprofile.ProfileUser {
		return linuxUserProduction(arguments, bin)
	}
	if profile != linuxprofile.ProfileFull {
		return networkrole.LinuxProduction{}, errors.New("unknown Linux isolation profile")
	}
	if developmentRoot != "" {
		if !filepath.IsAbs(developmentRoot) || filepath.Clean(developmentRoot) != developmentRoot {
			return networkrole.LinuxProduction{}, errors.New("isolated development root must be absolute and clean")
		}
		if arguments.Config != "" || arguments.ProtectedConfig != "" || arguments.FileStorage != "" {
			return networkrole.LinuxProduction{}, errors.New("isolated development root cannot be combined with config or storage path flags")
		}
		arguments.Config = filepath.Join(developmentRoot, "config", "alm.hujson")
		arguments.ProtectedConfig = filepath.Join(developmentRoot, "protected", "protected_alm.hujson")
		arguments.FileStorage = filepath.Join(developmentRoot, "runtime")
	}
	configPath, err := absolutePath(arguments.Config, defaultConfigPath)
	if err != nil {
		return networkrole.LinuxProduction{}, err
	}
	if info, statErr := os.Stat(configPath); statErr == nil && info.IsDir() {
		configPath = filepath.Join(configPath, "alm.hujson")
	}
	protectedPath, err := absolutePath(arguments.ProtectedConfig, defaultProtectedPath)
	if err != nil {
		return networkrole.LinuxProduction{}, err
	}
	runtimePath, err := absolutePath(arguments.FileStorage, defaultRuntimePath)
	if err != nil {
		return networkrole.LinuxProduction{}, err
	}
	identities := networkrole.LinuxProductionIdentities{}
	if developmentRoot != "" {
		identities, err = parseDevelopmentIdentities(developmentIDs)
		if err != nil {
			return networkrole.LinuxProduction{}, err
		}
	} else {
		if developmentIDs != "" {
			return networkrole.LinuxProduction{}, errors.New("development identities require an isolated development root")
		}
		for name, output := range map[string]*networkrole.LinuxIdentity{
			"almagest-runtime": &identities.Runtime,
			"almagest-writer":  &identities.Writer,
			"almagest-command": &identities.Helper,
		} {
			account, lookupErr := lookup(name)
			if lookupErr != nil {
				return networkrole.LinuxProduction{}, fmt.Errorf("required isolation account %s: %w; install the system profile or use explicit --no-isolation", name, lookupErr)
			}
			uid, uidErr := strconv.ParseUint(account.Uid, 10, 32)
			gid, gidErr := strconv.ParseUint(account.Gid, 10, 32)
			if uidErr != nil || gidErr != nil || uid == 0 || gid == 0 {
				return networkrole.LinuxProduction{}, fmt.Errorf("required isolation account %s has an invalid non-root identity", name)
			}
			*output = networkrole.LinuxIdentity{UID: uint32(uid), GID: uint32(gid)}
		}
	}
	arguments.Config, arguments.ProtectedConfig, arguments.FileStorage = configPath, protectedPath, runtimePath
	sources := bootstrap.ConfigSources{RegularPath: configPath, ProtectedPath: protectedPath,
		CombinedStdin: arguments.StdInConfig, DefaultRegular: []byte("{}"), DefaultProtected: []byte("{}")}
	helperPath, brokerPath := defaultHelperPath, defaultBrokerPath
	layout := bootstrap.StorageLayout{Legacy: "/var/lib/almagest/store", Runtime: runtimePath, Broker: defaultBrokerPath,
		Backup: "/var/lib/almagest/store.pre-isolation", State: "/var/lib/almagest/isolation-migration.json"}
	if developmentRoot != "" {
		helperPath = filepath.Join(developmentRoot, "helper")
		brokerPath = filepath.Join(developmentRoot, "broker")
		layout = bootstrap.StorageLayout{Legacy: filepath.Join(developmentRoot, "legacy-store"), Runtime: runtimePath,
			Broker: brokerPath, Backup: filepath.Join(developmentRoot, "legacy-store.pre-isolation"),
			State: filepath.Join(developmentRoot, "isolation-migration.json")}
	}
	return networkrole.LinuxProduction{Profile: linuxprofile.ProfileFull, Arguments: arguments, Sources: sources, Identities: identities, Stdin: os.Stdin,
		LookupEnv: os.LookupEnv, Output: os.Stderr, Paths: networkrole.LinuxProductionPaths{
			Launcher: filepath.Join(bin, "almagest-linux-role-launcher"), RuntimeExecutable: filepath.Join(bin, "almagestd"),
			WriterExecutable: filepath.Join(bin, "almagest-config-writer-role"), HelperExecutable: filepath.Join(bin, "almagest-command-role"),
			RuntimeDirectory: runtimePath, WriterDirectory: filepath.Dir(configPath), HelperDirectory: helperPath, BrokerDirectory: brokerPath,
			Layout: layout,
		}}, nil
}

func parseDevelopmentIdentities(value string) (networkrole.LinuxProductionIdentities, error) {
	parts := strings.Split(value, ",")
	if len(parts) != 3 {
		return networkrole.LinuxProductionIdentities{}, errors.New("isolated development identities must contain runtime, writer, and command UID/GID pairs")
	}
	parsed := make([]networkrole.LinuxIdentity, len(parts))
	seenUIDs, seenGIDs := map[uint32]bool{}, map[uint32]bool{}
	for index, part := range parts {
		values := strings.Split(part, ":")
		if len(values) != 2 {
			return networkrole.LinuxProductionIdentities{}, errors.New("isolated development identity must be UID:GID")
		}
		uid, uidErr := strconv.ParseUint(values[0], 10, 32)
		gid, gidErr := strconv.ParseUint(values[1], 10, 32)
		if uidErr != nil || gidErr != nil || uid == 0 || gid == 0 || uid == uint64(^uint32(0)) || gid == uint64(^uint32(0)) {
			return networkrole.LinuxProductionIdentities{}, errors.New("isolated development identity must use valid non-root UID and GID values")
		}
		identity := networkrole.LinuxIdentity{UID: uint32(uid), GID: uint32(gid)}
		if seenUIDs[identity.UID] || seenGIDs[identity.GID] {
			return networkrole.LinuxProductionIdentities{}, errors.New("isolated development identities must use distinct UID and GID values")
		}
		seenUIDs[identity.UID], seenGIDs[identity.GID] = true, true
		parsed[index] = identity
	}
	return networkrole.LinuxProductionIdentities{Runtime: parsed[0], Writer: parsed[1], Helper: parsed[2]}, nil
}

func absolutePath(value, fallback string) (string, error) {
	if value == "" {
		value = fallback
	}
	path, err := filepath.Abs(value)
	if err != nil {
		return "", err
	}
	return filepath.Clean(path), nil
}
