package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/alexflint/go-arg"
	"github.com/asciimoth/almagest/bin/almagestd/args"
	cmdrunner "github.com/asciimoth/almagest/bin/almagestd/command"
	"github.com/asciimoth/almagest/bin/almagestd/daemonservices"
	"github.com/asciimoth/almagest/bin/almagestd/daemonservices/localconfig"
	"github.com/asciimoth/almagest/bin/almagestd/daemonservices/localnetwork"
	"github.com/asciimoth/almagest/bin/almagestd/internal/daemonruntime"
	"github.com/asciimoth/almagest/bin/almagestd/platform"
	"github.com/asciimoth/almagest/bin/api"
	sandboxservice "github.com/asciimoth/almagest/bin/internal/subprogram/service"
	"github.com/asciimoth/almagest/bin/internal/tlsplatform"
	almagest "github.com/asciimoth/almagest/lib"
	sandbox "github.com/asciimoth/gobox"
	"github.com/asciimoth/gonnect"
	"github.com/kataras/golog"
)

func main() {
	// TUN and graph startup events can occur many times in one second. Keep
	// enough timestamp precision to correlate the daemon log with host network
	// captures and admin API actions.
	golog.SetTimeFormat("2006/01/02 15:04:05.000000")
	if isolatedSandboxSupervisorRole() {
		if err := runIsolatedSandboxSupervisorRole(); err != nil {
			golog.Fatal(err)
		}
		return
	}
	if isolatedRuntimeRole() {
		if err := runIsolatedRuntimeRole(); err != nil {
			golog.Fatal(err)
		}
		return
	}

	arguments, mode, err := parseDaemonArgs(os.Args[1:], os.Stdout)
	if err != nil {
		if errors.Is(err, arg.ErrHelp) {
			return
		}
		golog.Fatal(err)
		return
	}
	if arguments.DumpWebdemoClasses {
		if err := writeWebdemoNodeClassCache(os.Stdout); err != nil {
			golog.Fatal(err)
		}
		return
	}
	if mode == executionModeIsolation {
		err = runIsolatedNetworkRole()
	} else {
		err = runDaemonApplication(arguments, mode)
	}
	if err != nil {
		golog.Fatal(err)
	}
}

func parseDaemonArgs(cliArgs []string, output io.Writer) (args.Args, executionMode, error) {
	var arguments args.Args
	parser, err := arg.NewParser(arg.Config{Out: output}, &arguments)
	if err != nil {
		return args.Args{}, "", err
	}
	if err := parser.Parse(cliArgs); err != nil {
		if errors.Is(err, arg.ErrHelp) {
			parser.WriteHelp(output)
		}
		return args.Args{}, "", err
	}
	// The cache dump reads registration metadata only. It must not inherit or
	// validate a host execution policy because it does not start the daemon.
	if arguments.DumpWebdemoClasses {
		return arguments, executionModeDirect, nil
	}
	mode, err := selectExecutionMode(arguments)
	return arguments, mode, err
}

func runDaemonApplication(arguments args.Args, mode executionMode) error {
	golog.Infof("Execution mode: %s", mode)
	for _, warning := range executionModeStartupWarnings(mode) {
		golog.Warn(warning)
	}

	gonnect.UnfuckGoDns()
	randomSeeds, err := newDaemonRandomSeeds()
	if err != nil {
		return err
	}

	if arguments.LogLevel != "" {
		golog.SetLevel(arguments.LogLevel)
	}
	if err := platform.ValidateArgs(&arguments); err != nil {
		return err
	}
	if err := resolveCLIPathArgs(&arguments); err != nil {
		return err
	}
	if err := resolveTmpUnixAdminArg(&arguments); err != nil {
		return err
	}
	if arguments.StdInConfig && arguments.Snapshot == "-" {
		return fmt.Errorf("--stdin and --snapshot - cannot read from stdin together")
	}
	configureGlobalBufPool(arguments.NoPool)
	// Validate the native DLL search before changing to file storage. Keep the
	// result until the selected integration is known, so explicit debug mode
	// remains usable without Windows runtime dependencies.
	_, runtimeDependencyErr := platform.ValidateRuntimeDependencies()
	return platform.RunLifecycle(func(ctx context.Context, ready func()) error {
		return daemonruntime.Run(ctx, func(ctx context.Context, application *daemonruntime.Application) error {
			return runDaemon(ctx, application, ready, arguments, mode, randomSeeds, runtimeDependencyErr, directDaemonIntegrations())
		})
	})
}

