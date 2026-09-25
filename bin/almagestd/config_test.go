package main

import (
	"errors"
	"github.com/asciimoth/almagest/lib/storage"
	"testing"
	"time"

	"github.com/asciimoth/almagest/bin/almagestd/args"
)

func TestConfigSaveIdleDefault(t *testing.T) {
	cfg, err := ParseConfig([]byte(`{}`), []byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.SaveIdle(); got != 500*time.Millisecond {
		t.Fatalf("SaveIdle() = %v, want 500ms", got)
	}
}

func TestConfigSaveIdleConfigured(t *testing.T) {
	cfg, err := ParseConfig([]byte(`{"Config":{"Save":{"IdleMilliseconds":1250}}}`), []byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.SaveIdle(); got != 1250*time.Millisecond {
		t.Fatalf("SaveIdle() = %v, want 1250ms", got)
	}
}

func TestConfigSaveIdleNonPositiveUsesDefault(t *testing.T) {
	cfg, err := ParseConfig([]byte(`{"Config":{"Save":{"IdleMilliseconds":0}}}`), []byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.SaveIdle(); got != 500*time.Millisecond {
		t.Fatalf("SaveIdle() = %v, want 500ms", got)
	}
}

func TestTLSBuiltinCAOptionsDefaultAndConfigured(t *testing.T) {
	cfg, err := ParseConfig([]byte(`{}`), []byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	defaults, err := cfg.TLSBuiltinCAOptions()
	if err != nil {
		t.Fatal(err)
	}
	if defaults.Issuer != "Almagest" || defaults.ValidityDays != 0 || defaults.KeyAlgorithm != "ecdsa" || defaults.Curve != "P256" {
		t.Fatalf("default TLS builtin CA options = %#v, want Almagest no-expiration ECDSA P256", defaults)
	}

	cfg, err = ParseConfig([]byte(`{
		"Config": {
			"TLS": {
				"BuiltinCA": {
					"Issuer": "Lab CA",
					"ValidityDays": 30,
					"KeyAlgorithm": "rsa",
					"KeyBits": 4096
				}
			}
		}
	}`), []byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	options, err := cfg.TLSBuiltinCAOptions()
	if err != nil {
		t.Fatal(err)
	}
	if options.Issuer != "Lab CA" || options.ValidityDays != 30 || options.KeyAlgorithm != "rsa" || options.KeyBits != 4096 || options.Curve != "" {
		t.Fatalf("configured TLS builtin CA options = %#v, want RSA config", options)
	}
}

func TestTLSBuiltinCAOptionsInvalid(t *testing.T) {
	cfg, err := ParseConfig([]byte(`{
		"Config": {
			"TLS": {
				"BuiltinCA": {
					"ValidityDays": -1
				}
			}
		}
	}`), []byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cfg.TLSBuiltinCAOptions(); !errors.Is(err, storage.ErrTlsConfigInvalidValue) {
		t.Fatalf("TLSBuiltinCAOptions error = %v, want invalid value", err)
	}
}

func TestLogCacheConfigured(t *testing.T) {
	cfg, err := ParseConfig([]byte(`{
		"Logs": {
			"Cache": {
				"Debug": 1001,
				"Info": 1002,
				"Warn": 1003,
				"Error": 1004,
				"Fatal": 1005
			}
		}
	}`), []byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	want := LogCacheConfig{
		Debug: 1001,
		Info:  1002,
		Warn:  1003,
		Error: 1004,
		Fatal: 1005,
	}
	if got := cfg.LogCache(); got != want {
		t.Fatalf("LogCache() = %#v, want %#v", got, want)
	}
}

func TestLogCacheMergeArgsOverridesConfig(t *testing.T) {
	cfg, err := ParseConfig([]byte(`{
		"Logs": {
			"Cache": {
				"Debug": 1001,
				"Info": 1002,
				"Warn": 1003,
				"Error": 1004,
				"Fatal": 1005
			}
		}
	}`), []byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	debug := 2001
	warn := -1
	fatal := 2005
	got := cfg.LogCache().MergeArgs(&args.Args{
		LogCacheDebug: &debug,
		LogCacheWarn:  &warn,
		LogCacheFatal: &fatal,
	})
	want := LogCacheConfig{
		Debug: 2001,
		Info:  1002,
		Warn:  0,
		Error: 1004,
		Fatal: 2005,
	}
	if got != want {
		t.Fatalf("LogCache().MergeArgs() = %#v, want %#v", got, want)
	}
}

func TestProtectedConfigWebFilters(t *testing.T) {
	cfg, err := ParseConfig([]byte(`{}`), []byte(`{
		"Admin": {
			"Web": 8080,
			"WebPProf": true,
			"AllowCrossOrigin": true,
			"WebFilters": {
				"UIDs": [1000],
				"GIDs": [1001],
				"Usernames": ["alice"]
			}
		}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Protected.Admin.Web; got != 8080 {
		t.Fatalf("Admin.Web = %d, want 8080", got)
	}
	if !cfg.Protected.Admin.WebPProf {
		t.Fatal("Admin.WebPProf = false, want true")
	}
	if !cfg.Protected.Admin.AllowCrossOrigin {
		t.Fatal("Admin.AllowCrossOrigin = false, want true")
	}
	if got := cfg.Protected.Admin.WebFilters.UIDs; len(got) != 1 || got[0] != 1000 {
		t.Fatalf("Admin.WebFilters.UIDs = %v, want [1000]", got)
	}
	if got := cfg.Protected.Admin.WebFilters.GIDs; len(got) != 1 || got[0] != 1001 {
		t.Fatalf("Admin.WebFilters.GIDs = %v, want [1001]", got)
	}
	if got := cfg.Protected.Admin.WebFilters.Usernames; len(got) != 1 || got[0] != "alice" {
		t.Fatalf("Admin.WebFilters.Usernames = %v, want [alice]", got)
	}
	if !cfg.Protected.Admin.WebFilters.Enabled() {
		t.Fatal("Admin.WebFilters.Enabled() = false, want true")
	}
}

func TestProtectedConfigUnixFilters(t *testing.T) {
	cfg, err := ParseConfig([]byte(`{}`), []byte(`{
		"Admin": {
			"Unix": "/tmp/almagest-admin.sock",
			"UnixFilters": {
				"UIDs": [1000],
				"GIDs": [1001],
				"Usernames": ["alice"]
			}
		}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Protected.Admin.Unix; got != "/tmp/almagest-admin.sock" {
		t.Fatalf("Admin.Unix = %q, want /tmp/almagest-admin.sock", got)
	}
	if got := cfg.Protected.Admin.UnixFilters.UIDs; len(got) != 1 || got[0] != 1000 {
		t.Fatalf("Admin.UnixFilters.UIDs = %v, want [1000]", got)
	}
	if got := cfg.Protected.Admin.UnixFilters.GIDs; len(got) != 1 || got[0] != 1001 {
		t.Fatalf("Admin.UnixFilters.GIDs = %v, want [1001]", got)
	}
	if got := cfg.Protected.Admin.UnixFilters.Usernames; len(got) != 1 || got[0] != "alice" {
		t.Fatalf("Admin.UnixFilters.Usernames = %v, want [alice]", got)
	}
	if !cfg.Protected.Admin.UnixFilters.Enabled() {
		t.Fatal("Admin.UnixFilters.Enabled() = false, want true")
	}
}

func TestProtectedConfigSystemView(t *testing.T) {
	cfg, err := ParseConfig([]byte(`{}`), []byte(`{
		"System": {
			"Type": "debug",
			"BackendOwned": {
				"Dynamic": "value"
			}
		}
	}`))
	if err != nil {
		t.Fatal(err)
	}

	typ, err := cfg.Protected.System.Get([]string{"Type"})
	if err != nil {
		t.Fatalf("Protected.System.Get(Type): %v", err)
	}
	if typ != "debug" {
		t.Fatalf("Protected.System.Type = %v, want debug", typ)
	}

	dynamic, err := cfg.Protected.System.Get([]string{"BackendOwned", "Dynamic"})
	if err != nil {
		t.Fatalf("Protected.System.Get(BackendOwned.Dynamic): %v", err)
	}
	if dynamic != "value" {
		t.Fatalf("Protected.System.BackendOwned.Dynamic = %v, want value", dynamic)
	}
}

func TestProtectedConfigExperimentalTorDriver(t *testing.T) {
	cfg, err := ParseConfig([]byte(`{}`), []byte(`{
		"Experimental": {
			"TorDriver": {
				"TorExecutable": "/opt/tor/bin/tor",
				"Obfs4Executable": "/opt/tor/bin/lyrebird",
				"TempRoot": "/run/almagest/tor",
				"StateRoot": "/var/lib/almagest/tor",
				"Sandbox": "dynamic",
				"StartupTimeout": "31s",
				"CommandTimeout": "11s",
				"DialTimeout": "91s",
				"ShutdownTimeout": "12s",
				"MaxCircuitDirtiness": "11m",
				"CircuitBuildTimeout": "45s",
				"Identity": {"UID": 2001, "GID": 2002}
			}
		}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	host := cfg.Protected.Experimental.TorDriver
	if host.TorExecutable != "/opt/tor/bin/tor" || host.Obfs4Executable != "/opt/tor/bin/lyrebird" ||
		host.TempRoot != "/run/almagest/tor" || host.StateRoot != "/var/lib/almagest/tor" || host.Sandbox != "dynamic" ||
		host.StartupTimeout != "31s" || host.CommandTimeout != "11s" || host.DialTimeout != "91s" ||
		host.ShutdownTimeout != "12s" || host.MaxCircuitDirtiness != "11m" || host.CircuitBuildTimeout != "45s" {
		t.Fatalf("experimental Tor host config = %#v", host)
	}
	if host.Identity == nil || host.Identity.UID != 2001 || host.Identity.GID != 2002 {
		t.Fatalf("experimental Tor identity = %#v", host.Identity)
	}

	defaults, err := ParseConfig([]byte(`{}`), []byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := defaults.Protected.Experimental.TorDriver; got.TorExecutable != "" || got.Obfs4Executable != "" || got.Sandbox != "" ||
		got.StartupTimeout != "" || got.CommandTimeout != "" || got.DialTimeout != "" || got.ShutdownTimeout != "" ||
		got.MaxCircuitDirtiness != "" || got.CircuitBuildTimeout != "" || got.Identity != nil {
		t.Fatalf("default experimental Tor config = %#v, want PATH and automatic identity", got)
	}
}

func TestProtectedConfigCommandRunner(t *testing.T) {
	cfg, err := ParseConfig([]byte(`{}`), []byte(`{
		"CommandRunner": {
			"Commands": {
				"notify-dns": {
					"Command": ["/usr/local/bin/notify-dns", "${server}"],
					"Env": {"ALMAGEST_CALLBACK": "dns"},
					"UID": 1001,
					"GID": 1002,
					"Sync": true,
					"Timeout": 250000000,
					"AllowNodes": true,
					"Slots": {
						"server": {
							"MaxLength": 64,
							"Pattern": "[0-9.]+",
							"Required": true
						}
					}
				}
			}
		}
	}`))
	if err != nil {
		t.Fatal(err)
	}

	cmd := cfg.Protected.CommandRunner.Commands["notify-dns"]
	if len(cmd.Command) != 2 || cmd.Command[1] != "${server}" {
		t.Fatalf("CommandRunner.Commands.notify-dns.Command = %#v, want configured argv", cmd.Command)
	}
	if got := cmd.Env["ALMAGEST_CALLBACK"]; got != "dns" {
		t.Fatalf("CommandRunner env ALMAGEST_CALLBACK = %q, want dns", got)
	}
	if cmd.UID == nil || *cmd.UID != 1001 {
		t.Fatalf("CommandRunner UID = %v, want 1001", cmd.UID)
	}
	if cmd.GID == nil || *cmd.GID != 1002 {
		t.Fatalf("CommandRunner GID = %v, want 1002", cmd.GID)
	}
	if !cmd.Sync {
		t.Fatal("CommandRunner Sync = false, want true")
	}
	if got := cmd.Timeout; got != 250*time.Millisecond {
		t.Fatalf("CommandRunner Timeout = %s, want 250ms", got)
	}
	if !cmd.AllowNodes {
		t.Fatal("CommandRunner AllowNodes = false, want true")
	}
	slot := cmd.Slots["server"]
	if slot.MaxLength != 64 || slot.Pattern != "[0-9.]+" || !slot.Required {
		t.Fatalf("CommandRunner slot = %#v, want configured limits", slot)
	}
}

func TestProtectedConfigSecrets(t *testing.T) {
	t.Setenv("ALMAGEST_TEST_SECRET", "env-value")
	empty := ""
	cfg, err := ParseConfig([]byte(`{}`), []byte(`{
		"Secrets": {
			"literal": {
				"Value": "literal-value",
				"Nodes": ["client"]
			},
			"empty": {
				"Value": "",
				"Nodes": ["client"]
			},
			"from-env": {
				"Env": "ALMAGEST_TEST_SECRET",
				"Nodes": ["client", "client"]
			}
		}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Protected.Secrets["empty"].Value == nil || *cfg.Protected.Secrets["empty"].Value != empty {
		t.Fatalf("empty protected secret value = %#v, want explicit empty string", cfg.Protected.Secrets["empty"].Value)
	}
	secrets, err := cfg.Protected.ImmutableSecrets()
	if err != nil {
		t.Fatal(err)
	}
	if got := secrets["literal"].Value; got != "literal-value" {
		t.Fatalf("literal secret value = %q, want literal-value", got)
	}
	if got := secrets["empty"].Value; got != "" {
		t.Fatalf("empty secret value = %q, want empty string", got)
	}
	if got := secrets["from-env"].Value; got != "env-value" {
		t.Fatalf("env secret value = %q, want env-value", got)
	}
	if got := secrets["from-env"].Nodes; len(got) != 1 || got[0] != "client" {
		t.Fatalf("env secret nodes = %v, want normalized [client]", got)
	}
}

func TestProtectedConfigSecretMissingEnvErrors(t *testing.T) {
	cfg, err := ParseConfig([]byte(`{}`), []byte(`{
		"Secrets": {
			"missing": {"Env": "ALMAGEST_DOES_NOT_EXIST"}
		}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cfg.Protected.ImmutableSecrets(); !errors.Is(err, storage.ErrSecretInvalidValue) {
		t.Fatalf("ImmutableSecrets missing env error = %v, want ErrSecretInvalidValue", err)
	}
}

func TestProtectedConfigMergeWebFilterArgs(t *testing.T) {
	cfg, err := ParseConfig([]byte(`{}`), []byte(`{
		"Admin": {
			"Web": 8080,
			"WebFilters": {
				"UIDs": [1000],
				"GIDs": [1001],
				"Usernames": ["alice"]
			}
		}
	}`))
	if err != nil {
		t.Fatal(err)
	}

	web := uint16(9090)
	cfg.Protected.Merge(&args.Args{
		WebAdmin:                 &web,
		WebAdminFilterUIDs:       []uint32{2000, 2001},
		WebAdminFilterUsernames:  []string{"bob", "carol"},
		WebAdminPProf:            true,
		WebAdminAllowCrossOrigin: true,
	})

	if got := cfg.Protected.Admin.Web; got != 9090 {
		t.Fatalf("Admin.Web = %d, want 9090", got)
	}
	if !cfg.Protected.Admin.WebPProf {
		t.Fatal("Admin.WebPProf = false, want true")
	}
	if !cfg.Protected.Admin.AllowCrossOrigin {
		t.Fatal("Admin.AllowCrossOrigin = false, want true")
	}
	if got := cfg.Protected.Admin.WebFilters.UIDs; len(got) != 2 || got[0] != 2000 || got[1] != 2001 {
		t.Fatalf("Admin.WebFilters.UIDs = %v, want [2000 2001]", got)
	}
	if got := cfg.Protected.Admin.WebFilters.GIDs; len(got) != 1 || got[0] != 1001 {
		t.Fatalf("Admin.WebFilters.GIDs = %v, want config value [1001]", got)
	}
	if got := cfg.Protected.Admin.WebFilters.Usernames; len(got) != 2 || got[0] != "bob" || got[1] != "carol" {
		t.Fatalf("Admin.WebFilters.Usernames = %v, want [bob carol]", got)
	}
}

func TestProtectedConfigMergeUnixFilterArgs(t *testing.T) {
	cfg, err := ParseConfig([]byte(`{}`), []byte(`{
		"Admin": {
			"Unix": "/tmp/old.sock",
			"UnixFilters": {
				"UIDs": [1000],
				"GIDs": [1001],
				"Usernames": ["alice"]
			}
		}
	}`))
	if err != nil {
		t.Fatal(err)
	}

	unixSocket := "/tmp/new.sock"
	cfg.Protected.Merge(&args.Args{
		UnixAdmin:                &unixSocket,
		UnixAdminFilterUIDs:      []uint32{2000, 2001},
		UnixAdminFilterUsernames: []string{"bob", "carol"},
	})

	if got := cfg.Protected.Admin.Unix; got != "/tmp/new.sock" {
		t.Fatalf("Admin.Unix = %q, want /tmp/new.sock", got)
	}
	if got := cfg.Protected.Admin.UnixFilters.UIDs; len(got) != 2 || got[0] != 2000 || got[1] != 2001 {
		t.Fatalf("Admin.UnixFilters.UIDs = %v, want [2000 2001]", got)
	}
	if got := cfg.Protected.Admin.UnixFilters.GIDs; len(got) != 1 || got[0] != 1001 {
		t.Fatalf("Admin.UnixFilters.GIDs = %v, want config value [1001]", got)
	}
	if got := cfg.Protected.Admin.UnixFilters.Usernames; len(got) != 2 || got[0] != "bob" || got[1] != "carol" {
		t.Fatalf("Admin.UnixFilters.Usernames = %v, want [bob carol]", got)
	}
}
