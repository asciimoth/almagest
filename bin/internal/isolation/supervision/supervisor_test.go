package supervision

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/asciimoth/almagest/bin/internal/isolation/protocol"
)

type fakeProcess struct {
	role  protocol.Role
	ready chan ReadyResult
	done  chan error
	stop  func(context.Context) error
	kill  func() error
	once  sync.Once
}

func newFakeProcess(role protocol.Role) *fakeProcess {
	return &fakeProcess{role: role, ready: make(chan ReadyResult, 1), done: make(chan error, 1)}
}
func (p *fakeProcess) Role() protocol.Role       { return p.role }
func (p *fakeProcess) Ready() <-chan ReadyResult { return p.ready }
func (p *fakeProcess) Done() <-chan error        { return p.done }
func (p *fakeProcess) Stop(ctx context.Context) error {
	if p.stop != nil {
		return p.stop(ctx)
	}
	p.once.Do(func() { p.done <- nil })
	return nil
}
func (p *fakeProcess) Kill() error {
	if p.kill != nil {
		return p.kill()
	}
	p.once.Do(func() { p.done <- errors.New("killed") })
	return nil
}

type fakeHost struct {
	mu                    sync.Mutex
	recover, revoke       int
	recoverErr, revokeErr error
}

func (h *fakeHost) Recover(context.Context) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.recover++
	return h.recoverErr
}
func (h *fakeHost) Revoke(context.Context) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.revoke++
	return h.revokeErr
}

type fakeDegraded struct{ events chan protocol.Role }

func (d fakeDegraded) RoleFailed(role protocol.Role, _ error) { d.events <- role }

func testOptions() Options {
	var digest [32]byte
	digest[0] = 1
	application := make(chan struct{})
	close(application)
	return Options{StartupTimeout: 200 * time.Millisecond, ShutdownTimeout: 30 * time.Millisecond, RecoveryTimeout: 30 * time.Millisecond, PolicyDigest: digest, ApplicationReady: application}
}

func ready(role protocol.Role) ReadyResult {
	return ReadyResult{Role: role, Version: protocol.Version, PolicyDigest: testOptions().PolicyDigest, RestrictionsApplied: true}
}

func runSupervisor(t *testing.T, supervisor *Supervisor, ctx context.Context) <-chan error {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- supervisor.Run(ctx) }()
	return done
}

func waitReady(t *testing.T, supervisor *Supervisor) {
	t.Helper()
	select {
	case <-supervisor.Ready():
	case <-time.After(time.Second):
		t.Fatal("supervisor did not become ready")
	}
}

func TestSupervisorReadinessRuntimeLossAndBoundedShutdown(t *testing.T) {
	runtimeRole := newFakeProcess(protocol.RoleRuntime)
	writer := newFakeProcess(protocol.RoleConfigWriter)
	helper := newFakeProcess(protocol.RoleCommandHelper)
	host := &fakeHost{}
	supervisor, err := New(host, nil, testOptions(), runtimeRole, writer, helper)
	if err != nil {
		t.Fatal(err)
	}
	done := runSupervisor(t, supervisor, context.Background())
	if supervisor.Status().Ready {
		t.Fatal("status reported readiness before the barrier")
	}
	runtimeRole.ready <- ready(protocol.RoleRuntime)
	writer.ready <- ready(protocol.RoleConfigWriter)
	select {
	case <-supervisor.Ready():
		t.Fatal("ready before every required role")
	default:
	}
	helper.ready <- ready(protocol.RoleCommandHelper)
	waitReady(t, supervisor)
	runtimeRole.done <- errors.New("runtime crash")
	if err := <-done; !errors.Is(err, ErrRuntimeLost) {
		t.Fatalf("Run error = %v", err)
	}
	host.mu.Lock()
	defer host.mu.Unlock()
	if host.recover != 1 || host.revoke != 1 {
		t.Fatalf("host calls: recover=%d revoke=%d", host.recover, host.revoke)
	}
}

