//go:build !linux

package discovery

import (
	"os"
	"path/filepath"
)

func DefaultStateRoot(privateRoot string) string {
	return filepath.Join(privateRoot, "subprogram-sandbox")
}

func prepareStateRoot(path string) error {
	return os.MkdirAll(path, 0o700)
}
