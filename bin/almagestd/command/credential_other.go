//go:build !unix

package command

import (
	"errors"
	"os/exec"
)

func setCommandCredential(cmd *exec.Cmd, cfg resolvedCommand) error {
	_ = cmd
	if cfg.UID == nil && cfg.GID == nil {
		return nil
	}
	return errors.New("uid/gid command credentials are unsupported on this platform")
}
