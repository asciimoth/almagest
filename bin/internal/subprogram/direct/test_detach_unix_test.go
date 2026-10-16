//go:build unix

package direct

import (
	"os/exec"
	"syscall"
)

func detachHelperProcess(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}
