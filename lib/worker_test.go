package almagest

import (
	"context"
	"strings"
	"testing"
	"time"

	debugnodes "github.com/asciimoth/almagest/lib/debug"
	httpnodes "github.com/asciimoth/almagest/lib/http"
	"github.com/asciimoth/almagest/lib/logging"
	"github.com/asciimoth/almagest/lib/testkit"
	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/formular"
	sysnetdebug "github.com/asciimoth/gonnect/sysnet/debug"
	"github.com/asciimoth/pasta/pasta"
)

func TestEngineWorkerEventLoggingIsOptIn(t *testing.T) {
	for _, test := range []struct {
		name    string
		options []BuildEngineOption
	}{
		{name: "disabled"},
		{name: "enabled", options: []BuildEngineOption{WithWorkerEventLogging(true)}},
	} {
		t.Run(test.name, func(t *testing.T) {
			logf := logging.NewDebugLogFactory(32)
			engineAPI, err := BuildEngine(
				context.Background(),
				configer.NewMemory(nil),
				logf,
				&sysnetdebug.System{},
				nil,
				nil,
				nil,
				nil,
				nil,
				nil,
				true,
				test.options...,
			)
			if err != nil {
				t.Fatalf("BuildEngine error = %v, want nil", err)
			}
			t.Cleanup(func() {
				if err := engineAPI.Close(); err != nil {
					t.Errorf("engine.Close error = %v, want nil", err)
				}
			})

			node, err := engineAPI.AddNode(MainWorkspace, debugnodes.NodeTypeDebugErrorsWorkers, "workers")
			if err != nil {
				t.Fatalf("AddNode error = %v, want nil", err)
			}
			tracker := engineAPI.(*engine).workers
			done := make(chan struct{})
			if _, err := tracker.spawner(node).Spawn(func() { close(done) }, "normal test worker"); err != nil {
				t.Fatalf("Spawn normal worker error = %v, want nil", err)
			}
			<-done

			failedNode, err := engineAPI.AddNode(MainWorkspace, debugnodes.NodeTypeDebugErrorsWorkers, "failed workers")
			if err != nil {
				t.Fatalf("AddNode for failed worker error = %v, want nil", err)
			}
			if _, err := tracker.spawner(failedNode).Spawn(func() { panic("test") }, "failed test worker"); err != nil {
				t.Fatalf("Spawn failed worker error = %v, want nil", err)
			}
			waitWorkerTrackerEmpty(t, tracker)

			messages := make([]string, 0, 3)
			for _, entry := range logf.Drain() {
				if strings.HasPrefix(entry.Message, "engine worker ") {
					messages = append(messages, entry.Message)
				}
			}
			joined := strings.Join(messages, "\n")
			for _, event := range []string{"engine worker spawned", "engine worker stopped", "engine worker failed"} {
				want := len(test.options) > 0
				if got := strings.Contains(joined, event); got != want {
					t.Errorf("log contains %q = %t, want %t; logs:\n%s", event, got, want, joined)
				}
			}
		})
	}
}

func waitWorkerTrackerEmpty(t *testing.T, tracker *workerTracker) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(tracker.snapshot()) == 0 {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("workers did not stop: %#v", tracker.snapshot())
}

func TestEngineWorkersSnapshotIncludesRestoredStartupWorkers(t *testing.T) {
	engine, err := BuildEngine(
		context.Background(),
		configer.NewMemory(map[string]any{
			"nodes": map[string]any{
				"client": map[string]any{
					"Class": httpnodes.NodeTypeHTTPClient,
				},
			},
		}),
		logging.NewDebugLogFactory(1024),
		&sysnetdebug.System{},
		nil,
		nil, nil,
		nil,

		nil,
		nil,
		true)

	if err != nil {
		t.Fatalf("BuildEngine error = %v, want nil", err)
	}
	t.Cleanup(func() {
		if err := engine.Close(); err != nil {
			t.Errorf("engine.Close error = %v, want nil", err)
		}
	})

	node, ok := engine.NodeByName(MainWorkspace, "client")
	if !ok {
		t.Fatal("restored client node missing")
	}
	workers, ok := engine.WorkersSnapshot(MainWorkspace)
	if !ok {
		t.Fatal("WorkersSnapshot ok = false, want true")
	}
	for _, worker := range workers {
		if worker.Node == node && worker.WorkerName == "http client" {
			return
		}
	}
	t.Fatalf("workers = %#v, want restored http client startup worker for node %d", workers, node)
}

func TestEngineWorkerNotificationsTrackPanicLifecycle(t *testing.T) {
	engine, err := BuildEngine(
		context.Background(),
		configer.NewMemory(nil),
		logging.NewDebugLogFactory(1024),
		&sysnetdebug.System{},
		nil,
		nil, nil,
		nil,

		nil,
		nil,
		true)

	if err != nil {
		t.Fatalf("BuildEngine error = %v, want nil", err)
	}
	t.Cleanup(func() {
		if err := engine.Close(); err != nil {
			t.Errorf("engine.Close error = %v, want nil", err)
		}
	})

	events := make(chan WorkerNotification, 8)
	sub := engine.SubscribeWorkerNotifications(func(notification WorkerNotification) {
		events <- notification
	})
	if sub == 0 {
		t.Fatal("SubscribeWorkerNotifications returned 0")
	}
	t.Cleanup(func() { engine.UnsubscribeWorkerNotifications(sub) })

	node, err := engine.AddNode(MainWorkspace, debugnodes.NodeTypeDebugErrorsWorkers, "workers")
	if err != nil {
		t.Fatalf("AddNode error = %v, want nil", err)
	}
	engine.SendNodeFormularMsg(MainWorkspace, node, formular.ButtonPressMessage{
		MessageBase: testkit.FormMessageBase(formular.MessageButtonPress, node),
		BlockID:     "actions",
		ButtonID:    "panic-worker",
	})

	spawned := waitWorkerNotification(t, events, WorkerNotificationSpawned)
	if spawned.Worker == nil || spawned.Worker.Node != node || spawned.Worker.WorkerName != "debug panic worker" {
		t.Fatalf("spawned worker = %#v, want debug panic worker for node %d", spawned.Worker, node)
	}
	failed := waitWorkerNotification(t, events, WorkerNotificationFailed)
	if failed.Worker == nil || failed.Worker.ID != spawned.Worker.ID || failed.Worker.Error != pasta.ErrNodePanic.Error() {
		t.Fatalf("failed worker = %#v, want same worker with node panic", failed.Worker)
	}

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		snapshot, ok := engine.NodeSnapshot(MainWorkspace, node)
		if ok && snapshot.Placeholder && len(snapshot.Popups) > 0 && snapshot.Popups[0].Type == pasta.NodePopupErr {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	snapshot, _ := engine.NodeSnapshot(MainWorkspace, node)
	t.Fatalf("node snapshot after worker panic = %#v, want placeholder with error popup", snapshot)
}

func waitWorkerNotification(t *testing.T, events <-chan WorkerNotification, kind WorkerNotificationKind) WorkerNotification {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case event := <-events:
			if event.Kind == kind {
				return event
			}
		case <-deadline:
			t.Fatalf("timed out waiting for worker notification %q", kind)
		}
	}
}
