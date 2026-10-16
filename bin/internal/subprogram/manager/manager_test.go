package manager

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/asciimoth/almagest/lib/sandbox"
)

type authorityFunc func(context.Context, sandbox.Owner) error

func (f authorityFunc) Authorize(ctx context.Context, o sandbox.Owner) error { return f(ctx, o) }

type backendFunc func(context.Context, sandbox.Owner, string, sandbox.TemplateID, uint64) (sandbox.Sandbox, error)

func (f backendFunc) Prepare(c context.Context, o sandbox.Owner, s string, t sandbox.TemplateID, g uint64) (sandbox.Sandbox, error) {
	return f(c, o, s, t, g)
}

type fakeSandbox struct {
	sandbox.Sandbox
	mu       sync.Mutex
	status   sandbox.Status
	done     chan struct{}
	closeErr error
	closes   int
}

func fake(o sandbox.Owner, t sandbox.TemplateID, g uint64) *fakeSandbox {
	return &fakeSandbox{status: sandbox.Status{Owner: o, Template: t, Generation: g, State: sandbox.Ready}, done: make(chan struct{})}
}
func (s *fakeSandbox) Status() sandbox.Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.status.Clone()
}
func (s *fakeSandbox) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closes++
	if s.closeErr != nil {
		return s.closeErr
	}
	select {
	case <-s.done:
	default:
		close(s.done)
	}
	s.status.State = sandbox.Closed
	return nil
}
func (s *fakeSandbox) Done() <-chan struct{} { return s.done }
func testOwner() sandbox.Owner {
	return sandbox.Owner{Workspace: "main", Key: "opaque-1", Epoch: 1, Class: "test/class", Name: "First", NameRevision: 1}
}
func managerFor(t *testing.T, b Backend) *Manager {
	t.Helper()
	m, err := New(b, authorityFunc(func(context.Context, sandbox.Owner) error { return nil }), Limits{Owners: 8, Instances: 8, Receipts: 16})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })
	return m
}
func defaultBackend(_ context.Context, o sandbox.Owner, _ string, template sandbox.TemplateID, g uint64) (sandbox.Sandbox, error) {
	return fake(o, template, g), nil
}
func TestOwnerRenameKeepsInstance(t *testing.T) {
	m := managerFor(t, backendFunc(defaultBackend))
	ctx := context.Background()
	o := testOwner()
	b, err := m.BindOwner(ctx, o)
	if err != nil {
		t.Fatal(err)
	}
	instance, err := b.Acquire(ctx, "main", "test")
	if err != nil {
		t.Fatal(err)
	}
	request := sandbox.OwnerRename{Owner: o, NewName: "Second", OperationID: "rename-1"}
	renamed, err := m.RebindOwner(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if renamed.Key != o.Key || renamed.Epoch != o.Epoch || renamed.NameRevision != 2 {
		t.Fatal(renamed)
	}
	replay, err := m.RebindOwner(ctx, request)
	if err != nil || replay != renamed {
		t.Fatal(replay, err)
	}
	again, err := b.Acquire(ctx, "main", "test")
	if err != nil {
		t.Fatal(err)
	}
	if again.Status().Generation != instance.Status().Generation || instance.Status().Owner != renamed {
		t.Fatal("rename replaced resource or retained old label")
	}
	request.NewName = "Third"
	if _, err = m.RebindOwner(ctx, request); !errors.Is(err, sandbox.ErrConflict) {
		t.Fatal(err)
	}
	other := o
	other.Key = "opaque-2"
	if _, err = m.BindOwner(ctx, other); err != nil {
		t.Fatal("old name remained reserved", err)
	}
	if err = m.ReleaseOwner(ctx, o); err != nil {
		t.Fatal("pre-rename handle cannot release", err)
	}
	if _, err = b.Acquire(ctx, "main", "test"); !errors.Is(err, sandbox.ErrClosed) {
		t.Fatal(err)
	}
}
func TestAcquireSingleInstanceAndGeneration(t *testing.T) {
	var calls atomic.Int32
	m := managerFor(t, backendFunc(func(c context.Context, o sandbox.Owner, s string, p sandbox.TemplateID, g uint64) (sandbox.Sandbox, error) {
		calls.Add(1)
		return defaultBackend(c, o, s, p, g)
	}))
	ctx := context.Background()
	b, err := m.BindOwner(ctx, testOwner())
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() {
			if _, e := b.Acquire(ctx, "slot", "test"); e != nil {
				t.Error(e)
			}
		})
	}
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatal(calls.Load())
	}
	old, err := b.Acquire(ctx, "slot", "test")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = b.Acquire(ctx, "slot", "other"); !errors.Is(err, sandbox.ErrConflict) {
		t.Fatal(err)
	}
	if err = old.Close(); err != nil {
		t.Fatal(err)
	}
	next, err := b.Acquire(ctx, "slot", "test")
	if err != nil {
		t.Fatal(err)
	}
	if next.Status().Generation == old.Status().Generation {
		t.Fatal("reused generation")
	}
	if err = old.Close(); !errors.Is(err, sandbox.ErrStale) {
		t.Fatal("stale close", err)
	}
	if next.Status().State != sandbox.Ready {
		t.Fatal("stale close stopped replacement")
	}
}
func TestReleaseDuringPrepare(t *testing.T) {
	started := make(chan struct{})
	finish := make(chan struct{})
	disposed := make(chan *fakeSandbox, 1)
	m := managerFor(t, backendFunc(func(_ context.Context, o sandbox.Owner, _ string, p sandbox.TemplateID, g uint64) (sandbox.Sandbox, error) {
		close(started)
		<-finish
		instance := fake(o, p, g)
		disposed <- instance
		return instance, nil
	}))
	ctx := context.Background()
	o := testOwner()
	b, err := m.BindOwner(ctx, o)
	if err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() { _, e := b.Acquire(ctx, "slot", "test"); result <- e }()
	<-started
	if err = m.ReleaseOwner(ctx, o); err != nil {
		t.Fatal(err)
	}
	close(finish)
	if err = <-result; !errors.Is(err, sandbox.ErrClosed) {
		t.Fatal(err)
	}
	partial := <-disposed
	select {
	case <-partial.Done():
	case <-time.After(time.Second):
		t.Fatal("late resource leaked")
	}
	if err = m.Close(); err != nil {
		t.Fatal(err)
	}
}
func TestCanceledPrepareDoesNotPublish(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	m := managerFor(t, backendFunc(func(_ context.Context, o sandbox.Owner, _ string, p sandbox.TemplateID, g uint64) (sandbox.Sandbox, error) {
		cancel()
		return fake(o, p, g), nil
	}))
	b, err := m.BindOwner(ctx, testOwner())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = b.Acquire(ctx, "slot", "test"); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
func TestFailedCleanupQuarantinesOwner(t *testing.T) {
	failure := errors.New("unproven tree exit")
	m := managerFor(t, backendFunc(func(_ context.Context, o sandbox.Owner, _ string, p sandbox.TemplateID, g uint64) (sandbox.Sandbox, error) {
		s := fake(o, p, g)
		s.closeErr = failure
		return s, nil
	}))
	ctx := context.Background()
	b, err := m.BindOwner(ctx, testOwner())
	if err != nil {
		t.Fatal(err)
	}
	s, err := b.Acquire(ctx, "slot", "test")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	if _, err = b.Acquire(ctx, "slot", "test"); !errors.Is(err, sandbox.ErrClosed) {
		t.Fatal("reused unproven state", err)
	}
	if err = m.Close(); !errors.Is(err, failure) {
		t.Fatal("cleanup failure hidden", err)
	}
}
func TestAuthorityAndQuota(t *testing.T) {
	m, err := New(backendFunc(defaultBackend), authorityFunc(func(context.Context, sandbox.Owner) error { return sandbox.ErrDenied }), Limits{Owners: 1, Instances: 1, Receipts: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = m.Close() }()
	if _, err = m.BindOwner(context.Background(), testOwner()); !errors.Is(err, sandbox.ErrDenied) {
		t.Fatal(err)
	}
	m = managerFor(t, backendFunc(defaultBackend))
	m.limits.Instances = 1
	b, err := m.BindOwner(context.Background(), testOwner())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = b.Acquire(context.Background(), "one", "test"); err != nil {
		t.Fatal(err)
	}
	if _, err = b.Acquire(context.Background(), "two", "test"); !errors.Is(err, sandbox.ErrQuota) {
		t.Fatal(err)
	}
}

func TestNewEpochWaitsForCleanup(t *testing.T) {
	m := managerFor(t, backendFunc(defaultBackend))
	ctx := context.Background()
	o := testOwner()
	b, err := m.BindOwner(ctx, o)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = b.Acquire(ctx, "slot", "test"); err != nil {
		t.Fatal(err)
	}
	next := o
	next.Epoch++
	if _, err = m.BindOwner(ctx, next); !errors.Is(err, sandbox.ErrStale) {
		t.Fatal("accepted new epoch before release", err)
	}
	if err = m.ReleaseOwner(ctx, o); err != nil {
		t.Fatal(err)
	}
	<-m.owners[key(o)].done
	restored, err := m.BindOwner(ctx, next)
	if err != nil {
		t.Fatal(err)
	}
	instance, err := restored.Acquire(ctx, "slot", "test")
	if err != nil {
		t.Fatal(err)
	}
	if err = m.ReleaseOwner(ctx, o); !errors.Is(err, sandbox.ErrStale) {
		t.Fatal("old release reached new epoch", err)
	}
	if _, err = b.Acquire(ctx, "slot", "test"); !errors.Is(err, sandbox.ErrClosed) {
		t.Fatal("old binding revived", err)
	}
	if instance.Status().Owner.Epoch != next.Epoch {
		t.Fatal(instance.Status())
	}
}

func TestAcquireWaitCanCancel(t *testing.T) {
	entered := make(chan struct{})
	finish := make(chan struct{})
	m := managerFor(t, backendFunc(func(_ context.Context, o sandbox.Owner, _ string, p sandbox.TemplateID, g uint64) (sandbox.Sandbox, error) {
		close(entered)
		<-finish
		return fake(o, p, g), nil
	}))
	b, err := m.BindOwner(context.Background(), testOwner())
	if err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() { _, e := b.Acquire(context.Background(), "one", "test"); result <- e }()
	<-entered
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	waiting := make(chan error, 1)
	go func() { _, e := b.Acquire(ctx, "two", "test"); waiting <- e }()
	select {
	case err = <-waiting:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Error(err)
		}
	case <-time.After(time.Second):
		t.Error("canceled waiter blocked behind preparation")
	}

	close(finish)
	if err = <-result; err != nil {
		t.Fatal(err)
	}
}
