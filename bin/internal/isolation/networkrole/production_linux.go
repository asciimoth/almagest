//go:build linux && amd64

package networkrole

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/asciimoth/almagest/bin/internal/daemonargs"
	"github.com/asciimoth/almagest/bin/internal/isolation/admingate"
	"github.com/asciimoth/almagest/bin/internal/isolation/bootstrap"
	"github.com/asciimoth/almagest/bin/internal/isolation/callbackbridge"
	"github.com/asciimoth/almagest/bin/internal/isolation/launchconfig"
	"github.com/asciimoth/almagest/bin/internal/isolation/linuxprofile"
	"github.com/asciimoth/almagest/bin/internal/isolation/policy"
	"github.com/asciimoth/almagest/bin/internal/isolation/protocol"
	"github.com/asciimoth/almagest/bin/internal/isolation/roleentry"
	"github.com/asciimoth/almagest/bin/internal/isolation/supervision"
)

type LinuxIdentity struct {
	UID uint32
	GID uint32
}

type LinuxProductionPaths struct {
	Launcher, RuntimeExecutable, WriterExecutable, HelperExecutable string
	RuntimeDirectory, WriterDirectory, HelperDirectory              string
	BrokerDirectory                                                 string
	Layout                                                          bootstrap.StorageLayout
}

type LinuxProductionIdentities struct {
	Runtime LinuxIdentity
	Writer  LinuxIdentity
	Helper  LinuxIdentity
}

// LinuxProduction contains only launch-selected trusted state. Sources and
// paths are fixed before any role starts; regular config and snapshot bytes do
// not select authority.
type LinuxProduction struct {
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
	if production.Limits == (linuxprofile.Limits{}) {
		production.Limits = linuxprofile.DefaultLimits()
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
	admin, err := launchconfig.Admin(resolved.Authority.Admin,
		launchconfig.AdminDefaults{Unix: "/run/almagest/admin.sock"}, production.Arguments, &resolved.Runtime)
	if err != nil {
		return err
	}
	for _, endpoint := range admin {
		if endpoint.Kind == admingate.EndpointPipe {
			return errors.New("windows admin pipe is unavailable in the Linux isolation profile")
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

	trustedPaths := []string{production.Paths.Launcher, production.Paths.RuntimeExecutable}
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
	brokerIdentity := LinuxIdentity{UID: uint32(os.Geteuid()), GID: uint32(os.Getegid())}
	brokerDirectory, err := openPrivate(production.Paths.BrokerDirectory, brokerIdentity)
	if err != nil {
		return err
	}
	journal, err := supervision.OpenJournal(filepath.Join(production.Paths.BrokerDirectory, "recovery.json"), 0)
	if err != nil {
		return err
	}

	runtime := LinuxRole{Plan: linuxprofile.Plan{Role: linuxprofile.Runtime, UID: production.Identities.Runtime.UID,
		GID: production.Identities.Runtime.GID, Limits: production.Limits}, Inputs: linuxprofile.Inputs{
		Launcher: launcher, Executable: runtimeExecutable, Directory: runtimeDirectory,
	}}
	var writer *LinuxWriter
	if !production.Sources.CombinedStdin {
		if filepath.Dir(production.Sources.RegularPath) != production.Paths.WriterDirectory {
			return fmt.Errorf("regular config must be inside the fixed isolated writer directory")
		}
		writerExecutable, openErr := openTrusted(production.Paths.WriterExecutable)
		if openErr != nil {
			return openErr
		}
		writerDirectory, openErr := openPrivate(production.Paths.WriterDirectory, production.Identities.Writer)
		if openErr != nil {
			return openErr
		}
		writer = &LinuxWriter{LinuxRole: LinuxRole{Plan: linuxprofile.Plan{Role: linuxprofile.Writer,
			UID: production.Identities.Writer.UID, GID: production.Identities.Writer.GID, Limits: production.Limits},
			Inputs: linuxprofile.Inputs{Launcher: launcher, Executable: writerExecutable, Directory: writerDirectory}},
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
		helper = &LinuxHelper{LinuxRole: LinuxRole{Plan: linuxprofile.Plan{Role: linuxprofile.Command,
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
		if production.Output != nil {
			_, _ = fmt.Fprintf(production.Output, "network: "+format+"\n", values...)
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
	cgroups, err := linuxprofile.ProvisionServiceCgroups(rolePlan)
	if err != nil {
		return err
	}
	defer func() {
		result = errors.Join(result, cgroups.RestoreAndRemove(), cgroups.Close())
	}()
	return RunLinux(parent, LinuxLaunch{Runtime: runtime, Writer: writer, Helper: helper,
		RuntimeInput: roleentry.RuntimeInput{Version: protocol.Version, Production: true, NodeCommands: runtimeNodeCommands, Options: roleentry.RuntimeOptions{
			LogLevel: production.Arguments.LogLevel, LogWorkers: production.Arguments.LogWorkers,
			NoPool: production.Arguments.NoPool, NoDNSCacheExpiration: production.Arguments.NoDNSCacheExpiration,
			DebugNodes: production.Arguments.DebugNodes, E2ETailscaleRejectAfterSeconds: production.E2ETailscaleRejectAfterSeconds,
			LogCacheDebug: production.Arguments.LogCacheDebug,
			LogCacheInfo:  production.Arguments.LogCacheInfo, LogCacheWarn: production.Arguments.LogCacheWarn,
			LogCacheError: production.Arguments.LogCacheError, LogCacheFatal: production.Arguments.LogCacheFatal,
		}, Isolation: linuxIsolationStatus(writer != nil, helper != nil, !callbackBindings.Empty())}, RuntimeStorage: &bootstrap.RuntimeStorageInput{Regular: inputs.Regular, Protected: resolved.Runtime, Snapshot: snapshot},
		Policy: launchPolicy, Native: native, State: brokerDirectory, Cgroups: cgroups.Roles(), Limits: production.Limits,
		Journal: journal, Admin: admin, Output: production.Output, Callbacks: callbackBindings,
	}, ready)
}

func linuxIsolationStatus(writer, helper, callbacks bool) roleentry.RuntimeIsolationStatus {
	roles := map[string]string{"network": "ready", "runtime": "ready", "config_writer": "disabled", "command_helper": "disabled"}
	restrictions := map[string][]string{
		"network": {"trusted-broker", "separate-cgroup", "held-inputs"},
		"runtime": {"nonroot", "mount-namespace", "landlock", "seccomp", "no-new-privileges"},
	}
	if writer {
		roles["config_writer"] = "ready"
		restrictions["config_writer"] = []string{"nonroot", "fixed-target", "landlock", "seccomp", "no-new-privileges"}
	}
	if helper {
		roles["command_helper"] = "ready"
		restrictions["command_helper"] = []string{"nonroot", "fixed-executable", "landlock", "seccomp", "no-new-privileges"}
	}
	return roleentry.RuntimeIsolationStatus{ProtocolVersion: protocol.Version, RecoveryState: "complete", Roles: roles,
		Restrictions: restrictions,
		Capabilities: map[string]bool{"native_network": true, "config_persistence": writer, "commands": helper, "backend_callbacks": callbacks}}
}

func linuxCredentialClass() string {
	if os.Geteuid() == 0 {
		return "root"
	}
	return "ordinary-user"
}
