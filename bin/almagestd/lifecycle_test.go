package main

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestDaemonLifecycleClosesOwnedResourcesOnceInOrder(t *testing.T) {
	var mu sync.Mutex
	var calls []string
	record := func(name string) func() {
		return func() {
			mu.Lock()
			calls = append(calls, name)
			mu.Unlock()
		}
	}
	recordErr := func(name string) func() error {
		return func() error { record(name)(); return nil }
	}

	lifecycle := &daemonLifecycle{
		cancel:         record("cancel"),
		stopAdmin:      recordErr("admin"),
		waitBackground: record("wait"),
		closeEngine:    recordErr("engine"),
		closeSystem:    recordErr("system"),
		flushConfig:    recordErr("flush"),
		closeRunner:    record("runner"),
		closeNodeNames: record("names"),
		closeStorage:   recordErr("storage"),
		cleanupFiles:   recordErr("files"),
		timeout:        time.Second,
	}
	if err := lifecycle.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if err := lifecycle.Close(); err != nil {
		t.Fatalf("second Close() error = %v", err)
	}
	mu.Lock()
	got := strings.Join(calls, ",")
	mu.Unlock()
	const want = "cancel,admin,wait,engine,flush,runner,names,storage,files"
	if got != want {
		t.Fatalf("cleanup order = %q, want %q", got, want)
	}
}

func TestDaemonLifecycleRetainsCloseFailureAfterRecovery(t *testing.T) {
	closeFailure := errors.New("first close failed")
	var recoveryCalls int
	lifecycle := &daemonLifecycle{
		closeEngine: func() error { return closeFailure },
		closeSystem: func() error { recoveryCalls++; return nil },
		timeout:     time.Second,
	}
	err := lifecycle.Close()
	if !errors.Is(err, closeFailure) {
		t.Fatalf("Close() error = %v, want retained first failure", err)
	}
	if recoveryCalls != 1 {
		t.Fatalf("recovery calls = %d, want 1", recoveryCalls)
	}
}

func TestDaemonLifecycleClosesSystemAfterPartialStartup(t *testing.T) {
	var systemCalls int
	lifecycle := &daemonLifecycle{
		closeSystem: func() error { systemCalls++; return nil },
		timeout:     time.Second,
	}
	if err := lifecycle.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if systemCalls != 1 {
		t.Fatalf("system close calls = %d, want 1", systemCalls)
	}
}

func TestDaemonLifecycleBoundsCleanupStep(t *testing.T) {
	blocked := make(chan struct{})
	lifecycle := &daemonLifecycle{
		closeSystem: func() error { <-blocked; return nil },
		timeout:     10 * time.Millisecond,
	}
	started := time.Now()
	err := lifecycle.Close()
	close(blocked)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Close() error = %v, want deadline", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("Close() elapsed = %s, want bounded cleanup", elapsed)
	}
}
