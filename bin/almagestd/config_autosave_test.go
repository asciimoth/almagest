package main

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/asciimoth/almagest/bin/almagestd/daemonservices/localconfig"
	almagest "github.com/asciimoth/almagest/lib"
	"github.com/asciimoth/almagest/lib/logging"
	sysnetdebug "github.com/asciimoth/gonnect/sysnet/debug"
	"github.com/asciimoth/pasta/pasta"
)

func TestConfigAutosaverSerializesConcurrentRevisionUpdates(t *testing.T) {
	conf, err := ParseConfig([]byte(`{"nodes": {}}`), []byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	engine, err := almagest.BuildEngine(
		context.Background(), conf.Main, logging.NewDebugLogFactory(32), &sysnetdebug.System{},
		nil, nil, nil, nil, []pasta.NodeClass{adminAPIBatchNodeClass{}}, nil, false,
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = engine.Close() })
	current, err := conf.MainToBytes()
	if err != nil {
		t.Fatal(err)
	}
	var targetMu sync.Mutex
	target := bytes.Clone(current)
	persistence := localconfig.New(
		func() ([]byte, error) {
			targetMu.Lock()
			defer targetMu.Unlock()
			return bytes.Clone(target), nil
		},
		func(data []byte) error {
			targetMu.Lock()
			defer targetMu.Unlock()
			target = bytes.Clone(data)
			return nil
		},
	)
	saver := newConfigAutosaver(engine, conf, persistence, logging.NewDebugLogFactory(8).EngineLogger(), time.Second)
	snapshot, err := saver.Snapshot(false)
	if err != nil {
		t.Fatal(err)
	}

	texts := []string{
		`{"nodes":{"first":{"Class":"test/AdminAPIBatchNode"}}}`,
		`{"nodes":{"second":{"Class":"test/AdminAPIBatchNode"}}}`,
	}
	errorsFound := make(chan error, len(texts))
	var group sync.WaitGroup
	for _, text := range texts {
		group.Add(1)
		go func() {
			defer group.Done()
			_, updateErr := saver.Update(text, snapshot.Revision)
			errorsFound <- updateErr
		}()
	}
	group.Wait()
	close(errorsFound)
	successes, conflicts := 0, 0
	for updateErr := range errorsFound {
		switch {
		case updateErr == nil:
			successes++
		case errors.Is(updateErr, errConfigRevisionConflict):
			conflicts++
		default:
			t.Fatalf("Update() error = %v", updateErr)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("updates = %d successful, %d conflicts", successes, conflicts)
	}
	_, first := engine.NodeByName(almagest.MainWorkspace, "first")
	_, second := engine.NodeByName(almagest.MainWorkspace, "second")
	if first == second {
		t.Fatalf("active graph = first %v, second %v; want one committed node", first, second)
	}
	stored, err := conf.MainToBytes()
	if err != nil {
		t.Fatal(err)
	}
	targetMu.Lock()
	defer targetMu.Unlock()
	if !bytes.Equal(stored, target) {
		t.Fatalf("published config and fixed target differ:\npublished: %s\ntarget: %s", stored, target)
	}
}

func TestConfigAutosaverBuiltInNodeSnapshotsAreDeterministic(t *testing.T) {
	conf, err := ParseConfig([]byte(`{
		// This comment must stay attached to the document.
		"nodes": {
			"Proxy": {
				"Class": "almagest/LocalProxy",
				"socksHost": "127.0.0.1",
				"socksPort": 0
			},
			"Router": {
				"Class": "almagest/NetworkRouter",
				"rules": "TRUE\nSLOT 1"
			},
			"Mock": {
				"Class": "almagest/MockNetwork",
				"allowAnyHost": true
			},
			"Server": {
				"Class": "almagest/HttpServer",
				"host": "127.0.0.1",
				"port": 8080,
				"response": "stable"
			}
		}
	}`), []byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	engine, err := almagest.BuildEngine(
		context.Background(), conf.Main, logging.NewDebugLogFactory(32), &sysnetdebug.System{},
		nil, nil, nil, nil, nil, nil, false,
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = engine.Close() })

	writer := &configAutosaveTestWriter{}
	autosaver := newConfigAutosaver(
		engine, conf, newTestConfigPersistence(t, conf, writer), logging.NewDebugLogFactory(8).EngineLogger(), 0,
	)
	first, err := autosaver.Snapshot(false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(first.Text, "This comment must stay attached") {
		t.Fatalf("first snapshot removed the document comment: %s", first.Text)
	}

	for iteration := range 200 {
		current, snapshotErr := autosaver.Snapshot(false)
		if snapshotErr != nil {
			t.Fatalf("snapshot %d: %v", iteration, snapshotErr)
		}
		if current != first {
			t.Fatalf("snapshot %d changed without a graph mutation:\nfirst: %#v\ncurrent: %#v", iteration, first, current)
		}
	}

	readOnly, err := autosaver.Snapshot(true)
	if err != nil {
		t.Fatal(err)
	}
	if readOnly.Text != first.Text || readOnly.Revision != first.Revision || !readOnly.ReadOnly {
		t.Fatalf("read-only snapshot = %#v, want the same text and revision", readOnly)
	}

	updated, err := autosaver.Update(first.Text, first.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(updated.Changes, almagest.ConfigReconcileResult{}) {
		t.Fatalf("equivalent update changes = %#v, want none", updated.Changes)
	}
	if updated.Snapshot != first {
		t.Fatalf("equivalent update changed snapshot:\nfirst: %#v\nupdated: %#v", first, updated.Snapshot)
	}
	if len(writer.snapshot()) != 1 {
		t.Fatalf("equivalent update writes = %d, want 1", len(writer.snapshot()))
	}

	for iteration := range 100 {
		current, snapshotErr := autosaver.Snapshot(false)
		if snapshotErr != nil {
			t.Fatalf("post-update snapshot %d: %v", iteration, snapshotErr)
		}
		if current != first {
			t.Fatalf("post-update snapshot %d changed:\nfirst: %#v\ncurrent: %#v", iteration, first, current)
		}
	}
}

type configAutosaveTestWriter struct {
	mu                 sync.Mutex
	writes             [][]byte
	failWrites         int
	attempts           int
	notify             chan struct{}
	releaseFailedWrite <-chan struct{}
}

func (w *configAutosaveTestWriter) WriteConfig(data []byte) error {
	w.mu.Lock()
	w.attempts++
	if w.failWrites > 0 {
		w.failWrites--
		w.mu.Unlock()
		w.notifyAttempt()
		if w.releaseFailedWrite != nil {
			<-w.releaseFailedWrite
		}
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
		engine, conf, newTestConfigPersistence(t, conf, writer), logging.NewDebugLogFactory(8).EngineLogger(), time.Hour,
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
		engine, conf, newTestConfigPersistence(t, conf, writer), logging.NewDebugLogFactory(8).EngineLogger(), time.Hour,
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
		engine, conf, newTestConfigPersistence(t, conf, writer), logging.NewDebugLogFactory(8).EngineLogger(), time.Hour,
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
		engine, conf, newTestConfigPersistence(t, conf, writer), logging.NewDebugLogFactory(8).EngineLogger(), time.Millisecond,
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

func TestNodeConfigAutosaveTriggerQueuesStartupAndRuntimeChanges(t *testing.T) {
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

	writer := &configAutosaveTestWriter{notify: make(chan struct{}, 4)}
	saver := newConfigAutosaver(engine, conf, newTestConfigPersistence(t, conf, writer), logging.NewDebugLogFactory(8).EngineLogger(), time.Millisecond)
	trigger := newNodeConfigAutosaveTrigger()

	// A node can finish asynchronous startup before the daemon starts the
	// autosave workers. The buffered notification must survive that interval.
	startupResult := trigger.Notify(41)
	select {
	case err := <-startupResult:
		t.Fatalf("startup persistence completed before the worker started: %v", err)
	default:
	}
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); saver.Run(ctx) }()
	go func() { defer wg.Done(); trigger.Run(ctx, saver) }()
	defer func() {
		cancel()
		wg.Wait()
	}()

	select {
	case <-writer.notify:
	case <-time.After(time.Second):
		t.Fatal("queued startup configuration change was not saved")
	}
	if err := waitNodeConfigPersistence(t, startupResult); err != nil {
		t.Fatalf("startup persistence: %v", err)
	}

	runtimeResult := trigger.Notify(42)
	select {
	case <-writer.notify:
	case <-time.After(time.Second):
		t.Fatal("runtime configuration change was not saved")
	}
	if err := waitNodeConfigPersistence(t, runtimeResult); err != nil {
		t.Fatalf("runtime persistence: %v", err)
	}
	if writes := writer.snapshot(); len(writes) != 2 {
		t.Fatalf("config writes = %d, want 2", len(writes))
	}
}

func TestNodeConfigAutosaveTriggerAcknowledgesOnlySuccessfulDurableWrite(t *testing.T) {
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

	releaseFailedWrite := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseFailedWrite) }) }
	defer release()
	writer := &configAutosaveTestWriter{
		failWrites: 1, notify: make(chan struct{}, 4), releaseFailedWrite: releaseFailedWrite,
	}
	saver := newConfigAutosaver(engine, conf, newTestConfigPersistence(t, conf, writer), logging.NewDebugLogFactory(8).EngineLogger(), time.Millisecond)
	trigger := newNodeConfigAutosaveTrigger()
	result := trigger.Notify(1)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		trigger.Run(ctx, saver)
		close(done)
	}()
	t.Cleanup(func() { cancel(); <-done })

	select {
	case <-writer.notify:
	case <-time.After(time.Second):
		t.Fatal("first persistence attempt did not run")
	}
	select {
	case err = <-result:
		t.Fatalf("failed write was acknowledged: %v", err)
	default:
	}
	release()
	select {
	case <-writer.notify:
	case <-time.After(time.Second):
		t.Fatal("persistence retry did not run")
	}
	if err = waitNodeConfigPersistence(t, result); err != nil {
		t.Fatalf("successful retry result: %v", err)
	}
	if got := writer.attemptCount(); got != 2 {
		t.Fatalf("write attempts = %d, want 2", got)
	}
}