func TestSupervisorStartupFailureNeverPublishesReady(t *testing.T) {
	runtimeRole := newFakeProcess(protocol.RoleRuntime)
	writer := newFakeProcess(protocol.RoleConfigWriter)
	host := &fakeHost{}
	supervisor, err := New(host, nil, testOptions(), runtimeRole, writer)
	if err != nil {
		t.Fatal(err)
	}
	done := runSupervisor(t, supervisor, context.Background())
	runtimeRole.ready <- ready(protocol.RoleRuntime)
	writer.done <- errors.New("writer exited before ready")
	if err := <-done; !errors.Is(err, ErrStartupFailed) {
		t.Fatalf("Run error = %v", err)
	}
	select {
	case <-supervisor.Ready():
		t.Fatal("failed startup published readiness")
	default:
	}
}

func TestSupervisorClosedReadinessChannelFailsStartup(t *testing.T) {
	runtimeRole := newFakeProcess(protocol.RoleRuntime)
	close(runtimeRole.ready)
	supervisor, err := New(&fakeHost{}, nil, testOptions(), runtimeRole)
	if err != nil {
		t.Fatal(err)
	}
	if err := <-runSupervisor(t, supervisor, context.Background()); !errors.Is(err, ErrStartupFailed) {
		t.Fatalf("Run error = %v", err)
	}
	if status := supervisor.Status(); status.Ready || status.Roles[0].Alive || !status.Roles[0].Failed {
		t.Fatalf("status after broken readiness channel = %#v", status)
	}
}

func TestSupervisorRejectsInvalidReadinessProofs(t *testing.T) {
	tests := map[string]func(*ReadyResult){
		"role":         func(result *ReadyResult) { result.Role = protocol.RoleConfigWriter },
		"version":      func(result *ReadyResult) { result.Version++ },
		"policy":       func(result *ReadyResult) { result.PolicyDigest[0]++ },
		"restrictions": func(result *ReadyResult) { result.RestrictionsApplied = false },
	}
	for name, change := range tests {
		t.Run(name, func(t *testing.T) {
			runtimeRole := newFakeProcess(protocol.RoleRuntime)
			supervisor, err := New(&fakeHost{}, nil, testOptions(), runtimeRole)
			if err != nil {
				t.Fatal(err)
			}
			done := runSupervisor(t, supervisor, context.Background())
			result := ready(protocol.RoleRuntime)
			change(&result)
			runtimeRole.ready <- result
			if err := <-done; !errors.Is(err, protocol.ErrInvalidContract) {
				t.Fatalf("Run error = %v", err)
			}
		})
	}
}

func TestSupervisorStartupTimeoutIsBounded(t *testing.T) {
	runtimeRole := newFakeProcess(protocol.RoleRuntime)
	options := testOptions()
	options.StartupTimeout = 10 * time.Millisecond
	supervisor, err := New(&fakeHost{}, nil, options, runtimeRole)
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	if err := <-runSupervisor(t, supervisor, context.Background()); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Run error = %v", err)
	}
	if time.Since(started) > time.Second {
		t.Fatal("startup wait was not bounded")
	}
}

func TestSupervisorHelperFailureDegradesOnlyHelper(t *testing.T) {
	runtimeRole := newFakeProcess(protocol.RoleRuntime)
	helper := newFakeProcess(protocol.RoleCommandHelper)
	host := &fakeHost{}
	degraded := fakeDegraded{events: make(chan protocol.Role, 1)}
	supervisor, err := New(host, degraded, testOptions(), runtimeRole, helper)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := runSupervisor(t, supervisor, ctx)
	runtimeRole.ready <- ready(protocol.RoleRuntime)
	helper.ready <- ready(protocol.RoleCommandHelper)
	waitReady(t, supervisor)
	helper.done <- errors.New("helper crash")
	select {
	case role := <-degraded.events:
		if role != protocol.RoleCommandHelper {
			t.Fatalf("degraded role = %s", role)
		}
	case <-time.After(time.Second):
		t.Fatal("helper failure not reported")
	}
	select {
	case err := <-done:
		t.Fatalf("helper failure stopped service: %v", err)
	default:
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("shutdown error = %v", err)
	}
}

