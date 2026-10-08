//go:build linux && amd64

// linux-isolation-probe is the untrusted payload for the checkpoint 10
// development harness. It is not used by almagestd.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

const (
	auditArchX86_64 = 0xc000003e
	seccompSetMode  = 1
	seccompTSync    = 1
	seccompRetKill  = 0x80000000
	seccompRetErrno = 0x00050000
	seccompRetAllow = 0x7fff0000
)

type checkResult struct {
	Name   string `json:"name"`
	Passed bool   `json:"passed"`
	Detail string `json:"detail,omitempty"`
}

type report struct {
	Profile     string        `json:"profile"`
	Kernel      string        `json:"kernel"`
	GoVersion   string        `json:"go_version"`
	LandlockABI int           `json:"landlock_abi"`
	UID         int           `json:"uid"`
	GID         int           `json:"gid"`
	PID         int           `json:"pid"`
	ThreadIDs   []int         `json:"thread_ids"`
	Checks      []checkResult `json:"checks"`
}

type collector struct {
	mu     sync.Mutex
	checks []checkResult
}

func (c *collector) add(name string, passed bool, detail string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.checks = append(c.checks, checkResult{Name: name, Passed: passed, Detail: detail})
}

func errnoIsBlocked(err error) bool {
	return errors.Is(err, unix.EPERM) || errors.Is(err, unix.EACCES) || errors.Is(err, unix.ENOENT)
}

func installFinalSeccompFilter() error {
	filter := []unix.SockFilter{
		{Code: unix.BPF_LD | unix.BPF_W | unix.BPF_ABS, K: 4},
		{Code: unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K, Jt: 1, Jf: 0, K: auditArchX86_64},
		{Code: unix.BPF_RET | unix.BPF_K, K: seccompRetKill},
		{Code: unix.BPF_LD | unix.BPF_W | unix.BPF_ABS, K: 0},
		{Code: unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K, Jt: 0, Jf: 1, K: unix.SYS_EXECVE},
		{Code: unix.BPF_RET | unix.BPF_K, K: seccompRetErrno | uint32(unix.EPERM)},
		{Code: unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K, Jt: 0, Jf: 1, K: unix.SYS_EXECVEAT},
		{Code: unix.BPF_RET | unix.BPF_K, K: seccompRetErrno | uint32(unix.EPERM)},
		{Code: unix.BPF_RET | unix.BPF_K, K: seccompRetAllow},
	}
	program := unix.SockFprog{Len: uint16(len(filter)), Filter: &filter[0]}
	_, _, syscallErr := unix.RawSyscall(unix.SYS_SECCOMP, seccompSetMode, seccompTSync,
		uintptr(unsafe.Pointer(&program)))
	if syscallErr != 0 {
		return syscallErr
	}
	return nil
}

func threadChecks(c *collector, index int) {
	prefix := fmt.Sprintf("thread-%d", index)
	if file, err := os.Open("/blocked"); err == nil {
		_ = file.Close()
		c.add(prefix+"-landlock", false, "opened a file without a Landlock read rule")
	} else {
		c.add(prefix+"-landlock", errnoIsBlocked(err), err.Error())
	}

	fd, err := unix.Socket(unix.AF_INET, unix.SOCK_RAW, unix.IPPROTO_ICMP)
	if err == nil {
		_ = unix.Close(fd)
	}
	c.add(prefix+"-raw-socket", errors.Is(err, unix.EPERM), errorText(err))

	_, _, syscallErr := unix.RawSyscall(unix.SYS_BPF, 0, 0, 0)
	c.add(prefix+"-bpf", syscallErr == unix.EPERM, syscallErr.Error())

	_, _, syscallErr = unix.RawSyscall(unix.SYS_FORK, 0, 0, 0)
	c.add(prefix+"-process-create", syscallErr == unix.EPERM, syscallErr.Error())

	_, _, syscallErr = unix.RawSyscall6(unix.SYS_EXECVE, 0, 0, 0, 0, 0, 0)
	c.add(prefix+"-exec", syscallErr == unix.EPERM, syscallErr.Error())
}

func errorText(err error) string {
	if err == nil {
		return "success"
	}
	return err.Error()
}

