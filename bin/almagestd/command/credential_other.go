//go:build !unix && !windows

package command

import (
	"context"
	"errors"
	"os/exec"
)

func validateProcessConfig(cfg resolvedCommand) error {
	if cfg.UID == nil && cfg.GID == nil {
		return nil
	}
	return errors.New("uid/gid command credentials are unsupported on this platform")
}

func executeCommand(ctx context.Context, cfg resolvedCommand, env []string) error {
	cmd := exec.CommandContext(ctx, cfg.Command[0], cfg.Command[1:]...)
	cmd.Env = env
	return cmd.Run()
}
