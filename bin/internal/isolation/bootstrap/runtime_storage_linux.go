//go:build linux

package bootstrap

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/asciimoth/almagest/bin/internal/isolation/protocol"
	"github.com/asciimoth/almagest/bin/internal/isolation/roleentry"
	"github.com/asciimoth/almagest/bin/internal/snapshotlimits"
	"golang.org/x/sys/unix"
)

// PrepareRuntimeStorage atomically replaces the fixed startup inputs through a
// held directory descriptor. The directory must already be private and owned by
// R's final non-root identity. It can contain persistent application data;
// only the fixed reserved startup names and their staging names are touched.
func PrepareRuntimeStorage(directory *os.File, uid, gid uint32, input RuntimeStorageInput) error {
	if directory == nil || uid == 0 || gid == 0 || uid == ^uint32(0) || gid == ^uint32(0) || len(input.Regular) > protocol.MaxConfigBytes || len(input.TLSPlatform) > MaxImmutableInputBytes {
		return fmt.Errorf("%w: invalid runtime storage input", ErrInvalidInput)
	}
	if err := checkRuntimeDirectory(directory, uid, gid); err != nil {
		return err
	}
	protected, err := input.Protected.RuntimeDocument()
	if err != nil {
		return fmt.Errorf("prepare runtime protected input: %w", err)
	}
	values := []struct {
		name   string
		reader io.Reader
		limit  int64
	}{
		{roleentry.RuntimeRegularConfigFile, bytes.NewReader(input.Regular), protocol.MaxConfigBytes},
		{roleentry.RuntimeProtectedConfigFile, bytes.NewReader(protected), MaxProtectedInputBytes},
	}
	if len(input.TLSPlatform) != 0 {
		values = append(values, struct {
			name   string
			reader io.Reader
			limit  int64
		}{roleentry.RuntimeTLSPlatformFile, bytes.NewReader(input.TLSPlatform), MaxImmutableInputBytes})
	}
	if input.Snapshot != nil {
		values = append(values, struct {
			name   string
			reader io.Reader
			limit  int64
		}{roleentry.RuntimeSnapshotFile, input.Snapshot, snapshotlimits.ArchiveBytes})
	}
	staged := make([]string, 0, len(values))
	cleanup := func() {
		for _, name := range staged {
			_ = unix.Unlinkat(int(directory.Fd()), name, 0)
		}
	}
	defer cleanup()
	for _, value := range values {
		stage := value.name + ".bootstrap-new"
		if err := removeReserved(directory, stage); err != nil {
			return err
		}
		if err := writeRuntimeFile(directory, stage, uid, gid, value.reader, value.limit); err != nil {
			return fmt.Errorf("stage %s: %w", value.name, err)
		}
		staged = append(staged, stage)
	}
	for i, value := range values {
		if err := unix.Renameat(int(directory.Fd()), staged[i], int(directory.Fd()), value.name); err != nil {
			return fmt.Errorf("install %s: %w", value.name, err)
		}
		staged[i] = ""
	}
	if input.Snapshot == nil {
		if err := removeReserved(directory, roleentry.RuntimeSnapshotFile); err != nil {
			return err
		}
	}
	if len(input.TLSPlatform) == 0 {
		if err := removeReserved(directory, roleentry.RuntimeTLSPlatformFile); err != nil {
			return err
		}
	}
	if err := unix.Fsync(int(directory.Fd())); err != nil {
		return fmt.Errorf("sync runtime storage: %w", err)
	}
	return nil
}

func checkRuntimeDirectory(directory *os.File, uid, gid uint32) error {
	var stat unix.Stat_t
	if err := unix.Fstat(int(directory.Fd()), &stat); err != nil {
		return err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFDIR || stat.Mode&0o7777 != 0o700 || stat.Uid != uid || stat.Gid != gid {
		return fmt.Errorf("%w: runtime directory must be private and owned by the final identity", ErrUnsafeInput)
	}
	return nil
}

func removeReserved(directory *os.File, name string) error {
	err := unix.Unlinkat(int(directory.Fd()), name, 0)
	if err == nil || errors.Is(err, unix.ENOENT) {
		return nil
	}
	return fmt.Errorf("remove reserved runtime input %s: %w", name, err)
}

func writeRuntimeFile(directory *os.File, name string, uid, gid uint32, reader io.Reader, limit int64) error {
	fd, err := unix.Openat(int(directory.Fd()), name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return err
	}
	file := os.NewFile(uintptr(fd), name)
	failed := true
	defer func() {
		_ = file.Close()
		if failed {
			_ = unix.Unlinkat(int(directory.Fd()), name, 0)
		}
	}()
	written, err := io.Copy(file, io.LimitReader(reader, limit+1))
	if err != nil {
		return err
	}
	if written > limit {
		return ErrInputTooLarge
	}
	if err := unix.Fchmod(fd, 0o600); err != nil {
		return err
	}
	if err := unix.Fsync(fd); err != nil {
		return err
	}
	// Transfer ownership last. A capability-limited bootstrap can have
	// CAP_CHOWN without CAP_FOWNER, so it cannot change the mode after this
	// point when R has a different identity.
	if err := unix.Fchown(fd, int(uid), int(gid)); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	failed = false
	return nil
}
