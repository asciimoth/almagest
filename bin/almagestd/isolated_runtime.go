package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/asciimoth/almagest/bin/almagestd/args"
	"github.com/asciimoth/almagest/bin/almagestd/daemonservices"
	"github.com/asciimoth/almagest/bin/almagestd/internal/daemonruntime"
	"github.com/asciimoth/almagest/bin/almagestd/platform"
	"github.com/asciimoth/almagest/bin/internal/isolation/admingate"
	"github.com/asciimoth/almagest/bin/internal/isolation/commandruntime"
	"github.com/asciimoth/almagest/bin/internal/isolation/configpersistence"
	"github.com/asciimoth/almagest/bin/internal/isolation/protocol"
	"github.com/asciimoth/almagest/bin/internal/isolation/roleentry"
	client "github.com/asciimoth/almagest/bin/internal/isolation/runtime"
	"github.com/asciimoth/almagest/bin/internal/isolation/transport"
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
	if err := roleentry.SealRuntime(); err != nil {
		return fmt.Errorf("seal runtime: %w", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	bootstrap, input, err := roleentry.ReceiveRuntimeBootstrap(ctx, control)
	if err != nil {
		return fmt.Errorf("read runtime bootstrap: %w", err)
	}
	if (len(input.AdminEndpoints) != 0) != (adminStream != nil) || input.ConfigWritable != (configStream != nil) {
		return protocol.ErrInvalidContract
	}
	if err := roleentry.AcknowledgeBootstrap(ctx, control, bootstrap); err != nil {
		return fmt.Errorf("acknowledge runtime bootstrap: %w", err)
	}
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
	if configStream != nil {
		configPeer, err = transport.NewControl(ctx, configStream, bootstrap.Session, nil)
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
		commandPeer, peerErr := transport.NewControl(ctx, commandStream, bootstrap.Session, nil)
		if peerErr != nil {
			_ = system.Close()
			return fmt.Errorf("start runtime command client: %w", peerErr)
		}
		commands, err = commandruntime.New(ctx, commandPeer, bootstrap.Session, 1, nil)
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
		DebugNodes: input.Options.DebugNodes,
	}
	if input.StartupSnapshot {
		arguments.Snapshot = filepath.Join(storage, roleentry.RuntimeSnapshotFile)
	}
	randomSeeds, err := newDaemonRandomSeeds()
	if err != nil {
		return err
	}
	integrations := daemonIntegrations{
		prepareConfig: func(args.Args) (platform.ConfigBootstrap, error) {
			return platform.ConfigBootstrap{Regular: regular, Protected: protected, Persistence: persistence}, nil
		},
		enterStorage: func(path string) error {
			// The native launcher enters this fixed directory before it applies
			// the final syscall filter. R cannot inspect or change its cwd later.
			if filepath.Clean(path) != "/state" {
				return fmt.Errorf("invalid isolated runtime storage: %s", path)
			}
			return nil
		},
		loadOSAuthority: func(context.Context) (immutableTLSData, error) { return immutableTLSData{}, nil },
		prepareAdmin: func(conf *Config) (*AdminAPI, error) {
			return isolatedAdminAPI(conf, adminRuntime, input.AdminEndpoints)
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
	}
	return daemonruntime.Run(ctx, func(ctx context.Context, application *daemonruntime.Application) error {
		return runDaemon(ctx, application, func() {}, arguments, executionModeIsolationDevelopment, randomSeeds, nil, integrations)
	})
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

func isolatedAdminAPI(conf *Config, runtime *admingate.Runtime, endpoints []roleentry.RuntimeAdminEndpoint) (*AdminAPI, error) {
	admin := &AdminAPI{config: conf}
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
