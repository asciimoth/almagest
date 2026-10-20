//go:build linux && amd64

package networkrole

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/asciimoth/almagest/bin/internal/daemonargs"
	"github.com/asciimoth/almagest/bin/internal/isolation/admingate"
	"github.com/asciimoth/almagest/bin/internal/isolation/bootstrap"
	"github.com/asciimoth/almagest/bin/internal/isolation/callbackbridge"
	"github.com/asciimoth/almagest/bin/internal/isolation/launchconfig"
	"github.com/asciimoth/almagest/bin/internal/isolation/linuxprofile"
	"github.com/asciimoth/almagest/bin/internal/isolation/policy"
	"github.com/asciimoth/almagest/bin/internal/isolation/protocol"
	"github.com/asciimoth/almagest/bin/internal/isolation/roleentry"
	"github.com/asciimoth/almagest/bin/internal/isolation/sandboxrole"
	"github.com/asciimoth/almagest/bin/internal/isolation/supervision"
	sandboxdiscovery "github.com/asciimoth/almagest/bin/internal/subprogram/discovery"
	sandboxpolicy "github.com/asciimoth/almagest/bin/internal/subprogram/policy"
	"golang.org/x/sys/unix"
)

type LinuxIdentity struct {
	UID uint32
	GID uint32
}

type LinuxProductionPaths struct {
	Launcher, RuntimeExecutable, WriterExecutable, HelperExecutable string
	RuntimeDirectory, WriterDirectory, HelperDirectory              string
	BrokerDirectory                                                 string
	AdminUnixDefault                                                string
	Layout                                                          bootstrap.StorageLayout
}

type LinuxProductionIdentities struct {
	Runtime LinuxIdentity
	Writer  LinuxIdentity
	Helper  LinuxIdentity
}

// sandboxLaunchPolicy contains only the paths which trusted startup must keep
// outside the runtime storage tree. The sandbox supervisor performs the full,
// strict policy decode before it reports readiness.
type sandboxLaunchPolicy struct {
	TemporaryRoot   string
	RegistryRoot    string
	LinuxLauncher   string
	LinuxCgroupRoot string
	Programs        map[string]struct{ Executable string }
	Templates       map[string]struct {
		PersistentRoot string
		ReadOnly       []struct{ Source string }
	}
}

// LinuxProduction contains only launch-selected trusted state. Sources and
// paths are fixed before any role starts; regular config and snapshot bytes do
// not select authority.
type LinuxProduction struct {
	Profile    linuxprofile.Profile
	Arguments  daemonargs.Args
	Sources    bootstrap.ConfigSources
	Paths      LinuxProductionPaths
	Identities LinuxProductionIdentities
	Limits     linuxprofile.Limits
	// SystemRandomSeed is a trusted test input. Production launchers leave it
	// nil so native address allocation uses cryptographic process entropy.
	SystemRandomSeed *int64
	// E2ETailscaleRejectAfterSeconds is a trusted test input. Production
	// launchers leave it zero.
	E2ETailscaleRejectAfterSeconds uint32
	Stdin                          io.Reader
	LookupEnv                      func(string) (string, bool)
	Output                         io.Writer
	LocalesDirectory               string
}

