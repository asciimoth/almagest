//go:build linux && amd64

package direct

import (
	"golang.org/x/sys/unix"

	sandbox "github.com/asciimoth/gobox"
)

func nativePrerequisites() error {
	abi, _, errno := unix.Syscall(unix.SYS_LANDLOCK_CREATE_RULESET, 0, 0, unix.LANDLOCK_CREATE_RULESET_VERSION)
	if errno != 0 || abi < 3 {
		return sandbox.ErrUnavailable
	}
	if _, _, errno = unix.Syscall6(unix.SYS_PRCTL, unix.PR_GET_NO_NEW_PRIVS, 0, 0, 0, 0, 0); errno != 0 {
		return sandbox.ErrUnavailable
	}
	return nil
}
