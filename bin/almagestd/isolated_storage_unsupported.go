//go:build !windows && (!linux || !amd64)

package main

import "fmt"

func enterIsolatedRuntimeStorage(_, requested string) error {
	return fmt.Errorf("isolated runtime storage is unsupported: %s", requested)
}
