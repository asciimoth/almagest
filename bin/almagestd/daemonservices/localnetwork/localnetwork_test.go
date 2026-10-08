package localnetwork

import (
	"errors"
	"strings"
	"testing"

	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/gonnect/sysnet"
	sysnetdebug "github.com/asciimoth/gonnect/sysnet/debug"
)

func TestBuildDebugReturnsOriginalNativeNetworks(t *testing.T) {
	config := configer.NewMemory(map[string]any{
		"Debug": map[string]any{
			"NativeOutNetwork": true,
			"DefaultTun": map[string]any{
				"Builder": debugDefaultTunBuilderPipe,
			},
		},
	})

	system, err := BuildDebug(config)
	if err != nil {
		t.Fatalf("BuildDebug: %v", err)
	}
	t.Cleanup(func() {
		if err := system.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	if _, ok := system.(*sysnetdebug.System); !ok {
		t.Fatalf("system = %T, want original *debug.System", system)
	}
	if !system.OutNet().IsNative() {
		t.Fatal("OutNet IsNative = false, want true")
	}
	if !system.LocalNet().IsNative() {
		t.Fatal("LocalNet IsNative = false, want true")
	}
}

func TestBuildRejectsUnknownSystemType(t *testing.T) {
	system, err := Build(configer.NewMemory(map[string]any{"Type": "unknown"}), nil)
	if system != nil {
		_ = system.Close()
		t.Fatalf("system = %T, want nil", system)
	}
	if !errors.Is(err, ErrSystemType) {
		t.Fatalf("error = %v, want ErrSystemType", err)
	}
}

func TestBuildDebugRejectsUnknownDefaultTunBuilder(t *testing.T) {
	config := configer.NewMemory(map[string]any{
		"Debug": map[string]any{
			"DefaultTun": map[string]any{"Builder": "unknown"},
		},
	})
	system, err := BuildDebug(config)
	if system != nil {
		_ = system.Close()
		t.Fatalf("system = %T, want nil", system)
	}
	if err == nil || !strings.Contains(err.Error(), "unknown") {
		t.Fatalf("error = %v, want unknown builder error", err)
	}
}

func TestBuildDebugIgnoresNativeDependencyFailure(t *testing.T) {
	config := configer.NewMemory(map[string]any{
		"Type": "debug",
		"Debug": map[string]any{
			"DefaultTun": map[string]any{"Builder": debugDefaultTunBuilderPipe},
		},
	})
	want := errors.New("native dependency unavailable")
	system, err := BuildWithRuntimeDependencies(config, nil, want)
	if err != nil {
		t.Fatalf("BuildWithRuntimeDependencies: %v", err)
	}
	if _, ok := system.(*sysnetdebug.System); !ok {
		t.Fatalf("system = %T, want *debug.System", system)
	}
	if err := system.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestBuildDebugPreservesTunCapabilities(t *testing.T) {
	system, err := BuildDebug(configer.NewMemory(map[string]any{
		"Debug": map[string]any{
			"DefaultTun": map[string]any{"Builder": debugDefaultTunBuilderPipe},
		},
	}))
	if err != nil {
		t.Fatalf("BuildDebug: %v", err)
	}
	defer system.Close() //nolint:errcheck

	capability := system.Capabilities().Operation(sysnet.OperationKey{
		Target: sysnet.TargetTun, Operation: sysnet.OpCreate, Family: sysnet.FamilyIPv4,
	})
	if capability.State != sysnet.CapabilityAvailable {
		t.Fatalf("regular TUN create capability = %v, want available", capability.State)
	}
}
