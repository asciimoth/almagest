package main

import (
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/asciimoth/almagest/bin/almagestd/daemonservices/localnetwork"
	"github.com/asciimoth/gonnect/sysnet"
	sysnetdebug "github.com/asciimoth/gonnect/sysnet/debug"
)

func TestBuildDebugIntegrationNativeOutNetworkReachesLocalNetwork(t *testing.T) {
	cfg, err := ParseConfig([]byte(`{}`), []byte(`{
		"System": {"Type": "debug", "Debug": {"NativeOutNetwork": true}}
	}`))
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	system, err := localnetwork.BuildDebug(cfg.Protected.System)
	if err != nil {
		t.Fatalf("BuildDebug: %v", err)
	}
	t.Cleanup(func() {
		if err := system.Close(); err != nil {
			t.Errorf("system close: %v", err)
		}
	})
	if !system.OutNet().IsNative() {
		t.Fatal("debug OutNet IsNative = false with NativeOutNetwork enabled")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	listener, err := system.LocalNet().Listen(ctx, "tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("LocalNet listen: %v", err)
	}
	defer listener.Close() //nolint:errcheck
	accepted := make(chan error, 1)
	go func() {
		connection, acceptErr := listener.Accept()
		if acceptErr == nil {
			_, acceptErr = connection.Write([]byte("ok"))
			_ = connection.Close()
		}
		accepted <- acceptErr
	}()
	connection, err := system.OutNet().Dial(ctx, "tcp4", listener.Addr().String())
	if err != nil {
		t.Fatalf("OutNet dial to LocalNet listener: %v", err)
	}
	response, err := io.ReadAll(connection)
	_ = connection.Close()
	if err != nil || string(response) != "ok" {
		t.Fatalf("OutNet response = %q, %v; want ok", response, err)
	}
	if err := <-accepted; err != nil {
		t.Fatalf("LocalNet accept: %v", err)
	}
}

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
	system, err := localnetwork.BuildDebug(cfg.Protected.System)
	if err != nil {
		t.Fatalf("BuildDebug: %v", err)
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
	if debugSystem.OutNet().IsNative() {
		t.Fatal("debug OutNet IsNative = true without NativeOutNetwork")
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

	system, err := localnetwork.Build(cfg.Protected.System, nil)
	if err != nil {
		t.Fatalf("localnetwork.Build: %v", err)
	}
	if _, ok := system.(*sysnetdebug.System); !ok {
		t.Fatalf("system = %T, want *sysnetdebug.System", system)
	}
}

func TestLocalNetworkRejectsUnknownSystemType(t *testing.T) {
	cfg, err := ParseConfig([]byte(`{}`), []byte(`{
		"System": {"Type": "unknown"}
	}`))
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}

	system, err := localnetwork.Build(cfg.Protected.System, nil)
	if system != nil {
		_ = system.Close()
		t.Fatalf("system = %T, want nil", system)
	}
	if !errors.Is(err, localnetwork.ErrSystemType) {
		t.Fatalf("error = %v, want ErrSystemType", err)
	}
}

func TestLocalNetworkRejectsUnknownDebugDefaultTunBuilder(t *testing.T) {
	cfg, err := ParseConfig([]byte(`{}`), []byte(`{
		"System": {"Debug": {"DefaultTun": {"Builder": "unknown"}}}
	}`))
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}

	system, err := localnetwork.BuildDebug(cfg.Protected.System)
	if system != nil {
		_ = system.Close()
		t.Fatalf("system = %T, want nil", system)
	}
	if err == nil || !strings.Contains(err.Error(), "unknown") {
		t.Fatalf("error = %v, want unknown builder error", err)
	}
}

func TestDebugSystemIntegrationDoesNotRequireNativeDependencies(t *testing.T) {
	cfg, err := ParseConfig([]byte(`{}`), []byte(`{
		"System": {"Type": "debug", "Debug": {"DefaultTun": {"Builder": "pipe"}}}
	}`))
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	want := errors.New("native dependency unavailable")
	system, err := localnetwork.BuildWithRuntimeDependencies(cfg.Protected.System, nil, nil, want)
	if err != nil {
		t.Fatalf("BuildWithRuntimeDependencies: %v", err)
	}
	if _, ok := system.(*sysnetdebug.System); !ok {
		t.Fatalf("system = %T, want *sysnetdebug.System", system)
	}
}

func TestNativeSystemIntegrationPreservesBeforeChdirDependencyError(t *testing.T) {
	cfg, err := ParseConfig([]byte(`{}`), []byte(`{"System":{"Type":"native"}}`))
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	want := errors.New("wintun.dll is absent")
	system, err := localnetwork.BuildWithRuntimeDependencies(cfg.Protected.System, nil, nil, want)
	if system != nil {
		_ = system.Close()
		t.Fatalf("system = %T, want nil", system)
	}
	if !errors.Is(err, want) {
		t.Fatalf("error = %v, want wrapped dependency error", err)
	}
	if !strings.Contains(err.Error(), "before changing directory") {
		t.Fatalf("error = %q, want dependency validation phase", err)
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
		system, err = localnetwork.BuildDebug(cfg.Protected.System)
	} else {
		system, err = localnetwork.BuildDebug(cfg.Protected.System, *seed)
	}
	if err != nil {
		t.Fatalf("BuildDebug: %v", err)
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
	system, err := localnetwork.BuildDebug(cfg.Protected.System)
	if err != nil {
		t.Fatalf("BuildDebug: %v", err)
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

	system, err := localnetwork.BuildDebug(cfg.Protected.System)
	if err != nil {
		t.Fatalf("BuildDebug: %v", err)
	}
	debugSystem := system.(*sysnetdebug.System)
	if capability := debugSystem.Capabilities().Operation(sysnet.OperationKey{
		Target: sysnet.TargetTun, Operation: sysnet.OpCreate, Family: sysnet.FamilyIPv4,
	}); capability.State == sysnet.CapabilityUnsupported {
		t.Fatal("BuildDebug used regular System config; want protected-only defaults")
	}
	if debugSystem.DefaultTunBuilder == nil {
		t.Fatal("protected default DefaultTun builder is nil")
	}
	_ = system.Close()
}
