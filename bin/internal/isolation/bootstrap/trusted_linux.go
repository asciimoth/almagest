//go:build linux

package bootstrap

import (
	"errors"
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
	if err := checkTrustedLinkPath(path); err != nil {
		return nil, err
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return nil, fmt.Errorf("%w: resolve trusted file %s: %w", ErrUnsafeInput, path, err)
	}
	return openTrustedFileNoLinks(resolved)
}

// checkTrustedLinkPath permits root- or process-owned symbolic links, but only
// below trusted non-writable parents. The resolved path is checked separately
// before its held descriptor is returned.
func checkTrustedLinkPath(path string) error {
	parts := strings.Split(strings.TrimPrefix(filepath.Clean(path), "/"), "/")
	current := "/"
	for index, part := range parts {
		current = filepath.Join(current, part)
		var stat unix.Stat_t
		if err := unix.Lstat(current, &stat); err != nil {
			return fmt.Errorf("%w: inspect trusted path %s: %w", ErrUnsafeInput, current, err)
		}
		kind := stat.Mode & unix.S_IFMT
		if kind == unix.S_IFLNK {
			if stat.Uid != 0 && stat.Uid != uint32(os.Geteuid()) {
				return fmt.Errorf("%w: %s has an unsafe symbolic-link owner", ErrUnsafeInput, current)
			}
			continue
		}
		expected := uint32(unix.S_IFDIR)
		if index == len(parts)-1 {
			expected = unix.S_IFREG
		}
		if !trustedMetadata(&stat, expected) {
			return fmt.Errorf("%w: %s has unsafe type, owner, or permissions", ErrUnsafeInput, current)
		}
	}
	return nil
}

func openTrustedFileNoLinks(path string) (*os.File, error) {
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

func openUntrustedFile(path string) (*os.File, error) {
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path || containsParentReference(path) {
		return nil, fmt.Errorf("%w: untrusted input path must be absolute and clean", ErrInvalidInput)
	}
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	for _, part := range parts[:len(parts)-1] {
		next, openErr := unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		_ = unix.Close(fd)
		if openErr != nil {
			return nil, fmt.Errorf("%w: untrusted input parent: %v", ErrUnsafeInput, openErr)
		}
		fd = next
	}
	fileFD, err := unix.Openat(fd, parts[len(parts)-1], unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	_ = unix.Close(fd)
	if err != nil {
		return nil, err
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fileFD, &stat); err != nil || stat.Mode&unix.S_IFMT != unix.S_IFREG {
		_ = unix.Close(fileFD)
		return nil, fmt.Errorf("%w: untrusted input is not a regular file", ErrInvalidInput)
	}
	return os.NewFile(uintptr(fileFD), path), nil
}

func checkTrustedFD(fd int, kind uint32, label string) error {
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return err
	}
	if !trustedMetadata(&stat, kind) {
		return fmt.Errorf("%w: %s has unsafe type, owner, or permissions", ErrUnsafeInput, label)
	}
	return nil
}

func trustedMetadata(stat *unix.Stat_t, kind uint32) bool {
	if stat.Mode&unix.S_IFMT != kind || stat.Uid != 0 && stat.Uid != uint32(os.Geteuid()) {
		return false
	}
	if stat.Mode&0o022 == 0 {
		return true
	}
	return kind == unix.S_IFDIR && stat.Mode&unix.S_ISVTX != 0
}

// OpenPrivateDirectory opens a launch-selected role directory without
// following links. Every parent must be trusted and non-replaceable. The final
// directory must be mode 0700 and owned by the supplied final identity.
func OpenPrivateDirectory(path string, uid, gid uint32) (*os.File, error) {
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path || containsParentReference(path) {
		return nil, fmt.Errorf("%w: private directory path must be absolute and clean: %q", ErrUnsafeInput, path)
	}
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		return nil, fmt.Errorf("%w: root is not a private role directory", ErrUnsafeInput)
	}
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	current := "/"
	for index, part := range parts {
		next, openErr := unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		_ = unix.Close(fd)
		if openErr != nil {
			return nil, fmt.Errorf("%w: open private directory %s: %v", ErrUnsafeInput, filepath.Join(current, part), openErr)
		}
		fd = next
		current = filepath.Join(current, part)
		if index < len(parts)-1 {
			if checkErr := checkTrustedFD(fd, unix.S_IFDIR, current); checkErr != nil {
				_ = unix.Close(fd)
				return nil, checkErr
			}
		}
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		_ = unix.Close(fd)
		return nil, err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFDIR || stat.Mode&0o7777 != 0o700 || stat.Uid != uid || stat.Gid != gid {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("%w: private directory must be mode 0700 and owned by uid %d gid %d", ErrUnsafeInput, uid, gid)
	}
	return os.NewFile(uintptr(fd), path), nil
}

// EnsurePrivateDirectory creates missing path components through held parent
// descriptors. It never follows a symbolic link. Existing parents must have
// trusted directory metadata, and the final directory must be private.
func EnsurePrivateDirectory(path string, uid, gid uint32) error {
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path || containsParentReference(path) {
		return fmt.Errorf("%w: private directory path must be absolute and clean: %q", ErrUnsafeInput, path)
	}
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		return fmt.Errorf("%w: root is not a private role directory", ErrUnsafeInput)
	}
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(fd) }()
	current := "/"
	for index, part := range parts {
		next, openErr := unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if errors.Is(openErr, unix.ENOENT) {
			if err := unix.Mkdirat(fd, part, 0o700); err != nil {
				return fmt.Errorf("create private directory %s: %w", filepath.Join(current, part), err)
			}
			next, openErr = unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		}
		if openErr != nil {
			return fmt.Errorf("%w: open private directory %s: %v", ErrUnsafeInput, filepath.Join(current, part), openErr)
		}
		_ = unix.Close(fd)
		fd = next
		current = filepath.Join(current, part)
		if index < len(parts)-1 {
			if err := checkTrustedFD(fd, unix.S_IFDIR, current); err != nil {
				return err
			}
		}
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFDIR || stat.Mode&0o7777 != 0o700 || stat.Uid != uid || stat.Gid != gid {
		return fmt.Errorf("%w: private directory must be mode 0700 and owned by uid %d gid %d", ErrUnsafeInput, uid, gid)
	}
	return nil
}