// RunLinuxProduction prepares protected authority, opens every trusted object,
// provisions separate cgroups, and enters RunLinux. It never falls back to the
// direct daemon when a profile requirement is unavailable.
func RunLinuxProduction(parent context.Context, production LinuxProduction, ready func()) (result error) {
	if parent == nil || production.Arguments.NoIsolation || production.Arguments.Experimental {
		return supervision.ErrInvalidComposition
	}
	if production.Arguments.TmpFileStorage || production.Arguments.TmpUnixAdmin {
		return fmt.Errorf("temporary storage and admin paths are not qualified for isolated system launch; use fixed protected paths or explicit --no-isolation")
	}
	if production.Arguments.StdInConfig && production.Arguments.Snapshot == "-" {
		return errors.New("--stdin and --snapshot - cannot read from stdin together")
	}
	if production.LookupEnv == nil {
		production.LookupEnv = os.LookupEnv
	}
	limits, err := linuxProductionLimits(production.Arguments, production.Limits)
	if err != nil {
		return err
	}
	production.Limits = limits
	if production.Profile == linuxprofile.ProfileUser &&
		(production.Arguments.IsolationMemoryBytes != nil || production.Arguments.IsolationTasks != nil || production.Arguments.IsolationCPUQuota != nil) {
		return fmt.Errorf("explicit memory, task, and CPU isolation limits require delegated per-role cgroups")
	}
	output := newBoundedRoleOutput(production.Output)
	production.Output = output
	defer func() { result = errors.Join(result, output.Close()) }()
	loader := bootstrap.NativeLoader{}
	production.Sources.Stdin = production.Stdin
	if !production.Sources.CombinedStdin {
		// Regular config is R/C-owned data in isolated mode. Read it through the
		// no-follow untrusted loader even when direct mode would require the
		// explicit unsafe-config switch. It cannot select launch authority.
		production.Sources.UnsafeRegular = true
	}
	inputs, err := bootstrap.LoadConfig(production.Sources, loader)
	if err != nil {
		return err
	}
	resolved, err := bootstrap.ResolveProtected(inputs.Protected, loader, production.LookupEnv)
	if err != nil {
		return err
	}
	var sandboxConfig *sandboxLaunchPolicy
	var sandboxUnavailable sandboxrole.SandboxDiagnostics
	if sandboxConfigured(resolved.Authority.Sandbox) || production.Arguments.DiscoverSystemPrograms {
		policyData := resolved.Authority.Sandbox
		if len(policyData) == 0 {
			policyData = []byte("{}")
		}
		configured, parseErr := sandboxpolicy.Parse(policyData)
		if parseErr != nil {
			return fmt.Errorf("protected sandbox launch policy: %w", parseErr)
		}
		launcher := filepath.Join(filepath.Dir(production.Paths.RuntimeExecutable), "almagest-subprogram-launcher")
		explicitConfig := configured
		configured, found, configureErr := sandboxdiscovery.Configure(configured, sandboxdiscovery.Options{
			Enable: production.Arguments.DiscoverSystemPrograms, StateRoot: sandboxdiscovery.DefaultStateRoot(production.Paths.BrokerDirectory),
			LinuxLauncher: launcher, GOOS: "linux",
		})
		if configureErr != nil {
			_, _ = fmt.Fprintf(production.Output, "WARN sandbox discovery failed; explicit policy remains active: %v\n", configureErr)
			configured = explicitConfig
			found = nil
		}
		if debugLogsEnabled(production.Arguments.LogLevel) {
			for _, item := range found {
				_, _ = fmt.Fprintf(production.Output, "DEBUG sandbox discovered program=%s path=%s\n", item.Program, item.Executable)
			}
		}
		for id, reason := range configured.UnavailableTemplates {
			_, _ = fmt.Fprintf(production.Output, "WARN sandbox template=%s unavailable: %s\n", id, reason)
		}
		for id, reason := range configured.UnavailablePrograms {
			_, _ = fmt.Fprintf(production.Output, "WARN sandbox program=%s unavailable: %s\n", id, reason)
		}
		if len(configured.Programs) != 0 || len(configured.Templates) != 0 || len(configured.UnavailableTemplates) != 0 {
			configured, configureErr = sandboxpolicy.Normalize(configured)
			if configureErr != nil {
				return fmt.Errorf("protected sandbox launch policy: %w", configureErr)
			}
			sandboxUnavailable = sandboxDiagnostics(configured)
			resolved.Authority.Sandbox, configureErr = json.Marshal(configured)
			if configureErr != nil {
				return configureErr
			}
		} else {
			resolved.Authority.Sandbox = nil
		}
	}
	if sandboxConfigured(resolved.Authority.Sandbox) {
		var value sandboxLaunchPolicy
		decodeErr := json.Unmarshal(resolved.Authority.Sandbox, &value)
		if decodeErr != nil {
			return fmt.Errorf("protected sandbox launch policy: %w", decodeErr)
		}
		sandboxConfig = &value
	}
	adminDefault := production.Paths.AdminUnixDefault
	if adminDefault == "" {
		adminDefault = "/run/almagest/admin.sock"
	}
	admin, err := launchconfig.Admin(resolved.Authority.Admin,
		launchconfig.AdminDefaults{Unix: adminDefault}, production.Arguments, &resolved.Runtime)
	if err != nil {
		return err
	}
	for _, endpoint := range admin {
		if endpoint.Kind == admingate.EndpointPipe {
			return errors.New("windows admin pipe is unavailable in the Linux isolation profile")
		}
	}
	if production.Profile == linuxprofile.ProfileUser {
		if err := validateUserAdminEndpoints(admin, production); err != nil {
			return err
		}
	}
	fileConfig, err := launchconfig.LinuxSystem(resolved.Authority.System)
	if err != nil {
		return err
	}
	helperConfig, commandExecutable, err := launchconfig.Commands(resolved.Authority.CommandRunner)
	if err != nil {
		return err
	}
	callbackBindings, err := callbackbridge.Compile(fileConfig.Callbacks, &helperConfig)
	if err != nil {
		return err
	}
	if production.Profile == linuxprofile.ProfileUser && (commandExecutable != "" || !callbackBindings.Empty()) {
		return fmt.Errorf("%s does not support command helpers or native callbacks", linuxprofile.ProfileUser)
	}

	trustedPaths := []string{production.Paths.Launcher, production.Paths.RuntimeExecutable}
	if sandboxConfig != nil {
		trustedPaths = append(trustedPaths, sandboxConfig.TemporaryRoot, sandboxConfig.RegistryRoot)
		for _, path := range []string{sandboxConfig.LinuxLauncher, sandboxConfig.LinuxCgroupRoot} {
			if path != "" {
				trustedPaths = append(trustedPaths, path)
			}
		}
		for _, program := range sandboxConfig.Programs {
			trustedPaths = append(trustedPaths, program.Executable)
		}
		for _, template := range sandboxConfig.Templates {
			if template.PersistentRoot != "" {
				trustedPaths = append(trustedPaths, template.PersistentRoot)
			}
			for _, input := range template.ReadOnly {
				trustedPaths = append(trustedPaths, input.Source)
			}
		}
	}
	if production.Sources.ProtectedPath != "" {
		trustedPaths = append(trustedPaths, production.Sources.ProtectedPath)
	}
	if !production.Sources.CombinedStdin {
		trustedPaths = append(trustedPaths, production.Paths.WriterExecutable)
	}
	if commandExecutable != "" {
		trustedPaths = append(trustedPaths, production.Paths.HelperExecutable)
	}
	if commandExecutable != "" {
		trustedPaths = append(trustedPaths, commandExecutable)
	}
	if production.LocalesDirectory != "" {
		trustedPaths = append(trustedPaths, production.LocalesDirectory)
	}
	if err := production.Paths.Layout.RejectRuntimePaths(trustedPaths...); err != nil {
		return err
	}

	files := make([]*os.File, 0, 12)
	defer func() {
		for _, file := range files {
			result = errors.Join(result, file.Close())
		}
	}()
	openTrusted := func(path string) (*os.File, error) {
		file, err := bootstrap.OpenTrustedFile(path)
		if err == nil {
			files = append(files, file)
		}
		return file, err
	}
	openPrivate := func(path string, identity LinuxIdentity) (*os.File, error) {
		file, err := bootstrap.OpenPrivateDirectory(path, identity.UID, identity.GID)
		if err == nil && production.Profile == linuxprofile.ProfileUser {
			err = validateHeldPrivateTree(file, path, identity)
			if err != nil {
				_ = file.Close()
				file = nil
			}
		}
		if err == nil {
			files = append(files, file)
		}
		return file, err
	}
	launcher, err := openTrusted(production.Paths.Launcher)
	if err != nil {
		return err
	}
	runtimeExecutable, err := openTrusted(production.Paths.RuntimeExecutable)
	if err != nil {
		return err
	}
	runtimeDirectory, err := openPrivate(production.Paths.RuntimeDirectory, production.Identities.Runtime)
	if err != nil {
		return err
	}
	var locales *os.File
	if production.LocalesDirectory != "" {
		var openErr error
		locales, openErr = openOptionalLocalesDirectory(production.LocalesDirectory)
		if openErr != nil {
			return fmt.Errorf("open localized content root: %w", openErr)
		}
		if locales != nil {
			files = append(files, locales)
		}
	}
	brokerIdentity := LinuxIdentity{UID: uint32(os.Geteuid()), GID: uint32(os.Getegid())}
	brokerDirectory, err := openPrivate(production.Paths.BrokerDirectory, brokerIdentity)
	if err != nil {
		return err
	}
	lock, err := os.OpenFile(filepath.Join(production.Paths.BrokerDirectory, "instance.lock"), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return fmt.Errorf("open isolation instance lock: %w", err)
	}
	files = append(files, lock)
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		return fmt.Errorf("lock isolation instance: %w", err)
	}
	var runtimeCode, writerCode *os.File
	if production.Profile == linuxprofile.ProfileUser {
		if err := recoverUserCodeGeneration(production.Paths.BrokerDirectory, brokerDirectory, brokerIdentity); err != nil {
			return err
		}
		codeRoot, createErr := os.MkdirTemp(production.Paths.BrokerDirectory, ".code-")
		if createErr != nil {
			return fmt.Errorf("create private role code root: %w", createErr)
		}
		if err := os.Chmod(codeRoot, 0o700); err != nil {
			return err
		}
		if err := writeUserCodeReceipt(production.Paths.BrokerDirectory, brokerDirectory, filepath.Base(codeRoot), brokerIdentity); err != nil {
			_ = os.RemoveAll(codeRoot)
			return err
		}
		defer func() {
			if !errors.Is(result, supervision.ErrShutdownIncomplete) {
				result = errors.Join(result, removeUserCodeGeneration(production.Paths.BrokerDirectory, brokerDirectory, brokerIdentity))
			}
		}()
		for name, target := range map[string]**os.File{"runtime": &runtimeCode, "writer": &writerCode} {
			path := filepath.Join(codeRoot, name)
			if err := os.Mkdir(path, 0o700); err != nil {
				return err
			}
			file, openErr := openPrivate(path, brokerIdentity)
			if openErr != nil {
				return openErr
			}
			*target = file
		}
	}
	journal, err := supervision.OpenJournal(filepath.Join(production.Paths.BrokerDirectory, "recovery.json"), 0)
	if err != nil {
		return err
	}

	runtime := LinuxRole{Plan: linuxprofile.Plan{Profile: production.Profile, Role: linuxprofile.Runtime, UID: production.Identities.Runtime.UID,
		GID: production.Identities.Runtime.GID, Limits: production.Limits}, Inputs: linuxprofile.Inputs{
		Launcher: launcher, Executable: runtimeExecutable, Directory: runtimeDirectory, Code: runtimeCode,
	}}
	var writer *LinuxWriter
	var writerExecutable *os.File
	if !production.Sources.CombinedStdin {
		if filepath.Dir(production.Sources.RegularPath) != production.Paths.WriterDirectory {
			return fmt.Errorf("regular config must be inside the fixed isolated writer directory")
		}
		var openErr error
		writerExecutable, openErr = openTrusted(production.Paths.WriterExecutable)
		if openErr != nil {
			return openErr
		}
		writerDirectory, openErr := openPrivate(production.Paths.WriterDirectory, production.Identities.Writer)
		if openErr != nil {
			return openErr
		}
		writer = &LinuxWriter{LinuxRole: LinuxRole{Plan: linuxprofile.Plan{Profile: production.Profile, Role: linuxprofile.Writer,
			UID: production.Identities.Writer.UID, GID: production.Identities.Writer.GID, Limits: production.Limits},
			Inputs: linuxprofile.Inputs{Launcher: launcher, Executable: writerExecutable, Directory: writerDirectory, Code: writerCode}},
			Target: filepath.Base(production.Sources.RegularPath)}
	}
	var helper *LinuxHelper
	if commandExecutable != "" {
		helperExecutable, openErr := openTrusted(production.Paths.HelperExecutable)
		if openErr != nil {
			return openErr
		}
		helperDirectory, openErr := openPrivate(production.Paths.HelperDirectory, production.Identities.Helper)
		if openErr != nil {
			return openErr
		}
		command, openErr := openTrusted(commandExecutable)
		if openErr != nil {
			return openErr
		}
		helper = &LinuxHelper{LinuxRole: LinuxRole{Plan: linuxprofile.Plan{Profile: production.Profile, Role: linuxprofile.Command,
			UID: production.Identities.Helper.UID, GID: production.Identities.Helper.GID, Limits: production.Limits},
			Inputs: linuxprofile.Inputs{Launcher: launcher, Executable: helperExecutable, Directory: helperDirectory, Command: command}}, Config: helperConfig}
	}

	var snapshot io.ReadCloser
	if production.Arguments.Snapshot != "" {
		snapshot, err = bootstrap.OpenSnapshot(production.Arguments.Snapshot, production.Stdin)
		if err != nil {
			return err
		}
		defer func() { result = errors.Join(result, snapshot.Close()) }()
	}
	nodeCommands := make([]string, 0, len(helperConfig.Commands))
	runtimeNodeCommands := make([]roleentry.RuntimeNodeCommand, 0, len(helperConfig.Commands))
	for id, command := range helperConfig.Commands {
		if command.AllowNodes {
			nodeCommands = append(nodeCommands, id)
			profile := helperConfig.Profiles[command.Profile]
			slots := make([]string, 0, len(profile.Slots))
			for slot := range profile.Slots {
				slots = append(slots, slot)
			}
			slices.Sort(slots)
			runtimeNodeCommands = append(runtimeNodeCommands, roleentry.RuntimeNodeCommand{Name: id, Slots: slots})
		}
	}
	slices.Sort(nodeCommands)
	slices.SortFunc(runtimeNodeCommands, func(a, b roleentry.RuntimeNodeCommand) int { return strings.Compare(a.Name, b.Name) })
	launchPolicy := policy.Default(policy.LaunchAuthority{Backend: "linux", CredentialClass: linuxCredentialClass(),
		ProtectedDigest: resolved.Digest}, policy.CommandAuthority{NodeCommands: nodeCommands, Callbacks: callbackBindings.IDs()})
	native := fileConfig.Native(func(format string, values ...any) {
		if production.Output != nil && debugLogsEnabled(production.Arguments.LogLevel) {
			_, _ = fmt.Fprintf(production.Output, "DEBUG network "+format+"\n", values...)
		}
	})
	if production.SystemRandomSeed != nil {
		native.Allocator.Rng = rand.New(rand.NewSource(*production.SystemRandomSeed)) //nolint:gosec // Deterministic E2E input.
	}
	native.DefaultTunBaseName = "alm"
	rolePlan := linuxprofile.CgroupPlan{Backend: production.Limits, Runtime: production.Limits}
	if writer != nil {
		limits := production.Limits
		rolePlan.Writer = &limits
	}
	if helper != nil {
		limits := production.Limits
		rolePlan.Command = &limits
	}
	if sandboxConfig != nil {
		limits := production.Limits
		rolePlan.Sandbox = &limits
	}
	var cgroups *linuxprofile.ServiceCgroups
	if production.Profile != linuxprofile.ProfileUser {
		cgroups, err = linuxprofile.ProvisionServiceCgroups(rolePlan)
		if err != nil {
			return err
		}
		defer func() {
			result = errors.Join(result, cgroups.RestoreAndRemove(), cgroups.Close())
		}()
	}
	isolationStatus := linuxIsolationStatus(production.Profile, writer != nil, helper != nil, sandboxConfig != nil, !callbackBindings.Empty())
	trustedPlan, err := trustedPlanMaterial(files, production.Profile)
	if err != nil {
		return err
	}
	var expectedRuntimeCode, expectedWriterCode [32]byte
	if production.Profile == linuxprofile.ProfileUser {
		expectedRuntimeCode, err = heldFileDigest(runtimeExecutable)
		if err != nil {
			return err
		}
		if writerExecutable != nil {
			expectedWriterCode, err = heldFileDigest(writerExecutable)
			if err != nil {
				return err
			}
		}
	}
	if err := applyAdaptiveLinuxStatus(&isolationStatus, production, resolved.Digest, trustedPlan); err != nil {
		return fmt.Errorf("observe selected isolation plan: %w", err)
	}
	logAdaptiveLinuxStatus(production.Output, "selected", isolationStatus, debugLogsEnabled(production.Arguments.LogLevel))
	launch := LinuxLaunch{Runtime: runtime, Writer: writer, Helper: helper,
		RuntimeInput: roleentry.RuntimeInput{Version: protocol.Version, Production: true, NodeCommands: runtimeNodeCommands, Options: roleentry.RuntimeOptions{
			LogLevel: production.Arguments.LogLevel, LogWorkers: production.Arguments.LogWorkers,
			NoPool: production.Arguments.NoPool, NoDNSCacheExpiration: production.Arguments.NoDNSCacheExpiration,
			DebugNodes: production.Arguments.DebugNodes, E2ETailscaleRejectAfterSeconds: production.E2ETailscaleRejectAfterSeconds,
			LogCacheDebug: production.Arguments.LogCacheDebug,
			LogCacheInfo:  production.Arguments.LogCacheInfo, LogCacheWarn: production.Arguments.LogCacheWarn,
			LogCacheError: production.Arguments.LogCacheError, LogCacheFatal: production.Arguments.LogCacheFatal,
		}, Isolation: isolationStatus}, RuntimeStorage: &bootstrap.RuntimeStorageInput{Regular: inputs.Regular, Protected: resolved.Runtime, Snapshot: snapshot},
		Policy: launchPolicy, Native: native, State: brokerDirectory, Limits: production.Limits, Locales: locales,
		Journal: journal, Admin: admin, Output: production.Output, Callbacks: callbackBindings,
	}
	if sandboxConfig != nil {
		launch.Sandbox = &LinuxSandbox{Policy: slices.Clone(resolved.Authority.Sandbox), Unavailable: sandboxUnavailable}
	}
	if cgroups != nil {
		launch.Cgroups = cgroups.Roles()
		if launch.Sandbox != nil {
			root, rootErr := launch.Cgroups.SandboxRootPath()
			if rootErr != nil {
				return rootErr
			}
			launch.Sandbox.Policy, rootErr = setSandboxCgroupRoot(launch.Sandbox.Policy, root)
			if rootErr != nil {
				return rootErr
			}
		}
	}
	return RunLinux(parent, launch, func(finalStatus roleentry.RuntimeIsolationStatus) error {
		if production.Profile == linuxprofile.ProfileUser {
			if err := verifyUserCodeCopy(runtimeExecutable, runtimeCode, expectedRuntimeCode); err != nil {
				return fmt.Errorf("verify staged runtime executable: %w", err)
			}
			if writerCode != nil {
				if err := verifyUserCodeCopy(writerExecutable, writerCode, expectedWriterCode); err != nil {
					return fmt.Errorf("verify staged writer executable: %w", err)
				}
			}
		}
		logAdaptiveLinuxStatus(production.Output, "verified", finalStatus, debugLogsEnabled(production.Arguments.LogLevel))
		if ready != nil {
			ready()
		}
		return nil
	})
}