func TestSupervisorBoundsHungStopAndMarksRecovery(t *testing.T) {
	runtimeRole := newFakeProcess(protocol.RoleRuntime)
	runtimeRole.stop = func(ctx context.Context) error { <-ctx.Done(); select {} }
	runtimeRole.kill = func() error { return errors.New("kill failed") }
	host := &fakeHost{}
	supervisor, err := New(host, nil, testOptions(), runtimeRole)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := runSupervisor(t, supervisor, ctx)
	runtimeRole.ready <- ready(protocol.RoleRuntime)
	waitReady(t, supervisor)
	started := time.Now()
	cancel()
	if err := <-done; !errors.Is(err, ErrShutdownIncomplete) {
		t.Fatalf("Run error = %v", err)
	}
	if time.Since(started) > time.Second {
		t.Fatal("hung shutdown was not bounded")
	}
	if !supervisor.Status().RecoveryRequired {
		t.Fatal("incomplete shutdown did not require recovery")
	}
}

func TestSupervisorBoundsHungStartupRecovery(t *testing.T) {
	runtimeRole := newFakeProcess(protocol.RoleRuntime)
	host := &blockingHost{}
	options := testOptions()
	options.RecoveryTimeout = 10 * time.Millisecond
	supervisor, err := New(host, nil, options, runtimeRole)
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	if err := <-runSupervisor(t, supervisor, context.Background()); !errors.Is(err, ErrRecoveryUncertain) {
		t.Fatalf("Run error = %v", err)
	}
	if time.Since(started) > time.Second {
		t.Fatal("startup recovery was not bounded")
	}
}

func TestSupervisorRevocationFailureIsVisible(t *testing.T) {
	runtimeRole := newFakeProcess(protocol.RoleRuntime)
	host := &fakeHost{revokeErr: errors.New("native cleanup uncertain")}
	supervisor, err := New(host, nil, testOptions(), runtimeRole)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := runSupervisor(t, supervisor, ctx)
	runtimeRole.ready <- ready(protocol.RoleRuntime)
	waitReady(t, supervisor)
	cancel()
	if err := <-done; !errors.Is(err, ErrShutdownIncomplete) {
		t.Fatalf("Run error = %v", err)
	}
	if !supervisor.Status().RecoveryRequired {
		t.Fatal("revocation failure did not set recovery-required status")
	}
}

type blockingHost struct{}

func (*blockingHost) Recover(ctx context.Context) error { <-ctx.Done(); select {} }
func (*blockingHost) Revoke(ctx context.Context) error  { <-ctx.Done(); return ctx.Err() }

func TestSupervisorFullServiceRestartRunsRecoveryAgain(t *testing.T) {
	host := &fakeHost{}
	for run := 0; run < 2; run++ {
		runtimeRole := newFakeProcess(protocol.RoleRuntime)
		supervisor, err := New(host, nil, testOptions(), runtimeRole)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		done := runSupervisor(t, supervisor, ctx)
		runtimeRole.ready <- ready(protocol.RoleRuntime)
		waitReady(t, supervisor)
		cancel()
		<-done
	}
	host.mu.Lock()
	defer host.mu.Unlock()
	if host.recover != 2 {
		t.Fatalf("recovery calls after restart = %d", host.recover)
	}
	if host.revoke != 2 {
		t.Fatalf("revocation calls after restart = %d", host.revoke)
	}
}

