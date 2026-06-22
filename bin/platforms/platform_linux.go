//go:build linux

package platforms

import (
	_ "embed"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/gonnect/sysnet"
	"golang.org/x/sys/unix"
)

//go:embed default_linux_config.hujson
var DefaultConfigText string

//go:embed default_linux_protected_config.hujson
var DefaultProtectedConfigText string

func DefaultConfig() string {
	return "/etc/almagest/alm.hujson"
}

// IsDir reports if path is a dir.
// If path not exists, reports false.
func IsDir(path string) (bool, error) {
	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		} else {
			return false, err
		}
	}
	return info.IsDir(), nil
}

func trustedUID(uid uint32) bool {
	return uid == 0 || uid == uint32(unix.Geteuid())
}

// checkSafeFD verifies that fd refers to a safe filesystem object.
//
// It checks the already-opened fd with fstat(). This is important: checking a
// pathname with stat() and then opening it later would be racy.
//
// The object is considered safe when:
//
//   - it has the expected filesystem type;
//   - it is owned by root or by the current effective UID;
//   - it is not writable by group or others.
func checkSafeFD(fd int, wantType uint32, label string) error {
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return fmt.Errorf("%s: fstat: %w", label, err)
	}

	if st.Mode&unix.S_IFMT != wantType {
		return fmt.Errorf("%s: unexpected file type: mode %#o", label, st.Mode)
	}

	if !trustedUID(st.Uid) {
		return fmt.Errorf("%s: owner uid %d is not trusted", label, st.Uid)
	}

	if st.Mode&0o022 != 0 {
		return fmt.Errorf("%s: writable by group or others: mode %#o", label, st.Mode&0o7777)
	}

	return nil
}

// splitAbsPath splits an absolute path into path components.
//
// The function requires an absolute path because safety checks for relative
// paths depend on the current working directory, which may be attacker-controlled
// or may change during program execution.
//
// The function rejects ".." before cleaning the path. This avoids surprising
// behavior around symlinks and parent-directory traversal.
func splitAbsPath(path string) ([]string, error) {
	if path == "" {
		return nil, fmt.Errorf("empty path")
	}

	if !filepath.IsAbs(path) {
		return nil, fmt.Errorf("path must be absolute: %q", path)
	}

	if slices.Contains(strings.Split(path, "/"), "..") {
		return nil, fmt.Errorf("path must not contain '..': %q", path)
	}

	clean := filepath.Clean(path)
	if clean == "/" {
		return nil, nil
	}

	parts := strings.Split(strings.TrimPrefix(clean, "/"), "/")
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return nil, fmt.Errorf("bad path component %q in %q", part, path)
		}
	}

	return parts, nil
}

// splitAbsFilePath splits an absolute file path into parent directory components
// and the final file name.
//
// For example:
//
//	/etc/myprog/config -> []string{"etc", "myprog"}, "config"
//
// A trailing slash is rejected because this function is for regular files, not
// directories.
func splitAbsFilePath(path string) (parents []string, base string, err error) {
	if strings.HasSuffix(path, "/") {
		return nil, "", fmt.Errorf("file path must not end with slash: %q", path)
	}

	parts, err := splitAbsPath(path)
	if err != nil {
		return nil, "", err
	}

	if len(parts) == 0 {
		return nil, "", fmt.Errorf("root directory is not a file path")
	}

	return parts[:len(parts)-1], parts[len(parts)-1], nil
}

// openSafeDirComponents opens a directory path component-by-component.
//
// It starts at "/" and then opens each component with openat(). Every opened
// directory is immediately checked with fstat().
//
// Symlinks are rejected because every component is opened with O_NOFOLLOW.
// Untrusted writable parent directories are rejected because every directory
// must pass checkSafeFD().
//
// If createMissing is true, missing directories are created with mode 0700.
// Existing directories are never chmodded here; if an existing directory is
// unsafe, this function returns an error.
func openSafeDirComponents(parts []string, createMissing bool) (int, error) {
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, fmt.Errorf("open /: %w", err)
	}

	ok := false
	defer func() {
		if !ok {
			_ = unix.Close(fd)
		}
	}()

	if err := checkSafeFD(fd, unix.S_IFDIR, "/"); err != nil {
		return -1, err
	}

	current := "/"

	for _, part := range parts {
		nextPath := filepath.Join(current, part)

		nextFD, err := unix.Openat(
			fd,
			part,
			unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC,
			0,
		)

		if err == unix.ENOENT && createMissing {
			if err := unix.Mkdirat(fd, part, 0o700); err != nil && err != unix.EEXIST {
				return -1, fmt.Errorf("%s: mkdirat: %w", nextPath, err)
			}

			nextFD, err = unix.Openat(
				fd,
				part,
				unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC,
				0,
			)
		}

		if err != nil {
			return -1, fmt.Errorf("%s: openat: %w", nextPath, err)
		}

		if err := checkSafeFD(nextFD, unix.S_IFDIR, nextPath); err != nil {
			_ = unix.Close(nextFD)
			return -1, err
		}

		_ = unix.Close(fd)
		fd = nextFD
		current = nextPath
	}

	ok = true
	return fd, nil
}

