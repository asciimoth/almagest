//go:build almagest_e2e

package main

import (
	"os"
	"strconv"

	"github.com/asciimoth/almagest/bin/internal/isolation/roleentry"
)

func applyIsolatedE2EOptions(options roleentry.RuntimeOptions) error {
	if options.E2ETailscaleRejectAfterSeconds == 0 {
		return nil
	}
	return os.Setenv(
		"ALMAGEST_TAILSCALE_E2E_REJECT_AFTER_SECONDS",
		strconv.FormatUint(uint64(options.E2ETailscaleRejectAfterSeconds), 10),
	)
}
