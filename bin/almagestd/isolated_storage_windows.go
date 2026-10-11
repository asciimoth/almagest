//go:build windows

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func enterIsolatedRuntimeStorage(storage, requested string) error {
	storage = filepath.Clean(storage)
	requested = filepath.Clean(requested)
	if storage == "." || !filepath.IsAbs(storage) || !strings.EqualFold(storage, requested) {
		return fmt.Errorf("invalid isolated runtime storage: %s", requested)
	}
	// The Windows launcher starts in the protected executable directory to
	// constrain DLL lookup. Change directory only after bootstrap has selected
	// the held, AppContainer-owned storage path.
	return os.Chdir(storage)
}
