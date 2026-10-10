//go:build linux && amd64

package linuxprofile

import (
	"fmt"
	"runtime"
	"unsafe"

	"golang.org/x/sys/unix"
)

// Seal removes the native launcher's exec permission from ALL Go threads.
// Call before reading any application input or constructing application code.
// A failure is fatal to role startup. Command keeps exec for its one mounted
// command; its trusted dispatcher must enforce the protected argv contract.
func Seal(role Role) error {
	if role != Runtime && role != Writer && role != Command {
		return ErrUnavailable
	}
	// exec resets dumpability even when bootstrap disabled it.
	if err := unix.Prctl(unix.PR_SET_DUMPABLE, 0, 0, 0, 0); err != nil {
		return fmt.Errorf("%w: disable dumps: %v", ErrUnavailable, err)
	}
	filter := sealFilter(role)
	program := unix.SockFprog{Len: uint16(len(filter)), Filter: &filter[0]}
	result, _, errno := unix.RawSyscall(unix.SYS_SECCOMP, unix.SECCOMP_SET_MODE_FILTER,
		unix.SECCOMP_FILTER_FLAG_TSYNC, uintptr(unsafe.Pointer(&program)))
	runtime.KeepAlive(filter)
	// A positive TSYNC result identifies a thread that could not be synchronized.
	// It is an error even when errno is zero.
	if errno != 0 || result != 0 {
		return fmt.Errorf("%w: seccomp TSYNC result=%d errno=%v", ErrUnavailable, result, errno)
	}
	return nil
}

func sealFilter(role Role) []unix.SockFilter {
	f := []unix.SockFilter{
		{Code: unix.BPF_LD | unix.BPF_W | unix.BPF_ABS, K: 4},
		{Code: unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K, Jt: 1, K: unix.AUDIT_ARCH_X86_64},
		{Code: unix.BPF_RET | unix.BPF_K, K: unix.SECCOMP_RET_KILL_PROCESS},
		{Code: unix.BPF_LD | unix.BPF_W | unix.BPF_ABS},
	}
	denied := []uint32{unix.SYS_EXECVEAT}
	if role != Command {
		denied = append(denied, unix.SYS_EXECVE)
	}
	for _, call := range denied {
		f = append(f, unix.SockFilter{Code: unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K, Jf: 1, K: call},
			unix.SockFilter{Code: unix.BPF_RET | unix.BPF_K, K: unix.SECCOMP_RET_ERRNO | uint32(unix.EPERM)})
	}
	return append(f, unix.SockFilter{Code: unix.BPF_RET | unix.BPF_K, K: unix.SECCOMP_RET_ALLOW})
}
