//go:build !linux || !amd64

package main

import (
	"strings"
	"testing"
)

func TestUnsupportedDefaultIsolationFailsWithDirectModeGuidance(t *testing.T) {
	err := runIsolatedNetworkRole()
	if err == nil || !strings.Contains(err.Error(), "not qualified") || !strings.Contains(err.Error(), "--no-isolation") {
		t.Fatalf("unsupported isolation error = %v", err)
	}
}
