//go:build windows

package platform

import (
	"errors"
	"net/netip"
	"testing"

	"github.com/asciimoth/configer/hujson"
	"github.com/asciimoth/gonnect/sysnet"
	sysnetwindows "github.com/asciimoth/sysnet-windows"
	"golang.org/x/sys/windows"
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
