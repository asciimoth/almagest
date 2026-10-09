//go:build !windows

package commandhelper

import (
	"os/exec"
	"syscall"
	"time"
)

const commandWaitDelay = 500 * time.Millisecond

func configureCommand(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}

func configureStartedCommand(*exec.Cmd) {}
