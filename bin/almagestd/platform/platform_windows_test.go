//go:build windows

package platform

import (
	"testing"

	"github.com/asciimoth/configer/hujson"
	"github.com/asciimoth/gonnect/sysnet"
	sysnetwindows "github.com/asciimoth/sysnet-windows"
	"golang.org/x/sys/windows"
)

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
