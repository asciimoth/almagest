package supervision

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/asciimoth/almagest/bin/internal/isolation/protocol"
)

var (
	ErrInvalidComposition = errors.New("invalid isolated role composition")
	ErrStartupFailed      = errors.New("isolated role startup failed")
	ErrRuntimeLost        = errors.New("isolated runtime was lost")
	ErrShutdownIncomplete = errors.New("isolated role shutdown was incomplete")
)

// Process is an already-started restricted role. Supervisor intentionally has
// no launcher method: bootstrap starts all children, installs restrictions,
// and then becomes N before it constructs this coordinator.
type Process interface {
	Role() protocol.Role
	Ready() <-chan ReadyResult
	Done() <-chan error
	Stop(context.Context) error
	Kill() error
}

// ReadyResult is produced only after the role's authenticated bootstrap and
// final platform restriction checks finish.
type ReadyResult struct {
	Role                protocol.Role
	Version             uint32
	PolicyDigest        [32]byte
	RestrictionsApplied bool
	Err                 error
}

// Host owns startup reconciliation and session resource revocation in N.
type Host interface {
	Recover(context.Context) error
	Revoke(context.Context) error
}

// Degraded receives C/H failures. These failures disable only their named
// functions; they do not grant fallback authority or restart a child in place.
type Degraded interface {
	RoleFailed(protocol.Role, error)
}

type Options struct {
	StartupTimeout  time.Duration
	ShutdownTimeout time.Duration
	RecoveryTimeout time.Duration
	PolicyDigest    [32]byte
}

func (o Options) validate() error {
	if o.StartupTimeout <= 0 || o.ShutdownTimeout <= 0 || o.RecoveryTimeout <= 0 {
		return ErrInvalidComposition
	}
	var zero [32]byte
	if o.PolicyDigest == zero {
		return ErrInvalidComposition
	}
	return nil
}

type RoleStatus struct {
	Role   protocol.Role
	Ready  bool
	Alive  bool
	Failed bool
}

type Status struct {
	Ready            bool
	RecoveryRequired bool
	Roles            []RoleStatus
}

type roleState struct {
	process Process
	ready   bool
	alive   bool
	err     error
}

// Supervisor coordinates one complete process set. Construct a new instance
// with a new session for a service restart; child replacement is not supported.
type Supervisor struct {
	host     Host
	degraded Degraded
	options  Options
	ready    chan struct{}

	mu         sync.Mutex
	roles      map[protocol.Role]*roleState
	started    bool
	published  bool
	stopping   bool
	recovery   bool
	readyOnce  sync.Once
	revokeOnce sync.Once
	revokeErr  error
}

func New(host Host, degraded Degraded, options Options, processes ...Process) (*Supervisor, error) {
	if host == nil || options.validate() != nil {
		return nil, ErrInvalidComposition
	}
	s := &Supervisor{host: host, degraded: degraded, options: options, ready: make(chan struct{}), roles: make(map[protocol.Role]*roleState)}
	for _, process := range processes {
		if process == nil {
			return nil, ErrInvalidComposition
		}
		role := process.Role()
		if role != protocol.RoleRuntime && role != protocol.RoleConfigWriter && role != protocol.RoleCommandHelper || s.roles[role] != nil {
			return nil, ErrInvalidComposition
		}
		s.roles[role] = &roleState{process: process, alive: true}
	}
	if s.roles[protocol.RoleRuntime] == nil {
		return nil, ErrInvalidComposition
	}
	return s, nil
}

func (s *Supervisor) Ready() <-chan struct{} { return s.ready }

func (s *Supervisor) Status() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	status := Status{Ready: s.published && !s.stopping, RecoveryRequired: s.recovery, Roles: make([]RoleStatus, 0, len(s.roles))}
	for role, state := range s.roles {
		item := RoleStatus{Role: role, Ready: state.ready, Alive: state.alive, Failed: state.err != nil}
		status.Roles = append(status.Roles, item)
	}
	slices.SortFunc(status.Roles, func(a, b RoleStatus) int {
		if a.Role < b.Role {
			return -1
		}
		if a.Role > b.Role {
			return 1
		}
		return 0
	})
	return status
}

type roleEvent struct {
	role  protocol.Role
	ready bool
	proof ReadyResult
	err   error
}