func TestSupervisorRejectsInvalidRoleSets(t *testing.T) {
	host := &fakeHost{}
	if _, err := New(host, nil, testOptions()); !errors.Is(err, ErrInvalidComposition) {
		t.Fatalf("missing R error = %v", err)
	}
	runtimeRole := newFakeProcess(protocol.RoleRuntime)
	if _, err := New(host, nil, testOptions(), runtimeRole, runtimeRole); !errors.Is(err, ErrInvalidComposition) {
		t.Fatalf("duplicate role error = %v", err)
	}
	broker := newFakeProcess(protocol.RoleNetworkBroker)
	if _, err := New(host, nil, testOptions(), runtimeRole, broker); !errors.Is(err, ErrInvalidComposition) {
		t.Fatalf("child N error = %v", err)
	}
}

func TestSupervisorRejectsReadyFromExitedRole(t *testing.T) {
	for attempt := 0; attempt < 100; attempt++ {
		process := newFakeProcess(protocol.RoleRuntime)
		process.ready <- ready(protocol.RoleRuntime)
		process.done <- errors.New("exited before startup")
		supervisor, err := New(&fakeHost{}, nil, testOptions(), process)
		if err != nil {
			t.Fatal(err)
		}
		if err := supervisor.Run(context.Background()); !errors.Is(err, ErrStartupFailed) {
			t.Fatalf("startup accepted an exited role: %v", err)
		}
		select {
		case <-supervisor.Ready():
			t.Fatal("published readiness for an exited role")
		default:
		}
	}
}

func TestStopRoleKillsAfterGracefulStopError(t *testing.T) {
	process := newFakeProcess(protocol.RoleRuntime)
	failure := errors.New("graceful stop failed")
	process.stop = func(context.Context) error { return failure }
	killed := make(chan struct{})
	process.kill = func() error { close(killed); return nil }
	if err := stopRole(context.Background(), process, time.Second); !errors.Is(err, failure) {
		t.Fatalf("stop error lost: %v", err)
	}
	select {
	case <-killed:
	default:
		t.Fatal("failed stop did not kill the role")
	}
}

func TestStopRoleBoundsHungKill(t *testing.T) {
	process := newFakeProcess(protocol.RoleRuntime)
	failure := errors.New("graceful stop failed")
	process.stop = func(context.Context) error { return failure }
	release := make(chan struct{})
	defer close(release)
	process.kill = func() error { <-release; return nil }
	started := time.Now()
	err := stopRole(context.Background(), process, 10*time.Millisecond)
	if !errors.Is(err, failure) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("stop error = %v", err)
	}
	if time.Since(started) > time.Second {
		t.Fatal("kill wait was not bounded")
	}
}

func TestSupervisorRejectsNilContextWithoutStarting(t *testing.T) {
	host := &fakeHost{}
	supervisor, err := New(host, nil, testOptions(), newFakeProcess(protocol.RoleRuntime))
	if err != nil {
		t.Fatal(err)
	}
	if err := supervisor.Run(nil); !errors.Is(err, ErrInvalidComposition) { //nolint:staticcheck // Deliberate invalid input.
		t.Fatalf("nil context error = %v", err)
	}
	if host.recover != 0 || host.revoke != 0 || supervisor.started {
		t.Fatal("invalid context started supervision")
	}
}

func TestSupervisorStatusAfterCleanShutdown(t *testing.T) {
	process := newFakeProcess(protocol.RoleRuntime)
	supervisor, err := New(&fakeHost{}, nil, testOptions(), process)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := runSupervisor(t, supervisor, ctx)
	process.ready <- ready(protocol.RoleRuntime)
	waitReady(t, supervisor)
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("shutdown error = %v", err)
	}
	status := supervisor.Status()
	if status.Ready || status.RecoveryRequired || len(status.Roles) != 1 || status.Roles[0].Ready || status.Roles[0].Alive || status.Roles[0].Failed {
		t.Fatalf("clean shutdown status = %+v", status)
	}
}
