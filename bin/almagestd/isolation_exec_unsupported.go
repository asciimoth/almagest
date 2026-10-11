//go:build !linux || !amd64

package main

import "errors"

func runIsolatedNetworkRole() error {
	return errors.New("default isolation is not qualified on this platform; use explicit --no-isolation")
}
