package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/asciimoth/almagest/bin/almagestd/args"
	"github.com/asciimoth/almagest/bin/almagestd/command"
	"github.com/asciimoth/almagest/bin/almagestd/daemonservices"
	"github.com/asciimoth/almagest/bin/almagestd/internal/daemonruntime"
	"github.com/asciimoth/almagest/bin/almagestd/platform"
	"github.com/asciimoth/almagest/bin/api"
	"github.com/asciimoth/almagest/bin/internal/isolation/admingate"
	"github.com/asciimoth/almagest/bin/internal/isolation/commandruntime"
	"github.com/asciimoth/almagest/bin/internal/isolation/configpersistence"
	"github.com/asciimoth/almagest/bin/internal/isolation/protocol"
	"github.com/asciimoth/almagest/bin/internal/isolation/roleentry"
	client "github.com/asciimoth/almagest/bin/internal/isolation/runtime"
	"github.com/asciimoth/almagest/bin/internal/isolation/transport"
	sandboxisolated "github.com/asciimoth/almagest/bin/internal/subprogram/isolated"
	sandbox "github.com/asciimoth/gobox"
	"github.com/asciimoth/gonnect"
	"github.com/kataras/golog"
)

func isolatedRuntimeRole() bool {
	return os.Getenv("ALMAGEST_LINUX_ROLE") == "runtime" || os.Getenv("ALMAGEST_ROLE") == string(protocol.RoleRuntime)
}

