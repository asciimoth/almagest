//go:build almagest_e2e

package main

import (
	"fmt"
	"os"
	"strconv"
)

const e2eSystemRandomSeedEnv = "ALMAGEST_E2E_SYSTEM_RANDOM_SEED"
const e2eTailscaleRejectAfterEnv = "ALMAGEST_TAILSCALE_E2E_REJECT_AFTER_SECONDS"

func systemRandomSeedOverride() (*int64, error) {
	value := os.Getenv(e2eSystemRandomSeedEnv)
	if value == "" {
		return nil, nil
	}
	seed, err := strconv.ParseInt(value, 0, 64)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", e2eSystemRandomSeedEnv, err)
	}
	return &seed, nil
}

func tailscaleRejectAfterOverride() (uint32, error) {
	value := os.Getenv(e2eTailscaleRejectAfterEnv)
	if value == "" {
		return 0, nil
	}
	seconds, err := strconv.ParseUint(value, 10, 32)
	if err != nil || seconds == 0 || seconds > 86400 {
		return 0, fmt.Errorf("parse %s: invalid duration", e2eTailscaleRejectAfterEnv)
	}
	return uint32(seconds), nil
}