// ValidatePrivateTree rejects aliases and special objects before a user-profile
// role receives write access to an existing tree. OpenPrivateDirectory must be
// called first to validate every ancestor and pin the root directory.
func ValidatePrivateTree(path string, uid, gid uint32) error {
	var root unix.Stat_t
	if err := unix.Lstat(path, &root); err != nil {
		return err
	}
	if root.Mode&unix.S_IFMT != unix.S_IFDIR || root.Uid != uid || root.Gid != gid {
		return fmt.Errorf("%w: private tree root has unsafe metadata", ErrUnsafeInput)
	}
	var rootStatx unix.Statx_t
	if err := unix.Statx(unix.AT_FDCWD, path, unix.AT_SYMLINK_NOFOLLOW, unix.STATX_TYPE|unix.STATX_MNT_ID, &rootStatx); err != nil || rootStatx.Mask&unix.STATX_MNT_ID == 0 {
		return fmt.Errorf("%w: cannot identify private tree mount", ErrUnsafeInput)
	}
	return filepath.WalkDir(path, func(current string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		var stat unix.Stat_t
		if err := unix.Lstat(current, &stat); err != nil {
			return err
		}
		if stat.Dev != root.Dev || stat.Uid != uid || stat.Gid != gid || stat.Mode&0o022 != 0 {
			return fmt.Errorf("%w: private tree entry has unsafe owner, mode, or mount: %s", ErrUnsafeInput, current)
		}
		var statx unix.Statx_t
		if err := unix.Statx(unix.AT_FDCWD, current, unix.AT_SYMLINK_NOFOLLOW, unix.STATX_TYPE|unix.STATX_MNT_ID, &statx); err != nil ||
			statx.Mask&unix.STATX_MNT_ID == 0 || statx.Mnt_id != rootStatx.Mnt_id {
			return fmt.Errorf("%w: private tree crosses a mount boundary: %s", ErrUnsafeInput, current)
		}
		switch stat.Mode & unix.S_IFMT {
		case unix.S_IFDIR:
			return nil
		case unix.S_IFREG:
			if stat.Nlink != 1 || stat.Mode&(unix.S_ISUID|unix.S_ISGID) != 0 {
				return fmt.Errorf("%w: private tree file has aliases or set-ID mode: %s", ErrUnsafeInput, current)
			}
			return nil
		default:
			return fmt.Errorf("%w: private tree contains an unsupported object: %s", ErrUnsafeInput, current)
		}
	})
}

func containsParentReference(path string) bool {
	for _, part := range strings.Split(path, string(filepath.Separator)) {
		if part == ".." {
			return true
		}
	}
	return false
}
