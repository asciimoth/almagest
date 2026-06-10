package main

import (
	"context"
	"os/signal"
	"sync"
	"syscall"

	"github.com/alexflint/go-arg"
	"github.com/asciimoth/almagest/bin/platforms"
	"github.com/asciimoth/almagest/lib"
	_ "github.com/joho/godotenv/autoload"
	"github.com/kataras/golog"
)

type Args struct {
	// If StdInConfig==true, config should be loaded form stdin and
	// defaults should be used for protected config.
	// Else Config and ProtectedConfig path should be used to load HuJSON configs.
	// If Config is a directory, it should be treated as "{Config}/alm.hujson".
	// If Config is not set, platform defaults should be used.
	// If ProtectedConfig is not set, it should be treated as a "protected_alm.hujson"
	// in same dir where main config placed.
	// If any of config files is not present, defaults should be used.
	StdInConfig     bool   `arg:"--stdin" help:"read config from stdin"`
	Config          string `arg:"-c,--config,env:ALMAGEST_CONF" help:"config path"`
	ProtectedConfig string `arg:"-p,--protected,env:ALMAGEST_PROTECTED_CONF" help:"protected config path"`

	LogLevel string `arg:"-l,--lvl,env:ALMAGEST_LOG_LVL" help:"logging level debug|info|warn|error|fatal"`

	WebAdmin                *uint16  `arg:"-w,--web,env:ALMAGEST_WEB_ADMIN" help:"port to run local web admin panel on"`
	WebAdminFilterUIDs      []uint32 `arg:"--web-filter-uid,separate,env:ALMAGEST_WEB_ADMIN_FILTER_UIDS" help:"allowed web admin peer owner UID; repeat flag or comma-separate env values"`
	WebAdminFilterGIDs      []uint32 `arg:"--web-filter-gid,separate,env:ALMAGEST_WEB_ADMIN_FILTER_GIDS" help:"allowed web admin peer owner GID; repeat flag or comma-separate env values"`
	WebAdminFilterUsernames []string `arg:"--web-filter-user,separate,env:ALMAGEST_WEB_ADMIN_FILTER_USERS" help:"allowed web admin peer owner username; repeat flag or comma-separate env values"`

	DebugNodes bool `arg:"--debug-nodes" help:"add debug node classes"`
}

func main() {
	var args Args
	if err := arg.Parse(&args); err != nil {
		golog.Fatal(err)
		return
	}

	if args.LogLevel != "" {
		golog.SetLevel(args.LogLevel)
	}

	agent := RunAgent(args)

	conf, err := agent.ReadConfig()
	if err != nil {
		golog.Fatal(err)
		return
	}

	// Prepare admin api here (start listener/create unix socket/etc)
	admin, err := PrepareAdminAPI(conf)
	if err != nil {
		golog.Fatal(err)
		return
	}
	admin.projectVersionStatus = projectVersionStatus
	defer admin.Stop()

	logf := NewLogFactory(conf.LogCache())
	logProjectVersionStatus(admin.projectVersionStatus)

	// Setup system integration here before privileges drop
	sys, err := SystemIntegration(conf)
	if err != nil {
		golog.Fatal(err)
		return
	}
	defer sys.Close() //nolint

	platforms.DropPrivileges() // Currently just a stub

	// TODO: Reload config on SIGHUP instead of stopping
	ctx, cancel := signal.NotifyContext(
		context.Background(), syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP,
	)
	defer cancel()

	engine, err := almagest.BuildEngine(
		ctx,
		conf.Main,
		logf,
		sys,
		nil,
		globalBufPool,
		args.DebugNodes,
	)
	if err != nil {
		golog.Fatal("Engine setup", err)
	}
	defer engine.Close() //nolint

	var wg sync.WaitGroup
	admin.configAutosaver = newConfigAutosaver(engine, conf, agent, logf.DaemonLogger(), conf.SaveIdle())
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
