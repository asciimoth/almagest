//go:build !linux

package main

import (
	"errors"
	"os"

	"github.com/asciimoth/almagest/bin/internal/isolation/roleentry"
	"github.com/asciimoth/almagest/bin/internal/isolation/transport"
)

func isolatedSandboxSupervisorRole() bool { return false }

func runIsolatedSandboxSupervisorRole() error {
	return errors.New("isolated sandbox supervisor is unavailable on this platform")
}

func bindSandboxSupervisor(*os.File, roleentry.RuntimePeer) (transport.Stream, error) {
	return nil, errors.New("sandbox supervisor peer binding is unavailable on this platform")
}
