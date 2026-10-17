//go:build unix

package direct

import (
	"errors"
	"os/exec"
	"syscall"

	sandbox "github.com/asciimoth/gobox"
)

type processBoundary interface {
	Signal(sandbox.Signal) error
	Kill() error
	Close() error
}

type unixProcessBoundary struct{ pgid int }

func prepareProcess(command *exec.Cmd, _ *instance) {
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGKILL}
}

func attachProcess(command *exec.Cmd) (processBoundary, error) {
	pgid, err := syscall.Getpgid(command.Process.Pid)
	if err != nil {
		return nil, err
	}
	if pgid != command.Process.Pid {
		return nil, sandbox.ErrLaunch
	}
	return &unixProcessBoundary{pgid: pgid}, nil
}

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

func (b *unixProcessBoundary) Signal(signal sandbox.Signal) error {
	var native syscall.Signal
	switch signal {
	case sandbox.Interrupt:
		native = syscall.SIGINT
	case sandbox.Terminate:
		native = syscall.SIGTERM
	case sandbox.Kill:
		native = syscall.SIGKILL
	default:
		return sandbox.ErrUnsupported
	}
	err := syscall.Kill(-b.pgid, native)
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}
func (b *unixProcessBoundary) Kill() error  { return b.Signal(sandbox.Kill) }
func (b *unixProcessBoundary) Close() error { return b.Kill() }

func nativeSignal(signal syscall.Signal) sandbox.Signal {
	switch signal {
	case syscall.SIGINT:
		return sandbox.Interrupt
	case syscall.SIGTERM:
		return sandbox.Terminate
	case syscall.SIGKILL:
		return sandbox.Kill
	default:
		return sandbox.Signal(signal.String())
	}
}

func exitSignal(value any) sandbox.Signal {
	status, ok := value.(syscall.WaitStatus)
	if !ok || !status.Signaled() {
		return ""
	}
	return nativeSignal(status.Signal())
}
