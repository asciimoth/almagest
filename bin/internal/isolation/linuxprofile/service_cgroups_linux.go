//go:build linux

package linuxprofile

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"golang.org/x/sys/unix"
)

// ServiceCgroups owns a per-launch delegated subtree. Bootstrap first moves
// itself to a temporary sibling so the empty instance can enable controllers.
// RunLinux later moves it into the network role through RoleCgroups.
type ServiceCgroups struct {
	mu sync.Mutex

	parent                 *os.File
	bootstrap, instance    *os.File
	bootstrapName          string
	instanceName           string
	bootstrapDev           uint64
	bootstrapIno           uint64
	instanceDev            uint64
	instanceIno            uint64
	enabledControllers     []string
	roles                  *RoleCgroups
	closed, restored, gone bool
}

// ProvisionServiceCgroups creates a disposable role subtree below the current
// unified cgroup. The service manager must delegate cpu, memory, and pids. No
// path or cgroup name comes from config or the runtime.
func ProvisionServiceCgroups(plan CgroupPlan) (_ *ServiceCgroups, err error) {
	parent, err := openCurrentCgroup()
	if err != nil {
		return nil, fmt.Errorf("%w: current delegated cgroup: %v", ErrUnavailable, err)
	}
	out := &ServiceCgroups{parent: parent}
	failed := true
	defer func() {
		if failed {
			err = errors.Join(err, out.RestoreAndRemove())
		}
	}()
	suffix, err := randomCgroupSuffix()
	if err != nil {
		return nil, err
	}
	out.bootstrapName = "almagest-bootstrap-" + suffix
	out.instanceName = "almagest-instance-" + suffix
	for _, value := range []struct {
		name string
		file **os.File
		dev  *uint64
		ino  *uint64
	}{{out.bootstrapName, &out.bootstrap, &out.bootstrapDev, &out.bootstrapIno}, {out.instanceName, &out.instance, &out.instanceDev, &out.instanceIno}} {
		if err := unix.Mkdirat(int(parent.Fd()), value.name, 0o755); err != nil {
			return nil, fmt.Errorf("%w: create service cgroup %s: %v", ErrUnavailable, value.name, err)
		}
		file, dev, ino, err := openOwnedCgroupAt(parent, value.name)
		if err != nil {
			return nil, err
		}
		*value.file, *value.dev, *value.ino = file, dev, ino
	}
	if err := writeCgroupFile(out.bootstrap, "cgroup.procs", strconv.Itoa(os.Getpid())+"\n"); err != nil {
		return nil, fmt.Errorf("%w: enter bootstrap cgroup: %v", ErrUnavailable, err)
	}
	control, err := readCgroupFile(parent, "cgroup.subtree_control")
	if err != nil {
		return nil, fmt.Errorf("%w: read delegated controllers: %v", ErrUnavailable, err)
	}
	for _, controller := range []string{"cpu", "memory", "pids"} {
		if !containsWords(control, controller) {
			out.enabledControllers = append(out.enabledControllers, controller)
		}
	}
	if len(out.enabledControllers) != 0 {
		changes := make([]string, len(out.enabledControllers))
		for index, controller := range out.enabledControllers {
			changes[index] = "+" + controller
		}
		if err := writeCgroupFile(parent, "cgroup.subtree_control", strings.Join(changes, " ")+"\n"); err != nil {
			return nil, fmt.Errorf("%w: enable delegated controllers: %v", ErrUnavailable, err)
		}
	}
	out.roles, err = ProvisionCgroups(out.instance, plan)
	if err != nil {
		return nil, err
	}
	failed = false
	return out, nil
}

