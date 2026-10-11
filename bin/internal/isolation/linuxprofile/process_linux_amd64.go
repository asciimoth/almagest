//go:build linux && amd64

package linuxprofile

import (
	"context"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/asciimoth/almagest/bin/internal/isolation/protocol"
	"github.com/asciimoth/almagest/bin/internal/isolation/supervision"
	"github.com/asciimoth/almagest/bin/internal/isolation/transport"
	"golang.org/x/sys/unix"
)

// Process owns a fixed native launcher and its single exit waiter. Inputs remain
// caller-owned; close the parent's copies of child lanes after Start succeeds.
type Process struct {
	command    *exec.Cmd
	plan       Plan
	done       chan struct{}
	err        error // published by closing done
	mu         sync.Mutex
	supervised bool
	peer       transport.LinuxPeer
	childDone  chan struct{}
	childErr   error
}

// Start starts only the held-object native launch contract. It does not accept
// arbitrary arguments or an environment. Output must use trusted bounded sinks.
// Even file sinks are forwarded through parent-owned pipes: a restricted role
// must never inherit write authority to a host log file or terminal.
func Start(ctx context.Context, plan Plan, inputs Inputs, stdout, stderr io.Writer) (*Process, error) {
	command, err := CommandContext(ctx, plan, inputs)
	if err != nil {
		return nil, err
	}
	if stdout == nil {
		stdout = io.Discard
	}
	if stderr == nil {
		stderr = io.Discard
	}
	command.Stdout, command.Stderr = roleOutput{stdout}, roleOutput{stderr}
	if err := command.Start(); err != nil {
		return nil, err
	}
	p := &Process{command: command, plan: plan, done: make(chan struct{})}
	go func() { p.err = command.Wait(); close(p.done) }()
	return p, nil
}

// Hide *os.File from os/exec so it creates a pipe instead of inheriting the file.
type roleOutput struct{ io.Writer }

