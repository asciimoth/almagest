//go:build windows

package platform

import (
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/asciimoth/configer/hujson"
	"github.com/asciimoth/gonnect/sysnet"
	sysnetwindows "github.com/asciimoth/sysnet-windows"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

func TestWindowsCapabilityCatalogPreservesUnsupportedPolicies(t *testing.T) {
	system, err := sysnetwindows.New(sysnetwindows.SystemConfig{})
	if err != nil {
		t.Fatalf("windows.New: %v", err)
	}
	t.Cleanup(func() {
		if err := system.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})

	report := system.Capabilities()
	unsupportedOperations := []sysnet.OperationKey{
		{Target: sysnet.TargetTun, Operation: sysnet.OpRename, Family: sysnet.FamilyNone},
		{Target: sysnet.TargetDefaultTun, Operation: sysnet.OpRename, Family: sysnet.FamilyNone},
		{Target: sysnet.TargetDefaultTun, Operation: sysnet.OpSourceRoutes, Family: sysnet.FamilyIPv4},
		{Target: sysnet.TargetOutNet, Operation: sysnet.OpMulticastUDP, Family: sysnet.FamilyIPv4},
		{Target: sysnet.TargetOutNet, Operation: sysnet.OpMulticastUDP, Family: sysnet.FamilyIPv6},
	}
	for _, key := range unsupportedOperations {
		if capability := report.Operation(key); capability.State != sysnet.CapabilityUnsupported {
			t.Errorf("operation %+v capability = %+v, want unsupported", key, capability)
		}
	}
	for _, key := range []sysnet.RoutingProfileKey{
		{Family: sysnet.FamilyIPv4, Mode: sysnet.RoutingInclude},
		{Family: sysnet.FamilyIPv4, Mode: sysnet.RoutingFull, Strict: true},
		{Family: sysnet.FamilyIPv4, Mode: sysnet.RoutingExclude, Strict: true},
	} {
		if capability := report.DefaultTunProfile(key).Capability; capability.State != sysnet.CapabilityUnsupported {
			t.Errorf("routing profile %+v capability = %+v, want unsupported", key, capability)
		}
	}

	unsupportedOptions := []sysnet.DefaultTunOpts{
		{Strict: true},
		{Include: []sysnet.Rule{{Type: "win-exe-tree", Rule: `C:\Program Files\Almagest\app.exe`}}},
		{SourceRoutes: []sysnet.TunSourceRoute{{
			Destination: netip.MustParsePrefix("0.0.0.0/0"),
			Source:      netip.MustParseAddr("198.18.0.1"),
		}}},
	}
	for _, opts := range unsupportedOptions {
		if err := system.CheckDefaultTunOpts(opts).Err(); !errors.Is(err, sysnet.ErrNotSupported) {
			t.Errorf("CheckDefaultTunOpts(%+v) error = %v, want ErrNotSupported", opts, err)
		}
	}

	exclude := sysnet.RoutingProfileKey{Family: sysnet.FamilyIPv4, Mode: sysnet.RoutingExclude}
	if err := system.CheckRule(
		sysnet.Rule{Type: "uid", Rule: "1000"},
		sysnet.RuleContext{Routing: &exclude},
	).Err(); !errors.Is(err, sysnet.ErrNotSupported) {
		t.Fatalf("Linux rule validation error = %v, want ErrNotSupported", err)
	}
}

func TestWindowsSplitCapabilityRemainsUnknownUntilExclusionAttempt(t *testing.T) {
	system, err := sysnetwindows.New(sysnetwindows.SystemConfig{})
	if err != nil {
		t.Fatalf("windows.New: %v", err)
	}
	t.Cleanup(func() {
		if err := system.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})

	profile := system.Capabilities().DefaultTunProfile(sysnet.RoutingProfileKey{
		Family: sysnet.FamilyIPv4,
		Mode:   sysnet.RoutingExclude,
	})
	for _, binding := range profile.Rules {
		if binding.Type != "win-exe-tree" {
			continue
		}
		if binding.Capability.State != sysnet.CapabilityUnknown {
			t.Fatalf("win-exe-tree binding capability = %+v, want unknown", binding.Capability)
		}
		if !hasCapabilityReason(binding.Capability, sysnet.ReasonProbeNotRun) {
			t.Fatalf("win-exe-tree binding reasons = %v, want probe_not_run", binding.Capability.Reasons)
		}
		return
	}
	t.Fatal("win-exe-tree binding is absent from the exclude profile")
}

func TestWindowsDisabledMatchersFailBeforeBuild(t *testing.T) {
	system, err := sysnetwindows.New(sysnetwindows.SystemConfig{
		Features: sysnetwindows.FeatureConfig{DisableMatchers: true},
	})
	if err != nil {
		t.Fatalf("windows.New: %v", err)
	}
	t.Cleanup(func() {
		if err := system.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})

	matcher := sysnet.MatcherProfileKey{Family: sysnet.FamilyIPv4, Transport: sysnet.TransportTCP}
	rule := sysnet.Rule{Type: "win-pid", Rule: "42"}
	if err := system.CheckRule(rule, sysnet.RuleContext{Matcher: &matcher}).Err(); !errors.Is(err, sysnet.ErrNotSupported) {
		t.Fatalf("CheckRule error = %v, want ErrNotSupported", err)
	}
	if built, err := system.BuildMatcher(rule); built != nil || !errors.Is(err, sysnet.ErrNotSupported) {
		t.Fatalf("BuildMatcher = %T, %v; want nil, ErrNotSupported", built, err)
	}
}

func hasCapabilityReason(capability sysnet.Capability, reason sysnet.CapabilityReason) bool {
	for _, candidate := range capability.Reasons {
		if candidate == reason {
			return true
		}
	}
	return false
}

func TestWindowsNativeSystemSelection(t *testing.T) {
	cfg, err := hujson.Parse([]byte(`{"Windows":{"Features":{"DisableIPv6":true}}}`))
	if err != nil {
		t.Fatal(err)
	}
	system, err := SystemIntegration(cfg, nil)
	if err != nil {
		t.Fatalf("SystemIntegration: %v", err)
	}
	if _, ok := system.(*sysnetwindows.System); !ok {
		t.Fatalf("SystemIntegration returned %T, want *windows.System", system)
	}
	capability := system.Capabilities().Operation(sysnet.OperationKey{
		Target: sysnet.TargetTun, Operation: sysnet.OpCreate, Family: sysnet.FamilyIPv6,
	})
	if capability.State != sysnet.CapabilityUnsupported {
		t.Fatalf("disabled IPv6 TUN capability = %#v, want unsupported", capability)
	}
	if err := system.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestWindowsStableGUIDRejectsConcurrentTunRequests(t *testing.T) {
	if !windows.GetCurrentProcessToken().IsElevated() {
		t.Skip("native Wintun mutation requires an elevated test identity")
	}
	guid, err := windows.GenerateGUID()
	if err != nil {
		t.Fatalf("GenerateGUID: %v", err)
	}
	system, err := sysnetwindows.New(sysnetwindows.SystemConfig{
		AdapterNamePrefix: "alm-test",
		StableGUID:        guid.String(),
	})
	if err != nil {
		t.Fatalf("windows.New: %v", err)
	}
	t.Cleanup(func() {
		if err := system.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})

	first, err := system.BuildTun(sysnet.TunOpts{
		TunAddrs: []string{"198.18.246.1/32"},
	})
	if err != nil {
		t.Fatalf("first BuildTun: %v", err)
	}
	if second, err := system.BuildTun(sysnet.TunOpts{
		TunAddrs: []string{"198.18.247.1/32"},
	}); err == nil {
		if second != nil {
			_ = second.Close()
		}
		t.Fatal("second BuildTun succeeded with the same live stable GUID")
	}
	if _, err := system.GetTunAddrs(first); err != nil {
		t.Fatalf("first TUN is not usable after the collision: %v", err)
	}
}

func TestWindowsOrdinaryTunsDoNotStartSplitDriver(t *testing.T) {
	if !windows.GetCurrentProcessToken().IsElevated() {
		t.Skip("native TUN mutation requires an elevated test identity")
	}
	assertSplitDriverStopped := func(phase string) {
		t.Helper()
		manager, err := mgr.Connect()
		if err != nil {
			t.Fatalf("%s: connect service manager: %v", phase, err)
		}
		defer manager.Disconnect()
		service, err := manager.OpenService("mullvad-split-tunnel")
		if err != nil {
			t.Fatalf("%s: open split-driver service: %v", phase, err)
		}
		defer service.Close()
		status, err := service.Query()
		if err != nil {
			t.Fatalf("%s: query split-driver service: %v", phase, err)
		}
		if status.State != svc.Stopped {
			t.Fatalf("%s: split-driver state = %v, want stopped", phase, status.State)
		}
	}
	assertSplitDriverStopped("before construction")
	system, err := sysnetwindows.New(sysnetwindows.SystemConfig{AdapterNamePrefix: "alm-nosplit"})
	if err != nil {
		t.Fatalf("windows.New: %v", err)
	}
	t.Cleanup(func() {
		if err := system.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	assertSplitDriverStopped("after construction")
	device, err := system.BuildTun(sysnet.TunOpts{TunAddrs: []string{"198.18.245.1/32"}})
	if err != nil {
		t.Fatalf("BuildTun without exclusions: %v", err)
	}
	if err := device.Close(); err != nil {
		t.Fatalf("close regular TUN: %v", err)
	}
	assertSplitDriverStopped("after regular TUN")
	defaultDevice, err := system.BuildDefaultTun(sysnet.DefaultTunOpts{TunAddrs: []string{"198.18.243.1/32"}})
	if err != nil {
		t.Fatalf("BuildDefaultTun without exclusions: %v", err)
	}
	if err := defaultDevice.Close(); err != nil {
		t.Fatalf("close default TUN: %v", err)
	}
	assertSplitDriverStopped("after default TUN")
}

func TestWindowsSplitDependencyRejectionDoesNotProvisionService(t *testing.T) {
	if !windows.GetCurrentProcessToken().IsElevated() {
		t.Skip("split-driver service fixtures require an elevated disposable guest")
	}
	const serviceName = "mullvad-split-tunnel"
	manager, err := mgr.Connect()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Disconnect() })
	service, err := manager.OpenService(serviceName)
	if err != nil {
		t.Fatalf("open provisioned split-driver service: %v", err)
	}
	original, err := service.Config()
	if err != nil {
		_ = service.Close()
		t.Fatalf("read provisioned split-driver config: %v", err)
	}
	status, err := service.Query()
	if err != nil {
		_ = service.Close()
		t.Fatalf("query provisioned split-driver: %v", err)
	}
	if status.State != svc.Stopped {
		_ = service.Close()
		t.Fatalf("provisioned split-driver state = %v, want stopped", status.State)
	}

	attemptExcludedDefaultTun := func(name string) {
		t.Helper()
		executable, pathErr := filepath.Abs(os.Args[0])
		if pathErr != nil {
			t.Fatalf("resolve test executable: %v", pathErr)
		}
		system, buildErr := sysnetwindows.New(sysnetwindows.SystemConfig{AdapterNamePrefix: name})
		if buildErr != nil {
			t.Fatalf("windows.New: %v", buildErr)
		}
		device, buildErr := system.BuildDefaultTun(sysnet.DefaultTunOpts{
			TunAddrs: []string{"198.18.244.1/32"},
			Exclude:  []sysnet.Rule{{Type: "win-exe-tree", Rule: executable}},
		})
		if device != nil {
			_ = device.Close()
		}
		closeErr := system.Close()
		if buildErr == nil {
			t.Fatalf("BuildDefaultTun with rejected split dependency succeeded; close error: %v", closeErr)
		}
		if closeErr != nil {
			t.Fatalf("close after rejected split dependency: %v", closeErr)
		}
	}

	t.Run("stopped", func(t *testing.T) {
		attemptExcludedDefaultTun("alm-split-stop")
		got, err := service.Query()
		if err != nil {
			t.Fatal(err)
		}
		if got.State != svc.Stopped {
			t.Fatalf("service state = %v, want stopped", got.State)
		}
	})

	t.Run("incompatible", func(t *testing.T) {
		incompatible := original
		incompatible.BinaryPathName = `C:\Windows\System32\drivers\incompatible-almagest-fixture.sys`
		if err := service.UpdateConfig(incompatible); err != nil {
			t.Fatalf("set incompatible service config: %v", err)
		}
		t.Cleanup(func() {
			if err := service.UpdateConfig(original); err != nil {
				t.Errorf("restore service config: %v", err)
			}
		})
		attemptExcludedDefaultTun("alm-split-bad")
		got, err := service.Config()
		if err != nil {
			t.Fatal(err)
		}
		if got.BinaryPathName != incompatible.BinaryPathName {
			t.Fatalf("service binary path = %q, want rejected fixture %q", got.BinaryPathName, incompatible.BinaryPathName)
		}
	})

	// Run the absent-service fixture last. Deletion is asynchronous, and the
	// cleanup recreates the exact provisioned service for diagnostics.
	if err := service.Delete(); err != nil {
		t.Fatalf("delete service fixture: %v", err)
	}
	if err := service.Close(); err != nil {
		t.Fatalf("close deleted service fixture: %v", err)
	}
	service = nil
	t.Cleanup(func() {
		restored, err := manager.CreateService(serviceName, original.BinaryPathName, original)
		if err != nil {
			t.Errorf("restore deleted split-driver service: %v", err)
			return
		}
		_ = restored.Close()
	})
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		candidate, openErr := manager.OpenService(serviceName)
		if openErr != nil {
			break
		}
		_ = candidate.Close()
		time.Sleep(100 * time.Millisecond)
	}
	if candidate, openErr := manager.OpenService(serviceName); openErr == nil {
		_ = candidate.Close()
		t.Fatal("split-driver service was not deleted for the absent fixture")
	}
	attemptExcludedDefaultTun("alm-split-none")
	if candidate, openErr := manager.OpenService(serviceName); openErr == nil {
		_ = candidate.Close()
		t.Fatal("backend recreated the absent split-driver service")
	}
}
