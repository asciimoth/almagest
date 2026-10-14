//go:build linux

package linuxprofile

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"

	"golang.org/x/sys/unix"
)

const cgroupPeriod = uint64(100000)

// CgroupPlan defines separate resource domains for trusted N and restricted R,
// C, and H. Backend and Runtime are required. Writer and Command are optional.
// The instance cgroup supplied to ProvisionCgroups must be empty and delegated
// by the service manager or administrator.
type CgroupPlan struct {
	Backend Limits
	Runtime Limits
	Writer  *Limits
	Command *Limits
}

func DefaultCgroupPlan() CgroupPlan {
	limits := DefaultLimits()
	return CgroupPlan{Backend: limits, Runtime: limits}
}

func (p CgroupPlan) validate() error {
	for _, limits := range []*Limits{&p.Backend, &p.Runtime, p.Writer, p.Command} {
		if limits != nil {
			if err := limits.Validate(); err != nil {
				return err
			}
		}
	}
	return nil
}

type provisionedCgroup struct {
	name string
	file *os.File
	dev  uint64
	ino  uint64
}

// RoleCgroups owns the held cgroup descriptors created by ProvisionCgroups.
// File returns a borrowed descriptor. Close only closes descriptors. Remove
// additionally removes empty, identity-matching role cgroups.
type RoleCgroups struct {
	mu      sync.Mutex
	parent  *os.File
	roles   map[Role]*provisionedCgroup
	closed  bool
	removed bool
}

