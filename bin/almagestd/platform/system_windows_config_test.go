package platform

import (
	"errors"
	"os"
	"testing"
	"time"

	"github.com/asciimoth/configer/hujson"
	"github.com/asciimoth/gonnect/sysnet"
	sysnetwindows "github.com/asciimoth/sysnet-windows"
)

func TestWindowsDefaultConfigExample(t *testing.T) {
	mainData, err := os.ReadFile("default_windows_config.hujson")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := hujson.Parse(mainData); err != nil {
		t.Fatalf("parse default Windows config: %v", err)
	}
	protectedData, err := os.ReadFile("default_windows_protected_config.hujson")
	if err != nil {
		t.Fatal(err)
	}
	protected, err := hujson.Parse(protectedData)
	if err != nil {
		t.Fatalf("parse default Windows protected config: %v", err)
	}
	got, err := readWindowsSystemConfig(protected.View([]string{"System"}))
	if err != nil {
		t.Fatalf("read default Windows system config: %v", err)
	}
	if got.AdapterNamePrefix != "alm" || got.OperationTimeout != 30*time.Second || got.Recovery != sysnetwindows.RecoveryInspect {
		t.Fatalf("default Windows system config = %#v", got)
	}
}

func TestReadWindowsSystemConfigDefaults(t *testing.T) {
	cfg, err := hujson.Parse([]byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	got, err := readWindowsSystemConfig(cfg)
	if err != nil {
		t.Fatalf("readWindowsSystemConfig: %v", err)
	}
	if got.AdapterNamePrefix != windowsDefaultAdapterNamePrefix {
		t.Fatalf("AdapterNamePrefix = %q, want %q", got.AdapterNamePrefix, windowsDefaultAdapterNamePrefix)
	}
	if got.OperationTimeout != windowsDefaultOperationTimeout || got.Recovery != sysnetwindows.RecoveryInspect {
		t.Fatalf("defaults = timeout %v, recovery %v", got.OperationTimeout, got.Recovery)
	}
	if got.Logger == nil {
		t.Fatal("Logger = nil, want daemon logger")
	}
}

func TestReadWindowsSystemConfigExplicitFields(t *testing.T) {
	cfg, err := hujson.Parse([]byte(`{
		"Windows": {
			"AdapterNamePrefix": "private",
			"StableGUID": "01234567-89ab-cdef-0123-456789abcdef",
			"UnderlaySelector": "Ethernet",
			"Features": {
				"DisableIPv4": true,
				"DisableIPv6": true,
				"DisableExclusions": true,
				"DisableMatchers": true
			},
			"OperationTimeout": "12.5s",
			"Recovery": "owned",
			"Logger": {"ignored": true}
		}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	got, err := readWindowsSystemConfig(cfg)
	if err != nil {
		t.Fatalf("readWindowsSystemConfig: %v", err)
	}
	if got.AdapterNamePrefix != "private" || got.StableGUID != "01234567-89ab-cdef-0123-456789abcdef" || got.UnderlaySelector != "Ethernet" {
		t.Fatalf("identity fields = %#v", got)
	}
	if got.OperationTimeout != 12500*time.Millisecond || got.Recovery != sysnetwindows.RecoveryOwned {
		t.Fatalf("policy fields = timeout %v, recovery %v", got.OperationTimeout, got.Recovery)
	}
	if !got.Features.DisableIPv4 || !got.Features.DisableIPv6 || !got.Features.DisableExclusions || !got.Features.DisableMatchers {
		t.Fatalf("Features = %#v, want all disabled", got.Features)
	}
	if got.Logger == nil {
		t.Fatal("Logger = nil, want trusted daemon logger")
	}
}

func TestReadWindowsSystemConfigRejectsInvalidDurationAndRecovery(t *testing.T) {
	for _, test := range []struct {
		name string
		body string
	}{
		{name: "duration syntax", body: `{"Windows":{"OperationTimeout":"later"}}`},
		{name: "negative duration", body: `{"Windows":{"OperationTimeout":"-1s"}}`},
		{name: "recovery", body: `{"Windows":{"Recovery":"force"}}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfg, err := hujson.Parse([]byte(test.body))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := readWindowsSystemConfig(cfg); err == nil {
				t.Fatal("readWindowsSystemConfig succeeded")
			}
		})
	}
}

func TestWindowsSystemConfigRejectsInvalidGUID(t *testing.T) {
	cfg, err := hujson.Parse([]byte(`{"Windows":{"StableGUID":"not-a-guid"}}`))
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := readWindowsSystemConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	_, err = sysnetwindows.New(parsed)
	if err == nil || !errors.Is(err, sysnet.ErrInvalidOptions) {
		t.Fatalf("New error = %v, want invalid options", err)
	}
}
