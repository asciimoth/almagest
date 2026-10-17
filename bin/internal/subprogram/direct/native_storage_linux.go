//go:build linux

package direct

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"

	sandbox "github.com/asciimoth/gobox"
)

func prepareNativeStorageParent(path string, uid, gid uint32) error {
	if uid == 0 {
		return nil
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	changed := os.Geteuid() == 0 && uid != 0 && uid != uint32(os.Geteuid())
	if changed {
		// The parent permits traversal but not listing. The per-instance
		// directory remains private to the selected child identity.
		if err = os.Chmod(path, 0o711); err != nil {
			return err
		}
	}
	if err = validateNativeTraversal(path, uid, gid); err != nil && changed {
		err = errors.Join(err, os.Chmod(path, info.Mode().Perm()))
	}
	return err
}

func validateNativeTraversal(path string, uid, gid uint32) error {
	for current := filepath.Clean(path); ; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if err != nil {
			return err
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return sandbox.ErrDenied
		}
		mode := info.Mode().Perm()
		allowed := stat.Uid == uid && mode&0o100 != 0 || stat.Gid == gid && mode&0o010 != 0 || mode&0o001 != 0
		if !allowed {
			return sandbox.ErrDenied
		}
		parent := filepath.Dir(current)
		if parent == current {
			return nil
		}
	}
}

func prepareNativeStorage(path string, uid, gid uint32) error {
	if os.Geteuid() != 0 || uid == 0 || uid == uint32(os.Geteuid()) {
		return nil
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return sandbox.ErrDenied
	}
	if err := os.Chown(path, int(uid), int(gid)); err != nil {
		return err
	}
	if err = os.Chmod(path, 0o700); err != nil {
		err = errors.Join(err, os.Chown(path, int(stat.Uid), int(stat.Gid)), os.Chmod(path, info.Mode().Perm()))
	}
	return err
}