func openOptionalLocalesDirectory(path string) (*os.File, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), path), nil
}

func sandboxDiagnostics(config sandboxpolicy.Config) sandboxrole.SandboxDiagnostics {
	result := sandboxrole.SandboxDiagnostics{
		Templates: make(map[string]string, len(config.UnavailableTemplates)),
		Programs:  make(map[string]string, len(config.UnavailablePrograms)),
	}
	for id, reason := range config.UnavailableTemplates {
		result.Templates[string(id)] = reason
	}
	for id, reason := range config.UnavailablePrograms {
		result.Programs[string(id)] = reason
	}
	return result
}

func sandboxConfigured(value []byte) bool {
	trimmed := strings.TrimSpace(string(value))
	if trimmed == "" || trimmed == "null" || trimmed == "{}" {
		return false
	}
	var root map[string]json.RawMessage
	if json.Unmarshal(value, &root) != nil {
		// Let the normal policy decode return the detailed startup error.
		return true
	}
	_, programs := root["Programs"]
	_, templates := root["Templates"]
	var discover bool
	_ = json.Unmarshal(root["DiscoverSystemPrograms"], &discover)
	return programs || templates || discover
}

func validateHeldPrivateTree(held *os.File, path string, identity LinuxIdentity) error {
	if held == nil {
		return supervision.ErrInvalidComposition
	}
	var before, named, after unix.Stat_t
	if err := unix.Fstat(int(held.Fd()), &before); err != nil {
		return err
	}
	if err := unix.Lstat(path, &named); err != nil || before.Dev != named.Dev || before.Ino != named.Ino {
		return errors.New("private tree pathname does not identify the held directory")
	}
	if err := bootstrap.ValidatePrivateTree(path, identity.UID, identity.GID); err != nil {
		return err
	}
	if err := unix.Lstat(path, &after); err != nil || before.Dev != after.Dev || before.Ino != after.Ino {
		return errors.New("private tree identity changed during validation")
	}
	return nil
}

