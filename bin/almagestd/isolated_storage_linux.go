//go:build linux && amd64

package main

import (
	"fmt"
	"path/filepath"
)

func enterIsolatedRuntimeStorage(storage, requested string) error {
	// The Linux launcher enters the fixed chroot before it installs the final
	// syscall filter. R cannot inspect or change its working directory later.
	if filepath.Clean(storage) != "/state" || filepath.Clean(requested) != "/state" {
		return fmt.Errorf("invalid isolated runtime storage: %s", requested)
	}
	return nil
}
