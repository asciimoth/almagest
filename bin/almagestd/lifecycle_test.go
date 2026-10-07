package main

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/asciimoth/almagest/bin/almagestd/internal/daemonruntime"
)

// TestDaemonLifecycleClosesOwnedResourcesOnceInOrder retains the qualification
// case name used by the Windows matrix. Detailed lifecycle tests are in
// internal/daemonruntime.
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

	err := daemonruntime.Run(context.Background(), func(_ context.Context, app *daemonruntime.Application) error {
		app.OwnAdmin(recordErr("admin"))
		app.OwnBackground(record("wait"))
		app.OwnEngine(recordErr("engine"))
		app.OwnConfigFlush(recordErr("flush"))
		app.OwnCommands(record("commands"))
		app.OwnNodeNames(record("names"))
		app.OwnStorage(recordErr("storage"))
		app.OwnFiles(recordErr("files"))
		return nil
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	mu.Lock()
	got := strings.Join(calls, ",")
	mu.Unlock()
	const want = "admin,wait,engine,flush,commands,names,storage,files"
	if got != want {
		t.Fatalf("cleanup order = %q, want %q", got, want)
	}
}
