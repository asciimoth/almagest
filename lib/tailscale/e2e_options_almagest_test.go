//go:build almagest_e2e

package tailscalenodes

import (
	"testing"

	wgotailscale "github.com/asciimoth/wgo-tailscale"
	"github.com/asciimoth/wgo/device"
)

func TestApplyTailscaleE2EOptionsRejectAfter(t *testing.T) {
	t.Setenv(tailscaleE2ERejectAfterEnv, "20")

	options := applyTailscaleE2EOptions(wgotailscale.Options{})
	if options.Obfuscation == nil {
		t.Fatal("Obfuscation is nil")
	}
	if err := device.ValidateAmneziaWGConfig(*options.Obfuscation); err != nil {
		t.Fatalf("Obfuscation is invalid: %v", err)
	}
	got := options.Obfuscation.RejectAfterTime
	want := device.AmneziaWGRange{Min: 20, Max: 20, Set: true}
	if got != want {
		t.Fatalf("RejectAfterTime = %+v, want %+v", got, want)
	}
}

func TestApplyTailscaleE2EOptionsIgnoresInvalidValue(t *testing.T) {
	t.Setenv(tailscaleE2ERejectAfterEnv, "invalid")

	options := applyTailscaleE2EOptions(wgotailscale.Options{})
	if options.Obfuscation != nil {
		t.Fatalf("Obfuscation = %+v, want nil", options.Obfuscation)
	}
}
