package almagest

import (
	"context"
	"testing"
	"time"

	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/pasta/pasta"
)

func TestEngineUnsubscribeNotificationsRemovesSubscriptionMapping(t *testing.T) {
	engineAPI, err := BuildEngine(
		context.Background(),
		configer.NewMemory(nil),
		NewDebugLogFactory(1),
		newE2EDebugSystem(),
		nil,
		nil,
		true,
	)
	if err != nil {
		t.Fatalf("BuildEngine error = %v, want nil", err)
	}
	t.Cleanup(func() {
		if err := engineAPI.Close(); err != nil {
			t.Errorf("engine.Close error = %v, want nil", err)
		}
	})

	engineImpl := engineAPI.(*engine)
	for range 10 {
		id := engineAPI.SubscribeNotifications(func(EngineNotification) {})
		if id == 0 {
			t.Fatal("SubscribeNotifications returned 0")
		}
		if got := len(engineImpl.subscriptionIDMapping); got != 1 {
			t.Fatalf("subscription mapping count after subscribe = %d, want 1", got)
		}

		engineAPI.UnsubscribeNotifications(id)
		if got := len(engineImpl.subscriptionIDMapping); got != 0 {
			t.Fatalf("subscription mapping count after unsubscribe = %d, want 0", got)
		}
	}
}

func TestEngineSubscribeNotificationsCallbackCanReenterEngine(t *testing.T) {
	engineAPI, err := BuildEngine(
		context.Background(),
		configer.NewMemory(nil),
		NewDebugLogFactory(1),
		newE2EDebugSystem(),
		nil,
		nil,
		true,
	)
	if err != nil {
		t.Fatalf("BuildEngine error = %v, want nil", err)
	}
	t.Cleanup(func() {
		if err := engineAPI.Close(); err != nil {
			t.Errorf("engine.Close error = %v, want nil", err)
		}
	})

	done := make(chan struct{})
	go func() {
		id := engineAPI.SubscribeNotifications(func(notification EngineNotification) {
			if notification.Kind == NotificationColorMap {
				engineAPI.RequestFullSnapshot(notification.SubscriptionID)
			}
		})
		if id != 0 {
			engineAPI.UnsubscribeNotifications(id)
		}
		close(done)
	}()

	assertNoDeadlock(t, done, "SubscribeNotifications callback re-entered engine")
}

func TestEngineLockedWorkspaceWrappersFromNodeCallback(t *testing.T) {
	engineAPI, err := BuildEngine(
		context.Background(),
		configer.NewMemory(nil),
		NewDebugLogFactory(1),
		newE2EDebugSystem(),
		nil,
		nil,
		true,
	)
	if err != nil {
		t.Fatalf("BuildEngine error = %v, want nil", err)
	}
	t.Cleanup(func() {
		if err := engineAPI.Close(); err != nil {
			t.Errorf("engine.Close error = %v, want nil", err)
		}
	})
	engineImpl := engineAPI.(*engine)
	if err := engineImpl.mainWorkspace.AddNodeClass(engineLockedCallbackClass{engine: engineImpl}); err != nil {
		t.Fatalf("AddNodeClass error = %v, want nil", err)
	}

	done := make(chan error, 1)
	go func() {
		_, err := engineAPI.AddNode(E2EWorkspace, "test/EngineLockedCallback", "locked-callback")
		done <- err
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("AddNode error = %v, want nil", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("AddNode deadlocked while node callback used engine Locked wrappers")
	}
}

type engineLockedCallbackClass struct {
	engine *engine
}

func (c engineLockedCallbackClass) ClassName() string {
	return "test/EngineLockedCallback"
}

func (c engineLockedCallbackClass) ShortDescription() string {
	return "Engine locked callback test"
}

func (c engineLockedCallbackClass) LongDescription() string {
	return "Tests that engine Locked workspace wrappers can be called from node callbacks."
}

func (c engineLockedCallbackClass) DefaultNodeParams() pasta.NodeClassParams {
	return pasta.NodeClassParams{}
}

func (c engineLockedCallbackClass) NewNode(configer.Config, ...*pasta.NodeClassState) (pasta.Node, error) {
	return &engineLockedCallbackNode{engine: c.engine}, nil
}

type engineLockedCallbackNode struct {
	pasta.BasicNode

	engine *engine
}

func (n *engineLockedCallbackNode) OnInit(w *pasta.Workspace, _ pasta.Logger, id uint64, _ string, _ *pasta.NodeInitData, _, _, _, _ bool) error {
	if _, ok := n.engine.NodeSnapshotLocked(E2EWorkspace, id); !ok {
		return pasta.ErrNoNode
	}
	n.engine.SetNodePositionLocked(E2EWorkspace, id, "callback-position")
	return nil
}

func assertNoDeadlock(t *testing.T, done <-chan struct{}, context string) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("%s deadlocked", context)
	}
}
