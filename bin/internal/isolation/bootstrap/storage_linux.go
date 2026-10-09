//go:build linux

package bootstrap

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

func validateStorageParent(path string) error {
	if path == "" || !filepath.IsAbs(path) || containsParentReference(path) {
		return ErrUnsafeLayout
	}
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	current := "/"
	for _, part := range strings.Split(strings.TrimPrefix(filepath.Clean(path), "/"), "/") {
		if part == "" {
			continue
		}
		next, openErr := unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		_ = unix.Close(fd)
		if openErr != nil {
			return openErr
		}
		fd = next
		current = filepath.Join(current, part)
		if err := checkTrustedFD(fd, unix.S_IFDIR, current); err != nil {
			_ = unix.Close(fd)
			return fmt.Errorf("trusted storage parent: %w", err)
		}
	}
	return unix.Close(fd)
}

func openMigrationSource(path string) (*os.File, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		_ = unix.Close(fd)
		return nil, err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Nlink != 1 {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("migration source is not a single-link regular no-follow file")
	}
	return os.NewFile(uintptr(fd), path), nil
}
