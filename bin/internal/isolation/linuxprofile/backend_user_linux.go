//go:build linux

package linuxprofile

import (
	"fmt"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

// PrepareUserBackend applies the limits that do not require cgroup delegation.
// N keeps its launch capabilities because it owns native network operations.
func PrepareUserBackend(state *os.File, limits Limits) error {
	if state == nil || limits.Validate() != nil {
		return fmt.Errorf("%w: invalid user backend plan", ErrUnavailable)
	}
	var st unix.Stat_t
	if err := unix.Fstat(int(state.Fd()), &st); err != nil || st.Mode&unix.S_IFMT != unix.S_IFDIR ||
		st.Mode&0o7777 != 0o700 || st.Uid != uint32(os.Geteuid()) || st.Gid != uint32(os.Getegid()) {
		return fmt.Errorf("%w: user backend state must be a private owned directory", ErrUnavailable)
	}
	for _, item := range []struct {
		resource int
		maximum  uint64
	}{{unix.RLIMIT_CORE, 0}, {unix.RLIMIT_NOFILE, limits.Descriptors}, {unix.RLIMIT_FSIZE, limits.FileBytes}} {
		var old unix.Rlimit
		if err := unix.Getrlimit(item.resource, &old); err != nil {
			return fmt.Errorf("%w: read user backend limit: %v", ErrUnavailable, err)
		}
		maximum := min(item.maximum, old.Max)
		if err := syscall.Setrlimit(item.resource, &syscall.Rlimit{Cur: maximum, Max: maximum}); err != nil {
			return fmt.Errorf("%w: set user backend limit: %v", ErrUnavailable, err)
		}
	}
	if err := unix.Fchdir(int(state.Fd())); err != nil {
		return fmt.Errorf("%w: user backend working directory: %v", ErrUnavailable, err)
	}
	return nil
}