func heldFileDigest(file *os.File) ([32]byte, error) {
	if file == nil {
		return [32]byte{}, supervision.ErrInvalidComposition
	}
	var stat unix.Stat_t
	if err := unix.Fstat(int(file.Fd()), &stat); err != nil {
		return [32]byte{}, err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Size < 0 || stat.Size > 128<<20 {
		return [32]byte{}, errors.New("held executable has invalid type or size")
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, io.NewSectionReader(file, 0, stat.Size)); err != nil {
		return [32]byte{}, err
	}
	var value [32]byte
	copy(value[:], hash.Sum(nil))
	return value, nil
}

func verifyUserCodeCopy(source, directory *os.File, expected [32]byte) error {
	if source == nil || directory == nil {
		return supervision.ErrInvalidComposition
	}
	fd, err := unix.Openat(int(directory.Fd()), "role", unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	staged := os.NewFile(uintptr(fd), "role")
	defer staged.Close() //nolint:errcheck
	var sourceStat, copyStat unix.Stat_t
	if err := unix.Fstat(int(source.Fd()), &sourceStat); err != nil {
		return err
	}
	if err := unix.Fstat(fd, &copyStat); err != nil {
		return err
	}
	if sourceStat.Mode&unix.S_IFMT != unix.S_IFREG || copyStat.Mode&unix.S_IFMT != unix.S_IFREG ||
		copyStat.Mode&0o7777 != 0o555 || copyStat.Nlink != 1 || sourceStat.Size != copyStat.Size {
		return errors.New("staged executable metadata does not match its held source")
	}
	digest := func(file *os.File, size int64) ([32]byte, error) {
		hash := sha256.New()
		_, err := io.Copy(hash, io.NewSectionReader(file, 0, size))
		var value [32]byte
		copy(value[:], hash.Sum(nil))
		return value, err
	}
	sourceDigest, err := digest(source, sourceStat.Size)
	if err != nil {
		return err
	}
	copyDigest, err := digest(staged, copyStat.Size)
	if err != nil {
		return err
	}
	if sourceDigest != expected || copyDigest != expected {
		return errors.New("staged executable content does not match its held source")
	}
	return nil
}

func validateUserAdminEndpoints(endpoints []admingate.EndpointConfig, production LinuxProduction) error {
	identity := LinuxIdentity{UID: uint32(os.Geteuid()), GID: uint32(os.Getegid())}
	writable := []string{production.Paths.RuntimeDirectory, production.Paths.WriterDirectory, production.Paths.BrokerDirectory}
	trusted := []string{production.Paths.Launcher, production.Paths.RuntimeExecutable, production.Paths.WriterExecutable,
		production.Paths.HelperExecutable, production.Sources.ProtectedPath}
	for _, endpoint := range endpoints {
		if endpoint.Kind != admingate.EndpointUnix {
			continue
		}
		if !filepath.IsAbs(endpoint.Address) || filepath.Clean(endpoint.Address) != endpoint.Address || filepath.Base(endpoint.Address) == "." {
			return errors.New("user-profile Unix admin path must be absolute and clean")
		}
		parent := filepath.Dir(endpoint.Address)
		directory, err := bootstrap.OpenPrivateDirectory(parent, identity.UID, identity.GID)
		if err != nil {
			return fmt.Errorf("user-profile Unix admin parent must be a private launch-account directory: %w", err)
		}
		_ = directory.Close()
		for _, path := range writable {
			overlap, err := directoryPathsOverlap(parent, path)
			if err != nil {
				return fmt.Errorf("compare Unix admin and writable directory identities: %w", err)
			}
			if overlap {
				return errors.New("user-profile Unix admin directory must be disjoint from role-writable directories")
			}
		}
		for _, path := range trusted {
			if path != "" && pathWithin(path, parent) {
				return errors.New("trusted input is inside the user-profile Unix admin directory")
			}
		}
	}
	return nil
}

func pathWithin(path, directory string) bool {
	relative, err := filepath.Rel(directory, path)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func directoryPathsOverlap(left, right string) (bool, error) {
	type identity struct{ device, inode uint64 }
	identify := func(path string) (identity, error) {
		var stat unix.Stat_t
		if err := unix.Stat(path, &stat); err != nil {
			return identity{}, err
		}
		if stat.Mode&unix.S_IFMT != unix.S_IFDIR {
			return identity{}, errors.New("isolation domain is not a directory")
		}
		return identity{uint64(stat.Dev), stat.Ino}, nil
	}
	contains := func(path string, expected identity) (bool, error) {
		for {
			observed, err := identify(path)
			if err != nil || observed == expected {
				return observed == expected, err
			}
			parent := filepath.Dir(path)
			if parent == path {
				return false, nil
			}
			path = parent
		}
	}
	leftIdentity, err := identify(left)
	if err != nil {
		return false, err
	}
	rightIdentity, err := identify(right)
	if err != nil {
		return false, err
	}
	if result, err := contains(right, leftIdentity); err != nil || result {
		return result, err
	}
	return contains(left, rightIdentity)
}

func debugLogsEnabled(level string) bool {
	return strings.EqualFold(strings.TrimSpace(level), "debug")
}

func setSandboxCgroupRoot(data []byte, root string) ([]byte, error) {
	var config sandboxpolicy.Config
	if err := json.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("decode sandbox policy for managed cgroup: %w", err)
	}
	config.LinuxCgroupRoot = root
	result, err := json.Marshal(config)
	if err != nil {
		return nil, fmt.Errorf("encode sandbox policy for managed cgroup: %w", err)
	}
	return result, nil
}

func logAdaptiveLinuxStatus(output io.Writer, state string, status roleentry.RuntimeIsolationStatus, debug bool) {
	if output == nil {
		return
	}
	if debug {
		_, _ = fmt.Fprintf(output, "DEBUG isolation state=%s policy=%s profile=%s reduced=%t roles=%d\n",
			state, status.RequestedPolicy, status.ProfileID, status.Reduced, len(status.Mechanisms))
	}
	for _, warning := range status.Warnings {
		_, _ = fmt.Fprintf(output, "WARN isolation role=%s reason=%s effect=%s feature=%s\n",
			warning.Role, warning.Code, warning.Effect, warning.Feature)
	}
}

func linuxIsolationStatus(profile linuxprofile.Profile, writer, helper, sandboxSupervisor, callbacks bool) roleentry.RuntimeIsolationStatus {
	if profile == "" {
		profile = linuxprofile.ProfileFull
	}
	roles := map[string]string{"network": "ready", "runtime": "ready", "config_writer": "disabled", "command_helper": "disabled"}
	restrictions := map[string][]string{
		"network": {"trusted-broker", "held-inputs"},
		"runtime": {"nonroot", "landlock", "seccomp", "no-new-privileges"},
	}
	if profile == linuxprofile.ProfileFull {
		restrictions["network"] = append(restrictions["network"], "separate-cgroup")
		restrictions["runtime"] = append(restrictions["runtime"], "mount-namespace")
	}
	if writer {
		roles["config_writer"] = "ready"
		restrictions["config_writer"] = []string{"nonroot", "fixed-target", "landlock", "seccomp", "no-new-privileges"}
	}
	if helper {
		roles["command_helper"] = "ready"
		restrictions["command_helper"] = []string{"nonroot", "fixed-executable", "landlock", "seccomp", "no-new-privileges"}
	}
	if sandboxSupervisor {
		roles["sandbox_supervisor"] = "ready"
		restrictions["sandbox_supervisor"] = []string{"trusted-supervisor", "session-bound-rpc", "bounded-relay"}
	}
	return roleentry.RuntimeIsolationStatus{ProtocolVersion: protocol.Version, RecoveryState: "complete", Roles: roles,
		Restrictions: restrictions,
		Capabilities: map[string]bool{"native_network": true, "config_persistence": writer, "commands": helper, "subprogram_sandboxes": sandboxSupervisor, "backend_callbacks": callbacks}}
}

func applyAdaptiveLinuxStatus(status *roleentry.RuntimeIsolationStatus, production LinuxProduction, protectedDigest [32]byte, trustedPlan string) error {
	profile := production.Profile
	if profile == "" {
		profile = linuxprofile.ProfileFull
	}
	policy := production.Arguments.Isolation
	if policy == "" {
		policy = "auto"
	}
	ruid, euid, suid := unix.Getresuid()
	rgid, egid, sgid := unix.Getresgid()
	fsuid, err := unix.SetfsuidRetUid(-1)
	if err != nil {
		return err
	}
	fsgid, err := unix.SetfsgidRetGid(-1)
	if err != nil {
		return err
	}
	uid, gid := uint32(euid), uint32(egid)
	groups, err := os.Getgroups()
	if err != nil {
		return err
	}
	supplementary := make([]uint32, 0, len(groups))
	for _, group := range groups {
		supplementary = append(supplementary, uint32(group))
	}
	networkCredentials := roleentry.RuntimeIsolationCredentials{RealUID: uint32(ruid), EffectiveUID: uid, SavedUID: uint32(suid), FilesystemUID: uint32(fsuid),
		RealGID: uint32(rgid), EffectiveGID: gid, SavedGID: uint32(sgid), FilesystemGID: uint32(fsgid), SupplementaryGroups: supplementary}
	roleCredentials := func(identity LinuxIdentity) roleentry.RuntimeIsolationCredentials {
		value := roleentry.RuntimeIsolationCredentials{RealUID: identity.UID, EffectiveUID: identity.UID, SavedUID: identity.UID, FilesystemUID: identity.UID,
			RealGID: identity.GID, EffectiveGID: identity.GID, SavedGID: identity.GID, FilesystemGID: identity.GID}
		if profile == linuxprofile.ProfileUser {
			value.SupplementaryGroups = slices.Clone(supplementary)
		}
		return value
	}
	mechanisms := map[string]map[string]roleentry.RuntimeIsolationMechanism{
		"network": {"trusted-broker": {State: "applied"}, "held-inputs": {State: "applied"}, "role-cgroups": {State: "applied"}},
		"runtime": {"landlock": {State: "applied"}, "seccomp": {State: "applied"}, "no-new-privileges": {State: "applied"},
			"descriptor-allowlist": {State: "applied"}, "pidfd-supervision": {State: "applied"}, "mount-namespace": {State: "applied"},
			"private-namespaces": {State: "applied"}, "role-cgroups": {State: "applied"}, "separate-host-identities": {State: "applied"},
			"capability-sets": {State: "applied"}},
	}
	warnings := []roleentry.RuntimeIsolationWarning{}
	if profile == linuxprofile.ProfileUser {
		for _, role := range []string{"network", "runtime"} {
			mechanisms[role]["role-cgroups"] = roleentry.RuntimeIsolationMechanism{State: "unavailable", Reason: "not-delegated"}
		}
		mechanisms["runtime"]["mount-namespace"] = roleentry.RuntimeIsolationMechanism{State: "unavailable", Reason: "profile-not-implemented"}
		mechanisms["runtime"]["private-namespaces"] = roleentry.RuntimeIsolationMechanism{State: "unavailable", Reason: "profile-not-implemented"}
		mechanisms["runtime"]["separate-host-identities"] = roleentry.RuntimeIsolationMechanism{State: "unavailable", Reason: "same-uid-profile"}
		warnings = append(warnings,
			roleentry.RuntimeIsolationWarning{Role: "runtime", Code: "profile-not-implemented", Effect: "Private namespaces are unavailable; selected host metadata remains visible.", Feature: "The runtime can read the host name."},
			roleentry.RuntimeIsolationWarning{Role: "runtime", Code: "not-delegated", Effect: "Per-role memory, CPU, and task limits are unavailable.", Feature: "A runtime memory limit cannot be enforced."},
			roleentry.RuntimeIsolationWarning{Role: "runtime", Code: "same-uid-profile", Effect: "Roles use the launch account and rely on Landlock and seccomp for separation.", Feature: "Separate host user identities are unavailable."})
	}
	credentials := map[string]roleentry.RuntimeIsolationCredentials{"network": networkCredentials, "runtime": roleCredentials(production.Identities.Runtime)}
	networkCapabilities, err := currentCapabilitySets()
	if err != nil {
		return err
	}
	capabilities := map[string]roleentry.RuntimeIsolationCapabilitySets{"network": networkCapabilities, "runtime": emptyRoleCapabilitySets(profile, networkCapabilities)}
	if status.Roles["config_writer"] == "ready" {
		mechanisms["config_writer"] = map[string]roleentry.RuntimeIsolationMechanism{"landlock": {State: "applied"}, "seccomp": {State: "applied"},
			"no-new-privileges": {State: "applied"}, "descriptor-allowlist": {State: "applied"}, "fixed-target": {State: "applied"}, "capability-sets": {State: "applied"}}
		if profile == linuxprofile.ProfileFull {
			mechanisms["config_writer"]["mount-namespace"] = roleentry.RuntimeIsolationMechanism{State: "applied"}
			mechanisms["config_writer"]["private-namespaces"] = roleentry.RuntimeIsolationMechanism{State: "applied"}
			mechanisms["config_writer"]["role-cgroups"] = roleentry.RuntimeIsolationMechanism{State: "applied"}
			mechanisms["config_writer"]["separate-host-identities"] = roleentry.RuntimeIsolationMechanism{State: "applied"}
		} else {
			mechanisms["config_writer"]["role-cgroups"] = roleentry.RuntimeIsolationMechanism{State: "unavailable", Reason: "not-delegated"}
			mechanisms["config_writer"]["separate-host-identities"] = roleentry.RuntimeIsolationMechanism{State: "unavailable", Reason: "same-uid-profile"}
		}
		credentials["config_writer"] = roleCredentials(production.Identities.Writer)
		capabilities["config_writer"] = emptyRoleCapabilitySets(profile, networkCapabilities)
	}
	if status.Roles["command_helper"] == "ready" {
		mechanisms["command_helper"] = map[string]roleentry.RuntimeIsolationMechanism{"landlock": {State: "applied"}, "seccomp": {State: "applied"},
			"no-new-privileges": {State: "applied"}, "descriptor-allowlist": {State: "applied"}, "fixed-executable": {State: "applied"},
			"capability-sets": {State: "applied"}, "mount-namespace": {State: "applied"}, "private-namespaces": {State: "applied"},
			"role-cgroups": {State: "applied"}, "separate-host-identities": {State: "applied"}}
		credentials["command_helper"] = roleCredentials(production.Identities.Helper)
		capabilities["command_helper"] = emptyRoleCapabilitySets(profile, networkCapabilities)
	}
	if status.Roles["sandbox_supervisor"] == "ready" {
		mechanisms["sandbox_supervisor"] = map[string]roleentry.RuntimeIsolationMechanism{
			"trusted-supervisor": {State: "applied"}, "session-bound-rpc": {State: "applied"},
			"bounded-relay": {State: "applied"}, "role-cgroups": {State: "applied"},
			"pidfd-supervision": {State: "applied"}, "capability-sets": {State: "applied"},
		}
		if profile == linuxprofile.ProfileUser {
			mechanisms["sandbox_supervisor"]["role-cgroups"] = roleentry.RuntimeIsolationMechanism{State: "unavailable", Reason: "not-delegated"}
		}
		credentials["sandbox_supervisor"] = networkCredentials
		capabilities["sandbox_supervisor"] = networkCapabilities
	}
	limits, inheritedLower := adaptiveResourceLimits(profile, production.Arguments, production.Limits)
	if inheritedLower {
		warnings = append(warnings, roleentry.RuntimeIsolationWarning{Role: "runtime", Code: "inherited-lower", Effect: "An inherited hard process limit is lower than the selected profile limit.", Feature: "The runtime can open fewer files than requested."})
	}
	if status.Roles["config_writer"] == "ready" {
		limits["config_writer"] = slices.Clone(limits["runtime"])
	}
	if status.Roles["command_helper"] == "ready" {
		limits["command_helper"] = slices.Clone(limits["runtime"])
	}
	if status.Roles["sandbox_supervisor"] == "ready" {
		limits["sandbox_supervisor"] = slices.Clone(limits["runtime"])
	}
	revision := "full-v1"
	if profile == linuxprofile.ProfileUser {
		revision = "user-v1"
	}
	plan := fmt.Sprintf("protocol=%d|profile-revision=%s|launcher=%s|seccomp=%s|landlock=abi3|descriptor-topology=launcher,runtime,runtime-dir,broker-dir,lock,optional-writer,optional-helper|%x|%s|%s|%s|%d:%d|%d:%d|%d:%d|%+v",
		protocol.Version, revision, revision, revision, protectedDigest, trustedPlan, policy, profile, production.Identities.Runtime.UID,
		production.Identities.Runtime.GID, production.Identities.Writer.UID, production.Identities.Writer.GID,
		uid, gid, production.Limits)
	digest := sha256.Sum256([]byte(plan))
	status.StatusSchemaVersion, status.RequestedPolicy, status.ProfileID, status.ProfileRevision = 1, policy, string(profile), 1
	status.VerificationState, status.Reduced = "verified", profile == linuxprofile.ProfileUser
	status.Mechanisms, status.Credentials, status.CapabilitySets = mechanisms, credentials, capabilities
	status.ResourceLimits, status.Warnings = limits, warnings
	status.ObservationTime, status.SelectedPlanDigest = time.Now().UTC().Format(time.RFC3339Nano), hex.EncodeToString(digest[:])
	return nil
}

func trustedPlanMaterial(files []*os.File, profile linuxprofile.Profile) (string, error) {
	var material strings.Builder
	_, _ = fmt.Fprintf(&material, "profile=%s|files=%d|", profile, len(files))
	for index, file := range files {
		if file == nil {
			_, _ = fmt.Fprintf(&material, "%d:nil|", index)
			continue
		}
		var stat unix.Stat_t
		if err := unix.Fstat(int(file.Fd()), &stat); err != nil {
			return "", err
		}
		_, _ = fmt.Fprintf(&material, "%d:%d:%d:%d:%d:%d:%d", index, stat.Dev, stat.Ino, stat.Mode, stat.Size, stat.Mtim.Sec, stat.Mtim.Nsec)
		if stat.Mode&unix.S_IFMT == unix.S_IFREG {
			hash := sha256.New()
			if _, err := io.Copy(hash, io.NewSectionReader(file, 0, stat.Size)); err != nil {
				return "", err
			}
			_, _ = fmt.Fprintf(&material, ":%x", hash.Sum(nil))
		}
		material.WriteByte('|')
	}
	return material.String(), nil
}

type userCodeGeneration struct {
	Name          string
	Device, Inode uint64
	Mount         uint64
}

func inspectUserCodeGeneration(path string, identity LinuxIdentity) (userCodeGeneration, error) {
	var stat unix.Stat_t
	if err := unix.Lstat(path, &stat); err != nil {
		return userCodeGeneration{}, err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFDIR || stat.Uid != identity.UID || stat.Gid != identity.GID || stat.Mode&0o7777 != 0o700 {
		return userCodeGeneration{}, errors.New("role code generation has unsafe metadata")
	}
	var statx unix.Statx_t
	if err := unix.Statx(unix.AT_FDCWD, path, unix.AT_SYMLINK_NOFOLLOW, unix.STATX_TYPE|unix.STATX_MNT_ID, &statx); err != nil || statx.Mask&unix.STATX_MNT_ID == 0 {
		return userCodeGeneration{}, errors.New("cannot identify role code generation mount")
	}
	return userCodeGeneration{Name: filepath.Base(path), Device: uint64(stat.Dev), Inode: stat.Ino, Mount: statx.Mnt_id}, nil
}

func readUserCodeReceipt(broker *os.File, identity LinuxIdentity) (userCodeGeneration, error) {
	const receiptName = ".code-generation"
	receiptFD, err := unix.Openat(int(broker.Fd()), receiptName, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return userCodeGeneration{}, err
	}
	receipt := os.NewFile(uintptr(receiptFD), receiptName)
	var stat unix.Stat_t
	if unix.Fstat(receiptFD, &stat) != nil || stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Mode&0o7777 != 0o600 ||
		stat.Uid != identity.UID || stat.Gid != identity.GID || stat.Nlink != 1 || stat.Size <= 0 || stat.Size > 256 {
		_ = receipt.Close()
		return userCodeGeneration{}, errors.New("invalid role code generation receipt")
	}
	data, readErr := io.ReadAll(io.LimitReader(receipt, 257))
	closeErr := receipt.Close()
	if readErr != nil || closeErr != nil {
		return userCodeGeneration{}, errors.Join(readErr, closeErr)
	}
	record := string(data)
	var version string
	var generation userCodeGeneration
	if count, scanErr := fmt.Sscanf(strings.TrimSuffix(record, "\n"), "%s %s %d %d %d", &version, &generation.Name,
		&generation.Device, &generation.Inode, &generation.Mount); scanErr != nil || count != 5 || version != "v1" ||
		!strings.HasPrefix(generation.Name, ".code-") || filepath.Base(generation.Name) != generation.Name || generation.Name == ".code-" {
		return userCodeGeneration{}, errors.New("invalid role code generation receipt")
	}
	if record != fmt.Sprintf("v1 %s %d %d %d\n", generation.Name, generation.Device, generation.Inode, generation.Mount) {
		return userCodeGeneration{}, errors.New("non-canonical role code generation receipt")
	}
	return generation, nil
}

func validateUserCodeGeneration(path string, expected userCodeGeneration, identity LinuxIdentity) error {
	observed, err := inspectUserCodeGeneration(path, identity)
	if err != nil {
		return err
	}
	if observed != expected {
		return errors.New("role code generation identity changed")
	}
	return bootstrap.ValidatePrivateTree(path, identity.UID, identity.GID)
}

func removeUserCodeGeneration(brokerPath string, broker *os.File, identity LinuxIdentity) error {
	expected, err := readUserCodeReceipt(broker, identity)
	if err != nil {
		return fmt.Errorf("read role code generation receipt: %w", err)
	}
	path := filepath.Join(brokerPath, expected.Name)
	if err := validateUserCodeGeneration(path, expected, identity); err != nil {
		return fmt.Errorf("validate role code generation before cleanup: %w", err)
	}
	if err := os.RemoveAll(path); err != nil {
		return fmt.Errorf("remove role code generation: %w", err)
	}
	if err := unix.Unlinkat(int(broker.Fd()), ".code-generation", 0); err != nil {
		return err
	}
	return broker.Sync()
}

func recoverUserCodeGeneration(brokerPath string, broker *os.File, identity LinuxIdentity) error {
	generation, err := readUserCodeReceipt(broker, identity)
	if err == nil {
		path := filepath.Join(brokerPath, generation.Name)
		if err := validateUserCodeGeneration(path, generation, identity); err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("validate stale role code generation: %w", err)
			}
			if err := unix.Unlinkat(int(broker.Fd()), ".code-generation", 0); err != nil {
				return err
			}
		} else if err := removeUserCodeGeneration(brokerPath, broker, identity); err != nil {
			return fmt.Errorf("recover stale role code generation: %w", err)
		}
	} else if !errors.Is(err, unix.ENOENT) {
		return fmt.Errorf("open role code generation receipt: %w", err)
	}
	entries, err := os.ReadDir(brokerPath)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".code-") {
			return fmt.Errorf("ambiguous role code generation without receipt: %s", entry.Name())
		}
	}
	if generation.Name != "" {
		return broker.Sync()
	}
	return nil
}