// Wait joins the owned waiter; a deadline never proves process exit.
func (p *Process) Wait(ctx context.Context) error {
	if ctx == nil || p == nil || p.command == nil {
		return supervision.ErrInvalidComposition
	}
	select {
	case <-p.done:
		return p.err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (p *Process) Kill() error {
	if p == nil || p.command == nil {
		return supervision.ErrInvalidComposition
	}
	return p.command.Process.Kill()
}

// WaitStopped confirms both wrapper and pinned namespace-child exit. Unlike
// Wait it ignores a known nonzero exit status, but never hides observation
// failure. Bootstrap uses this when rolling back a partially started set.
func (p *Process) WaitStopped(ctx context.Context) error {
	if p == nil || ctx == nil || p.command == nil {
		return supervision.ErrInvalidComposition
	}
	p.mu.Lock()
	childDone := p.childDone
	p.mu.Unlock()
	if childDone == nil {
		return supervision.ErrShutdownIncomplete
	}
	select {
	case <-p.done:
	case <-ctx.Done():
		return ctx.Err()
	}
	if p.command.ProcessState == nil {
		return supervision.ErrShutdownIncomplete
	}
	select {
	case <-childDone:
		return p.childErr
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Peer identifies the native launcher's one namespace child, not the wrapper.
// The connected socket still authenticates every message's PID, UID and GID;
// this procfs observation is not itself a restriction or readiness proof.
func (p *Process) Peer(ctx context.Context) (transport.LinuxPeer, error) {
	if ctx == nil || p == nil || p.command == nil {
		return transport.LinuxPeer{}, supervision.ErrInvalidComposition
	}
	if err := ctx.Err(); err != nil {
		return transport.LinuxPeer{}, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.childDone != nil {
		return p.peer, nil
	}
	bounded, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	pid := p.command.Process.Pid
	path := fmt.Sprintf("/proc/%d/task/%d/children", pid, pid)
	for {
		if err := bounded.Err(); err != nil {
			return transport.LinuxPeer{}, err
		}
		select {
		case <-p.done:
			return transport.LinuxPeer{}, fmt.Errorf("%w: launcher exited before peer binding", ErrUnavailable)
		default:
		}
		data, err := os.ReadFile(path)
		if err == nil {
			child, err := singleChildPID(data)
			if err != nil {
				return transport.LinuxPeer{}, err
			}
			if child != 0 {
				select {
				case <-p.done:
					return transport.LinuxPeer{}, fmt.Errorf("%w: launcher exited during peer binding", ErrUnavailable)
				default:
				}
				fd, err := unix.PidfdOpen(int(child), 0)
				if err != nil {
					return transport.LinuxPeer{}, fmt.Errorf("pin role lifetime: %w", err)
				}
				p.peer = transport.LinuxPeer{PID: child, UID: p.plan.UID, GID: p.plan.GID}
				p.childDone = make(chan struct{})
				go func() {
					defer unix.Close(fd) //nolint:errcheck
					p.childErr = waitChildExit(fd)
					close(p.childDone)
				}()
				return p.peer, nil
			}
		} else if !os.IsNotExist(err) {
			return transport.LinuxPeer{}, err
		}
		select {
		case <-bounded.Done():
			return transport.LinuxPeer{}, bounded.Err()
		case <-p.done:
			return transport.LinuxPeer{}, fmt.Errorf("%w: launcher exited before peer binding", ErrUnavailable)
		case <-ticker.C:
		}
	}
}

func waitChildExit(fd int) error {
	if fd < 0 || fd > math.MaxInt32 {
		return supervision.ErrInvalidComposition
	}
	for {
		poll := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
		_, err := unix.Poll(poll, -1)
		if err == unix.EINTR {
			continue
		}
		if err != nil {
			return err
		}
		if poll[0].Revents&(unix.POLLIN|unix.POLLHUP) != 0 {
			return nil
		}
		return fmt.Errorf("%w: role pidfd observation failed", ErrUnavailable)
	}
}

func singleChildPID(data []byte) (int32, error) {
	fields := strings.Fields(string(data))
	if len(fields) == 0 {
		return 0, nil
	}
	if len(fields) != 1 {
		return 0, fmt.Errorf("%w: unexpected launcher child count", ErrUnavailable)
	}
	value, err := strconv.ParseInt(fields[0], 10, 32)
	if err != nil || value <= 0 || strconv.FormatInt(value, 10) != fields[0] {
		return 0, fmt.Errorf("%w: invalid launcher child identity", ErrUnavailable)
	}
	return int32(value), nil
}

// Supervise binds the role to the validated launch plan. Only one supervisor
// may own a process. Peer must first pin the namespace child's exit handle.
// The fixed stop callback normally closes its service lane.
func (p *Process) Supervise(ready <-chan supervision.ReadyResult, stop func(context.Context) error) (*supervision.StartedProcess, error) {
	if p == nil || p.command == nil {
		return nil, supervision.ErrInvalidComposition
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.supervised || p.childDone == nil {
		return nil, supervision.ErrInvalidComposition
	}
	var role protocol.Role
	switch p.plan.Role {
	case Runtime:
		role = protocol.RoleRuntime
	case Writer:
		role = protocol.RoleConfigWriter
	case Command:
		role = protocol.RoleCommandHelper
	default:
		return nil, supervision.ErrInvalidComposition
	}
	process, err := supervision.NewObservedProcess(role, ready, func() (bool, error) {
		// An observation failure must report role loss immediately, even when
		// the other process is still alive. Successful shutdown needs both.
		select {
		case <-p.done:
			if p.command.ProcessState == nil {
				return false, p.err
			}
		case <-p.childDone:
			if p.childErr != nil {
				return false, p.childErr
			}
		}
		<-p.done
		<-p.childDone
		// Wait can fail without a ProcessState if native exit could not be observed.
		if p.childErr != nil {
			return false, p.childErr
		}
		return p.command.ProcessState != nil, p.err
	}, stop, p.Kill)
	if err != nil {
		return nil, err
	}
	p.supervised = true
	return process, nil
}
