package direct

import "golang.org/x/sys/unix"

func helperNamespaceResult() string {
	if err := unix.Unshare(unix.CLONE_NEWUSER); err != nil {
		return "denied"
	}
	return "escaped"
}
