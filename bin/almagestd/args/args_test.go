package args

import (
	"bytes"
	"testing"

	"github.com/alexflint/go-arg"
)

func TestNoDNSCacheExpirationFlag(t *testing.T) {
	var a Args
	parser, err := arg.NewParser(arg.Config{IgnoreEnv: true, Out: new(bytes.Buffer)}, &a)
	if err != nil {
		t.Fatal(err)
	}
	if err := parser.Parse([]string{"--no-dns-cache-expiration"}); err != nil {
		t.Fatalf("Parse(--no-dns-cache-expiration): %v", err)
	}
	if !a.NoDNSCacheExpiration {
		t.Fatal("NoDNSCacheExpiration = false, want true")
	}
}

func TestNoDNSCacheExpirationEnv(t *testing.T) {
	t.Setenv("ALMAGEST_NO_DNS_CACHE_EXPIRATION", "true")

	var a Args
	parser, err := arg.NewParser(arg.Config{Out: new(bytes.Buffer)}, &a)
	if err != nil {
		t.Fatal(err)
	}
	if err := parser.Parse(nil); err != nil {
		t.Fatalf("Parse with ALMAGEST_NO_DNS_CACHE_EXPIRATION: %v", err)
	}
	if !a.NoDNSCacheExpiration {
		t.Fatal("NoDNSCacheExpiration = false, want true from environment")
	}
}

func TestLogWorkersFlag(t *testing.T) {
	var a Args
	parser, err := arg.NewParser(arg.Config{IgnoreEnv: true, Out: new(bytes.Buffer)}, &a)
	if err != nil {
		t.Fatal(err)
	}
	if err := parser.Parse([]string{"--log-workers"}); err != nil {
		t.Fatalf("Parse(--log-workers): %v", err)
	}
	if !a.LogWorkers {
		t.Fatal("LogWorkers = false, want true")
	}
}

func TestExperimentalFlag(t *testing.T) {
	var a Args
	parser, err := arg.NewParser(arg.Config{IgnoreEnv: true, Out: new(bytes.Buffer)}, &a)
	if err != nil {
		t.Fatal(err)
	}
	if a.Experimental {
		t.Fatal("Experimental = true before parsing --experimental")
	}
	if err := parser.Parse([]string{"--experimental"}); err != nil {
		t.Fatalf("Parse(--experimental): %v", err)
	}
	if !a.Experimental {
		t.Fatal("Experimental = false, want true")
	}
}

func TestNoIsolationFlag(t *testing.T) {
	var a Args
	parser, err := arg.NewParser(arg.Config{IgnoreEnv: true, Out: new(bytes.Buffer)}, &a)
	if err != nil {
		t.Fatal(err)
	}
	if err := parser.Parse([]string{"--no-isolation"}); err != nil {
		t.Fatalf("Parse(--no-isolation): %v", err)
	}
	if !a.NoIsolation {
		t.Fatal("NoIsolation = false, want true")
	}
}

func TestIsolationOptions(t *testing.T) {
	var fromFlags Args
	parser, err := arg.NewParser(arg.Config{IgnoreEnv: true, Out: new(bytes.Buffer)}, &fromFlags)
	if err != nil {
		t.Fatal(err)
	}
	if err := parser.Parse([]string{"--isolation=auto", "--isolation-state-dir", "/srv/almagest-user"}); err != nil {
		t.Fatalf("parse isolation flags: %v", err)
	}
	if fromFlags.Isolation != "auto" || fromFlags.IsolationStateDir != "/srv/almagest-user" {
		t.Fatalf("isolation flags = policy %q state %q", fromFlags.Isolation, fromFlags.IsolationStateDir)
	}

	t.Setenv("ALMAGEST_ISOLATION", "strict")
	var fromEnvironment Args
	parser, err = arg.NewParser(arg.Config{Out: new(bytes.Buffer)}, &fromEnvironment)
	if err != nil {
		t.Fatal(err)
	}
	if err := parser.Parse(nil); err != nil {
		t.Fatalf("parse ALMAGEST_ISOLATION: %v", err)
	}
	if fromEnvironment.Isolation != "strict" {
		t.Fatalf("Isolation = %q, want strict from environment", fromEnvironment.Isolation)
	}

	var overridden Args
	parser, err = arg.NewParser(arg.Config{Out: new(bytes.Buffer)}, &overridden)
	if err != nil {
		t.Fatal(err)
	}
	if err := parser.Parse([]string{"--isolation=off"}); err != nil {
		t.Fatalf("parse isolation override: %v", err)
	}
	if overridden.Isolation != "off" {
		t.Fatalf("Isolation = %q, want CLI value off", overridden.Isolation)
	}
}

func TestWebAdminAllowCrossOriginFlagAndEnvironment(t *testing.T) {
	var fromFlag Args
	parser, err := arg.NewParser(arg.Config{IgnoreEnv: true, Out: new(bytes.Buffer)}, &fromFlag)
	if err != nil {
		t.Fatal(err)
	}
	if err := parser.Parse([]string{"--web-allow-cross-origin"}); err != nil {
		t.Fatalf("Parse(--web-allow-cross-origin): %v", err)
	}
	if !fromFlag.WebAdminAllowCrossOrigin {
		t.Fatal("WebAdminAllowCrossOrigin = false, want true from flag")
	}

	t.Setenv("ALMAGEST_WEB_ADMIN_ALLOW_CROSS_ORIGIN", "true")
	var fromEnvironment Args
	parser, err = arg.NewParser(arg.Config{Out: new(bytes.Buffer)}, &fromEnvironment)
	if err != nil {
		t.Fatal(err)
	}
	if err := parser.Parse(nil); err != nil {
		t.Fatalf("Parse with ALMAGEST_WEB_ADMIN_ALLOW_CROSS_ORIGIN: %v", err)
	}
	if !fromEnvironment.WebAdminAllowCrossOrigin {
		t.Fatal("WebAdminAllowCrossOrigin = false, want true from environment")
	}
}

func TestStartupSnapshotFlagsAndEnvironment(t *testing.T) {
	for _, test := range []struct {
		name string
		args []string
		want string
	}{
		{name: "file", args: []string{"--snapshot", "/tmp/state.zip"}, want: "/tmp/state.zip"},
		{name: "stdin", args: []string{"--snapshot", "-"}, want: "-"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var a Args
			parser, err := arg.NewParser(arg.Config{IgnoreEnv: true, Out: new(bytes.Buffer)}, &a)
			if err != nil {
				t.Fatal(err)
			}
			if err := parser.Parse(test.args); err != nil {
				t.Fatalf("parse startup snapshot flag: %v", err)
			}
			if a.Snapshot != test.want {
				t.Fatalf("Snapshot = %q, want %q", a.Snapshot, test.want)
			}
		})
	}

	t.Setenv("ALMAGEST_SNAPSHOT", "/tmp/from-env.zip")
	var a Args
	parser, err := arg.NewParser(arg.Config{Out: new(bytes.Buffer)}, &a)
	if err != nil {
		t.Fatal(err)
	}
	if err := parser.Parse(nil); err != nil {
		t.Fatalf("parse ALMAGEST_SNAPSHOT: %v", err)
	}
	if a.Snapshot != "/tmp/from-env.zip" {
		t.Fatalf("Snapshot = %q, want environment value", a.Snapshot)
	}
}
