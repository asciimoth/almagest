//go:build almagest_e2e

package main

import (
	"os"
	"testing"

	"github.com/asciimoth/almagest/bin/internal/isolation/roleentry"
)

func TestApplyIsolatedE2EOptions(t *testing.T) {
	t.Setenv("ALMAGEST_TAILSCALE_E2E_REJECT_AFTER_SECONDS", "")
	if err := applyIsolatedE2EOptions(roleentry.RuntimeOptions{E2ETailscaleRejectAfterSeconds: 20}); err != nil {
		t.Fatal(err)
	}
	if got := os.Getenv("ALMAGEST_TAILSCALE_E2E_REJECT_AFTER_SECONDS"); got != "20" {
		t.Fatalf("reject-after environment = %q", got)
	}
}
