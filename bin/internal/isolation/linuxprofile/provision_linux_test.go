//go:build linux

package linuxprofile

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestCgroupPlanValidation(t *testing.T) {
	plan := DefaultCgroupPlan()
	if err := plan.validate(); err != nil {
		t.Fatal(err)
	}
	bad := plan
	bad.Runtime.MemoryBytes = 0
	if err := bad.validate(); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("invalid runtime limits: %v", err)
	}
	writer := DefaultLimits()
	plan.Writer = &writer
	if err := plan.validate(); err != nil {
		t.Fatal(err)
	}
	if total, overflow := addBounded(^uint64(0), 1); total != 0 || !overflow {
		t.Fatal("aggregate overflow accepted")
	}
	if !containsWords("memory pids cpu io", "cpu", "memory", "pids") || containsWords("cpu memory", "cpu", "memory", "pids") {
		t.Fatal("controller set validation failed")
	}
}

func TestProvisionCgroupsRejectsUnsafeInputs(t *testing.T) {
	if _, err := ProvisionCgroups(nil, DefaultCgroupPlan()); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("nil instance: %v", err)
	}
	directory, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer directory.Close() //nolint:errcheck
	if _, err := ProvisionCgroups(directory, DefaultCgroupPlan()); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("regular directory: %v", err)
	}
}

func TestNativeProvisionRoleCgroups(t *testing.T) {
	if os.Getenv("ALMAGEST_CGROUP_ROLE_CHILD") == "1" {
		data, err := os.ReadFile("/proc/self/cgroup")
		if err != nil {
			t.Fatal(err)
		}
		fmt.Print(string(data))
		return
	}
	if os.Getenv("ALMAGEST_PROFILE_CGROUP_PROVISION") != "1" {
		t.Skip("requires a disposable private cgroup namespace")
	}

	root, err := os.Open("/sys/fs/cgroup")
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close() //nolint:errcheck
	bootstrapName := "almagest-bootstrap-" + strconv.Itoa(os.Getpid())
	instanceName := "almagest-instance-" + strconv.Itoa(os.Getpid())
	for _, name := range []string{bootstrapName, instanceName} {
		if err := unix.Mkdirat(int(root.Fd()), name, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	bootstrap, err := openCgroupAt(root, bootstrapName)
	if err != nil {
		t.Fatal(err)
	}
	defer bootstrap.Close() //nolint:errcheck
	if err := writeCgroupFile(bootstrap, "cgroup.procs", strconv.Itoa(os.Getpid())+"\n"); err != nil {
		t.Fatal(err)
	}
	if err := writeCgroupFile(root, "cgroup.subtree_control", "+cpu +memory +pids\n"); err != nil {
		t.Fatal(err)
	}
	instance, err := openCgroupAt(root, instanceName)
	if err != nil {
		t.Fatal(err)
	}
	defer instance.Close() //nolint:errcheck

	limits := DefaultLimits()
	limits.MemoryBytes = 64 << 20
	limits.Tasks = 16
	limits.CPUQuota = 25000
	cgroups, err := ProvisionCgroups(instance, CgroupPlan{Backend: limits, Runtime: limits})
	if err != nil {
		t.Fatal(err)
	}
	defer cgroups.Close() //nolint:errcheck
	for _, role := range []Role{Network, Runtime} {
		file, err := cgroups.File(role)
		if err != nil {
			t.Fatal(err)
		}
		for name, want := range map[string]string{
			"memory.max":       "67108864",
			"memory.swap.max":  "0",
			"memory.oom.group": "1",
			"pids.max":         "16",
			"cpu.max":          "25000 100000",
		} {
			got, err := readCgroupFile(file, name)
			if err != nil || strings.TrimSpace(got) != want {
				t.Fatalf("%s %s = %q, want %q: %v", role, name, got, want, err)
			}
		}
	}
	if _, err := cgroups.File(Writer); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("unprovisioned writer: %v", err)
	}

	runtimeGroup, err := cgroups.File(Runtime)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestNativeProvisionRoleCgroups$")
	cmd.Env = append(os.Environ(), "ALMAGEST_CGROUP_ROLE_CHILD=1")
	cmd.SysProcAttr = &syscall.SysProcAttr{UseCgroupFD: true, CgroupFD: int(runtimeGroup.Fd())}
	output, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(output), "/"+instanceName+"/runtime") {
		t.Fatalf("runtime was not born in its cgroup: %v\n%s", err, output)
	}
	state, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close() //nolint:errcheck
	if err := state.Chmod(0o700); err != nil {
		t.Fatal(err)
	}
	if err := cgroups.EnterBackend(state, limits); err != nil {
		t.Fatal(err)
	}
	networkGroup, err := cgroups.File(Network)
	if err != nil {
		t.Fatal(err)
	}
	members, err := readCgroupFile(networkGroup, "cgroup.procs")
	if err != nil || !containsWords(members, strconv.Itoa(os.Getpid())) {
		t.Fatalf("backend membership = %q: %v", members, err)
	}
	if err := writeCgroupFile(bootstrap, "cgroup.procs", strconv.Itoa(os.Getpid())+"\n"); err != nil {
		t.Fatal(err)
	}
	if err := cgroups.Remove(); err != nil {
		t.Fatal(err)
	}
	if _, err := cgroups.File(Runtime); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("removed cgroups remain accessible: %v", err)
	}
	if err := unix.Unlinkat(int(root.Fd()), instanceName, unix.AT_REMOVEDIR); err != nil {
		t.Fatal(err)
	}
	t.Log("separate backend and runtime cgroups provisioned; child entered atomically and backend moved after launch")
}

func openCgroupAt(parent *os.File, name string) (*os.File, error) {
	fd, err := unix.Openat(int(parent.Fd()), name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), name), nil
}
