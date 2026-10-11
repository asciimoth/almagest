//go:build linux && amd64

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"syscall"

	"github.com/asciimoth/almagest/bin/internal/isolation/bootstrap"
)

// runIsolatedNetworkRole replaces the runtime-linked command before it reads
// attacker-controlled config. N is a separate minimal binary and becomes the
// service process; no extra privileged launcher remains.
func runIsolatedNetworkRole() error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	rolePath := filepath.Join(filepath.Dir(self), "almagest-network-role")
	role, err := bootstrap.OpenTrustedFile(rolePath)
	if err != nil {
		return fmt.Errorf("open trusted isolated network role: %w; install the complete role package or use explicit --no-isolation", err)
	}
	defer role.Close() //nolint:errcheck // Exec closes it; failure returns here.
	argv := append([]string{rolePath}, os.Args[1:]...)
	return syscall.Exec("/proc/self/fd/"+strconv.FormatUint(uint64(role.Fd()), 10), argv, os.Environ())
}