func writeUserCodeReceipt(brokerPath string, broker *os.File, generation string, identity LinuxIdentity) error {
	const receiptName = ".code-generation"
	if !strings.HasPrefix(generation, ".code-") || filepath.Base(generation) != generation {
		return errors.New("invalid role code generation name")
	}
	code, err := inspectUserCodeGeneration(filepath.Join(brokerPath, generation), identity)
	if err != nil {
		return err
	}
	fd, err := unix.Openat(int(broker.Fd()), receiptName, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return fmt.Errorf("create role code generation receipt in %s: %w", brokerPath, err)
	}
	file := os.NewFile(uintptr(fd), receiptName)
	_, writeErr := fmt.Fprintf(file, "v1 %s %d %d %d\n", code.Name, code.Device, code.Inode, code.Mount)
	syncErr := file.Sync()
	closeErr := file.Close()
	return errors.Join(writeErr, syncErr, closeErr, broker.Sync())
}

func adaptiveResourceLimits(profile linuxprofile.Profile, arguments daemonargs.Args, limits linuxprofile.Limits) (map[string][]roleentry.RuntimeIsolationResourceLimit, bool) {
	descriptors, fileBytes := limits.Descriptors, limits.FileBytes
	networkDescriptors, networkFileBytes := uint64(0), uint64(0)
	inheritedLower := false
	var inherited unix.Rlimit
	if unix.Getrlimit(unix.RLIMIT_NOFILE, &inherited) == nil {
		networkDescriptors = inherited.Cur
		if inherited.Max < descriptors {
			descriptors, inheritedLower = inherited.Max, true
		}
	}
	if unix.Getrlimit(unix.RLIMIT_FSIZE, &inherited) == nil {
		networkFileBytes = inherited.Cur
		if inherited.Max < fileBytes {
			fileBytes, inheritedLower = inherited.Max, true
		}
	}
	descriptorSource, fileSource := "default", "default"
	if arguments.IsolationDescriptors != nil {
		descriptorSource = "configured"
	}
	if arguments.IsolationFileBytes != nil {
		fileSource = "configured"
	}
	process := []roleentry.RuntimeIsolationResourceLimit{
		{Name: "descriptors", Requested: strconv.FormatUint(limits.Descriptors, 10), Effective: strconv.FormatUint(descriptors, 10), Unit: "count", Scope: "process", Source: descriptorSource, Enforcement: "applied"},
		{Name: "file-size", Requested: strconv.FormatUint(limits.FileBytes, 10), Effective: strconv.FormatUint(fileBytes, 10), Unit: "bytes", Scope: "process", Source: fileSource, Enforcement: "applied"},
	}
	memorySource, tasksSource, cpuSource := "default", "default", "default"
	if arguments.IsolationMemoryBytes != nil {
		memorySource = "configured"
	}
	if arguments.IsolationTasks != nil {
		tasksSource = "configured"
	}
	if arguments.IsolationCPUQuota != nil {
		cpuSource = "configured"
	}
	if profile == linuxprofile.ProfileFull {
		process = append(process,
			roleentry.RuntimeIsolationResourceLimit{Name: "memory", Requested: strconv.FormatUint(limits.MemoryBytes, 10), Effective: strconv.FormatUint(limits.MemoryBytes, 10), Unit: "bytes", Scope: "role", Source: memorySource, Enforcement: "applied"},
			roleentry.RuntimeIsolationResourceLimit{Name: "memory-swap", Requested: "0", Effective: "0", Unit: "bytes", Scope: "role", Source: "default", Enforcement: "applied"},
			roleentry.RuntimeIsolationResourceLimit{Name: "tasks", Requested: strconv.FormatUint(limits.Tasks, 10), Effective: strconv.FormatUint(limits.Tasks, 10), Unit: "count", Scope: "role", Source: tasksSource, Enforcement: "applied"},
			roleentry.RuntimeIsolationResourceLimit{Name: "cpu-quota", Requested: strconv.FormatUint(limits.CPUQuota, 10), Effective: strconv.FormatUint(limits.CPUQuota, 10), Unit: "microseconds", Scope: "role", Source: cpuSource, Enforcement: "applied"})
	} else {
		process = append(process,
			roleentry.RuntimeIsolationResourceLimit{Name: "memory", Requested: strconv.FormatUint(limits.MemoryBytes, 10), Effective: "max", Unit: "bytes", Scope: "role", Source: memorySource, Enforcement: "unavailable"},
			roleentry.RuntimeIsolationResourceLimit{Name: "tasks", Requested: strconv.FormatUint(limits.Tasks, 10), Effective: "max", Unit: "count", Scope: "role", Source: tasksSource, Enforcement: "unavailable"},
			roleentry.RuntimeIsolationResourceLimit{Name: "cpu-quota", Requested: strconv.FormatUint(limits.CPUQuota, 10), Effective: "max", Unit: "microseconds", Scope: "role", Source: cpuSource, Enforcement: "unavailable"})
	}
	networkLimits := slices.Clone(process)
	networkLimits[0] = roleentry.RuntimeIsolationResourceLimit{Name: "descriptors", Requested: strconv.FormatUint(networkDescriptors, 10),
		Effective: strconv.FormatUint(networkDescriptors, 10), Unit: "count", Scope: "inherited", Source: "inherited", Enforcement: "observed"}
	networkLimits[1] = roleentry.RuntimeIsolationResourceLimit{Name: "file-size", Requested: strconv.FormatUint(networkFileBytes, 10),
		Effective: strconv.FormatUint(networkFileBytes, 10), Unit: "bytes", Scope: "inherited", Source: "inherited", Enforcement: "observed"}
	return map[string][]roleentry.RuntimeIsolationResourceLimit{"network": networkLimits, "runtime": process}, inheritedLower
}