func randomCgroupSuffix() (string, error) {
	var value [8]byte
	if _, err := io.ReadFull(rand.Reader, value[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(value[:]), nil
}

func openCurrentCgroup() (*os.File, error) {
	data, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		return nil, fmt.Errorf("read /proc/self/cgroup: %w", err)
	}
	if len(data) > 64*1024 {
		return nil, errors.New("/proc/self/cgroup exceeds 64 KiB")
	}
	path := ""
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if strings.HasPrefix(line, "0::/") || line == "0::/" {
			path = strings.TrimPrefix(line, "0::")
			break
		}
	}
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, errors.New("unified cgroup path is absent or invalid")
	}
	fd, err := unix.Open("/sys/fs/cgroup", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	for _, part := range strings.Split(strings.TrimPrefix(path, "/"), "/") {
		if part == "" {
			continue
		}
		if part == "." || part == ".." {
			_ = unix.Close(fd)
			return nil, errors.New("invalid unified cgroup component")
		}
		next, openErr := unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		_ = unix.Close(fd)
		if openErr != nil {
			return nil, openErr
		}
		fd = next
	}
	file := os.NewFile(uintptr(fd), "current-service-cgroup")
	var fs unix.Statfs_t
	if err := unix.Fstatfs(fd, &fs); err != nil || fs.Type != unix.CGROUP2_SUPER_MAGIC {
		_ = file.Close()
		return nil, errors.New("current hierarchy is not cgroup v2")
	}
	return file, nil
}

func openOwnedCgroupAt(parent *os.File, name string) (*os.File, uint64, uint64, error) {
	fd, err := unix.Openat(int(parent.Fd()), name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, 0, 0, err
	}
	file := os.NewFile(uintptr(fd), name)
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		_ = file.Close()
		return nil, 0, 0, err
	}
	return file, uint64(stat.Dev), stat.Ino, nil
}

// Roles returns the launch-owned role cgroups. The caller does not own it.
func (c *ServiceCgroups) Roles() *RoleCgroups {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.roles
}

// RestoreAndRemove first parks N in its bootstrap sibling, removes the role
// subtree, restores the parent's controller state, and only then moves N back.
// A cgroup with domain controllers enabled cannot contain an internal process.
func (c *ServiceCgroups) RestoreAndRemove() error {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.gone {
		return nil
	}
	var result error
	if c.bootstrap != nil && !c.restored {
		if err := writeCgroupFile(c.bootstrap, "cgroup.procs", strconv.Itoa(os.Getpid())+"\n"); err != nil {
			result = errors.Join(result, fmt.Errorf("park service process: %w", err))
		}
	}
	if c.roles != nil {
		if err := c.roles.Remove(); err != nil {
			result = errors.Join(result, err)
		}
	}
	remove := func(value struct {
		name     string
		dev, ino uint64
	}) {
		if value.name == "" {
			return
		}
		var stat unix.Stat_t
		if err := unix.Fstatat(int(c.parent.Fd()), value.name, &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			if !errors.Is(err, unix.ENOENT) {
				result = errors.Join(result, err)
			}
			return
		}
		if uint64(stat.Dev) != value.dev || stat.Ino != value.ino {
			result = errors.Join(result, fmt.Errorf("%w: service cgroup identity changed", ErrUnavailable))
			return
		}
		if err := unix.Unlinkat(int(c.parent.Fd()), value.name, unix.AT_REMOVEDIR); err != nil {
			result = errors.Join(result, err)
		}
	}
	remove(struct {
		name     string
		dev, ino uint64
	}{c.instanceName, c.instanceDev, c.instanceIno})
	if len(c.enabledControllers) != 0 {
		changes := make([]string, len(c.enabledControllers))
		for index, controller := range c.enabledControllers {
			changes[index] = "-" + controller
		}
		if err := writeCgroupFile(c.parent, "cgroup.subtree_control", strings.Join(changes, " ")+"\n"); err != nil {
			result = errors.Join(result, fmt.Errorf("restore delegated controllers: %w", err))
		}
	}
	if c.parent != nil && !c.restored {
		if err := writeCgroupFile(c.parent, "cgroup.procs", strconv.Itoa(os.Getpid())+"\n"); err != nil {
			result = errors.Join(result, fmt.Errorf("restore service cgroup: %w", err))
		} else {
			c.restored = true
		}
	}
	if c.restored {
		remove(struct {
			name     string
			dev, ino uint64
		}{c.bootstrapName, c.bootstrapDev, c.bootstrapIno})
	}
	if result == nil {
		c.gone = true
	}
	return result
}

// Close closes held descriptors without changing membership or removing state.
func (c *ServiceCgroups) Close() error {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil
	}
	c.closed = true
	var result error
	if c.roles != nil {
		result = errors.Join(result, c.roles.Close())
	}
	for _, file := range []*os.File{c.instance, c.bootstrap, c.parent} {
		if file != nil {
			result = errors.Join(result, file.Close())
		}
	}
	return result
}
