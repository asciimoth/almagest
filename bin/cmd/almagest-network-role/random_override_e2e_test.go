//go:build almagest_e2e

package main

import "testing"

func TestSystemRandomSeedOverrideForE2E(t *testing.T) {
	t.Setenv(e2eSystemRandomSeedEnv, "30521")

	seed, err := systemRandomSeedOverride()
	if err != nil {
		t.Fatal(err)
	}
	if seed == nil || *seed != 30521 {
		t.Fatalf("systemRandomSeedOverride = %v", seed)
	}
}

func TestSystemRandomSeedOverrideForE2ERejectsInvalidSeed(t *testing.T) {
	t.Setenv(e2eSystemRandomSeedEnv, "invalid")

	if _, err := systemRandomSeedOverride(); err == nil {
		t.Fatal("systemRandomSeedOverride error = nil")
	}
}

func TestTailscaleRejectAfterOverrideForE2E(t *testing.T) {
	t.Setenv(e2eTailscaleRejectAfterEnv, "20")

	seconds, err := tailscaleRejectAfterOverride()
	if err != nil || seconds != 20 {
		t.Fatalf("tailscaleRejectAfterOverride = %d, %v", seconds, err)
	}
}

func TestTailscaleRejectAfterOverrideForE2ERejectsInvalidValue(t *testing.T) {
	for _, value := range []string{"invalid", "0", "86401"} {
		t.Run(value, func(t *testing.T) {
			t.Setenv(e2eTailscaleRejectAfterEnv, value)
			if _, err := tailscaleRejectAfterOverride(); err == nil {
				t.Fatal("tailscaleRejectAfterOverride error = nil")
			}
		})
	}
}
