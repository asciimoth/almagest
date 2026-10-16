//go:build !unix && !windows

package service

import (
	"os"

	"github.com/asciimoth/almagest/lib/sandbox"
)

func lockRegistryFile(*os.File) error   { return sandbox.ErrUnavailable }
func unlockRegistryFile(*os.File) error { return nil }
