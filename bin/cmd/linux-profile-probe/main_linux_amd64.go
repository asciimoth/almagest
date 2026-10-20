//go:build linux && amd64

// linux-profile-probe exercises the production restriction component only in
// Docker. It is not a daemon role and does not change host network settings.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"time"
	"unsafe"

	"github.com/asciimoth/almagest/bin/internal/isolation/commandhelper"
	"github.com/asciimoth/almagest/bin/internal/isolation/configwriter"
	"github.com/asciimoth/almagest/bin/internal/isolation/linuxprofile"
	"github.com/asciimoth/almagest/bin/internal/isolation/protocol"
	bolt "go.etcd.io/bbolt"
	"golang.org/x/sys/unix"
)

type report struct {
	Role      linuxprofile.Role
	Threads   []int
	Checks    map[string]bool
	Errors    map[string]string
	GoVersion string
}

var output = report{Checks: map[string]bool{}, Errors: map[string]string{}, GoVersion: runtime.Version()}
var mu sync.Mutex

// Set only by the qualification build, never by a role request or environment.
var probeMode = "normal"

func tsyncFailure() {
	ready := make(chan error, 1)
	stop := make(chan struct{})
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		filter := []unix.SockFilter{{Code: unix.BPF_RET | unix.BPF_K, K: unix.SECCOMP_RET_ALLOW}}
		program := unix.SockFprog{Len: 1, Filter: &filter[0]}
		_, _, errno := unix.RawSyscall(unix.SYS_SECCOMP, unix.SECCOMP_SET_MODE_FILTER, 0, uintptr(unsafe.Pointer(&program)))
		runtime.KeepAlive(filter)
		if errno != 0 {
			ready <- errno
		} else {
			ready <- nil
		}
		<-stop
	}()
	if err := <-ready; err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	err := linuxprofile.Seal(linuxprofile.Runtime)
	close(stop)
	if !errors.Is(err, linuxprofile.ErrUnavailable) {
		fmt.Fprintln(os.Stderr, "TSYNC accepted a diverged thread")
		os.Exit(1)
	}
	fmt.Println("TSYNC divergence rejected:", err)
}

func check(name string, ok bool, err error) {
	mu.Lock()
	defer mu.Unlock()
	output.Checks[name] = ok
	if !ok && err != nil {
		output.Errors[name] = err.Error()
	}
}

func denied(err error) bool {
	return errors.Is(err, unix.EPERM) || errors.Is(err, unix.EACCES) || errors.Is(err, unix.ENOENT)
}

func threadChecks(index int, role linuxprofile.Role) {
	userProfile := os.Getenv("ALMAGEST_LINUX_PROFILE") == string(linuxprofile.ProfileUser)
	prefix := fmt.Sprintf("thread-%d-", index)
	for _, path := range []string{"/blocked", "/proc/self/status", "/sys/kernel", "/run/dbus/system_bus_socket", "/etc/passwd", "/dev/net/tun", "/oldroot/etc/passwd"} {
		f, err := os.Open(path)
		if f != nil {
			_ = f.Close()
		}
		check(prefix+path, denied(err), err)
	}
	for _, call := range []uintptr{unix.SYS_MOUNT, unix.SYS_SETNS, unix.SYS_UNSHARE, unix.SYS_PTRACE,
		unix.SYS_PROCESS_VM_READV, unix.SYS_PROCESS_VM_WRITEV, unix.SYS_PIDFD_GETFD, unix.SYS_PIDFD_OPEN,
		unix.SYS_BPF, unix.SYS_PERF_EVENT_OPEN, unix.SYS_IO_URING_SETUP, unix.SYS_KEYCTL,
		unix.SYS_FORK, unix.SYS_VFORK, unix.SYS_EXECVEAT, unix.SYS_IOCTL} {
		_, _, err := unix.RawSyscall6(call, 0, 0, 0, 0, 0, 0)
		check(prefix+fmt.Sprintf("syscall-%d", call), err == unix.EPERM || call&0x40000000 != 0 && err == unix.ENOSYS, err)
	}
	if role != linuxprofile.Command {
		_, _, err := unix.RawSyscall(unix.SYS_EXECVE, 0, 0, 0)
		check(prefix+"exec", err == unix.EPERM, err)
	}
	for _, domain := range []int{unix.AF_INET, unix.AF_INET6, unix.AF_UNIX, unix.AF_NETLINK, unix.AF_PACKET} {
		fd, err := unix.Socket(domain, unix.SOCK_STREAM, 0)
		if fd >= 0 {
			_ = unix.Close(fd)
		}
		check(prefix+fmt.Sprintf("socket-%d", domain), errors.Is(err, unix.EPERM), err)
	}
	h := unix.CapUserHeader{Version: unix.LINUX_CAPABILITY_VERSION_3}
	d := [2]unix.CapUserData{}
	err := unix.Capget(&h, &d[0])
	want, _ := strconv.ParseUint(os.Getenv("ALMAGEST_RETAINED_CAPS"), 10, 64)
	actual := uint64(d[0].Effective) | uint64(d[1].Effective)<<32
	check(prefix+"capabilities", err == nil && actual == want &&
		d[0].Effective == d[0].Permitted && d[1].Effective == d[1].Permitted &&
		d[0].Inheritable == d[0].Permitted && d[1].Inheritable == d[1].Permitted &&
		actual == 0, err)
	if !userProfile {
		for cap := uintptr(0); cap < 41; cap++ {
			v, _, e := unix.RawSyscall(unix.SYS_PRCTL, unix.PR_CAPBSET_READ, cap, 0)
			check(prefix+fmt.Sprintf("bounding-%d", cap), e == 0 && v == uintptr((want>>cap)&1), e)
		}
	}
	check(prefix+"identity", os.Getuid() == 65534 && os.Geteuid() == 65534 && os.Getgid() == 65534, nil)
	groups, err := os.Getgroups()
	check(prefix+"groups", err == nil && (userProfile || len(groups) == 0), err)
	bits, _, e := unix.RawSyscall(unix.SYS_PRCTL, unix.PR_GET_SECUREBITS, 0, 0)
	check(prefix+"securebits", e == 0 && (userProfile || bits == 0xef), e)
}

