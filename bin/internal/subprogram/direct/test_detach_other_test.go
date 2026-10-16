//go:build !unix

package direct

import "os/exec"

func detachHelperProcess(*exec.Cmd) {}
