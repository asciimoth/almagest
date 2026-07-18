package platform

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
)

const tmpAdminUnixSocketPrefix = "alm-sock-"

// tmpAdminUnixSocketPathIn returns an absolute, currently-unused temporary path
// suitable for a Unix admin socket. It does not create the socket; the daemon's
// Unix admin listener owns bind-time setup, permissions, and cleanup.
func tmpAdminUnixSocketPathIn(parent string) (string, error) {
	parent, err := filepath.Abs(parent)
	if err != nil {
		return "", fmt.Errorf("temporary unix admin socket parent: %w", err)
	}
	info, err := os.Stat(parent)
	if err != nil {
		return "", fmt.Errorf("temporary unix admin socket parent: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("temporary unix admin socket parent %s is not a directory", parent)
	}

	var random [8]byte
	for range 100 {
		if _, err := rand.Read(random[:]); err != nil {
			return "", fmt.Errorf("temporary unix admin socket random name: %w", err)
		}
		path := filepath.Join(parent, tmpAdminUnixSocketPrefix+hex.EncodeToString(random[:]))
		if _, err := os.Lstat(path); os.IsNotExist(err) {
			return path, nil
		} else if err != nil {
			return "", fmt.Errorf("temporary unix admin socket path: %w", err)
		}
	}
	return "", fmt.Errorf("temporary unix admin socket path: failed to find unused random name")
}
