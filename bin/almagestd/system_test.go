package main

import (
	"testing"

	"github.com/asciimoth/gonnect/sysnet"
	sysnetdebug "github.com/asciimoth/gonnect/sysnet/debug"
)

func TestBuildDebugIntegrationOptions(t *testing.T) {
	cfg, err := ParseConfig([]byte(`{
		"System": {
			"Debug": {
				"DisableTun": true,
				"DisableDefaultTun": true,
				"DisableDynTun": true,
				"DisableDynDefaultTun": true,
				"DisableTunNames": true,
				"DisableDefaultTunNames": true,
				"DisableStrictMode": true,
				"DefaultTun": {
					"Builder": "pipe"
				}
			}
		}
	}`), []byte(`{}`))
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	system, err := BuildDebugIntegration(cfg)
	if err != nil {
		t.Fatalf("BuildDebugIntegration: %v", err)
	}
	debugSystem, ok := system.(*sysnetdebug.System)
	if !ok {
		t.Fatalf("debug system = %T, want *sysnetdebug.System", system)
	}
	features := debugSystem.Features()
	if features.Tun || features.DefaultTun || features.DynTun || features.DynDefaultTun ||
		features.TunNames || features.DefaultTunNames || features.StrictMode {
		t.Fatalf("features = %#v, want all debug features disabled", features)
	}
	if !debugSystem.LocalNet().IsNative() {
		t.Fatal("debug LocalNet IsNative = false, want native local network")
	}
	if debugSystem.DefaultTunBuilder != nil {
		t.Fatal("pipe debug DefaultTun builder configured custom builder")
	}
}

func TestBuildDebugIntegrationVTunSocksDefaultTunBuilder(t *testing.T) {
	cfg, err := ParseConfig([]byte(`{
		"System": {
			"Debug": {
				"DefaultTun": {
					"SocksPort": 0,
					"Name": "test-debug-default-tun"
				}
			}
		}
	}`), []byte(`{}`))
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	system, err := BuildDebugIntegration(cfg)
	if err != nil {
		t.Fatalf("BuildDebugIntegration: %v", err)
	}
	debugSystem := system.(*sysnetdebug.System)
	if debugSystem.DefaultTunBuilder == nil {
		t.Fatal("DefaultTunBuilder = nil, want vtun-socks builder")
	}
	dtun, err := debugSystem.BuildDefaultTun(sysnet.DefaultTunOpts{TunAddrs: []string{"10.213.0.2/32"}})
	if err != nil {
		t.Fatalf("BuildDefaultTun: %v", err)
	}
	if dtun == nil {
		t.Fatal("BuildDefaultTun returned nil tun")
	}
	if err := debugSystem.Close(); err != nil {
		t.Fatalf("debug system close: %v", err)
	}
}
