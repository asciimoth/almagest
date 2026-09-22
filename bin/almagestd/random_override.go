//go:build !almagest_e2e

package main

func overrideDaemonRandomSeeds(
	seeds daemonRandomSeeds,
) (daemonRandomSeeds, error) {
	return seeds, nil
}