func writer(state string) error {
	path := filepath.Join(state, "config")
	if err := os.WriteFile(path, []byte("before"), 0600); err != nil {
		return err
	}
	s := protocol.Session{ID: protocol.SessionID{1}, Epoch: 1, Version: protocol.Version}
	w, err := configwriter.New(s, path)
	if err != nil {
		return err
	}
	defer w.Close() //nolint:errcheck
	req := protocol.ReplaceConfigRequest{Meta: protocol.RequestMeta{RequestID: 1, SessionID: s.ID, SessionEpoch: 1, GraphEpoch: 1, OperationID: "profile-write"}, ExpectedRevision: protocol.ConfigRevision([]byte("before")), Data: []byte("after")}
	result, err := w.Replace(context.Background(), req)
	if err != nil {
		return err
	}
	if result.Outcome != protocol.OutcomeCommitted {
		return fmt.Errorf("uncommitted write: %+v", result)
	}
	return nil
}

func database(state string) error {
	db, err := bolt.Open(filepath.Join(state, "state.db"), 0600, nil)
	if err != nil {
		return err
	}
	defer db.Close() //nolint:errcheck
	return db.Update(func(tx *bolt.Tx) error {
		b, err := tx.CreateBucketIfNotExists([]byte("profile"))
		if err != nil {
			return err
		}
		return b.Put([]byte("key"), []byte("value"))
	})
}

