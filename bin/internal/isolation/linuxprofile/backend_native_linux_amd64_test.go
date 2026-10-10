//go:build linux && amd64

package linuxprofile

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func backendThreadState(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(fmt.Sprintf("/proc/self/task/%d/status", unix.Gettid()))
	if err != nil {
		t.Fatal(err)
	}
	var result strings.Builder
	for _, line := range strings.Split(string(data), "\n") {
		for _, prefix := range []string{"Uid:", "Gid:", "Groups:", "Cap", "NoNewPrivs:", "Seccomp:"} {
			if strings.HasPrefix(line, prefix) {
				result.WriteString(line + "\n")
			}
		}
	}
	for _, namespace := range []string{"net", "mnt", "pid", "user", "ipc", "uts"} {
		value, err := os.Readlink("/proc/self/ns/" + namespace)
		if err != nil {
			t.Fatal(err)
		}
		result.WriteString(namespace + ":" + value + "\n")
	}
	return result.String()
}

func TestNativePrepareBackend(t *testing.T) {
	nativeEnvironment(t)
	if os.Getenv("ALMAGEST_BACKEND_PREPARE_CHILD") != "1" {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestNativePrepareBackend$", "-test.v")
		cmd.Env = append(os.Environ(), "ALMAGEST_BACKEND_PREPARE_CHILD=1")
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("backend preparation: %v\n%s", err, output)
		}
		t.Logf("%s", output)
		return
	}
	// Only this disposable child changes cwd or lowers process resource limits.
	state, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close() //nolint:errcheck
	if err := state.Chmod(0700); err != nil {
		t.Fatal(err)
	}
	cgroup, err := os.Open("/sys/fs/cgroup")
	if err != nil {
		t.Fatal(err)
	}
	defer cgroup.Close() //nolint:errcheck
	var memlock unix.Rlimit
	if err := unix.Getrlimit(unix.RLIMIT_MEMLOCK, &memlock); err != nil {
		t.Fatal(err)
	}
	ready := make(chan struct{}, 8)
	prepared := make(chan struct{})
	var threads sync.WaitGroup
	for range 8 {
		threads.Go(func() {
			runtime.LockOSThread()
			defer runtime.UnlockOSThread()
			before := backendThreadState(t)
			ready <- struct{}{}
			<-prepared
			if after := backendThreadState(t); before != after {
				t.Errorf("backend thread authority changed\nbefore: %s\nafter: %s", before, after)
			}
		})
	}
	for range 8 {
		<-ready
	}
	err = PrepareBackend(state, cgroup, DefaultLimits())
	close(prepared)
	threads.Wait()
	if err != nil {
		t.Fatal(err)
	}
	var after unix.Rlimit
	if err := unix.Getrlimit(unix.RLIMIT_MEMLOCK, &after); err != nil || after != memlock {
		t.Fatal("BPF memlock limit changed", err)
	}
	for resource, maximum := range map[int]uint64{unix.RLIMIT_CORE: 0, unix.RLIMIT_NOFILE: DefaultLimits().Descriptors, unix.RLIMIT_FSIZE: DefaultLimits().FileBytes} {
		if err := unix.Getrlimit(resource, &after); err != nil || after.Cur > maximum || after.Max > maximum {
			t.Fatal("resource ceiling not applied", resource, after, err)
		}
	}
	if path, err := os.Getwd(); err != nil || path != state.Name() {
		t.Fatal("backend working directory not fixed", path, err)
	}
	if err := exec.Command("/bin/true").Run(); err != nil {
		t.Fatal("backend utility exec denied", err)
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal("backend native socket denied", err)
	}
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	t.Log("backend credentials and host namespaces preserved on eight threads; native socket and utility exec passed")
}
