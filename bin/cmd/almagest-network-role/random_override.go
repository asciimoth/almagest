//go:build !almagest_e2e

package main

func systemRandomSeedOverride() (*int64, error) {
	return nil, nil
}

func tailscaleRejectAfterOverride() (uint32, error) {
	return 0, nil
}