func executionModeStartupWarnings(mode executionMode) []string {
	if mode != executionModeDirect {
		return nil
	}
	return []string{
		"Daemon process isolation is disabled",
		"A compromised graph runtime can access all daemon privileges and host resources",
	}
}

type daemonIntegrations struct {
	parseConfig     func([]byte, []byte) (*Config, error)
	prepareConfig   func(args.Args) (platform.ConfigBootstrap, error)
	enterStorage    func(string) error
	loadOSAuthority func(context.Context) (immutableTLSData, error)
	prepareAdmin    func(*Config) (*AdminAPI, error)
	buildCommands   func(context.Context, *LogFactory, *Config) (daemonservices.NodeCommandDispatcher, daemonservices.BackendCallbackDispatcher, func(), error)
	buildNetwork    func(*Config, daemonservices.BackendCallbackDispatcher, *LogFactory, error, int64) (daemonservices.HostNetwork, error)
	buildSandboxes  func(*Config) (sandbox.Service, error)
}

func directDaemonIntegrations() daemonIntegrations {
	return daemonIntegrations{
		parseConfig:   ParseConfig,
		prepareConfig: platform.PrepareConfig,
		enterStorage:  os.Chdir,
		loadOSAuthority: func(ctx context.Context) (immutableTLSData, error) {
			return loadTLSPlatformData(ctx, tlsplatform.NewAuthorityProvider(tlsplatform.SystemAuthorityFetcher()))
		},
		prepareAdmin: PrepareAdminAPI,
		buildCommands: func(ctx context.Context, logf *LogFactory, conf *Config) (daemonservices.NodeCommandDispatcher, daemonservices.BackendCallbackDispatcher, func(), error) {
			runner, err := cmdrunner.NewRunner(logf.CommandLogger(), conf.Protected.CommandRunner)
			if err != nil {
				return nil, nil, nil, err
			}
			runner.Start(ctx)
			return runner, runner, runner.Close, nil
		},
		buildNetwork: func(conf *Config, callbacks daemonservices.BackendCallbackDispatcher, logf *LogFactory, dependencyErr error, seed int64) (daemonservices.HostNetwork, error) {
			return localnetwork.BuildWithRuntimeDependencies(conf.Protected.System, callbacks, logf.NetworkLogger(), dependencyErr, seed)
		},
		buildSandboxes: func(conf *Config) (sandbox.Service, error) {
			if len(conf.Protected.Sandbox.Programs) == 0 && len(conf.Protected.Sandbox.Templates) == 0 &&
				len(conf.Protected.Sandbox.UnavailableTemplates) == 0 {
				return nil, nil
			}
			return sandboxservice.NewHost(conf.Protected.Sandbox)
		},
	}
}

func (i daemonIntegrations) validate() error {
	if i.parseConfig == nil || i.prepareConfig == nil || i.enterStorage == nil || i.loadOSAuthority == nil || i.prepareAdmin == nil || i.buildCommands == nil || i.buildNetwork == nil {
		return errors.New("incomplete daemon integration")
	}
	return nil
}