// MakeSureDirExists creates path and all missing parents with strict directory
// permissions.
//
// Missing directories are created with mode 0700.
//
// Existing directories are not modified. Instead, every existing directory in
// the path must already be safe:
//
//   - owned by root or by the current effective UID;
//   - not writable by group or others;
//   - not a symlink.
func MakeSureDirExists(path string) error {
	parts, err := splitAbsPath(path)
	if err != nil {
		return err
	}

	fd, err := openSafeDirComponents(parts, true)
	if err != nil {
		return err
	}

	return unix.Close(fd)
}

// openSafeFileAt opens a file inside an already-opened safe parent directory.
//
// The final path component is opened with O_NOFOLLOW, so a final symlink is
// rejected. The opened fd is then checked with fstat(), so the check applies to
// exactly the file that will be read.
func openSafeFileAt(parentFD int, name string, fullPath string) (*os.File, error) {
	fd, err := unix.Openat(
		parentFD,
		name,
		unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC,
		0,
	)
	if err != nil {
		return nil, fmt.Errorf("%s: openat: %w", fullPath, err)
	}

	if err := checkSafeFD(fd, unix.S_IFREG, fullPath); err != nil {
		_ = unix.Close(fd)
		return nil, err
	}

	return os.NewFile(uintptr(fd), fullPath), nil
}

// MakeSureSafeFileExists creates a config file and all missing parent
// directories if they do not exist.
//
// Missing parent directories are created with mode 0700.
// A missing file is created with mode 0600 and filled with initial.
//
// Existing files are not modified, but they must already be safe:
//
//   - regular file;
//   - owned by root or by the current effective UID;
//   - not writable by group or others;
//   - not a symlink.
//
// Existing parent directories must also be safe by the same ownership/write
// rules, and symlinks in the path are rejected.
//
// This function is suitable for startup initialization. It is not enough for
// reloads by itself; every reload should still use OpenSafeFile or ReadSafeFile.
func MakeSureSafeFileExists(path string, initial []byte) error {
	parents, base, err := splitAbsFilePath(path)
	if err != nil {
		return err
	}

	parentFD, err := openSafeDirComponents(parents, true)
	if err != nil {
		return err
	}
	defer unix.Close(parentFD) //nolint:errcheck

	fd, err := unix.Openat(
		parentFD,
		base,
		unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC,
		0o600,
	)

	if err == unix.EEXIST {
		f, err := openSafeFileAt(parentFD, base, path)
		if err != nil {
			return err
		}
		return f.Close()
	}

	if err != nil {
		return fmt.Errorf("%s: create: %w", path, err)
	}

	if err := checkSafeFD(fd, unix.S_IFREG, path); err != nil {
		_ = unix.Close(fd)
		return err
	}

	f := os.NewFile(uintptr(fd), path)

	if len(initial) > 0 {
		if _, err := f.Write(initial); err != nil {
			_ = f.Close()
			return fmt.Errorf("%s: write initial config: %w", path, err)
		}
	}

	if err := f.Sync(); err != nil {
		_ = f.Close()
		return fmt.Errorf("%s: sync: %w", path, err)
	}

	return f.Close()
}

// OpenSafeFile opens path only if the file and all parent directories are safe.
//
// Use this every time the config is loaded or reloaded.
//
// The safety check is performed by walking the path with openat(), rejecting
// symlinks, and checking every opened directory/file with fstat(). This avoids
// classic time-of-check/time-of-use races where an attacker swaps the path
// between stat() and open().
func OpenSafeFile(path string) (*os.File, error) {
	parents, base, err := splitAbsFilePath(path)
	if err != nil {
		return nil, err
	}

	parentFD, err := openSafeDirComponents(parents, false)
	if err != nil {
		return nil, err
	}
	defer unix.Close(parentFD) //nolint:errcheck

	return openSafeFileAt(parentFD, base, path)
}

// ReadSafeFile safely opens and reads a config file.
//
// This should be used for every config reload. The file is opened, checked, and
// read through the same file descriptor.
func ReadSafeFile(path string) ([]byte, error) {
	f, err := OpenSafeFile(path)
	if err != nil {
		return nil, err
	}
	defer f.Close() //nolint:errcheck

	return io.ReadAll(f)
}

func SystemIntegration(cfg configer.Config) (sysnet.System, error) {
	panic(ErrPlatform.Error())
}

func DropPrivileges() {
	// TODO:
}
