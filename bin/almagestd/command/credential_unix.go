//go:build unix

package command

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

func prepareCommand(cmd *exec.Cmd, cfg resolvedCommand) error {
	// Use a separate process group so cancellation stops descendants that the
	// configured command starts, not only the direct command process.
	attrs := &syscall.SysProcAttr{Setpgid: true}
	if cfg.UID != nil || cfg.GID != nil {
		uid := uint32(os.Geteuid())
		if cfg.UID != nil {
			uid = *cfg.UID
		}
		gid := uint32(os.Getegid())
		if cfg.GID != nil {
			gid = *cfg.GID
		}
		cred := &syscall.Credential{
			Uid:    uid,
			Gid:    gid,
			Groups: []uint32{},
		}
		if os.Geteuid() != 0 {
			cred.NoSetGroups = true
		}
		attrs.Credential = cred
	}
	cmd.SysProcAttr = attrs
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return os.ErrProcessDone
		}
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	return nil
}
