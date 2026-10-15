//go:build linux && amd64

package main

import (
	"fmt"
	"os"
	"path/filepath"
)

func enterIsolatedRuntimeStorage(storage, requested string) error {
	if os.Getenv("ALMAGEST_LINUX_PROFILE") == "linux-user-v1" {
		if filepath.IsAbs(storage) && filepath.Clean(storage) == storage && requested == storage {
			return nil
		}
		return fmt.Errorf("invalid user-profile runtime storage: %s", requested)
	}
	// The Linux launcher enters the fixed chroot before it installs the final
	// syscall filter. R cannot inspect or change its working directory later.
	if filepath.Clean(storage) != "/state" || filepath.Clean(requested) != "/state" {
		return fmt.Errorf("invalid isolated runtime storage: %s", requested)
	}
	return nil
}
