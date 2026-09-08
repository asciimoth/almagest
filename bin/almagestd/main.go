package main

import (
	"context"
	almagest "github.com/asciimoth/almagest/lib"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/alexflint/go-arg"
	"github.com/asciimoth/almagest/bin/almagestd/args"
	cmdrunner "github.com/asciimoth/almagest/bin/almagestd/command"
	"github.com/asciimoth/almagest/bin/almagestd/platform"
	"github.com/asciimoth/gonnect"
	_ "github.com/joho/godotenv/autoload"
	"github.com/kataras/golog"
)

func main() {
	gonnect.UnfuckGoDns()

	fatal := func(v ...any) {
		golog.Fatal(v...)
	}
	randomSeeds, err := newDaemonRandomSeeds()
	if err != nil {
		fatal(err)
		return
	}

	var args args.Args
	if err = arg.Parse(&args); err != nil {
		fatal(err)
		return
	}

	if args.LogLevel != "" {
		golog.SetLevel(args.LogLevel)
	}
	if err := resolveCLIPathArgs(&args); err != nil {
		fatal(err)
		return
	}
	if err := resolveTmpUnixAdminArg(&args); err != nil {
		fatal(err)
		return
	}
	configureGlobalBufPool(args.NoPool)

	agent := platform.RunAgent(args)

	configData, protectedData, err := agent.ReadConfig()
	if err != nil {
		fatal(err)
		return
	}
	conf, err := ParseConfig(configData, protectedData)
	if err != nil {
		fatal(err)
		return
	}
	conf.Protected.Merge(&args)

	fsPath, fsCleanup, err := platform.SetupFS(args.FileStorage, args.TmpFileStorage)
	if err != nil {
		fatal(err)
		return
	}

	var cleanupOnce sync.Once
	cleanupFileStorage := func() {
		cleanupOnce.Do(func() {
			if err := fsCleanup(); err != nil {
				golog.Warnf("File storage cleanup failed: %v", err)
			}
		})
	}
	defer cleanupFileStorage()
	var kv *bboltKVManager
	fatal = func(v ...any) {
		if kv != nil {
			_ = kv.Close()
		}
		cleanupFileStorage()
		golog.Fatal(v...)
	}

	if err := os.Chdir(fsPath); err != nil {
		fatal(err)
		return
	}
	golog.Infof("Using file storage at %s", fsPath)

	immutableSecrets, err := conf.Protected.ImmutableSecrets()
	if err != nil {
		fatal(err)
		return
	}
	osTLSData, err := loadOSTLSData(context.Background(), platform.OSAuthorityFetcher())
	if err != nil {
		fatal(err)
		return
	}
	protectedTLSData, err := conf.Protected.ImmutableTLS()
	if err != nil {
		fatal(err)
		return
	}
	builtinCAOptions, err := conf.TLSBuiltinCAOptions()
	if err != nil {
		fatal(err)
		return
	}
	builtinCA, err := conf.Protected.TLSBuiltinCA()
	if err != nil {
		fatal(err)
		return
	}
	if builtinCA == nil {
		builtinCA, err = loadOrCreateTLSBuiltinCA(fsPath, builtinCAOptions, time.Now())
		if err != nil {
			fatal(err)
			return
		}
	}
	kv, err = openKVManagerInStorage(fsPath, bboltKVManagerOptions{
		DisableDNSCacheExpiration: args.NoDNSCacheExpiration,
		ImmutableSecrets:          immutableSecrets,
		ImmutableTLS:              mergeImmutableTLSData(osTLSData, protectedTLSData),
		BuiltinCA:                 builtinCA,
	})
	if err != nil {
		fatal(err)
		return
	}
	defer kv.Close() //nolint
	golog.Infof("Using kv database at %s", filepath.Join(fsPath, kvDBFileName))
	if err := kv.reconcileKVOwnerEntries(configuredNodeNameSet(conf.Main)); err != nil {
		fatal("KV ownership cleanup", err)
		return
	}

	// Prepare admin api here (start listener/create unix socket/etc)
	admin, err := PrepareAdminAPI(conf)
	if err != nil {
		fatal(err)
		return
	}
	admin.configReadOnly = agent.ConfRO()
	admin.fileStorage = fsPath
	admin.projectVersionStatus = projectVersionStatus
	admin.kv = kv
	admin.dns = kv
	admin.secrets = kv
	admin.tls = kv
	admin.tlsOSAuthorityFetcher = platform.OSAuthorityFetcher()
	defer admin.Stop()

	logf := NewLogFactory(conf.LogCache().MergeArgs(&args))
	logProjectVersionStatus(admin.projectVersionStatus)
	daemonLogger := logf.DaemonLogger()
	nodeNames, err := newDaemonNodeNameIndex(kv, conf.Main, daemonLogger)
	if err != nil {
		fatal("Node name index setup", err)
		return
	}
	defer nodeNames.Close()
	admin.nodeNames = nodeNames.index

	commandRunner, err := cmdrunner.NewRunner(daemonLogger, conf.Protected.CommandRunner)
	if err != nil {
		fatal(err)
		return
	}
	commandRunner.Start(context.Background())
	defer commandRunner.Close()

	// Setup system integration here before privileges drop
	sys, err := SystemIntegration(conf, commandRunner, randomSeeds.system)
	if err != nil {
		fatal(err)
		return
	}
	defer sys.Close() //nolint

	platform.DropPrivileges() // Currently just a stub

	// TODO: Reload config on SIGHUP instead of stopping
	ctx, cancel := signal.NotifyContext(
		context.Background(), syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP,
	)
	defer cancel()

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
		daemonNodeClasses(conf.Protected.CommandRunner, commandRunner),
		globalBufPool,
		args.DebugNodes,
		almagest.WithWorkerEventLogging(args.LogWorkers),
		almagest.WithNodeNameAvailableCallback(nodeNames.index.available),
		almagest.WithRandomSeed(randomSeeds.workspace),
	)
	engineBuildElapsed := time.Since(engineBuildStart)
	if err != nil {
		daemonLogger.Debugf("engine build failed after %s: %v", engineBuildElapsed, err)
		fatal("Engine setup", err)
	}
	daemonLogger.Debugf("engine built in %s", engineBuildElapsed)
	defer engine.Close() //nolint

	var wg sync.WaitGroup
	admin.configAutosaver = newConfigAutosaver(engine, conf, agent, daemonLogger, conf.SaveIdle())
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

	// Launch admin api fully
	admin.Run(ctx, cancel, &wg, engine, sys, logf)

	wg.Go(func() {
		<-ctx.Done()
		golog.Info("Stopping")
	})

	wg.Wait()
}
