//go:build linux && amd64

package linuxprofile

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"syscall"
)

// ProbeUserPrerequisites tests mandatory kernel mechanisms in a disposable
// native child. It does not create sockets, routes, devices, or persistent
// state. The caller supplies a held, trust-checked launcher object.
func ProbeUserPrerequisites(launcher *os.File, executables ...*os.File) error {
	if launcher == nil {
		return ErrUnavailable
	}
	if len(executables) > 2 {
		return ErrUnavailable
	}
	arguments := []string{"probe-user-v1"}
	for range executables {
		arguments = append(arguments, "executable")
	}
	command := exec.Command("/proc/self/fd/3", arguments...) //nolint:gosec // Held launcher descriptor.
	command.Env = []string{}
	command.Stdin = nil
	command.Stdout = io.Discard
	var stderr bytes.Buffer
	command.Stderr = &stderr
	command.ExtraFiles = append([]*os.File{launcher}, executables...)
	command.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
	if err := command.Run(); err != nil {
		message := stderr.String()
		if len(message) > 512 {
			message = message[:512]
		}
		return fmt.Errorf("%w: user-profile preflight failed: %s", ErrUnavailable, message)
	}
	return nil
}
