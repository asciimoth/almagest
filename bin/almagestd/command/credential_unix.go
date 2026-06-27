//go:build unix

package command

import (
	"os"
	"os/exec"
	"syscall"
)

func setCommandCredential(cmd *exec.Cmd, cfg Command) error {
	if cfg.UID == nil && cfg.GID == nil {
		return nil
	}
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
	cmd.SysProcAttr = &syscall.SysProcAttr{Credential: cred}
	return nil
}
