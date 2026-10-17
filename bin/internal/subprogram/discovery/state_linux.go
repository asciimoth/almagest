//go:build linux

package discovery

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	sandbox "github.com/asciimoth/gobox"
)

// DefaultStateRoot places sandbox state beside a private daemon directory.
// A non-root sandbox identity cannot traverse that private directory.
func DefaultStateRoot(privateRoot string) string {
	return filepath.Clean(privateRoot) + ".subprogram-sandbox"
}

func prepareStateRoot(path string) error {
	if err := os.MkdirAll(path, 0o711); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || stat.Uid != uint32(os.Geteuid()) || info.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("%w: state root must be a real directory owned by the daemon and not group/world writable", sandbox.ErrDenied)
	}
	// Sandboxed non-root identities can traverse this directory but cannot
	// list it. Each service root and instance directory remains private.
	return os.Chmod(path, 0o711)
}
