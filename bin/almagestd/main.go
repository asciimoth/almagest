package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/alexflint/go-arg"
	"github.com/asciimoth/almagest/bin/almagestd/args"
	cmdrunner "github.com/asciimoth/almagest/bin/almagestd/command"
	"github.com/asciimoth/almagest/bin/almagestd/platform"
	"github.com/asciimoth/almagest/bin/api"
	almagest "github.com/asciimoth/almagest/lib"
	"github.com/asciimoth/gonnect"
	_ "github.com/joho/godotenv/autoload"
	"github.com/kataras/golog"
)

func main() {
	// TUN and graph startup events can occur many times in one second. Keep
	// enough timestamp precision to correlate the daemon log with host network
	// captures and admin API actions.
	golog.SetTimeFormat("2006/01/02 15:04:05.000000")

	var arguments args.Args
	if err := arg.Parse(&arguments); err != nil {
		golog.Fatal(err)
		return
	}
	mode, err := selectExecutionMode(arguments)
	if err != nil {
		golog.Fatal(err)
		return
	}
	golog.Infof("Execution mode: %s (no sandbox)", mode)

	gonnect.UnfuckGoDns()
	randomSeeds, err := newDaemonRandomSeeds()
	if err != nil {
		golog.Fatal(err)
		return
	}

	if arguments.LogLevel != "" {
		golog.SetLevel(arguments.LogLevel)
	}
	if err := platform.ValidateArgs(&arguments); err != nil {
		golog.Fatal(err)
		return
	}
	if err := resolveCLIPathArgs(&arguments); err != nil {
		golog.Fatal(err)
		return
	}
	if err := resolveTmpUnixAdminArg(&arguments); err != nil {
		golog.Fatal(err)
		return
	}
	if arguments.StdInConfig && arguments.Snapshot == "-" {
		golog.Fatal(fmt.Errorf("--stdin and --snapshot - cannot read from stdin together"))
		return
	}
	configureGlobalBufPool(arguments.NoPool)
	// Validate the native DLL search before changing to file storage. Keep the
	// result until the selected integration is known, so explicit debug mode
	// remains usable without Windows runtime dependencies.
	_, runtimeDependencyErr := platform.ValidateRuntimeDependencies()
	if err := platform.RunLifecycle(func(ctx context.Context, ready func()) error {
		return runDaemon(ctx, ready, arguments, mode, randomSeeds, runtimeDependencyErr)
	}); err != nil {
		golog.Fatal(err)
	}
}

func runDaemon(
	parent context.Context,
	ready func(),
	arguments args.Args,
	mode executionMode,
	randomSeeds daemonRandomSeeds,
	runtimeDependencyErr error,
) (result error) {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	agent, err := platform.PrepareAgent(arguments)
	if err != nil {
		return fmt.Errorf("prepare config files: %w", err)
	}

	configData, protectedData, err := agent.ReadConfig()
	if err != nil {
		return err
	}
	fsPath, fsCleanup, err := platform.SetupFS(arguments.FileStorage, arguments.TmpFileStorage)
	if err != nil {
		return err
	}
	lifecycle := &daemonLifecycle{
		cancel:       cancel,
		cleanupFiles: fsCleanup,
	}
	defer func() { result = errors.Join(result, lifecycle.Close()) }()
	var kv *bboltKVManager

	if err := os.Chdir(fsPath); err != nil {
		return err
	}
	golog.Infof("Using file storage at %s", fsPath)
	if arguments.Snapshot != "" {
		var info api.SnapshotInfo
		configData, info, err = restoreStartupSnapshot(ctx, fsPath, arguments.Snapshot, os.Stdin, agent)
		if err != nil {
			return fmt.Errorf("load startup snapshot: %w", err)
		}
		golog.Infof("Loaded startup snapshot %s", info.UUID)
		if !agent.ConfRO() {
			if err := agent.WriteConfig(configData); err != nil {
				golog.Warnf("Startup snapshot config is active but could not be written: %v", err)
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
	lifecycle.closeStorage = kv.Close
	golog.Infof("Using kv database at %s", filepath.Join(fsPath, kvDBFileName))
	if err := kv.reconcileKVOwnerEntries(configuredNodeNameSet(conf.Main)); err != nil {
		return fmt.Errorf("KV ownership cleanup: %w", err)
	}

	// Prepare admin api here (start listener/create unix socket/etc)
	admin, err := PrepareAdminAPI(conf)
	if err != nil {
		return err
	}
	lifecycle.stopAdmin = admin.Stop
	admin.configReadOnly = agent.ConfRO()
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
	logProjectVersionStatus(admin.projectVersionStatus)
	daemonLogger := logf.DaemonLogger()
	nodeNames, err := newDaemonNodeNameIndex(kv, conf.Main, daemonLogger)
	if err != nil {
		return fmt.Errorf("node name index setup: %w", err)
	}
	lifecycle.closeNodeNames = nodeNames.Close
	admin.nodeNames = nodeNames.index

	commandRunner, err := cmdrunner.NewRunner(daemonLogger, conf.Protected.CommandRunner)
	if err != nil {
		return err
	}
	commandRunner.Start(ctx)
	lifecycle.closeRunner = commandRunner.Close

	// Setup system integration here before privileges drop
	sys, err := SystemIntegrationWithRuntimeDependencies(conf, commandRunner, runtimeDependencyErr, randomSeeds.system)
	if err != nil {
		return err
	}
	lifecycle.closeSystem = sys.Close

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
		sys,
		kv,
		kv,
		kv,
		kv,
		daemonNodeClasses(conf.Protected.CommandRunner, commandRunner, arguments.Experimental, conf.Protected.Experimental),
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
	lifecycle.closeEngine = engine.Close

	var wg sync.WaitGroup
	lifecycle.waitBackground = wg.Wait
	admin.configAutosaver = newConfigAutosaver(engine, conf, agent, daemonLogger, conf.SaveIdle())
	lifecycle.flushConfig = admin.configAutosaver.flushPendingError
	admin.snapshots = newSnapshotService(
		fsPath,
		admin.configAutosaver,
		engine,
		kv,
		conf,
		agent,
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
	admin.Run(ctx, cancel, &wg, engine, sys, logf)
	ready()

	<-ctx.Done()
	golog.Info("Stopping")
	return nil
}
