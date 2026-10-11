package network

import (
	"context"
	"errors"
	"net"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/asciimoth/almagest/bin/internal/isolation/protocol"
	client "github.com/asciimoth/almagest/bin/internal/isolation/runtime"
	"github.com/asciimoth/almagest/bin/internal/isolation/supervision"
	"github.com/asciimoth/almagest/bin/internal/isolation/transport"
)

type serviceReconciler struct {
	inspect func(context.Context, supervision.Effect) (supervision.IdentityState, error)
}

func (r serviceReconciler) Inspect(ctx context.Context, e supervision.Effect) (supervision.IdentityState, error) {
	if r.inspect != nil {
		return r.inspect(ctx, e)
	}
	return supervision.IdentityAbsent, nil
}
func (serviceReconciler) Cleanup(context.Context, supervision.Effect) error {
	return supervision.ErrRecoveryUncertain
}

func serviceFixture(t *testing.T) (*Broker, transport.Lanes, transport.Lanes, supervision.Process, supervision.Options) {
	t.Helper()
	b, _ := testBroker(t)
	var err error
	b.journal, err = supervision.OpenJournal(filepath.Join(t.TempDir(), "journal.json"), 16)
	if err != nil {
		t.Fatal(err)
	}
	a, z := net.Pipe()
	c, d := net.Pipe()
	e, g := net.Pipe()
	server, runtime := transport.Lanes{Control: a, Data: c, Diagnostic: e}, transport.Lanes{Control: z, Data: d, Diagnostic: g}
	t.Cleanup(func() { server.Close(); runtime.Close() })
	proof := make(chan supervision.ReadyResult, 1)
	proof <- supervision.ReadyResult{Role: protocol.RoleRuntime, Version: protocol.Version, PolicyDigest: b.session.PolicyDigest, RestrictionsApplied: true}
	exited := make(chan struct{})
	var once sync.Once
	stop := func() error { once.Do(func() { close(exited) }); return nil }
	t.Cleanup(func() { _ = stop() })
	p, err := supervision.NewStartedProcess(protocol.RoleRuntime, proof, func() error { <-exited; return nil }, func(context.Context) error { return stop() }, stop)
	if err != nil {
		t.Fatal(err)
	}
	return b, server, runtime, p, supervision.Options{StartupTimeout: time.Second, ShutdownTimeout: time.Second, RecoveryTimeout: time.Second}
}

