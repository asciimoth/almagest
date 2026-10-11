//go:build windows

package windowsprofile

import (
	"context"
	"fmt"

	"github.com/asciimoth/almagest/bin/internal/isolation/protocol"
	"github.com/asciimoth/almagest/bin/internal/isolation/supervision"
	"golang.org/x/sys/windows"
)

// Supervise binds a launched R/C/H process to the shared supervisor. It pins
// an observation-only, non-inheritable process handle before starting the
// waiter. Concurrent Close cannot turn handle closure into false exit proof.
// ready and stop belong to trusted bootstrap's authenticated fixed channels.
// The caller must still Close the native Process after supervision finishes.
func (p *Process) Supervise(ready <-chan supervision.ReadyResult, stop func(context.Context) error) (*supervision.StartedProcess, error) {
	if ready == nil || stop == nil {
		return nil, supervision.ErrInvalidComposition
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	var role protocol.Role
	switch p.role {
	case Runtime:
		role = protocol.RoleRuntime
	case Writer:
		role = protocol.RoleConfigWriter
	case Command:
		role = protocol.RoleCommandHelper
	default:
		return nil, supervision.ErrInvalidComposition
	}
	if p.process == 0 || p.job == 0 || p.supervised {
		return nil, supervision.ErrInvalidComposition
	}
	var pinned windows.Handle
	if err := windows.DuplicateHandle(windows.CurrentProcess(), p.process, windows.CurrentProcess(), &pinned,
		windows.SYNCHRONIZE|windows.PROCESS_QUERY_LIMITED_INFORMATION, false, 0); err != nil {
		return nil, fmt.Errorf("pin role exit observation: %w", err)
	}
	process, err := supervision.NewObservedProcess(role, ready, func() (bool, error) {
		defer windows.CloseHandle(pinned) //nolint:errcheck
		status, err := windows.WaitForSingleObject(pinned, windows.INFINITE)
		if err != nil {
			return false, fmt.Errorf("observe role exit: %w", err)
		}
		if status != windows.WAIT_OBJECT_0 {
			return false, fmt.Errorf("unexpected role wait result: %d", status)
		}
		var code uint32
		if err := windows.GetExitCodeProcess(pinned, &code); err != nil {
			return true, fmt.Errorf("read role exit status: %w", err)
		}
		if code != 0 {
			return true, fmt.Errorf("role exited with status %d", code)
		}
		return true, nil
	}, stop, p.Kill)
	if err != nil {
		_ = windows.CloseHandle(pinned)
		return nil, err
	}
	p.supervised = true
	return process, nil
}
