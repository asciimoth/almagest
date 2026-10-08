package daemonruntime

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestApplicationClosesOwnedServicesOnceInOrder(t *testing.T) {
	var mu sync.Mutex
	var calls []string
	record := func(name string) func() { return func() { mu.Lock(); calls = append(calls, name); mu.Unlock() } }
	recordErr := func(name string) func() error { return func() error { record(name)(); return nil } }
	a := New(record("cancel"))
	a.timeout = time.Second
	a.OwnAdmin(recordErr("admin"))
	a.OwnBackground(record("wait"))
	a.OwnEngine(recordErr("engine"))
	a.OwnHostNetwork(recordErr("network"))
	a.OwnConfigFlush(recordErr("flush"))
	a.OwnCommands(record("commands"))
	a.OwnNodeNames(record("names"))
	a.OwnStorage(recordErr("storage"))
	a.OwnFiles(recordErr("files"))
	a.OwnLogging(recordErr("logging"))
	if err := a.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if err := a.Close(); err != nil {
		t.Fatalf("second Close() error = %v", err)
	}
	mu.Lock()
	got := strings.Join(calls, ",")
	mu.Unlock()
	const want = "cancel,admin,wait,engine,flush,commands,names,storage,files,logging"
	if got != want {
		t.Fatalf("cleanup order = %q, want %q", got, want)
	}
}

func TestApplicationClosesNetworkAfterPartialConstruction(t *testing.T) {
	var calls int
	a := New(nil)
	a.OwnHostNetwork(func() error { calls++; return nil })
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("network close calls = %d, want 1", calls)
	}
}

func TestApplicationRetainsEngineCloseFailureAfterRecovery(t *testing.T) {
	failure := errors.New("engine close failed")
	var recoveryCalls int
	a := New(nil)
	a.OwnEngine(func() error { return failure })
	a.OwnHostNetwork(func() error { recoveryCalls++; return nil })
	if err := a.Close(); !errors.Is(err, failure) {
		t.Fatalf("Close() error = %v", err)
	}
	if recoveryCalls != 1 {
		t.Fatalf("recovery calls = %d, want 1", recoveryCalls)
	}
}

func TestApplicationBoundsCleanupStep(t *testing.T) {
	blocked := make(chan struct{})
	a := New(nil)
	a.timeout = 10 * time.Millisecond
	a.OwnHostNetwork(func() error { <-blocked; return nil })
	err := a.Close()
	close(blocked)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Close() error = %v", err)
	}
}

func TestRunClosesServicesAfterConstructionError(t *testing.T) {
	constructionErr := errors.New("construction failed")
	var closes int
	err := Run(context.Background(), func(_ context.Context, app *Application) error {
		app.OwnStorage(func() error { closes++; return nil })
		return constructionErr
	})
	if !errors.Is(err, constructionErr) {
		t.Fatalf("Run() error = %v, want construction error", err)
	}
	if closes != 1 {
		t.Fatalf("storage close calls = %d, want 1", closes)
	}
}

func TestRunHandlesShutdownDuringConstruction(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	cancel()
	var closes int
	err := Run(parent, func(ctx context.Context, app *Application) error {
		app.OwnStorage(func() error { closes++; return nil })
		return ctx.Err()
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error = %v, want canceled", err)
	}
	if closes != 1 {
		t.Fatalf("storage close calls = %d, want 1", closes)
	}
}