var linuxCapabilityNames = []string{"CAP_CHOWN", "CAP_DAC_OVERRIDE", "CAP_DAC_READ_SEARCH", "CAP_FOWNER", "CAP_FSETID", "CAP_KILL", "CAP_SETGID", "CAP_SETUID", "CAP_SETPCAP", "CAP_LINUX_IMMUTABLE", "CAP_NET_BIND_SERVICE", "CAP_NET_BROADCAST", "CAP_NET_ADMIN", "CAP_NET_RAW", "CAP_IPC_LOCK", "CAP_IPC_OWNER", "CAP_SYS_MODULE", "CAP_SYS_RAWIO", "CAP_SYS_CHROOT", "CAP_SYS_PTRACE", "CAP_SYS_PACCT", "CAP_SYS_ADMIN", "CAP_SYS_BOOT", "CAP_SYS_NICE", "CAP_SYS_RESOURCE", "CAP_SYS_TIME", "CAP_SYS_TTY_CONFIG", "CAP_MKNOD", "CAP_LEASE", "CAP_AUDIT_WRITE", "CAP_AUDIT_CONTROL", "CAP_SETFCAP", "CAP_MAC_OVERRIDE", "CAP_MAC_ADMIN", "CAP_SYSLOG", "CAP_WAKE_ALARM", "CAP_BLOCK_SUSPEND", "CAP_AUDIT_READ", "CAP_PERFMON", "CAP_BPF", "CAP_CHECKPOINT_RESTORE"}

