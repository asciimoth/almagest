//go:build almagest_e2e

package experimentalnodes

import (
	"io/fs"
	"os"
	"path/filepath"

	tor "github.com/asciimoth/tor-driver"
)

// applyXTorE2EDependencies permits the Docker suite to add authorities for a
// hermetic private Tor network. It is absent from production builds and does
// not add a raw torrc field to protected or workspace configuration.
func applyXTorE2EDependencies(deps tor.Dependencies) tor.Dependencies {
	path := os.Getenv("ALMAGEST_TOR_E2E_TORRC")
	if path == "" || deps.FS == nil {
		return deps
	}
	extra, err := os.ReadFile(path)
	if err != nil {
		if deps.Logger != nil {
			deps.Logger.Errf("XTorDriver e2e authority file: %v", err)
		}
		return deps
	}
	deps.FS = &xTorE2EFileSystem{FileSystem: deps.FS, extra: extra}
	return deps
}

type xTorE2EFileSystem struct {
	tor.FileSystem
	extra []byte
}

func (f *xTorE2EFileSystem) WriteFile(path string, data []byte, mode fs.FileMode, owner *tor.Identity) error {
	if filepath.Base(path) == "torrc" {
		merged := make([]byte, 0, len(data)+len(f.extra)+2)
		merged = append(merged, data...)
		if len(merged) > 0 && merged[len(merged)-1] != '\n' {
			merged = append(merged, '\n')
		}
		merged = append(merged, f.extra...)
		if len(merged) > 0 && merged[len(merged)-1] != '\n' {
			merged = append(merged, '\n')
		}
		data = merged
	}
	return f.FileSystem.WriteFile(path, data, mode, owner)
}
