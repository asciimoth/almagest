//go:build linux

package direct

import (
	"errors"
	"strings"

	"golang.org/x/sys/unix"
)

func helperLinuxSecurityResult() string {
	checks := []struct {
		name string
		run  func() error
	}{
		{"mount", func() error { return unix.Mount("none", "/", "", 0, "") }},
		{"ptrace", func() error { return unix.PtraceAttach(1) }},
		{"process-vm", func() error {
			local, remote := []unix.Iovec{{}}, []unix.RemoteIovec{{}}
			_, err := unix.ProcessVMReadv(1, local, remote, 0)
			return err
		}},
		{"netlink", func() error {
			fd, err := unix.Socket(unix.AF_NETLINK, unix.SOCK_RAW, unix.NETLINK_ROUTE)
			if fd >= 0 {
				_ = unix.Close(fd)
			}
			return err
		}},
		{"packet", func() error {
			fd, err := unix.Socket(unix.AF_PACKET, unix.SOCK_RAW, 0)
			if fd >= 0 {
				_ = unix.Close(fd)
			}
			return err
		}},
		{"root-identity", func() error { return unix.Setuid(0) }},
	}
	failed := make([]string, 0, len(checks))
	for _, check := range checks {
		if err := check.run(); err == nil ||
			!errors.Is(err, unix.EPERM) && !errors.Is(err, unix.EACCES) && !errors.Is(err, unix.EAFNOSUPPORT) {
			failed = append(failed, check.name)
		}
	}
	if len(failed) != 0 {
		return "escaped:" + strings.Join(failed, ",")
	}
	return "denied"
}

func helperControllerSignalResult() string {
	if err := unix.Kill(1, 0); err != nil {
		return "probe-denied"
	}
	if err := unix.Kill(1, unix.SIGTERM); !errors.Is(err, unix.EPERM) {
		return "escaped"
	}
	return "denied"
}
