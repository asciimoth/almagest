//go:build !unix && !windows

package service

import (
	"os"

	sandbox "github.com/asciimoth/gobox"
)

func lockRegistryFile(*os.File) error   { return sandbox.ErrUnavailable }
func unlockRegistryFile(*os.File) error { return nil }
