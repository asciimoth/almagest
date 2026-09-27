package main

import (
	"net"
	"testing"

	"github.com/asciimoth/gonnect/sysnet"
	sysnetdebug "github.com/asciimoth/gonnect/sysnet/debug"
)

func TestBuildDebugIntegrationOptions(t *testing.T) {
	cfg, err := ParseConfig([]byte(`{}`), []byte(`{
		"System": {
			"Debug": {
				"DisableTun": true,
				"DisableDefaultTun": true,
				"DisableDynTun": true,
				"DisableDynDefaultTun": true,
				"DisableTunNames": true,
				"DisableDefaultTunNames": true,
				"DisableStrictMode": true,
				"DisableDefaultTunSourceRoutes": true,
				"DefaultTun": {
					"Builder": "pipe"
				}
			}
		}
	}`))
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
	report := debugSystem.Capabilities()
	checks := []sysnet.OperationKey{
		{Target: sysnet.TargetTun, Operation: sysnet.OpCreate, Family: sysnet.FamilyIPv4},
		{Target: sysnet.TargetTun, Operation: sysnet.OpCreateNamed, Family: sysnet.FamilyIPv4},
		{Target: sysnet.TargetTun, Operation: sysnet.OpSetMTU, Family: sysnet.FamilyNone},
		{Target: sysnet.TargetDefaultTun, Operation: sysnet.OpCreate, Family: sysnet.FamilyIPv4},
		{Target: sysnet.TargetDefaultTun, Operation: sysnet.OpCreateNamed, Family: sysnet.FamilyIPv4},
		{Target: sysnet.TargetDefaultTun, Operation: sysnet.OpSetMTU, Family: sysnet.FamilyNone},
		{Target: sysnet.TargetDefaultTun, Operation: sysnet.OpSourceRoutes, Family: sysnet.FamilyIPv4},
	}
	for _, key := range checks {
		if capability := report.Operation(key); capability.State != sysnet.CapabilityUnsupported {
			t.Fatalf("capability %+v = %#v, want unsupported", key, capability)
		}
	}
	strict := report.DefaultTunProfile(sysnet.RoutingProfileKey{
		Family: sysnet.FamilyIPv4,
		Mode:   sysnet.RoutingFull,
		Strict: true,
	})
	if strict.State != sysnet.CapabilityUnsupported {
		t.Fatalf("strict routing capability = %#v, want unsupported", strict.Capability)
	}
	if !debugSystem.LocalNet().IsNative() {
		t.Fatal("debug LocalNet IsNative = false, want native local network")
	}
	if debugSystem.DefaultTunBuilder != nil {
		t.Fatal("pipe debug DefaultTun builder configured custom builder")
	}
}

func TestSystemIntegrationUsesProtectedSystemType(t *testing.T) {
	cfg, err := ParseConfig([]byte(`{}`), []byte(`{
		"System": {
			"Type": "debug",
			"Debug": {
				"DefaultTun": {
					"Builder": "pipe"
				}
			}
		}
	}`))
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}

	system, err := SystemIntegration(cfg, nil)
	if err != nil {
		t.Fatalf("SystemIntegration: %v", err)
	}
	if _, ok := system.(*sysnetdebug.System); !ok {
		t.Fatalf("system = %T, want *sysnetdebug.System", system)
	}
}

func TestBuildDebugIntegrationRandomSeedControlsAllocator(t *testing.T) {
	firstSeed := int64(42)
	secondSeed := int64(43)
	first := firstDebugIntegrationSubnet(t, &firstSeed)
	if got := firstDebugIntegrationSubnet(t, &firstSeed); !got.IP.Equal(first.IP) {
		t.Fatalf("same seed allocated subnet %v, want %v", got, first)
	}
	if got := firstDebugIntegrationSubnet(t, &secondSeed); got.IP.Equal(first.IP) {
		t.Fatalf("different seeds allocated the same subnet %v", got)
	}

	defaultSubnet := firstDebugIntegrationSubnet(t, nil)
	if got := firstDebugIntegrationSubnet(t, nil); !got.IP.Equal(defaultSubnet.IP) {
		t.Fatalf("default seeds allocated subnets %v and %v, want equal subnets", defaultSubnet, got)
	}
}

func firstDebugIntegrationSubnet(t *testing.T, seed *int64) *net.IPNet {
	t.Helper()
	cfg, err := ParseConfig([]byte(`{}`), []byte(`{
		"System": {
			"Debug": {
				"DefaultTun": {"Builder": "pipe"}
			}
		}
	}`))
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	var system sysnet.System
	if seed == nil {
		system, err = BuildDebugIntegration(cfg)
	} else {
		system, err = BuildDebugIntegration(cfg, *seed)
	}
	if err != nil {
		t.Fatalf("BuildDebugIntegration: %v", err)
	}
	t.Cleanup(func() {
		if err := system.Close(); err != nil {
			t.Errorf("system close: %v", err)
		}
	})
	subnet := system.AllocSubnet().AllocSubnet4(24)
	if subnet == nil {
		t.Fatal("AllocSubnet4 returned nil")
	}
	return subnet
}

func TestBuildDebugIntegrationVTunSocksDefaultTunBuilder(t *testing.T) {
	cfg, err := ParseConfig([]byte(`{}`), []byte(`{
		"System": {
			"Debug": {
				"DefaultTun": {
					"SocksPort": 0,
					"Name": "test-debug-default-tun"
				}
			}
		}
	}`))
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

func TestDebugIntegrationIgnoresMainSystemConfig(t *testing.T) {
	cfg, err := ParseConfig([]byte(`{
		"System": {
			"Debug": {
				"DisableTun": true,
				"DefaultTun": {
					"Builder": "pipe"
				}
			}
		}
	}`), []byte(`{}`))
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}

	got := readDebugSystemConfig(cfg)
	if got.DisableTun {
		t.Fatal("readDebugSystemConfig used main System config; want protected-only config")
	}
	if got.DefaultTun.Builder != debugDefaultTunBuilderVTunSocks {
		t.Fatalf("DefaultTun.Builder = %q, want protected default %q", got.DefaultTun.Builder, debugDefaultTunBuilderVTunSocks)
	}
}
