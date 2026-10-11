package nativebackend

import (
	"testing"
	"time"

	sysnetwindows "github.com/asciimoth/sysnet-windows"
)

func TestParseWindowsFileConfigMatchesDefaultsAndOverrides(t *testing.T) {
	defaults, err := ParseWindowsFileConfig([]byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	native := defaults.Native(nil)
	if native.AdapterNamePrefix != WindowsDefaultAdapterNamePrefix || native.OperationTimeout != WindowsDefaultOperationTimeout || native.Recovery != sysnetwindows.RecoveryInspect || native.Logger == nil {
		t.Fatalf("defaults = %#v", native)
	}
	override, err := ParseWindowsFileConfig([]byte(`{
		"AdapterNamePrefix":"private",
		"StableGUID":"01234567-89ab-cdef-0123-456789abcdef",
		"UnderlaySelector":"Ethernet",
		"Features":{"DisableIPv4":true,"DisableMatchers":true},
		"OperationTimeout":"12.5s",
		"Recovery":"owned",
		"Logger":{"untrusted":true}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	called := false
	native = override.Native(func(string, ...any) { called = true })
	native.Logger.Printf("test")
	if native.AdapterNamePrefix != "private" || native.StableGUID == "" || native.UnderlaySelector != "Ethernet" ||
		native.OperationTimeout != 12500*time.Millisecond || native.Recovery != sysnetwindows.RecoveryOwned ||
		!native.Features.DisableIPv4 || !native.Features.DisableMatchers || !called {
		t.Fatalf("override = %#v called=%v", native, called)
	}
}

func TestParseWindowsFileConfigRejectsInvalidScalars(t *testing.T) {
	for _, input := range []string{
		`{"OperationTimeout":"later"}`,
		`{"OperationTimeout":"-1s"}`,
		`{"Recovery":"force"}`,
		`{"OperationTimeout":1}`,
	} {
		if _, err := ParseWindowsFileConfig([]byte(input)); err == nil {
			t.Fatalf("accepted %s", input)
		}
	}
}

func FuzzWindowsFileConfig(f *testing.F) {
	f.Add(`{}`)
	f.Add(`{"OperationTimeout":"1s","Recovery":"inspect"}`)
	f.Add(`{"Recovery":false}`)
	f.Fuzz(func(t *testing.T, input string) {
		config, err := ParseWindowsFileConfig([]byte(input))
		if err != nil {
			return
		}
		native := config.Native(nil)
		if native.Logger == nil || native.OperationTimeout < 0 {
			t.Fatalf("unsafe parsed config = %#v", native)
		}
	})
}
