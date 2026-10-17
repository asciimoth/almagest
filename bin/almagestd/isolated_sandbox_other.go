//go:build !linux

package main

import (
	"errors"
	"os"

	"github.com/asciimoth/almagest/bin/internal/isolation/roleentry"
	"github.com/asciimoth/almagest/bin/internal/isolation/transport"
)

var errSandboxSupervisorUnavailable = errors.New("sandbox supervisor is unavailable on this platform")

func isolatedSandboxSupervisorRole() bool { return false }

func runIsolatedSandboxSupervisorRole() error {
	return errSandboxSupervisorUnavailable
}

func bindSandboxSupervisor(*os.File, roleentry.RuntimePeer) (transport.Stream, error) {
	return nil, errSandboxSupervisorUnavailable
}
