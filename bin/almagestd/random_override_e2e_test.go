//go:build almagest_e2e

package main

import "testing"

func TestOverrideDaemonRandomSeedsForE2E(t *testing.T) {
	t.Setenv(e2eSystemRandomSeedEnv, "30521")

	want := daemonRandomSeeds{system: 30521, workspace: 22}
	got, err := overrideDaemonRandomSeeds(
		daemonRandomSeeds{system: 11, workspace: 22},
	)
	if err != nil {
		t.Fatalf("overrideDaemonRandomSeeds error = %v", err)
	}
	if got != want {
		t.Fatalf("overrideDaemonRandomSeeds = %+v, want %+v", got, want)
	}
}

func TestOverrideDaemonRandomSeedsForE2ERejectsInvalidSeed(t *testing.T) {
	t.Setenv(e2eSystemRandomSeedEnv, "invalid")

	if _, err := overrideDaemonRandomSeeds(daemonRandomSeeds{}); err == nil {
		t.Fatal("overrideDaemonRandomSeeds error = nil")
	}
}
