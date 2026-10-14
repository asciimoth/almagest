//go:build linux

package linuxprofile

import (
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

// PrepareBackend completes trusted bootstrap in place, without exec, namespace
// entry, or credential changes. The restricted launcher accepts only R/C/H;
// N retains the host context required by DNS and process filtering.
// This function does not sandbox node code. Only the
// trusted, node-free bootstrap executable may call it, after starting restricted
// children and before accepting their requests.
//
// Bootstrap owns both held directories. The state directory must be private to
// N's effective identity. The current process must already belong to the finite
// cgroup supplied by deployment. No missing authority is obtained. On any error
// bootstrap must exit: a resource-limit change may already have taken effect.
// The caller retains ownership of the descriptors.
func PrepareBackend(state, cgroup *os.File, limits Limits) error {
	if err := limits.Validate(); err != nil {
		return err
	}
	if state == nil || cgroup == nil {
		return fmt.Errorf("%w: missing backend directories", ErrUnavailable)
	}
	var st unix.Stat_t
	if err := unix.Fstat(int(state.Fd()), &st); err != nil {
		return fmt.Errorf("%w: backend state: %v", ErrUnavailable, err)
	}
	if st.Mode&unix.S_IFMT != unix.S_IFDIR || st.Mode&07777 != 0700 || st.Uid != uint32(os.Geteuid()) {
		return fmt.Errorf("%w: backend state must be a private owned directory", ErrUnavailable)
	}
	if err := checkBackendCgroup(cgroup, limits); err != nil {
		return fmt.Errorf("%w: backend cgroup: %v", ErrUnavailable, err)
	}
	for _, item := range []struct {
		resource int
		maximum  uint64
	}{{unix.RLIMIT_CORE, 0}, {unix.RLIMIT_NOFILE, limits.Descriptors}, {unix.RLIMIT_FSIZE, limits.FileBytes}} {
		var old unix.Rlimit
		if err := unix.Getrlimit(item.resource, &old); err != nil {
			return fmt.Errorf("%w: read backend limit: %v", ErrUnavailable, err)
		}
		maximum := min(item.maximum, old.Max)
		// Use the standard library so os/exec also forgets Go's startup copy
		// of RLIMIT_NOFILE. Otherwise a later utility launch can try to restore
		// an old hard limit and fail after this irreversible reduction.
		if err := syscall.Setrlimit(item.resource, &syscall.Rlimit{Cur: maximum, Max: maximum}); err != nil {
			return fmt.Errorf("%w: set backend limit: %v", ErrUnavailable, err)
		}
	}
	// In contrast to R, leave MEMLOCK unchanged: loading native BPF objects can
	// require the launch limit on supported kernels. Keep native utility exec,
	// process inspection, mounts and all credentials at their launch values.
	if err := unix.Fchdir(int(state.Fd())); err != nil {
		return fmt.Errorf("%w: backend working directory: %v", ErrUnavailable, err)
	}
	return nil
}

func checkBackendCgroup(directory *os.File, limits Limits) error {
	var fs unix.Statfs_t
	if err := unix.Fstatfs(int(directory.Fd()), &fs); err != nil {
		return err
	}
	if fs.Type != unix.CGROUP2_SUPER_MAGIC {
		return fmt.Errorf("cgroup v2 required")
	}
	read := func(name string) (string, error) {
		fd, err := unix.Openat(int(directory.Fd()), name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
		if err != nil {
			return "", err
		}
		f := os.NewFile(uintptr(fd), name)
		defer f.Close() //nolint:errcheck
		data, err := io.ReadAll(io.LimitReader(f, 65537))
		if err != nil {
			return "", err
		}
		if len(data) > 65536 {
			return "", fmt.Errorf("cgroup value exceeds bound")
		}
		return string(data), nil
	}
	values := make(map[string]string)
	for _, name := range []string{"memory.max", "memory.swap.max", "pids.max", "cpu.max", "cgroup.procs"} {
		value, err := read(name)
		if err != nil {
			return err
		}
		values[name] = value
	}
	return validateBackendCgroup(values, limits, os.Getpid())
}

func validateBackendCgroup(values map[string]string, limits Limits, pid int) error {
	decimal := func(value string) (uint64, error) {
		value = strings.TrimSuffix(value, "\n")
		if len(value) == 0 || len(value) > 20 {
			return 0, fmt.Errorf("invalid cgroup number")
		}
		for _, c := range value {
			if c < '0' || c > '9' {
				return 0, fmt.Errorf("invalid cgroup number")
			}
		}
		return strconv.ParseUint(value, 10, 64)
	}
	for name, maximum := range map[string]uint64{"memory.max": limits.MemoryBytes, "pids.max": limits.Tasks} {
		n, err := decimal(values[name])
		if err != nil || n == 0 || n > maximum {
			return fmt.Errorf("unmet %s ceiling", name)
		}
	}
	if n, err := decimal(values["memory.swap.max"]); err != nil || n != 0 {
		return fmt.Errorf("swap must be disabled")
	}
	quota, period, ok := strings.Cut(strings.TrimSuffix(values["cpu.max"], "\n"), " ")
	n, err := decimal(quota)
	if !ok || err != nil || n == 0 || n > limits.CPUQuota || period != "100000" {
		return fmt.Errorf("unmet CPU ceiling")
	}
	found := false
	for _, line := range strings.Split(strings.TrimSuffix(values["cgroup.procs"], "\n"), "\n") {
		n, err := decimal(line)
		if err != nil || n == 0 {
			return fmt.Errorf("invalid cgroup member")
		}
		found = found || pid > 0 && n == uint64(pid)
	}
	if !found {
		return fmt.Errorf("backend must already belong to supplied cgroup")
	}
	return nil
}