// Run reconciles trusted host state, waits for every role, publishes readiness,
// and supervises until shutdown or R loss. It can be called only once.
func (s *Supervisor) Run(ctx context.Context) error {
	s.mu.Lock()
	if s.started || s.stopping {
		s.mu.Unlock()
		return ErrInvalidComposition
	}
	s.started = true
	s.mu.Unlock()
	err := s.recover(ctx)
	if err != nil {
		s.markRecovery()
		return errors.Join(ErrStartupFailed, err, s.shutdown(context.Background()))
	}

	events := make(chan roleEvent, len(s.roles)*2)
	for role, state := range s.roles {
		go observeRole(ctx, role, state.process, events)
	}
	startup, cancelStartup := context.WithTimeout(ctx, s.options.StartupTimeout)
	pending := len(s.roles)
	for pending > 0 {
		select {
		case event := <-events:
			if event.ready && event.err == nil && s.validReadiness(event.role, event.proof) {
				s.markReady(event.role)
				pending--
				continue
			}
			if event.err == nil {
				event.err = protocol.ErrInvalidContract
			}
			cancelStartup()
			s.markFailed(event.role, event.err)
			return errors.Join(ErrStartupFailed, event.err, s.shutdown(context.Background()))
		case <-startup.Done():
			cancelStartup()
			return errors.Join(ErrStartupFailed, startup.Err(), s.shutdown(context.Background()))
		}
	}
	cancelStartup()
	s.mu.Lock()
	s.published = true
	s.mu.Unlock()
	s.readyOnce.Do(func() { close(s.ready) })

	for {
		select {
		case event := <-events:
			if event.ready {
				continue
			}
			s.markFailed(event.role, event.err)
			if event.role == protocol.RoleRuntime {
				revokeErr := s.revoke()
				return errors.Join(ErrRuntimeLost, event.err, revokeErr, s.shutdown(context.Background()))
			}
			if s.degraded != nil {
				// A C/H notification cannot block N's runtime-loss handling.
				go s.degraded.RoleFailed(event.role, event.err)
			}
		case <-ctx.Done():
			return errors.Join(ctx.Err(), s.shutdown(context.Background()))
		}
	}
}

func (s *Supervisor) validReadiness(role protocol.Role, result ReadyResult) bool {
	return result.Role == role && result.Version == protocol.Version &&
		result.PolicyDigest == s.options.PolicyDigest && result.RestrictionsApplied
}

func observeRole(ctx context.Context, role protocol.Role, process Process, events chan<- roleEvent) {
	select {
	case result, ok := <-process.Ready():
		if !ok && result.Err == nil {
			result.Err = ErrStartupFailed
		}
		select {
		case events <- roleEvent{role: role, ready: true, proof: result, err: result.Err}:
		case <-ctx.Done():
			return
		}
	case err, ok := <-process.Done():
		if !ok && err == nil {
			err = ErrStartupFailed
		}
		select {
		case events <- roleEvent{role: role, err: err}:
		case <-ctx.Done():
		}
		return
	case <-ctx.Done():
		return
	}
	select {
	case err, ok := <-process.Done():
		if !ok && err == nil {
			err = errors.New("role exited")
		}
		select {
		case events <- roleEvent{role: role, err: err}:
		case <-ctx.Done():
		}
	case <-ctx.Done():
	}
}

func (s *Supervisor) markReady(role protocol.Role) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if state := s.roles[role]; state != nil {
		state.ready = true
	}
}

func (s *Supervisor) markFailed(role protocol.Role, err error) {
	if err == nil {
		err = errors.New("role exited")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if state := s.roles[role]; state != nil {
		state.alive = false
		state.err = err
	}
}

func (s *Supervisor) markRecovery() {
	s.mu.Lock()
	s.recovery = true
	s.mu.Unlock()
}

func (s *Supervisor) recover(parent context.Context) error {
	ctx, cancel := context.WithTimeout(parent, s.options.RecoveryTimeout)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- s.host.Recover(ctx) }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return errors.Join(ErrRecoveryUncertain, ctx.Err())
	}
}

func (s *Supervisor) revoke() error {
	s.revokeOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), s.options.RecoveryTimeout)
		defer cancel()
		done := make(chan error, 1)
		go func() { done <- s.host.Revoke(ctx) }()
		select {
		case s.revokeErr = <-done:
		case <-ctx.Done():
			s.revokeErr = errors.Join(ErrRecoveryUncertain, ctx.Err())
		}
		if s.revokeErr != nil {
			s.markRecovery()
		}
	})
	return s.revokeErr
}

func (s *Supervisor) shutdown(parent context.Context) error {
	s.mu.Lock()
	if s.stopping {
		s.mu.Unlock()
		return nil
	}
	s.stopping = true
	states := make([]*roleState, 0, len(s.roles))
	// Stop R first. It cannot submit more work while other roles drain.
	if runtime := s.roles[protocol.RoleRuntime]; runtime != nil {
		states = append(states, runtime)
	}
	for _, role := range []protocol.Role{protocol.RoleCommandHelper, protocol.RoleConfigWriter} {
		if state := s.roles[role]; state != nil {
			states = append(states, state)
		}
	}
	s.mu.Unlock()

	var joined error
	for index, state := range states {
		if err := stopRole(parent, state.process, s.options.ShutdownTimeout); err != nil {
			joined = errors.Join(joined, fmt.Errorf("stop %s: %w", state.process.Role(), err))
		}
		if index == 0 {
			joined = errors.Join(joined, s.revoke())
		}
	}
	if joined != nil {
		s.markRecovery()
		return errors.Join(ErrShutdownIncomplete, joined)
	}
	return nil
}

func stopRole(parent context.Context, process Process, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	stopped := make(chan error, 1)
	go func() { stopped <- process.Stop(ctx) }()
	select {
	case err := <-stopped:
		return err
	case <-ctx.Done():
		return errors.Join(ctx.Err(), process.Kill())
	}
}
