package main

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	almagest "github.com/asciimoth/almagest/lib"
	"github.com/asciimoth/almagest/lib/logging"
	sysnetdebug "github.com/asciimoth/gonnect/sysnet/debug"
	"github.com/asciimoth/pasta/pasta"
)

type configAutosaveTestWriter struct {
	mu         sync.Mutex
	writes     [][]byte
	failWrites int
	attempts   int
	notify     chan struct{}
}

func (w *configAutosaveTestWriter) WriteConfig(data []byte) error {
	w.mu.Lock()
	w.attempts++
	if w.failWrites > 0 {
		w.failWrites--
		w.mu.Unlock()
		w.notifyAttempt()
		return errors.New("test config write failure")
	}
	w.writes = append(w.writes, append([]byte(nil), data...))
	w.mu.Unlock()
	w.notifyAttempt()
	return nil
}

func (w *configAutosaveTestWriter) notifyAttempt() {
	if w.notify == nil {
		return
	}
	select {
	case w.notify <- struct{}{}:
	default:
	}
}

func (w *configAutosaveTestWriter) snapshot() [][]byte {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make([][]byte, len(w.writes))
	for i, data := range w.writes {
		out[i] = append([]byte(nil), data...)
	}
	return out
}

func (w *configAutosaveTestWriter) attemptCount() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.attempts
}

func TestConfigAutosaverFlushesPendingGraphChangeOnShutdown(t *testing.T) {
	conf, err := ParseConfig([]byte(`{
		"nodes": {
			"A": {"Class": "test/ConfigAPIReconcileNode", "Links": ["Out -> [B] In"]},
			"B": {"Class": "test/ConfigAPIReconcileNode"}
		}
	}`), []byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	engine, err := almagest.BuildEngine(
		context.Background(), conf.Main, logging.NewDebugLogFactory(32), &sysnetdebug.System{},
		nil, nil, nil, nil, []pasta.NodeClass{configAPIReconcileNodeClass{}}, nil, false,
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = engine.Close() })

	snapshot, ok := engine.WorkspaceSnapshot(almagest.MainWorkspace)
	if !ok || len(snapshot.Links) != 1 {
		t.Fatalf("initial links = %d, want 1", len(snapshot.Links))
	}
	for id := range snapshot.Links {
		engine.RemoveLink(almagest.MainWorkspace, id)
	}

	writer := &configAutosaveTestWriter{}
	autosaver := newConfigAutosaver(
		engine, conf, writer, logging.NewDebugLogFactory(8).EngineLogger(), time.Hour,
	)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		autosaver.Run(ctx)
		close(done)
	}()
	autosaver.Schedule()
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("autosaver did not stop")
	}

	writes := writer.snapshot()
	if len(writes) != 1 {
		t.Fatalf("config writes = %d, want shutdown flush", len(writes))
	}
	if strings.Contains(string(writes[0]), "Out -> [B] In") {
		t.Fatalf("shutdown config retained removed link: %s", writes[0])
	}
}

func TestConfigAutosaverDoesNotWriteCleanConfigOnShutdown(t *testing.T) {
	conf, err := ParseConfig([]byte(`{"nodes": {}}`), []byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	engine, err := almagest.BuildEngine(
		context.Background(), conf.Main, logging.NewDebugLogFactory(16), &sysnetdebug.System{},
		nil, nil, nil, nil, nil, nil, false,
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = engine.Close() })

	writer := &configAutosaveTestWriter{}
	autosaver := newConfigAutosaver(
		engine, conf, writer, logging.NewDebugLogFactory(8).EngineLogger(), time.Hour,
	)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	autosaver.Run(ctx)

	if writes := writer.snapshot(); len(writes) != 0 {
		t.Fatalf("config writes = %d, want none", len(writes))
	}
}

func TestConfigAutosaverPersistsChangeScheduledAfterRunStops(t *testing.T) {
	conf, err := ParseConfig([]byte(`{
		"nodes": {
			"A": {"Class": "test/ConfigAPIReconcileNode", "Links": ["Out -> [B] In"]},
			"B": {"Class": "test/ConfigAPIReconcileNode"}
		}
	}`), []byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	engine, err := almagest.BuildEngine(
		context.Background(), conf.Main, logging.NewDebugLogFactory(32), &sysnetdebug.System{},
		nil, nil, nil, nil, []pasta.NodeClass{configAPIReconcileNodeClass{}}, nil, false,
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = engine.Close() })

	writer := &configAutosaveTestWriter{}
	autosaver := newConfigAutosaver(
		engine, conf, writer, logging.NewDebugLogFactory(8).EngineLogger(), time.Hour,
	)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	autosaver.Run(ctx)

	snapshot, ok := engine.WorkspaceSnapshot(almagest.MainWorkspace)
	if !ok || len(snapshot.Links) != 1 {
		t.Fatalf("initial links = %d, want 1", len(snapshot.Links))
	}
	for id := range snapshot.Links {
		engine.RemoveLink(almagest.MainWorkspace, id)
	}
	autosaver.Schedule()

	writes := writer.snapshot()
	if len(writes) != 1 {
		t.Fatalf("config writes = %d, want late shutdown flush", len(writes))
	}
	if strings.Contains(string(writes[0]), "Out -> [B] In") {
		t.Fatalf("late shutdown config retained removed link: %s", writes[0])
	}
}

func TestConfigAutosaverRetriesPendingChangeAfterWriteFailure(t *testing.T) {
	conf, err := ParseConfig([]byte(`{"nodes": {}}`), []byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	engine, err := almagest.BuildEngine(
		context.Background(), conf.Main, logging.NewDebugLogFactory(16), &sysnetdebug.System{},
		nil, nil, nil, nil, nil, nil, false,
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = engine.Close() })

	writer := &configAutosaveTestWriter{
		failWrites: 1,
		notify:     make(chan struct{}, 2),
	}
	autosaver := newConfigAutosaver(
		engine, conf, writer, logging.NewDebugLogFactory(8).EngineLogger(), time.Millisecond,
	)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		autosaver.Run(ctx)
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})

	autosaver.Schedule()
	for attempt := 1; attempt <= 2; attempt++ {
		select {
		case <-writer.notify:
		case <-time.After(time.Second):
			t.Fatalf("config write attempts = %d, want retry attempt %d", writer.attemptCount(), attempt)
		}
	}

	if attempts := writer.attemptCount(); attempts != 2 {
		t.Fatalf("config write attempts = %d, want 2", attempts)
	}
	if writes := writer.snapshot(); len(writes) != 1 {
		t.Fatalf("successful config writes = %d, want 1", len(writes))
	}
}