func runDaemon(
	ctx context.Context,
	application *daemonruntime.Application,
	ready func(),
	arguments args.Args,
	mode executionMode,
	randomSeeds daemonRandomSeeds,
	runtimeDependencyErr error,
	integrations daemonIntegrations,
) error {
	if err := integrations.validate(); err != nil {
		return err
	}
	configBootstrap, err := integrations.prepareConfig(arguments)
	if err != nil {
		return fmt.Errorf("prepare config files: %w", err)
	}
	configData := configBootstrap.Regular
	protectedData := configBootstrap.Protected
	persistenceRevision := daemonservices.ConfigRevision(configData)
	fsPath, fsCleanup, err := platform.SetupFS(arguments.FileStorage, arguments.TmpFileStorage)
	if err != nil {
		return err
	}
	application.OwnFiles(fsCleanup)
	var kv *bboltKVManager

	if err := integrations.enterStorage(fsPath); err != nil {
		return err
	}
	golog.Infof("Using file storage at %s", fsPath)
	interrupted, err := recoverInterruptedRestore(ctx, fsPath, configData, configBootstrap)
	if err != nil {
		return fmt.Errorf("recover interrupted live restore: %w", err)
	}
	if interrupted.RolledBack {
		configData = interrupted.ConfigData
		golog.Warn("Recovered application state from an interrupted live restore")
	}
	if interrupted.UnresolvedConfigWrite {
		golog.Warn("A committed live restore has an unresolved regular-config write; restored storage was kept")
		if arguments.Snapshot != "" {
			return fmt.Errorf("%w: resolve the committed live-restore config before applying a startup snapshot", errRestoreRecoveryRequired)
		}
	}
	if arguments.Snapshot != "" {
		snapshotInput, openErr := openStartupSnapshotInput(arguments.Snapshot, os.Stdin)
		if openErr != nil {
			return fmt.Errorf("load startup snapshot: %w", openErr)
		}
		var info api.SnapshotInfo
		configData, info, err = restoreStartupSnapshot(ctx, fsPath, snapshotInput, configBootstrap)
		closeErr := snapshotInput.Close()
		if err != nil {
			return fmt.Errorf("load startup snapshot: %w", err)
		}
		if closeErr != nil {
			return fmt.Errorf("load startup snapshot: close archive: %w", closeErr)
		}
		golog.Infof("Loaded startup snapshot %s", info.UUID)
		if configBootstrap.Persistence != nil {
			result := configBootstrap.Persistence.Replace(persistenceRevision, "startup-snapshot", configData)
			if err := result.Err(); err != nil {
				golog.Warnf("Startup snapshot config is active but could not be written: %v", err)
			} else {
				persistenceRevision = result.Revision
			}
		}
	}
	conf, err := integrations.parseConfig(configData, protectedData)
	if err != nil {
		return err
	}
	conf.Protected.Merge(&arguments)
	if err := conf.Protected.MergeTLSCRLArguments(arguments); err != nil {
		return err
	}
	if err := prepareSystemProgramPolicy(conf, fsPath); err != nil {
		return fmt.Errorf("prepare system program sandbox policy: %w", err)
	}

	immutableSecrets, err := conf.Protected.ImmutableSecrets()
	if err != nil {
		return err
	}
	osTLSData, err := integrations.loadOSAuthority(ctx)
	if err != nil {
		return err
	}
	protectedTLSData, err := conf.Protected.ImmutableTLS()
	if err != nil {
		return err
	}
	builtinCAOptions, err := conf.TLSBuiltinCAOptions()
	if err != nil {
		return err
	}
	builtinCA, err := conf.Protected.TLSBuiltinCA()
	if err != nil {
		return err
	}
	if builtinCA == nil {
		builtinCA, err = loadOrCreateTLSBuiltinCA(fsPath, builtinCAOptions, time.Now())
		if err != nil {
			return err
		}
	}
	kv, err = openKVManagerInStorage(fsPath, bboltKVManagerOptions{
		DisableDNSCacheExpiration: arguments.NoDNSCacheExpiration,
		ImmutableSecrets:          immutableSecrets,
		ImmutableTLS:              mergeImmutableTLSData(osTLSData, protectedTLSData),
		BuiltinCA:                 builtinCA,
	})
	if err != nil {
		return err
	}
	dnsManager, memoryDNS := dnsCacheManagerForExecutionMode(mode, kv, arguments.NoDNSCacheExpiration)
	application.OwnStorage(func() error {
		var cacheErr error
		if memoryDNS != nil {
			cacheErr = memoryDNS.Close()
		}
		return errors.Join(cacheErr, kv.Close())
	})
	golog.Infof("Using kv database at %s", filepath.Join(fsPath, kvDBFileName))
	if err := kv.reconcileKVOwnerEntries(configuredNodeNameSet(conf.Main)); err != nil {
		return fmt.Errorf("KV ownership cleanup: %w", err)
	}

	// Prepare admin api here (start listener/create unix socket/etc)
	admin, err := integrations.prepareAdmin(conf)
	if err != nil {
		return err
	}
	application.OwnAdmin(admin.Stop)
	admin.configReadOnly = configBootstrap.Persistence == nil
	admin.fileStorage = fsPath
	admin.projectVersionStatus = projectVersionStatus
	admin.executionMode = string(mode)
	if interrupted.UnresolvedConfigWrite {
		admin.mutations.setInitialRecoveryRequired(errors.New("committed snapshot restore has an unresolved regular-config write"))
	}
	admin.kv = kv
	admin.dns = kv
	admin.secrets = kv
	admin.tls = kv
	admin.tlsOSDataLoader = integrations.loadOSAuthority
	admin.experimentalNodeTypes = make(map[string]bool)
	admin.deprecatedNodeTypes = map[string]bool{
		"almagest/AmnesiaCtrl": true,
	}

	logf := NewLogFactory(conf.LogCache().MergeArgs(&arguments))
	application.OwnLogging(logf.Close)
	configBootstrap.Persistence = localconfig.WithLogger(configBootstrap.Persistence, logf.ConfigLogger())
	logProjectVersionStatus(admin.projectVersionStatus)
	daemonLogger := logf.RuntimeLogger()
	nodeNames, err := newDaemonNodeNameIndex(kv, conf.Main, daemonLogger)
	if err != nil {
		return fmt.Errorf("node name index setup: %w", err)
	}
	application.OwnNodeNames(nodeNames.Close)
	admin.nodeNames = nodeNames.index

	nodeCommands, backendCallbacks, closeCommands, err := integrations.buildCommands(ctx, logf, conf)
	if err != nil {
		return err
	}
	application.OwnCommands(closeCommands)

	// Direct mode keeps its launch identity because the host-network backend
	// can need that authority for its full lifetime. Isolated mode reaches this
	// code only after the native launcher installed the runtime role's final
	// identity and restrictions, before exec started the Go runtime.
	hostNetwork, err := integrations.buildNetwork(conf, backendCallbacks, logf, runtimeDependencyErr, randomSeeds.system)
	if err != nil {
		return err
	}
	application.OwnHostNetwork(hostNetwork.Close)
	var sandboxService sandbox.Service
	if integrations.buildSandboxes != nil {
		sandboxService, err = integrations.buildSandboxes(conf)
		if err != nil {
			return fmt.Errorf("build subprogram sandbox service: %w", err)
		}
	}
	if reporter, ok := sandboxService.(sandbox.StatusReporter); ok {
		admin.sandboxStatus = func() *api.SubprogramSandboxStatus {
			return mapSandboxStatus(reporter.ServiceStatus())
		}
	}

	nodeConfigAutosave := newNodeConfigAutosaveTrigger()
	var nodeConfigChanged func(uint64) <-chan error
	if !admin.configReadOnly {
		nodeConfigChanged = nodeConfigAutosave.Notify
	}
	engineBuildStart := time.Now()
	engineOptions := []almagest.BuildEngineOption{
		almagest.WithWorkerEventLogging(arguments.LogWorkers),
		almagest.WithNodeNameAvailableCallback(nodeNames.index.available),
		almagest.WithNodeConfigChangeCallback(nodeConfigChanged),
		almagest.WithRandomSeed(randomSeeds.workspace),
	}
	if sandboxService != nil {
		engineOptions = append(engineOptions, almagest.WithSandboxes(sandboxService))
	}
	engine, err := almagest.BuildEngine(
		ctx,
		conf.Main,
		logf,
		hostNetwork,
		kv,
		dnsManager,
		kv,
		kv,
		daemonNodeClasses(conf.Protected.CommandRunner, nodeCommands),
		globalBufPool,
		arguments.DebugNodes,
		engineOptions...,
	)
	engineBuildElapsed := time.Since(engineBuildStart)
	if err != nil {
		daemonLogger.Debugf("engine build failed after %s: %v", engineBuildElapsed, err)
		return fmt.Errorf("engine setup: %w", err)
	}
	daemonLogger.Debugf("engine built in %s", engineBuildElapsed)
	application.OwnEngine(engine.Close)

	var wg sync.WaitGroup
	application.OwnBackground(wg.Wait)
	admin.configAutosaver = newConfigAutosaver(
		engine, conf, configBootstrap.Persistence, daemonLogger, conf.SaveIdle(), persistenceRevision,
	)
	application.OwnConfigFlush(admin.configAutosaver.flushPendingError)
	admin.snapshots = newSnapshotService(
		fsPath,
		admin.configAutosaver,
		engine,
		kv,
		conf,
		configBootstrap,
		admin.configReadOnly,
		daemonLogger,
		nil,
	)
	admin.snapshots.tlsLoader = integrations.loadOSAuthority
	admin.snapshots.examples = admin.examples
	admin.snapshots.restore.setEpochReleaser(hostNetwork)
	admin.snapshots.requireRecovery = admin.mutations.requireRecovery
	wg.Go(func() {
		admin.configAutosaver.Run(ctx)
	})
	if !admin.configReadOnly {
		wg.Go(func() {
			nodeConfigAutosave.Run(ctx, admin.configAutosaver)
		})
	}

	// Launch admin api fully
	admin.Run(ctx, application.Cancel, &wg, engine, hostNetwork, logf)
	ready()

	<-ctx.Done()
	golog.Info("Stopping")
	return nil
}