func main() {
	if probeMode == "runtime-client" {
		if err := runtimeClient(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	if probeMode == "x32" {
		_, _, _ = unix.RawSyscall(unix.SYS_GETPID|0x40000000, 0, 0, 0)
		fmt.Fprintln(os.Stderr, "alternate syscall ABI was not killed")
		os.Exit(1)
	}
	if probeMode == "tsync-failure" {
		tsyncFailure()
		return
	}
	role := linuxprofile.Role(os.Getenv("ALMAGEST_LINUX_ROLE"))
	userProfile := os.Getenv("ALMAGEST_LINUX_PROFILE") == string(linuxprofile.ProfileUser)
	output.Role = role
	// Create sibling threads before Seal to prove TSYNC covers them.
	ready := make(chan int, 4)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			runtime.LockOSThread()
			defer runtime.UnlockOSThread()
			ready <- unix.Gettid()
			<-start
			threadChecks(i, role)
		}()
	}
	for i := 0; i < 4; i++ {
		output.Threads = append(output.Threads, <-ready)
	}
	if err := linuxprofile.Seal(role); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	close(start)
	wg.Wait()
	// Repeat the checks from a later pinned goroutine after the final filter.
	done := make(chan struct{})
	go func() { runtime.LockOSThread(); defer runtime.UnlockOSThread(); threadChecks(4, role); close(done) }()
	<-done
	check("process-shape", userProfile && os.Getpid() > 1 || !userProfile && os.Getpid() == 1, nil)
	check("environment-cleared", os.Getenv("UNTRUSTED_SENTINEL") == "" && os.Getenv("PATH") == "", nil)
	locales := os.Getenv("ALMAGEST_LOCALES_DIR")
	if locales == "" && role == linuxprofile.Runtime && !userProfile {
		locales = "/locales"
	}
	if role == linuxprofile.Runtime {
		data, readErr := os.ReadFile(filepath.Join(locales, "visible"))
		check("locales-read", locales != "" && string(data) == "localized content", readErr)
		writeErr := os.WriteFile(filepath.Join(locales, "write"), []byte("denied"), 0o600)
		check("locales-write-denied", denied(writeErr) || errors.Is(writeErr, unix.EROFS), writeErr)
		_, escapeErr := os.ReadFile(filepath.Join(locales, "escape"))
		check("locales-symlink-escape-denied", denied(escapeErr), escapeErr)
		executeErr := unix.Access(filepath.Join(locales, "visible"), unix.X_OK)
		check("locales-execute-denied", userProfile || denied(executeErr), executeErr)
	} else {
		check("locales-not-delegated", locales == "", nil)
	}
	_, err := unix.FcntlInt(2000, unix.F_GETFD, 0)
	check("high-descriptor-closed", errors.Is(err, unix.EBADF), err)
	for _, tc := range []struct {
		key  int
		want uint64
	}{{unix.RLIMIT_CORE, 0}, {unix.RLIMIT_NOFILE, linuxprofile.DefaultLimits().Descriptors}, {unix.RLIMIT_MEMLOCK, 0}, {unix.RLIMIT_FSIZE, linuxprofile.DefaultLimits().FileBytes}} {
		var value unix.Rlimit
		err := unix.Getrlimit(tc.key, &value)
		check(fmt.Sprintf("limit-%d", tc.key), err == nil && value.Cur == tc.want && value.Max == tc.want, err)
	}
	for _, tc := range []struct {
		op   int
		want uintptr
	}{{unix.PR_GET_NO_NEW_PRIVS, 1}, {unix.PR_GET_SECCOMP, 2}, {unix.PR_GET_DUMPABLE, 0}} {
		v, _, e := unix.RawSyscall(unix.SYS_PRCTL, uintptr(tc.op), 0, 0)
		check(fmt.Sprintf("prctl-%d", tc.op), e == 0 && v == tc.want, e)
	}
	file := os.NewFile(3, "control")
	b := make([]byte, len("profile-channel"))
	_, err = io.ReadFull(file, b)
	check("inherited-channel", err == nil && string(b) == "profile-channel", err)
	for _, name := range []string{"ALMAGEST_DATA_FD", "ALMAGEST_DIAGNOSTICS_FD", "ALMAGEST_ADMIN_FD", "ALMAGEST_CONFIG_FD", "ALMAGEST_COMMAND_FD", "ALMAGEST_CALLBACK_FD"} {
		fd, _ := strconv.Atoi(os.Getenv(name))
		if fd == 0 {
			continue
		}
		lane := os.NewFile(uintptr(fd), name)
		data := make([]byte, len("profile-lane"))
		_, err := io.ReadFull(lane, data)
		check(name, err == nil && string(data) == "profile-lane", err)
		_ = lane.Close()
	}
	state := "/state"
	temporary := "/tmp"
	if userProfile {
		state = "."
		temporary = ".tmp"
		var peerLimit unix.Rlimit
		err = unix.Prlimit(os.Getppid(), unix.RLIMIT_NOFILE, nil, &peerLimit)
		check("same-uid-prlimit-denied", denied(err), err)
		_, _, signalErr := unix.RawSyscall(unix.SYS_KILL, uintptr(os.Getppid()), 0, 0)
		check("same-uid-signal-denied", signalErr == unix.EPERM, signalErr)
		_, err = unix.FcntlInt(3, unix.F_SETOWN, os.Getppid())
		check("async-owner-denied", denied(err), err)
		directoryFD := unix.AT_FDCWD
		_, _, chmodErr := unix.RawSyscall6(unix.SYS_FCHMODAT, uintptr(directoryFD),
			uintptr(unsafe.Pointer(unsafe.StringData("renamed\x00"))), 0600, 0, 0, 0)
		check("pathname-metadata-denied", chmodErr == unix.EPERM, chmodErr)
	}
	err = os.WriteFile(filepath.Join(state, "data"), []byte("data"), 0600)
	check("storage-write", err == nil, err)
	err = os.Rename(filepath.Join(state, "data"), filepath.Join(state, "renamed"))
	check("storage-rename", err == nil, err)
	err = os.Truncate(filepath.Join(state, "renamed"), 1)
	check("pathname-truncate-policy", userProfile && denied(err) || !userProfile && err == nil, err)
	err = os.Symlink("/blocked", filepath.Join(state, "escape"))
	check("storage-symlink-create", err == nil, err)
	_, err = os.ReadFile(filepath.Join(state, "escape"))
	check("symlink-escape-denied", denied(err), err)
	err = os.WriteFile(filepath.Join(temporary, "scratch"), []byte("scratch"), 0600)
	check("temporary-storage", err == nil, err)
	switch role {
	case linuxprofile.Runtime:
		err = database(state)
		check("bbolt-after-seal", err == nil, err)
	case linuxprofile.Writer:
		err = writer(state)
		check("config-writer-after-seal", err == nil, err)
	case linuxprofile.Command:
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		result := commandhelper.NewProcessLauncher("fixed").Run(ctx, commandhelper.Execution{Sandbox: "fixed", Executable: "/command", WorkingDirectory: "/state", OutputLimit: 1024})
		cancel()
		check("fixed-command-after-seal", result.Started && result.Err == nil && result.ExitCode == 0, result.Err)
	}
	runtime.GC()
	check("go-gc", true, nil)
	if err := json.NewEncoder(os.Stdout).Encode(output); err != nil {
		os.Exit(1)
	}
	for _, ok := range output.Checks {
		if !ok {
			os.Exit(1)
		}
	}
}
