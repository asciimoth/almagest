package main

import (
	"context"
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
	almagest "github.com/asciimoth/almagest/lib"
	"github.com/asciimoth/gonnect"
	"github.com/kataras/golog"
)

func main() {
	// TUN and graph startup events can occur many times in one second. Keep
	// enough timestamp precision to correlate the daemon log with host network
	// captures and admin API actions.
	golog.SetTimeFormat("2006/01/02 15:04:05.000000")

	arguments, mode, err := parseDaemonArgs()
	if err != nil {
		golog.Fatal(err)
		return
	}
	if err := runDaemonApplication(arguments, mode); err != nil {
		golog.Fatal(err)
	}
}

func parseDaemonArgs() (args.Args, executionMode, error) {
	var arguments args.Args
	if err := arg.Parse(&arguments); err != nil {
		return args.Args{}, "", err
	}
	mode, err := selectExecutionMode(arguments)
	return arguments, mode, err
}

func runDaemonApplication(arguments args.Args, mode executionMode) error {
	golog.Infof("Execution mode: %s (no sandbox)", mode)

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
			return runDaemon(ctx, application, ready, arguments, mode, randomSeeds, runtimeDependencyErr)
		})
	})
}

func runDaemon(
	ctx context.Context,
	application *daemonruntime.Application,
	ready func(),
	arguments args.Args,
	mode executionMode,
	randomSeeds daemonRandomSeeds,
	runtimeDependencyErr error,
) error {
	configBootstrap, err := platform.PrepareConfig(arguments)
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

	if err := os.Chdir(fsPath); err != nil {
		return err
	}
	golog.Infof("Using file storage at %s", fsPath)
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
	conf, err := ParseConfig(configData, protectedData)
	if err != nil {
		return err
	}
	conf.Protected.Merge(&arguments)

	immutableSecrets, err := conf.Protected.ImmutableSecrets()
	if err != nil {
		return err
	}
	osTLSData, err := loadOSTLSData(ctx, platform.OSAuthorityFetcher())
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
	application.OwnStorage(kv.Close)
	golog.Infof("Using kv database at %s", filepath.Join(fsPath, kvDBFileName))
	if err := kv.reconcileKVOwnerEntries(configuredNodeNameSet(conf.Main)); err != nil {
		return fmt.Errorf("KV ownership cleanup: %w", err)
	}

	// Prepare admin api here (start listener/create unix socket/etc)
	admin, err := PrepareAdminAPI(conf)
	if err != nil {
		return err
	}
	application.OwnAdmin(admin.Stop)
	admin.configReadOnly = configBootstrap.Persistence == nil
	admin.fileStorage = fsPath
	admin.projectVersionStatus = projectVersionStatus
	admin.executionMode = string(mode)
	admin.kv = kv
	admin.dns = kv
	admin.secrets = kv
	admin.tls = kv
	admin.tlsOSAuthorityFetcher = platform.OSAuthorityFetcher()
	admin.experimentalNodeTypes = daemonExperimentalNodeTypes(arguments.Experimental)

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

	commandRunner, err := cmdrunner.NewRunner(logf.CommandLogger(), conf.Protected.CommandRunner)
	if err != nil {
		return err
	}
	commandRunner.Start(ctx)
	application.OwnCommands(commandRunner.Close)
	var nodeCommands daemonservices.NodeCommandDispatcher = commandRunner
	var backendCallbacks daemonservices.BackendCallbackDispatcher = commandRunner

	// Setup system integration here before privileges drop
	hostNetwork, err := localnetwork.BuildWithRuntimeDependencies(
		conf.Protected.System,
		backendCallbacks,
		logf.NetworkLogger(),
		runtimeDependencyErr,
		randomSeeds.system,
	)
	if err != nil {
		return err
	}
	application.OwnHostNetwork(hostNetwork.Close)

	platform.DropPrivileges() // Currently just a stub

	nodeConfigAutosave := newNodeConfigAutosaveTrigger()
	var nodeConfigChanged func(uint64) <-chan error
	if !admin.configReadOnly {
		nodeConfigChanged = nodeConfigAutosave.Notify
	}
	engineBuildStart := time.Now()
	engine, err := almagest.BuildEngine(
		ctx,
		conf.Main,
		logf,
		hostNetwork,
		kv,
		kv,
		kv,
		kv,
		daemonNodeClasses(conf.Protected.CommandRunner, nodeCommands, arguments.Experimental, conf.Protected.Experimental),
		globalBufPool,
		arguments.DebugNodes,
		almagest.WithWorkerEventLogging(arguments.LogWorkers),
		almagest.WithNodeNameAvailableCallback(nodeNames.index.available),
		almagest.WithNodeConfigChangeCallback(nodeConfigChanged),
		almagest.WithRandomSeed(randomSeeds.workspace),
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
		admin.tlsOSAuthorityFetcher,
	)
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
