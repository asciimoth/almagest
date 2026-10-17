//go:build !unix && !windows

package direct

import (
	"os/exec"
	"syscall"

	sandbox "github.com/asciimoth/gobox"
)

type processBoundary interface {
	Signal(sandbox.Signal) error
	Kill() error
	Close() error
}

func prepareProcess(*exec.Cmd, *instance)              {}
func attachProcess(*exec.Cmd) (processBoundary, error) { return nil, sandbox.ErrUnavailable }
func startProcess(command *exec.Cmd, _ *instance) (processBoundary, error) {
	if err := command.Start(); err != nil {
		return nil, err
	}
	boundary, err := attachProcess(command)
	if err != nil {
		_ = command.Process.Kill()
		_, _ = command.Process.Wait()
	}
	return boundary, err
}
func nativeSignal(syscall.Signal) sandbox.Signal { return sandbox.Signal("") }
func exitSignal(any) sandbox.Signal              { return "" }