// ProvisionCgroups creates fixed-name role cgroups below an empty held cgroup
// v2 directory. It uses only descriptor-relative operations. It never moves a
// process; restricted children enter their role cgroups with CLONE_INTO_CGROUP,
// and bootstrap calls MoveCurrent(Network) only after all children start.
func ProvisionCgroups(instance *os.File, plan CgroupPlan) (_ *RoleCgroups, err error) {
	if instance == nil {
		return nil, fmt.Errorf("%w: missing cgroup instance", ErrUnavailable)
	}
	if err := plan.validate(); err != nil {
		return nil, err
	}
	var fs unix.Statfs_t
	if err := unix.Fstatfs(int(instance.Fd()), &fs); err != nil || fs.Type != unix.CGROUP2_SUPER_MAGIC {
		return nil, fmt.Errorf("%w: cgroup v2 instance required", ErrUnavailable)
	}
	if members, err := readCgroupFile(instance, "cgroup.procs"); err != nil {
		return nil, fmt.Errorf("%w: read instance members: %v", ErrUnavailable, err)
	} else if strings.TrimSpace(members) != "" {
		return nil, fmt.Errorf("%w: cgroup instance must contain no processes", ErrUnavailable)
	}
	roles := []struct {
		role   Role
		name   string
		limits *Limits
		weight uint64
	}{
		{Network, "network", &plan.Backend, 1000},
		{Runtime, "runtime", &plan.Runtime, 100},
		{Writer, "writer", plan.Writer, 50},
		{Command, "command", plan.Command, 50},
	}
	var memory, tasks uint64
	for _, role := range roles {
		if role.limits == nil {
			continue
		}
		var overflow bool
		memory, overflow = addBounded(memory, role.limits.MemoryBytes)
		if overflow {
			return nil, fmt.Errorf("%w: aggregate cgroup memory overflow", ErrUnavailable)
		}
		tasks, overflow = addBounded(tasks, role.limits.Tasks)
		if overflow {
			return nil, fmt.Errorf("%w: aggregate cgroup task overflow", ErrUnavailable)
		}
	}
	if err := requireCgroupCapacity(instance, "memory.max", memory); err != nil {
		return nil, fmt.Errorf("%w: instance memory capacity: %v", ErrUnavailable, err)
	}
	if err := requireCgroupCapacity(instance, "pids.max", tasks); err != nil {
		return nil, fmt.Errorf("%w: instance task capacity: %v", ErrUnavailable, err)
	}
	controllers, err := readCgroupFile(instance, "cgroup.controllers")
	if err != nil || !containsWords(controllers, "cpu", "memory", "pids") {
		return nil, fmt.Errorf("%w: cpu, memory, and pids controllers must be delegated", ErrUnavailable)
	}
	if err := writeCgroupFile(instance, "cgroup.subtree_control", "+cpu +memory +pids\n"); err != nil {
		return nil, fmt.Errorf("%w: enable role controllers: %v", ErrUnavailable, err)
	}

	out := &RoleCgroups{parent: instance, roles: make(map[Role]*provisionedCgroup)}
	failed := true
	defer func() {
		if failed {
			err = errors.Join(err, out.Remove())
		}
	}()
	for _, role := range roles {
		if role.limits == nil {
			continue
		}
		if err := unix.Mkdirat(int(instance.Fd()), role.name, 0o755); err != nil {
			return nil, fmt.Errorf("%w: create %s cgroup: %v", ErrUnavailable, role.name, err)
		}
		fd, err := unix.Openat(int(instance.Fd()), role.name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if err != nil {
			_ = unix.Unlinkat(int(instance.Fd()), role.name, unix.AT_REMOVEDIR)
			return nil, fmt.Errorf("%w: open %s cgroup: %v", ErrUnavailable, role.name, err)
		}
		file := os.NewFile(uintptr(fd), role.name)
		var stat unix.Stat_t
		if err := unix.Fstat(fd, &stat); err != nil {
			_ = file.Close()
			_ = unix.Unlinkat(int(instance.Fd()), role.name, unix.AT_REMOVEDIR)
			return nil, err
		}
		entry := &provisionedCgroup{name: role.name, file: file, dev: uint64(stat.Dev), ino: stat.Ino}
		out.roles[role.role] = entry
		if err := configureRoleCgroup(file, *role.limits, role.weight); err != nil {
			return nil, fmt.Errorf("%w: configure %s cgroup: %v", ErrUnavailable, role.name, err)
		}
	}
	failed = false
	return out, nil
}

func addBounded(a, b uint64) (uint64, bool) {
	if ^uint64(0)-a < b {
		return 0, true
	}
	return a + b, false
}

func requireCgroupCapacity(directory *os.File, name string, required uint64) error {
	value, err := readCgroupFile(directory, name)
	if err != nil {
		return err
	}
	value = strings.TrimSpace(value)
	if value == "max" {
		return nil
	}
	available, err := strconv.ParseUint(value, 10, 64)
	if err != nil || available < required {
		return fmt.Errorf("requires %d, available %q", required, value)
	}
	return nil
}

func containsWords(value string, required ...string) bool {
	words := make(map[string]bool)
	for _, word := range strings.Fields(value) {
		words[word] = true
	}
	for _, word := range required {
		if !words[word] {
			return false
		}
	}
	return true
}

func configureRoleCgroup(directory *os.File, limits Limits, weight uint64) error {
	values := []struct{ name, value string }{
		{"memory.max", strconv.FormatUint(limits.MemoryBytes, 10) + "\n"},
		{"memory.swap.max", "0\n"},
		{"memory.oom.group", "1\n"},
		{"pids.max", strconv.FormatUint(limits.Tasks, 10) + "\n"},
		{"cpu.max", fmt.Sprintf("%d %d\n", limits.CPUQuota, cgroupPeriod)},
		{"cpu.weight", strconv.FormatUint(weight, 10) + "\n"},
	}
	for _, value := range values {
		if err := writeCgroupFile(directory, value.name, value.value); err != nil {
			return fmt.Errorf("%s: %w", value.name, err)
		}
	}
	return nil
}

func readCgroupFile(directory *os.File, name string) (string, error) {
	fd, err := unix.Openat(int(directory.Fd()), name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if err != nil {
		return "", err
	}
	file := os.NewFile(uintptr(fd), name)
	defer file.Close() //nolint:errcheck
	data, err := io.ReadAll(io.LimitReader(file, 65537))
	if err != nil {
		return "", err
	}
	if len(data) > 65536 {
		return "", protocolLimitError(name)
	}
	return string(data), nil
}

func protocolLimitError(name string) error { return fmt.Errorf("%s exceeds 64 KiB", name) }

func writeCgroupFile(directory *os.File, name, value string) error {
	fd, err := unix.Openat(int(directory.Fd()), name, unix.O_WRONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	file := os.NewFile(uintptr(fd), name)
	_, writeErr := io.WriteString(file, value)
	closeErr := file.Close()
	return errors.Join(writeErr, closeErr)
}

// File returns a borrowed role descriptor for CommandContext or
// PrepareBackend. The caller must not close it.
func (c *RoleCgroups) File(role Role) (*os.File, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed || c.removed {
		return nil, os.ErrClosed
	}
	entry := c.roles[role]
	if entry == nil {
		return nil, fmt.Errorf("%w: role cgroup is not provisioned", ErrUnavailable)
	}
	return entry.file, nil
}

// MoveCurrent moves only the calling process into a provisioned role cgroup.
// Trusted bootstrap uses Network after restricted children are started.
func (c *RoleCgroups) MoveCurrent(role Role) error {
	file, err := c.File(role)
	if err != nil {
		return err
	}
	return writeCgroupFile(file, "cgroup.procs", strconv.Itoa(os.Getpid())+"\n")
}

// EnterBackend moves trusted bootstrap to N's cgroup and completes backend
// preparation. Call it only after all restricted children have started. Any
// error is fatal because the process or its hard limits may already differ.
func (c *RoleCgroups) EnterBackend(state *os.File, limits Limits) error {
	file, err := c.File(Network)
	if err != nil {
		return err
	}
	if err := writeCgroupFile(file, "cgroup.procs", strconv.Itoa(os.Getpid())+"\n"); err != nil {
		return fmt.Errorf("%w: enter backend cgroup: %v", ErrUnavailable, err)
	}
	return PrepareBackend(state, file, limits)
}

func (c *RoleCgroups) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil
	}
	c.closed = true
	var result error
	for _, entry := range c.roles {
		result = errors.Join(result, entry.file.Close())
	}
	return result
}

// Remove removes only empty role cgroups whose current directory identity
// still matches the descriptor opened at provisioning time.
func (c *RoleCgroups) Remove() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.removed {
		return nil
	}
	var result error
	for _, role := range []Role{Command, Writer, Runtime, Network} {
		entry := c.roles[role]
		if entry == nil {
			continue
		}
		var stat unix.Stat_t
		if err := unix.Fstatat(int(c.parent.Fd()), entry.name, &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			result = errors.Join(result, err)
			continue
		}
		if uint64(stat.Dev) != entry.dev || stat.Ino != entry.ino {
			result = errors.Join(result, fmt.Errorf("%w: %s cgroup identity changed", ErrUnavailable, entry.name))
			continue
		}
		if err := unix.Unlinkat(int(c.parent.Fd()), entry.name, unix.AT_REMOVEDIR); err != nil {
			result = errors.Join(result, err)
		}
	}
	if result == nil {
		c.removed = true
	}
	if !c.closed {
		c.closed = true
		for _, entry := range c.roles {
			result = errors.Join(result, entry.file.Close())
		}
	}
	return result
}