func mapSandboxStatus(input sandbox.ServiceStatus) *api.SubprogramSandboxStatus {
	limits := make(map[string]int64, len(input.Limits))
	for name, value := range input.Limits {
		limits[name] = value
	}
	result := &api.SubprogramSandboxStatus{Mode: string(input.Mode), Platform: input.Platform,
		EffectiveIdentity: input.EffectiveIdentity, Templates: make(map[string]api.SubprogramTemplateStatus, len(input.Templates)), Limits: limits,
		ActiveOwners: input.ActiveOwners, OrphanedOwners: input.OrphanedOwners, QuarantinedOwners: input.QuarantinedOwners}
	for id, status := range input.Templates {
		template := api.SubprogramTemplateStatus{Profile: status.Profile, EffectiveIdentity: status.EffectiveIdentity, StrictSatisfied: status.StrictSatisfied,
			FullBoundary: status.FullBoundary, Features: make([]api.SubprogramFeatureStatus, 0, len(status.Features)),
			Warnings: append([]string{}, status.Warnings...)}
		for _, feature := range status.Features {
			template.Features = append(template.Features, api.SubprogramFeatureStatus{Feature: string(feature.Feature), State: string(feature.State), Reason: feature.Reason})
		}
		result.Templates[string(id)] = template
	}
	return result
}

// openStartupSnapshotInput resolves the operator-selected archive before the
// runtime parses it. The restore coordinator receives only the archive bytes;
// it cannot select or open another host path.
func openStartupSnapshotInput(path string, stdin io.Reader) (io.ReadCloser, error) {
	if path == "-" {
		if stdin == nil {
			return nil, fmt.Errorf("snapshot archive path - requires stdin")
		}
		return io.NopCloser(stdin), nil
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open startup snapshot archive %s: %w", path, err)
	}
	return file, nil
}