func main() {
	var results collector
	abi, _ := strconv.Atoi(os.Getenv("ALMAGEST_LANDLOCK_ABI"))

	results.add("uid-drop", os.Getuid() == 65534 && os.Geteuid() == 65534,
		fmt.Sprintf("uid=%d euid=%d", os.Getuid(), os.Geteuid()))
	results.add("gid-drop", os.Getgid() == 65534 && os.Getegid() == 65534,
		fmt.Sprintf("gid=%d egid=%d", os.Getgid(), os.Getegid()))
	results.add("pid-namespace", os.Getpid() == 1, fmt.Sprintf("pid=%d", os.Getpid()))
	hostname, hostnameErr := os.Hostname()
	results.add("uts-namespace", hostnameErr == nil && hostname == "almagest-runtime", hostname)

	noNewPrivileges, _, noNewPrivilegesErr := unix.RawSyscall(unix.SYS_PRCTL,
		unix.PR_GET_NO_NEW_PRIVS, 0, 0)
	results.add("no-new-privileges", noNewPrivilegesErr == 0 && noNewPrivileges == 1,
		fmt.Sprintf("value=%d errno=%s", noNewPrivileges, noNewPrivilegesErr.Error()))
	seccompMode, _, seccompErr := unix.RawSyscall(unix.SYS_PRCTL, unix.PR_GET_SECCOMP, 0, 0)
	results.add("initial-seccomp", seccompErr == 0 && seccompMode == 2,
		fmt.Sprintf("mode=%d errno=%s", seccompMode, seccompErr.Error()))

	capHeader := unix.CapUserHeader{Version: unix.LINUX_CAPABILITY_VERSION_3}
	capData := [2]unix.CapUserData{}
	capErr := unix.Capget(&capHeader, &capData[0])
	capEmpty := capErr == nil && capData[0] == (unix.CapUserData{}) && capData[1] == (unix.CapUserData{})
	results.add("empty-capability-sets", capEmpty, errorText(capErr))
	groups, groupsErr := os.Getgroups()
	results.add("empty-supplementary-groups", groupsErr == nil && len(groups) == 0,
		fmt.Sprintf("groups=%v error=%v", groups, groupsErr))
	boundingEmpty := true
	for capability := uintptr(0); capability < 64; capability++ {
		value, _, capabilityErr := unix.RawSyscall(unix.SYS_PRCTL, unix.PR_CAPBSET_READ, capability, 0)
		if capabilityErr == 0 && value != 0 {
			boundingEmpty = false
			break
		}
		if capabilityErr != 0 && capabilityErr != unix.EINVAL {
			boundingEmpty = false
			break
		}
	}
	results.add("empty-capability-bounding-set", boundingEmpty, "checked capability numbers 0 through 63")
	securebits, _, securebitsErr := unix.RawSyscall(unix.SYS_PRCTL, unix.PR_GET_SECUREBITS, 0, 0)
	results.add("locked-securebits", securebitsErr == 0 && securebits != 0,
		fmt.Sprintf("value=%#x errno=%s", securebits, securebitsErr.Error()))

	if err := os.WriteFile("/storage/runtime.txt", []byte("ordinary Go storage\n"), 0o600); err != nil {
		results.add("storage-write", false, err.Error())
	} else if err := os.Rename("/storage/runtime.txt", "/storage/runtime-renamed.txt"); err != nil {
		results.add("storage-rename", false, err.Error())
	} else if err := os.Truncate("/storage/runtime-renamed.txt", 8); err != nil {
		results.add("storage-truncate", false, err.Error())
	} else {
		results.add("storage-operations", true, "write, rename, and truncate succeeded")
	}
	if file, err := os.Open("/etc/passwd"); err == nil {
		_ = file.Close()
		results.add("host-files-hidden", false, "opened /etc/passwd")
	} else {
		results.add("host-files-hidden", errnoIsBlocked(err), err.Error())
	}
	if file, err := os.Open("/dev/net/tun"); err == nil {
		_ = file.Close()
		results.add("tun-device-hidden", false, "opened /dev/net/tun")
	} else {
		results.add("tun-device-hidden", errnoIsBlocked(err), err.Error())
	}
	_, dnsHelperErr := os.Stat("/sbin/resolvconf")
	results.add("dns-helper-hidden", errnoIsBlocked(dnsHelperErr), errorText(dnsHelperErr))

	channelBuffer := make([]byte, 64)
	count, _, channelErr := unix.Recvfrom(3, channelBuffer, 0)
	channelOK := channelErr == nil && string(channelBuffer[:count]) == "broker-channel-token\x00"
	results.add("private-inherited-channel", channelOK, errorText(channelErr))
	_, descriptorErr := unix.FcntlInt(1000, unix.F_GETFD, 0)
	results.add("unknown-descriptor-closed", errors.Is(descriptorErr, unix.EBADF), errorText(descriptorErr))

	_, _, mountErr := unix.RawSyscall6(unix.SYS_MOUNT, 0, 0, 0, 0, 0, 0)
	results.add("mount-administration", mountErr == unix.EPERM, mountErr.Error())
	_, _, setnsErr := unix.RawSyscall(unix.SYS_SETNS, ^uintptr(0), 0, 0)
	results.add("namespace-administration", setnsErr == unix.EPERM, setnsErr.Error())
	_, _, pidfdErr := unix.RawSyscall(unix.SYS_PIDFD_OPEN, 1, 0, 0)
	results.add("pidfd-token-theft", pidfdErr == unix.EPERM, pidfdErr.Error())
	_, _, pidfdGetfdErr := unix.RawSyscall(unix.SYS_PIDFD_GETFD, 3, 0, 0)
	results.add("descriptor-theft", pidfdGetfdErr == unix.EPERM, pidfdGetfdErr.Error())
	_, _, ptraceErr := unix.RawSyscall6(unix.SYS_PTRACE, unix.PTRACE_ATTACH, 1, 0, 0, 0, 0)
	results.add("ptrace-token-theft", ptraceErr == unix.EPERM, ptraceErr.Error())
	_, _, processVMErr := unix.RawSyscall6(unix.SYS_PROCESS_VM_READV, 1, 0, 0, 0, 0, 0)
	results.add("process-memory-theft", processVMErr == unix.EPERM, processVMErr.Error())
	fd, networkErr := unix.Socket(unix.AF_INET, unix.SOCK_STREAM, 0)
	if networkErr == nil {
		_ = unix.Close(fd)
	}
	results.add("native-network-socket", errors.Is(networkErr, unix.EPERM), errorText(networkErr))

	const threadCount = 4
	runtime.GOMAXPROCS(threadCount + 1)
	ready := make(chan int, threadCount)
	start := make(chan struct{})
	var waitGroup sync.WaitGroup
	waitGroup.Add(threadCount)
	for index := 0; index < threadCount; index++ {
		go func() {
			defer waitGroup.Done()
			runtime.LockOSThread()
			defer runtime.UnlockOSThread()
			ready <- unix.Gettid()
			<-start
			threadChecks(&results, index)
		}()
	}
	threadIDs := make([]int, 0, threadCount)
	for range threadCount {
		threadIDs = append(threadIDs, <-ready)
	}
	filterErr := installFinalSeccompFilter()
	results.add("seccomp-tsync", filterErr == nil, errorText(filterErr))
	close(start)
	waitGroup.Wait()

	done := make(chan struct{})
	go func() {
		defer close(done)
		buffer := make([]byte, 4<<20)
		for index := range buffer {
			buffer[index] = byte(index)
		}
		runtime.GC()
	}()
	select {
	case <-done:
		results.add("go-runtime-memory", true, "allocation and garbage collection succeeded")
	case <-time.After(2 * time.Second):
		results.add("go-runtime-memory", false, "runtime operation timed out")
	}

	var uts unix.Utsname
	kernel := "unknown"
	if err := unix.Uname(&uts); err == nil {
		kernel = unix.ByteSliceToString(uts.Release[:])
	}
	output := report{
		Profile:     "runtime-r-v1",
		Kernel:      kernel,
		GoVersion:   runtime.Version(),
		LandlockABI: abi,
		UID:         os.Getuid(),
		GID:         os.Getgid(),
		PID:         os.Getpid(),
		ThreadIDs:   threadIDs,
		Checks:      results.checks,
	}
	if err := json.NewEncoder(os.Stdout).Encode(output); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	for _, result := range results.checks {
		if !result.Passed {
			os.Exit(1)
		}
	}
}