func currentCapabilitySets() (roleentry.RuntimeIsolationCapabilitySets, error) {
	header := unix.CapUserHeader{Version: unix.LINUX_CAPABILITY_VERSION_3}
	data := [2]unix.CapUserData{}
	if err := unix.Capget(&header, &data[0]); err != nil {
		return roleentry.RuntimeIsolationCapabilitySets{}, err
	}
	read := func(low, high uint32) []string {
		values := []string{}
		for bit, name := range linuxCapabilityNames {
			if bit < 32 && low&(1<<bit) != 0 || bit >= 32 && high&(1<<(bit-32)) != 0 {
				values = append(values, name)
			}
		}
		return values
	}
	sets := roleentry.RuntimeIsolationCapabilitySets{Inheritable: read(data[0].Inheritable, data[1].Inheritable),
		Permitted: read(data[0].Permitted, data[1].Permitted), Effective: read(data[0].Effective, data[1].Effective)}
	for bit, name := range linuxCapabilityNames {
		if value, err := unix.PrctlRetInt(unix.PR_CAPBSET_READ, uintptr(bit), 0, 0, 0); err != nil {
			return roleentry.RuntimeIsolationCapabilitySets{}, err
		} else if value == 1 {
			sets.Bounding = append(sets.Bounding, name)
		}
		if value, err := unix.PrctlRetInt(unix.PR_CAP_AMBIENT, unix.PR_CAP_AMBIENT_IS_SET, uintptr(bit), 0, 0); err != nil {
			return roleentry.RuntimeIsolationCapabilitySets{}, err
		} else if value == 1 {
			sets.Ambient = append(sets.Ambient, name)
		}
	}
	return sets, nil
}

func emptyRoleCapabilitySets(profile linuxprofile.Profile, network roleentry.RuntimeIsolationCapabilitySets) roleentry.RuntimeIsolationCapabilitySets {
	sets := roleentry.RuntimeIsolationCapabilitySets{}
	if profile == linuxprofile.ProfileUser {
		sets.Bounding = slices.Clone(network.Bounding)
	}
	return sets
}

func linuxCredentialClass() string {
	if os.Geteuid() == 0 {
		return "root"
	}
	return "ordinary-user"
}
