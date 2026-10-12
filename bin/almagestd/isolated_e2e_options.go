//go:build !almagest_e2e

package main

import (
	"github.com/asciimoth/almagest/bin/internal/isolation/protocol"
	"github.com/asciimoth/almagest/bin/internal/isolation/roleentry"
)

func applyIsolatedE2EOptions(options roleentry.RuntimeOptions) error {
	if options.E2ETailscaleRejectAfterSeconds != 0 {
		return protocol.ErrInvalidContract
	}
	return nil
}
