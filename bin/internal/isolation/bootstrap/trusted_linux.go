//go:build linux

package bootstrap

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

func openTrustedFile(path string) (*os.File, error) {
	if path == "" || !filepath.IsAbs(path) || containsParentReference(path) {
		return nil, fmt.Errorf("%w: trusted path must be absolute and clean: %q", ErrUnsafeInput, path)
	}
	parts := strings.Split(strings.TrimPrefix(filepath.Clean(path), "/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		return nil, fmt.Errorf("%w: trusted path is not a file", ErrUnsafeInput)
	}
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	current := "/"
	for _, part := range parts[:len(parts)-1] {
		next, openErr := unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		_ = unix.Close(fd)
		if openErr != nil {
			return nil, fmt.Errorf("%w: open trusted parent %s: %w", ErrUnsafeInput, filepath.Join(current, part), openErr)
		}
		fd = next
		current = filepath.Join(current, part)
		if checkErr := checkTrustedFD(fd, unix.S_IFDIR, current); checkErr != nil {
			_ = unix.Close(fd)
			return nil, checkErr
		}
	}
	name := parts[len(parts)-1]
	fileFD, err := unix.Openat(fd, name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	_ = unix.Close(fd)
	if err != nil {
		return nil, fmt.Errorf("%w: open trusted file %s: %w", ErrUnsafeInput, path, err)
	}
	if err := checkTrustedFD(fileFD, unix.S_IFREG, path); err != nil {
		_ = unix.Close(fileFD)
		return nil, err
	}
	return os.NewFile(uintptr(fileFD), path), nil
}

func checkTrustedFD(fd int, kind uint32, label string) error {
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return err
	}
	if stat.Mode&unix.S_IFMT != kind || stat.Uid != 0 && stat.Uid != uint32(os.Geteuid()) || stat.Mode&0o022 != 0 {
		return fmt.Errorf("%w: %s has unsafe type, owner, or permissions", ErrUnsafeInput, label)
	}
	return nil
}

func containsParentReference(path string) bool {
	for _, part := range strings.Split(path, string(filepath.Separator)) {
		if part == ".." {
			return true
		}
	}
	return false
}
