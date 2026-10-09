//go:build linux

package configwriter

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/asciimoth/almagest/bin/internal/isolation/protocol"
	"golang.org/x/sys/unix"
)

type fileToken struct{ device, inode uint64 }

type linuxTarget struct {
	directory *os.File
	name      string
	mode      uint32
	uid, gid  uint32
	device    uint64
}

func openTarget(path string) (fixedTarget, error) {
	if path == "" || !filepath.IsAbs(path) || strings.HasSuffix(path, "/") {
		return nil, fmt.Errorf("config target must be an absolute file path")
	}
	clean := filepath.Clean(path)
	if clean != path || strings.ContainsRune(filepath.Base(clean), os.PathSeparator) || filepath.Base(clean) == "." {
		return nil, fmt.Errorf("config target path is not canonical")
	}
	parent, name := filepath.Dir(clean), filepath.Base(clean)
	root, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	how := &unix.OpenHow{Flags: unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC,
		Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS}
	dirfd, err := unix.Openat2(root, strings.TrimPrefix(parent, "/"), how)
	_ = unix.Close(root)
	if err != nil {
		return nil, fmt.Errorf("open fixed config directory: %w", err)
	}
	directory := os.NewFile(uintptr(dirfd), parent)
	ok := false
	defer func() {
		if !ok {
			_ = directory.Close()
		}
	}()
	var directoryStat unix.Stat_t
	if err := unix.Fstat(dirfd, &directoryStat); err != nil {
		return nil, err
	}
	if directoryStat.Mode&unix.S_IFMT != unix.S_IFDIR || directoryStat.Mode&0o022 != 0 ||
		(directoryStat.Uid != 0 && directoryStat.Uid != uint32(os.Geteuid())) {
		return nil, fmt.Errorf("fixed config directory has unsafe type, owner, or mode")
	}
	t := &linuxTarget{directory: directory, name: name, device: uint64(directoryStat.Dev)}
	fd, st, err := t.openChecked(false)
	if err != nil {
		return nil, err
	}
	_ = unix.Close(fd)
	t.mode, t.uid, t.gid = st.Mode&0o7777, st.Uid, st.Gid
	ok = true
	return t, nil
}

func (t *linuxTarget) openChecked(enforceMetadata bool) (int, unix.Stat_t, error) {
	how := &unix.OpenHow{Flags: unix.O_RDONLY | unix.O_NOFOLLOW | unix.O_CLOEXEC,
		Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS}
	fd, err := unix.Openat2(int(t.directory.Fd()), t.name, how)
	if err != nil {
		return -1, unix.Stat_t{}, fmt.Errorf("open fixed config target: %w", err)
	}
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		_ = unix.Close(fd)
		return -1, st, err
	}
	if st.Mode&unix.S_IFMT != unix.S_IFREG || st.Nlink != 1 || uint64(st.Dev) != t.device ||
		st.Mode&0o022 != 0 || (st.Uid != 0 && st.Uid != uint32(os.Geteuid())) {
		_ = unix.Close(fd)
		return -1, st, fmt.Errorf("fixed config target has unsafe type, links, owner, mode, or volume")
	}
	if enforceMetadata && (st.Mode&0o7777 != t.mode || st.Uid != t.uid || st.Gid != t.gid) {
		_ = unix.Close(fd)
		return -1, st, fmt.Errorf("fixed config target metadata changed")
	}
	return fd, st, nil
}

func (t *linuxTarget) read(limit int) (targetSnapshot, error) {
	fd, st, err := t.openChecked(true)
	if err != nil {
		return targetSnapshot{}, err
	}
	f := os.NewFile(uintptr(fd), t.name)
	defer f.Close() //nolint:errcheck
	if st.Size < 0 || st.Size > int64(limit) {
		return targetSnapshot{}, protocol.ErrLimitExceeded
	}
	data, err := io.ReadAll(io.LimitReader(f, int64(limit)+1))
	if err != nil {
		return targetSnapshot{}, err
	}
	if len(data) > limit {
		return targetSnapshot{}, protocol.ErrLimitExceeded
	}
	return targetSnapshot{data: data, token: fileToken{uint64(st.Dev), st.Ino}}, nil
}

func (t *linuxTarget) replace(ctx context.Context, expected fileToken, data []byte) (bool, error) {
	fd, st, err := t.openChecked(true)
	if err != nil {
		return false, err
	}
	_ = unix.Close(fd)
	if (fileToken{uint64(st.Dev), st.Ino}) != expected {
		return false, protocol.ErrOperationConflict
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}

	name, temporaryFD, err := t.createTemporary()
	if err != nil {
		return false, err
	}
	temporary := os.NewFile(uintptr(temporaryFD), name)
	defer func() {
		_ = temporary.Close()
		_ = unix.Unlinkat(int(t.directory.Fd()), name, 0)
	}()
	if err := unix.Fchmod(temporaryFD, t.mode); err != nil {
		return false, fmt.Errorf("set config mode: %w", err)
	}
	if err := unix.Fchown(temporaryFD, int(t.uid), int(t.gid)); err != nil {
		return false, fmt.Errorf("set config owner: %w", err)
	}
	if err := writeAll(temporary, data); err != nil {
		return false, fmt.Errorf("write config: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		return false, fmt.Errorf("sync config: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return false, fmt.Errorf("close config: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	// Recheck the exact object directly before the commit. A cooperating editor
	// must use the revision contract; a privileged hostile administrator is
	// outside the isolation boundary.
	fd, st, err = t.openChecked(true)
	if err != nil {
		return false, err
	}
	_ = unix.Close(fd)
	if (fileToken{uint64(st.Dev), st.Ino}) != expected {
		return false, protocol.ErrOperationConflict
	}
	if err := unix.Renameat(int(t.directory.Fd()), name, int(t.directory.Fd()), t.name); err != nil {
		return false, fmt.Errorf("replace config: %w", err)
	}
	if err := t.directory.Sync(); err != nil {
		return true, fmt.Errorf("sync config directory: %w", err)
	}
	return true, nil
}

func (t *linuxTarget) createTemporary() (string, int, error) {
	for range 32 {
		var random [12]byte
		if _, err := rand.Read(random[:]); err != nil {
			return "", -1, err
		}
		name := ".almagest-config-" + hex.EncodeToString(random[:])
		fd, err := unix.Openat(int(t.directory.Fd()), name,
			unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
		if err == nil {
			return name, fd, nil
		}
		if !errors.Is(err, unix.EEXIST) {
			return "", -1, fmt.Errorf("create temporary config: %w", err)
		}
	}
	return "", -1, fmt.Errorf("create temporary config: name collisions")
}

func (t *linuxTarget) close() error { return t.directory.Close() }