func TestNodeConfigAutosaveTriggerBatchesQueuedAcknowledgements(t *testing.T) {
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
	saver := newConfigAutosaver(engine, conf, newTestConfigPersistence(t, conf, writer), logging.NewDebugLogFactory(8).EngineLogger(), time.Millisecond)
	trigger := newNodeConfigAutosaveTrigger()
	first := trigger.Notify(1)
	second := trigger.Notify(2)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { trigger.Run(ctx, saver); close(done) }()
	defer func() { cancel(); <-done }()

	if err = waitNodeConfigPersistence(t, first); err != nil {
		t.Fatal(err)
	}
	if err = waitNodeConfigPersistence(t, second); err != nil {
		t.Fatal(err)
	}
	if got := writer.attemptCount(); got != 1 {
		t.Fatalf("write attempts = %d, want one batched write", got)
	}
}

func TestNodeConfigAutosaveTriggerReturnsFinalShutdownFailure(t *testing.T) {
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

	writer := &configAutosaveTestWriter{failWrites: 100, notify: make(chan struct{}, 2)}
	saver := newConfigAutosaver(engine, conf, newTestConfigPersistence(t, conf, writer), logging.NewDebugLogFactory(8).EngineLogger(), time.Hour)
	trigger := newNodeConfigAutosaveTrigger()
	result := trigger.Notify(1)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { trigger.Run(ctx, saver); close(done) }()
	select {
	case <-writer.notify:
	case <-time.After(time.Second):
		t.Fatal("persistence attempt did not run")
	}
	cancel()
	if err = waitNodeConfigPersistence(t, result); err == nil || !strings.Contains(err.Error(), "test config write failure") {
		t.Fatalf("shutdown persistence result = %v, want write failure", err)
	}
	<-done
	if err = waitNodeConfigPersistence(t, trigger.Notify(2)); err == nil || !strings.Contains(err.Error(), "stopped") {
		t.Fatalf("post-shutdown persistence result = %v, want stopped error", err)
	}
}

func waitNodeConfigPersistence(t *testing.T, result <-chan error) error {
	t.Helper()
	select {
	case err := <-result:
		return err
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for node configuration persistence")
		return nil
	}
}