func TestServiceRecoversBeforeRPCAndRequiresApplicationReadiness(t *testing.T) {
	b, server, runtime, p, options := serviceFixture(t)
	if err := b.journal.Put(supervision.Effect{ID: "old-effect", SessionID: b.session.ID, ResourceType: protocol.ResourceTun, State: supervision.EffectOwned, Identity: "old-owned-identity"}); err != nil {
		t.Fatal(err)
	}
	inspecting, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	reconciler := serviceReconciler{inspect: func(ctx context.Context, _ supervision.Effect) (supervision.IdentityState, error) {
		close(inspecting)
		select {
		case <-release:
			return supervision.IdentityAbsent, nil
		case <-ctx.Done():
			return supervision.IdentityUnknown, ctx.Err()
		}
	}}
	// An unrelated caller-supplied signal must not bypass the broker signal.
	forged := make(chan struct{})
	close(forged)
	options.ApplicationReady = forged
	s, err := NewService(b, server, reconciler, time.Second, options, nil, p)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()
	connected := make(chan *client.Client, 1)
	connectErr := make(chan error, 1)
	go func() {
		c, err := client.New(ctx, runtime, b.session)
		if err != nil {
			connectErr <- err
			return
		}
		connected <- c
	}()
	select {
	case <-inspecting:
	case <-ctx.Done():
		t.Fatal("recovery did not start")
	}
	select {
	case <-connected:
		t.Fatal("RPC connected before recovery finished")
	case err := <-connectErr:
		t.Fatal(err)
	case <-time.After(20 * time.Millisecond):
	}
	once.Do(func() { close(release) })
	var c *client.Client
	select {
	case c = <-connected:
	case err := <-connectErr:
		t.Fatal(err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	defer c.Close() //nolint:errcheck
	if len(b.journal.Snapshot()) != 0 {
		t.Fatal("accepted RPC before resolving old journal")
	}
	select {
	case <-s.Ready():
		t.Fatal("forged application signal bypassed startup")
	case <-time.After(20 * time.Millisecond):
	}
	if err := c.MarkReady(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-s.Ready():
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if !s.Status().Ready {
		t.Fatal("service not ready")
	}
	_ = c.Close()
	select {
	case err := <-done:
		if !errors.Is(err, supervision.ErrRuntimeLost) {
			t.Fatalf("broker loss: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("broker loss did not stop process set")
	}
	if status := s.Status(); status.Ready || status.Roles[0].Alive {
		t.Fatalf("shutdown status: %+v", status)
	}
	if err := s.Run(ctx); !errors.Is(err, supervision.ErrInvalidComposition) {
		t.Fatalf("session reused: %v", err)
	}
}

func TestServiceUnresolvedRecoveryNeverServes(t *testing.T) {
	b, server, _, p, options := serviceFixture(t)
	if err := b.journal.Put(supervision.Effect{ID: "uncertain", SessionID: b.session.ID, ResourceType: protocol.ResourceTun, State: supervision.EffectPending}); err != nil {
		t.Fatal(err)
	}
	s, err := NewService(b, server, serviceReconciler{}, time.Second, options, nil, p)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Run(context.Background()); !errors.Is(err, supervision.ErrStartupFailed) {
		t.Fatalf("unresolved recovery: %v", err)
	}
	if s.serving || !s.Status().RecoveryRequired || len(b.journal.Snapshot()) != 1 {
		t.Fatal("unresolved recovery did not remain closed and visible")
	}
}

func TestServiceRejectsIncompleteOrReusedComposition(t *testing.T) {
	for _, missing := range []string{"journal", "control", "data", "diagnostic", "reconciler", "process", "timeout", "used"} {
		t.Run(missing, func(t *testing.T) {
			b, server, _, p, options := serviceFixture(t)
			var reconciler supervision.Reconciler = serviceReconciler{}
			processes := []supervision.Process{p}
			switch missing {
			case "journal":
				b.journal = nil
			case "control":
				server.Control = nil
			case "data":
				server.Data = nil
			case "diagnostic":
				server.Diagnostic = nil
			case "reconciler":
				reconciler = nil
			case "process":
				processes = nil
			case "timeout":
				options.ShutdownTimeout = 0
			case "used":
				if _, err := NewService(b, server, reconciler, time.Second, options, nil, processes...); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := NewService(b, server, reconciler, time.Second, options, nil, processes...); !errors.Is(err, supervision.ErrInvalidComposition) {
				t.Fatalf("missing %s: %v", missing, err)
			}
		})
	}
}

func TestServiceLateRecoveryCannotStartServer(t *testing.T) {
	b, server, _, p, options := serviceFixture(t)
	if err := b.journal.Put(supervision.Effect{ID: "old", SessionID: b.session.ID, ResourceType: protocol.ResourceTun, State: supervision.EffectOwned, Identity: "old-identity"}); err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	inspecting := make(chan struct{})
	reconciler := serviceReconciler{inspect: func(context.Context, supervision.Effect) (supervision.IdentityState, error) {
		close(inspecting)
		<-release // Model a native inspection which ignores its deadline.
		return supervision.IdentityAbsent, nil
	}}
	s, err := NewService(b, server, reconciler, 20*time.Millisecond, options, nil, p)
	if err != nil {
		t.Fatal(err)
	}
	// Exercise the callback after its supervisor deadline, and join the whole
	// callback (including journal persistence), not just the native inspector.
	s.ctx, s.cancel = context.WithCancel(context.Background())
	s.active = true
	defer s.cancel()
	host := serviceHost{BrokerHost: supervision.BrokerHost{Journal: b.journal, Reconciler: reconciler, Broker: b, PerEffect: time.Second}, service: s}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	var recoverErr error
	go func() { recoverErr = host.Recover(ctx); close(done) }()
	t.Cleanup(func() {
		cancel()
		once.Do(func() { close(release) })
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("recovery callback did not finish")
		}
	})
	select {
	case <-inspecting:
	case <-time.After(time.Second):
		t.Fatal("recovery inspection did not start")
	}
	cancel()
	s.mu.Lock()
	s.active = false
	s.cancel()
	s.mu.Unlock()
	once.Do(func() { close(release) })
	select {
	case <-done:
		if !errors.Is(recoverErr, context.Canceled) {
			t.Fatalf("late recovery: %v", recoverErr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("recovery callback did not finish")
	}
	s.mu.Lock()
	serving, active := s.serving, s.active
	s.mu.Unlock()
	if serving || active {
		t.Fatal("late recovery started a closed service")
	}
	select {
	case <-s.Ready():
		t.Fatal("late recovery published readiness")
	default:
	}
}
