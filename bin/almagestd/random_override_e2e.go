//go:build almagest_e2e

package main

import (
	"fmt"
	"os"
	"strconv"
)

const e2eSystemRandomSeedEnv = "ALMAGEST_E2E_SYSTEM_RANDOM_SEED"

func overrideDaemonRandomSeeds(
	seeds daemonRandomSeeds,
) (daemonRandomSeeds, error) {
	value := os.Getenv(e2eSystemRandomSeedEnv)
	if value == "" {
		return seeds, nil
	}
	seed, err := strconv.ParseInt(value, 0, 64)
	if err != nil {
		return daemonRandomSeeds{}, fmt.Errorf(
			"parse %s: %w",
			e2eSystemRandomSeedEnv,
			err,
		)
	}
	seeds.system = seed
	return seeds, nil
}
