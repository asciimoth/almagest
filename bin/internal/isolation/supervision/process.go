package supervision

import (
	"context"
	"sync"

	"github.com/asciimoth/almagest/bin/internal/isolation/protocol"
)

// StartedProcess adapts an already-started restricted platform process. It
// owns its single exit wait. It has no executable, argument, or launch API.
type StartedProcess struct {
	role     protocol.Role
	ready    <-chan ReadyResult
	done     chan error
	exited   chan struct{}
	stopDone chan struct{}
	stop     func(context.Context) error
	kill     func() error
	stopOnce sync.Once
	killOnce sync.Once
	stopErr  error
	killErr  error
}

// NewStartedProcess takes ownership of wait; no other caller may invoke that
// waiter. wait must report actual process exit, not merely a signal delivery.
// stop requests graceful shutdown through the platform's fixed control path.
// kill terminates the complete role process set (including its native wrapper
// or job). All callbacks are fixed by trusted bootstrap, never supplied by R.
func NewStartedProcess(role protocol.Role, ready <-chan ReadyResult, wait func() error, stop func(context.Context) error, kill func() error) (*StartedProcess, error) {
	if wait == nil {
		return nil, ErrInvalidComposition
	}
	return NewObservedProcess(role, ready, func() (bool, error) { return true, wait() }, stop, kill)
}

// NewObservedProcess supports native waiters which can fail without proving
// exit. confirmed must be true only after native process exit was observed.
// Observation failure reports role loss but cannot make Stop report success.
func NewObservedProcess(role protocol.Role, ready <-chan ReadyResult, wait func() (confirmed bool, err error), stop func(context.Context) error, kill func() error) (*StartedProcess, error) {
	if role != protocol.RoleRuntime && role != protocol.RoleConfigWriter && role != protocol.RoleCommandHelper && role != protocol.RoleSandboxSupervisor || ready == nil || wait == nil || stop == nil || kill == nil {
		return nil, ErrInvalidComposition
	}
	p := &StartedProcess{role: role, ready: ready, done: make(chan error, 1), exited: make(chan struct{}), stopDone: make(chan struct{}), stop: stop, kill: kill}
	go func() {
		confirmed, err := wait()
		if confirmed {
			close(p.exited)
		} else if err == nil {
			err = ErrShutdownIncomplete
		}
		p.done <- err
		close(p.done)
	}()
	return p, nil
}

func (p *StartedProcess) Role() protocol.Role       { return p.role }
func (p *StartedProcess) Ready() <-chan ReadyResult { return p.ready }
func (p *StartedProcess) Done() <-chan error        { return p.done }

// Stop reports success only after the exit waiter finishes. Concurrent calls
// send at most one graceful request and can each bound their own exit wait.
func (p *StartedProcess) Stop(ctx context.Context) error {
	if ctx == nil {
		return ErrInvalidComposition
	}
	select {
	case <-p.exited:
		return nil
	default:
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	p.stopOnce.Do(func() {
		go func() {
			p.stopErr = p.stop(ctx)
			close(p.stopDone)
		}()
	})
	select {
	case <-p.exited:
		return nil
	case <-p.stopDone:
	case <-ctx.Done():
		return ctx.Err()
	}
	if p.stopErr != nil {
		return p.stopErr
	}
	select {
	case <-p.exited:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (p *StartedProcess) Kill() error {
	select {
	case <-p.exited:
		return nil
	default:
	}
	p.killOnce.Do(func() { p.killErr = p.kill() })
	return p.killErr
}
