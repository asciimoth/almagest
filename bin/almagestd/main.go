package main

import (
	"context"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/alexflint/go-arg"
	"github.com/asciimoth/almagest/bin/almagestd/args"
	cmdrunner "github.com/asciimoth/almagest/bin/almagestd/command"
	"github.com/asciimoth/almagest/bin/almagestd/platform"
	"github.com/asciimoth/almagest/lib"
	"github.com/asciimoth/gonnect"
	_ "github.com/joho/godotenv/autoload"
	"github.com/kataras/golog"
)

func main() {
	gonnect.UnfuckGoDns()

	var args args.Args
	if err := arg.Parse(&args); err != nil {
		golog.Fatal(err)
		return
	}
	configureGlobalBufPool(args.NoPool)

	if args.LogLevel != "" {
		golog.SetLevel(args.LogLevel)
	}

	agent := platform.RunAgent(args)

	configData, protectedData, err := agent.ReadConfig()
	if err != nil {
		golog.Fatal(err)
		return
	}
	conf, err := ParseConfig(configData, protectedData)
	if err != nil {
		golog.Fatal(err)
		return
	}
	conf.Protected.Merge(&args)

	// Prepare admin api here (start listener/create unix socket/etc)
	admin, err := PrepareAdminAPI(conf)
	if err != nil {
		golog.Fatal(err)
		return
	}
	admin.projectVersionStatus = projectVersionStatus
	defer admin.Stop()

	logf := NewLogFactory(conf.LogCache().MergeArgs(&args))
	logProjectVersionStatus(admin.projectVersionStatus)
	daemonLogger := logf.DaemonLogger()

	commandRunner, err := cmdrunner.NewRunner(daemonLogger, conf.Protected.CommandRunner)
	if err != nil {
		golog.Fatal(err)
		return
	}
	commandRunner.Start(context.Background())
	defer commandRunner.Close()

	// Setup system integration here before privileges drop
	sys, err := SystemIntegration(conf, commandRunner)
	if err != nil {
		golog.Fatal(err)
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
		nil,
		globalBufPool,
		args.DebugNodes,
	)
	engineBuildElapsed := time.Since(engineBuildStart)
	if err != nil {
		daemonLogger.Debugf("engine build failed after %s: %v", engineBuildElapsed, err)
		golog.Fatal("Engine setup", err)
	}
	daemonLogger.Debugf("engine built in %s", engineBuildElapsed)
	defer engine.Close() //nolint

	var wg sync.WaitGroup
	admin.configAutosaver = newConfigAutosaver(engine, conf, agent, daemonLogger, conf.SaveIdle())
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