func runIsolatedRuntimeRole() error {
	if len(os.Args) != 1 {
		return roleentry.ErrStandaloneRole
	}
	open := func(lane roleentry.Lane, required bool) (transport.Stream, error) {
		value := os.Getenv(string(lane))
		if value == "" || value == "0" {
			if required {
				return nil, fmt.Errorf("%w: missing %s", roleentry.ErrStandaloneRole, lane)
			}
			return nil, nil
		}
		return roleentry.OpenLane(lane, os.Getenv)
	}
	control, err := open(roleentry.LaneBootstrap, true)
	if err != nil {
		return err
	}
	defer control.Close() //nolint:errcheck
	data, err := open(roleentry.LaneData, true)
	if err != nil {
		return err
	}
	defer data.Close() //nolint:errcheck
	diagnostic, err := open(roleentry.LaneDiagnostics, true)
	if err != nil {
		return err
	}
	defer diagnostic.Close() //nolint:errcheck
	adminStream, err := open(roleentry.LaneAdmin, false)
	if err != nil {
		return err
	}
	configStream, err := open(roleentry.LaneConfig, false)
	if err != nil {
		return err
	}
	commandStream, err := open(roleentry.LaneCommand, false)
	if err != nil {
		return err
	}
	var sandboxControlFile, sandboxDataFile *os.File
	if os.Getenv(string(roleentry.LaneSandbox)) != "" && os.Getenv(string(roleentry.LaneSandbox)) != "0" {
		sandboxControlFile, err = roleentry.OpenLaneFile(roleentry.LaneSandbox, os.Getenv)
		if err != nil {
			return err
		}
		defer func() {
			if sandboxControlFile != nil {
				_ = sandboxControlFile.Close()
			}
		}()
	}
	if os.Getenv(string(roleentry.LaneSandboxData)) != "" && os.Getenv(string(roleentry.LaneSandboxData)) != "0" {
		sandboxDataFile, err = roleentry.OpenLaneFile(roleentry.LaneSandboxData, os.Getenv)
		if err != nil {
			return err
		}
		defer func() {
			if sandboxDataFile != nil {
				_ = sandboxDataFile.Close()
			}
		}()
	}
	if err := roleentry.SealRuntime(); err != nil {
		return fmt.Errorf("seal runtime: %w", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	bootstrap, input, err := roleentry.ReceiveRuntimeBootstrap(ctx, control)
	if err != nil {
		return fmt.Errorf("read runtime bootstrap: %w", err)
	}
	if (len(input.AdminEndpoints) != 0) != (adminStream != nil) || input.ConfigWritable != (configStream != nil) ||
		input.Sandbox != (sandboxControlFile != nil && sandboxDataFile != nil) || (sandboxControlFile == nil) != (sandboxDataFile == nil) {
		return protocol.ErrInvalidContract
	}
	if err := roleentry.AcknowledgeBootstrap(ctx, control, bootstrap); err != nil {
		return fmt.Errorf("acknowledge runtime bootstrap: %w", err)
	}
	if bootstrap.ProfileID != "" {
		input.Isolation, err = roleentry.ReceiveFinalIsolationStatus(ctx, control)
		if err != nil {
			return fmt.Errorf("read verified isolation status: %w", err)
		}
		if input.Isolation.ProfileID != bootstrap.ProfileID || input.Isolation.SelectedPlanDigest != bootstrap.PlanDigest {
			return protocol.ErrInvalidContract
		}
	}
	var sandboxClient *sandboxisolated.Client
	if input.Sandbox {
		sandboxControl, bindErr := bindSandboxSupervisor(sandboxControlFile, input.SandboxPeer)
		if bindErr != nil {
			return fmt.Errorf("bind sandbox supervisor control: %w", bindErr)
		}
		sandboxControlFile = nil
		sandboxData, bindErr := bindSandboxSupervisor(sandboxDataFile, input.SandboxPeer)
		if bindErr != nil {
			_ = sandboxControl.Close()
			return fmt.Errorf("bind sandbox supervisor data: %w", bindErr)
		}
		sandboxDataFile = nil
		sandboxClient, err = sandboxisolated.NewClient(ctx, sandboxControl, sandboxData, bootstrap.Session)
		if err != nil {
			return fmt.Errorf("start sandbox supervisor client: %w", err)
		}
		defer func() {
			if sandboxClient != nil {
				_ = sandboxClient.Close()
			}
		}()
	}
	gonnect.UnfuckGoDns()
	if input.Options.LogLevel != "" {
		golog.SetLevel(input.Options.LogLevel)
	}
	if err := applyIsolatedE2EOptions(input.Options); err != nil {
		return err
	}
	configureGlobalBufPool(input.Options.NoPool)
	networkClient, err := client.New(ctx, transport.Lanes{Control: control, Data: data, Diagnostic: diagnostic}, bootstrap.Session)
	if err != nil {
		return fmt.Errorf("start runtime network client: %w", err)
	}
	system := networkClient.System()
	go func() {
		<-networkClient.Done()
		cancel()
	}()

	var adminRuntime *admingate.Runtime
	if adminStream != nil {
		ids := make([]uint16, len(input.AdminEndpoints))
		for index, endpoint := range input.AdminEndpoints {
			ids[index] = endpoint.ID
		}
		adminRuntime, err = admingate.NewRuntime(ctx, adminStream, bootstrap.Session, ids)
		if err != nil {
			_ = system.Close()
			return fmt.Errorf("start runtime admin gate: %w", err)
		}
		defer adminRuntime.Close() //nolint:errcheck
	}

	var persistence daemonservices.ConfigPersistence
	var configPeer *transport.Peer
	// N closes the network control lane to request graceful runtime shutdown.
	// Keep auxiliary role lanes alive while Application closes the engine and
	// flushes its final config. Their supervisors stop only after R exits.
	auxiliaryCtx, cancelAuxiliary := context.WithCancel(context.Background())
	defer cancelAuxiliary()
	if configStream != nil {
		configPeer, err = transport.NewControl(auxiliaryCtx, configStream, bootstrap.Session, nil)
		if err != nil {
			_ = system.Close()
			return fmt.Errorf("start runtime config client: %w", err)
		}
		defer configPeer.Close() //nolint:errcheck
		persistence, err = configpersistence.New(configPeer, bootstrap.Session, 1, 0)
		if err != nil {
			_ = system.Close()
			return err
		}
	}

	var commands *commandruntime.Dispatcher
	if commandStream != nil {
		commandPeer, peerErr := transport.NewControl(auxiliaryCtx, commandStream, bootstrap.Session, nil)
		if peerErr != nil {
			_ = system.Close()
			return fmt.Errorf("start runtime command client: %w", peerErr)
		}
		commands, err = commandruntime.New(auxiliaryCtx, commandPeer, bootstrap.Session, 1, nil)
		if err != nil {
			_ = commandPeer.Close()
			_ = system.Close()
			return err
		}
	}

	storage := os.Getenv("ALMAGEST_STORAGE")
	if storage == "" {
		storage = "/state"
	}
	regular, protected, err := readRuntimeConfig(storage)
	if err != nil {
		_ = system.Close()
		if commands != nil {
			commands.Close()
		}
		return err
	}
	arguments := args.Args{
		FileStorage: storage, LogLevel: input.Options.LogLevel, LogWorkers: input.Options.LogWorkers,
		NoPool: input.Options.NoPool, NoDNSCacheExpiration: input.Options.NoDNSCacheExpiration,
		DebugNodes: input.Options.DebugNodes, LogCacheDebug: input.Options.LogCacheDebug,
		LogCacheInfo: input.Options.LogCacheInfo, LogCacheWarn: input.Options.LogCacheWarn,
		LogCacheError: input.Options.LogCacheError, LogCacheFatal: input.Options.LogCacheFatal,
	}
	if input.StartupSnapshot {
		arguments.Snapshot = filepath.Join(storage, roleentry.RuntimeSnapshotFile)
	}
	randomSeeds, err := newDaemonRandomSeeds()
	if err != nil {
		return err
	}
	integrations := daemonIntegrations{
		parseConfig: func(regular, protected []byte) (*Config, error) {
			config, err := ParseIsolatedConfig(regular, protected)
			if err != nil {
				return nil, err
			}
			config.Protected.CommandRunner.Commands = make(map[string]command.AllowedCommand, len(input.NodeCommands))
			for _, allowed := range input.NodeCommands {
				slots := make(map[string]command.Slot, len(allowed.Slots))
				for _, name := range allowed.Slots {
					slots[name] = command.Slot{}
				}
				config.Protected.CommandRunner.Commands[allowed.Name] = command.AllowedCommand{AllowNodes: true, Slots: slots}
			}
			return config, nil
		},
		prepareConfig: func(args.Args) (platform.ConfigBootstrap, error) {
			return platform.ConfigBootstrap{Regular: regular, Protected: protected, Persistence: persistence}, nil
		},
		enterStorage: func(path string) error {
			return enterIsolatedRuntimeStorage(storage, path)
		},
		loadOSAuthority: loadIsolatedRuntimeOSAuthorities,
		prepareAdmin: func(conf *Config) (*AdminAPI, error) {
			return isolatedAdminAPI(conf, adminRuntime, input.AdminEndpoints, input.Isolation)
		},
		buildCommands: func(context.Context, *LogFactory, *Config) (daemonservices.NodeCommandDispatcher, daemonservices.BackendCallbackDispatcher, func(), error) {
			if commands == nil {
				return nil, nil, nil, nil
			}
			return commands, nil, commands.Close, nil
		},
		buildNetwork: func(*Config, daemonservices.BackendCallbackDispatcher, *LogFactory, error, int64) (daemonservices.HostNetwork, error) {
			return system, nil
		},
		buildSandboxes: func(conf *Config) (sandbox.Service, error) {
			service := isolatedSandboxService(sandboxClient)
			sandboxClient = nil
			return service, nil
		},
	}
	mode := executionModeIsolationDevelopment
	if input.Production {
		mode = executionModeIsolation
	}
	return daemonruntime.Run(ctx, func(ctx context.Context, application *daemonruntime.Application) error {
		var readyErr error
		err := runDaemon(ctx, application, func() {
			readyErr = networkClient.MarkReady(ctx)
			if readyErr != nil {
				application.Cancel()
			}
		}, arguments, mode, randomSeeds, nil, integrations)
		return errors.Join(err, readyErr)
	})
}

func loadIsolatedRuntimeOSAuthorities(ctx context.Context) (immutableTLSData, error) {
	return loadOSTLSData(ctx, platform.FallbackAuthorityFetcher())
}

func isolatedSandboxService(client *sandboxisolated.Client) sandbox.Service {
	if client == nil {
		return nil
	}
	return client
}

func readRuntimeConfig(storage string) ([]byte, []byte, error) {
	read := func(name string) ([]byte, error) {
		path := filepath.Join(storage, name)
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		if err := os.Remove(path); err != nil {
			return nil, err
		}
		return data, nil
	}
	regular, err := read(roleentry.RuntimeRegularConfigFile)
	if err != nil {
		return nil, nil, fmt.Errorf("read runtime regular config: %w", err)
	}
	protected, err := read(roleentry.RuntimeProtectedConfigFile)
	if err != nil {
		return nil, nil, fmt.Errorf("read runtime protected config: %w", err)
	}
	return regular, protected, nil
}

func isolatedAdminAPI(conf *Config, runtime *admingate.Runtime, endpoints []roleentry.RuntimeAdminEndpoint, status roleentry.RuntimeIsolationStatus) (*AdminAPI, error) {
	admin := &AdminAPI{config: conf}
	if err := admin.prepareLocalizedContent(defaultLocalesRoot()); err != nil {
		return nil, err
	}
	if err := admin.prepareExamples(defaultExamplesRoot()); err != nil {
		return nil, err
	}
	if status.ProtocolVersion != 0 {
		admin.isolationStatus = &api.IsolationStatus{ProtocolVersion: status.ProtocolVersion, StatusSchemaVersion: status.StatusSchemaVersion, Restrictions: status.Restrictions,
			Roles: status.Roles, Capabilities: status.Capabilities, RecoveryState: status.RecoveryState,
			RequestedPolicy: status.RequestedPolicy, ProfileID: status.ProfileID, ProfileRevision: status.ProfileRevision,
			VerificationState: status.VerificationState, Reduced: status.Reduced,
			Mechanisms: mapIsolationMechanisms(status.Mechanisms), Credentials: mapIsolationCredentials(status.Credentials),
			CapabilitySets: mapIsolationCapabilitySets(status.CapabilitySets), ResourceLimits: mapIsolationResourceLimits(status.ResourceLimits),
			Warnings: mapIsolationWarnings(status.Warnings), ObservationTime: status.ObservationTime, SelectedPlanDigest: status.SelectedPlanDigest}
	}
	if len(endpoints) == 0 {
		return admin, nil
	}
	if runtime == nil {
		return nil, protocol.ErrInvalidContract
	}
	for _, endpoint := range endpoints {
		listener, err := runtime.Listener(endpoint.ID)
		if err != nil {
			return nil, err
		}
		switch endpoint.Kind {
		case "web":
			admin.webListener = listener
			admin.webPProf = conf.Protected.Admin.WebPProf
			admin.webAllowCrossOrigin = conf.Protected.Admin.AllowCrossOrigin
		case "unix":
			admin.unixListener = listener
		case "pipe":
			admin.pipeListener = listener
		default:
			return nil, protocol.ErrInvalidContract
		}
	}
	return admin, nil
}

func mapIsolationMechanisms(input map[string]map[string]roleentry.RuntimeIsolationMechanism) map[string]map[string]api.IsolationMechanism {
	if input == nil {
		return nil
	}
	result := make(map[string]map[string]api.IsolationMechanism, len(input))
	for role, mechanisms := range input {
		mapped := make(map[string]api.IsolationMechanism, len(mechanisms))
		for name, value := range mechanisms {
			mapped[name] = api.IsolationMechanism{State: value.State, Reason: value.Reason}
		}
		result[role] = mapped
	}
	return result
}

func mapIsolationCredentials(input map[string]roleentry.RuntimeIsolationCredentials) map[string]api.IsolationCredentials {
	if input == nil {
		return nil
	}
	result := make(map[string]api.IsolationCredentials, len(input))
	for role, value := range input {
		result[role] = api.IsolationCredentials{RealUID: value.RealUID, EffectiveUID: value.EffectiveUID, SavedUID: value.SavedUID,
			FilesystemUID: value.FilesystemUID, RealGID: value.RealGID, EffectiveGID: value.EffectiveGID, SavedGID: value.SavedGID,
			FilesystemGID: value.FilesystemGID, SupplementaryGroups: append([]uint32{}, value.SupplementaryGroups...)}
	}
	return result
}

func mapIsolationCapabilitySets(input map[string]roleentry.RuntimeIsolationCapabilitySets) map[string]api.IsolationCapabilitySets {
	if input == nil {
		return nil
	}
	result := make(map[string]api.IsolationCapabilitySets, len(input))
	for role, value := range input {
		result[role] = api.IsolationCapabilitySets{Inheritable: append([]string{}, value.Inheritable...), Permitted: append([]string{}, value.Permitted...),
			Effective: append([]string{}, value.Effective...), Bounding: append([]string{}, value.Bounding...), Ambient: append([]string{}, value.Ambient...)}
	}
	return result
}

func mapIsolationResourceLimits(input map[string][]roleentry.RuntimeIsolationResourceLimit) map[string][]api.IsolationResourceLimit {
	if input == nil {
		return nil
	}
	result := make(map[string][]api.IsolationResourceLimit, len(input))
	for role, limits := range input {
		mapped := make([]api.IsolationResourceLimit, len(limits))
		for index, value := range limits {
			mapped[index] = api.IsolationResourceLimit{Name: value.Name, Requested: value.Requested, Effective: value.Effective,
				Unit: value.Unit, Scope: value.Scope, Source: value.Source, Enforcement: value.Enforcement}
		}
		result[role] = mapped
	}
	return result
}

func mapIsolationWarnings(input []roleentry.RuntimeIsolationWarning) []api.IsolationWarning {
	if input == nil {
		return nil
	}
	result := make([]api.IsolationWarning, len(input))
	for index, value := range input {
		result[index] = api.IsolationWarning{Role: value.Role, Code: value.Code, Effect: value.Effect, Feature: value.Feature}
	}
	return result
}
