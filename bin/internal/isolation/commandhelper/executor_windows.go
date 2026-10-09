//go:build windows

package commandhelper

import (
	"os/exec"
	"time"
)

const commandWaitDelay = 500 * time.Millisecond

func configureCommand(*exec.Cmd)        {}
func configureStartedCommand(*exec.Cmd) {}
