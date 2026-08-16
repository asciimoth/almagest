package testkit

import (
	"testing"
	"time"

	"github.com/asciimoth/formular"
	"github.com/asciimoth/pasta/pasta"
)

// MustNodeSnapshot returns a node snapshot or fails the test.
func MustNodeSnapshot(t *testing.T, w *pasta.Workspace, node uint64) pasta.NodeSnapshot {
	t.Helper()
	snapshot, ok := w.NodeSnapshot(node)
	if !ok {
		t.Fatalf("NodeSnapshot(%d) missing", node)
	}
	return snapshot
}

// EngineSnapshots is the engine snapshot API used by test helpers.
type EngineSnapshots interface {
	NodeSnapshot(workspace string, id uint64) (pasta.NodeSnapshot, bool)
	WorkspaceSnapshot(workspace string) (pasta.WorkspaceSnapshot, bool)
}

// PastedNodeByClass returns a pasted node with the given class or fails the test.
func PastedNodeByClass(t *testing.T, engine EngineSnapshots, workspace string, ids []uint64, class string) uint64 {
	t.Helper()
	for _, id := range ids {
		snapshot, ok := engine.NodeSnapshot(workspace, id)
		if ok && snapshot.Class == class {
			return id
		}
	}
	t.Fatalf("pasted node with class %s missing in %v", class, ids)
	return 0
}

// NodeIDByName returns a node ID by name or fails the test.
func NodeIDByName(t *testing.T, engine EngineSnapshots, workspace string, name string) uint64 {
	t.Helper()
	snapshot, ok := engine.WorkspaceSnapshot(workspace)
	if !ok {
		t.Fatal("WorkspaceSnapshot returned ok=false")
	}
	for id, node := range snapshot.Nodes {
		if node.Name == name {
			return id
		}
	}
	t.Fatalf("node named %q missing", name)
	return 0
}

// AssertStringSlice fails unless got and want contain the same strings in order.
func AssertStringSlice(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("values = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("values = %#v, want %#v", got, want)
		}
	}
}

// MustFindBlock returns a menu block or fails the test.
func MustFindBlock(t *testing.T, snapshot formular.MenuSnapshotMessage, id string) formular.Block {
	t.Helper()
	block, ok := FindBlock(snapshot, id)
	if ok {
		return block
	}
	t.Fatalf("block %q missing", id)
	return formular.Block{}
}

// FindBlock returns a menu block by ID.
func FindBlock(snapshot formular.MenuSnapshotMessage, id string) (formular.Block, bool) {
	for _, block := range snapshot.Blocks {
		if block.ID == id {
			return block, true
		}
	}
	return formular.Block{}, false
}

// FindItem returns a block item by ID.
func FindItem(block formular.Block, id string) (formular.Item, bool) {
	for _, item := range block.Items {
		if item.ID == id {
			return item, true
		}
	}
	return formular.Item{}, false
}

// FormMessageBase builds a formular message base for a node menu.
func FormMessageBase(messageType string, node uint64) formular.MessageBase {
	return formular.MessageBase{
		Type:           messageType,
		MenuID:         pasta.NodeMenuID(node),
		MenuGeneration: 1,
	}
}

// SubscribeMenuState subscribes to node menu snapshots and returns their state.
func SubscribeMenuState(t *testing.T, w *pasta.Workspace) (*formular.MenuSnapshotState, uint64) {
	t.Helper()
	state := formular.NewMenuSnapshotState()
	sub := w.SubscribeNotifications(func(notification pasta.WorkspaceNotification) {
		if notification.Kind == pasta.NotificationNodeMenu {
			state.Apply(notification.Formular)
		}
	})
	if sub == 0 {
		t.Fatal("SubscribeNotifications returned 0")
	}
	return state, sub
}

// AssertLeadingBlock fails unless the first menu block has the expected ID.
func AssertLeadingBlock(t *testing.T, state *formular.MenuSnapshotState, node uint64, blockID string) {
	t.Helper()
	snapshot, ok := state.Snapshot(pasta.NodeMenuID(node))
	if !ok {
		t.Fatalf("menu snapshot missing for node %d", node)
	}
	if len(snapshot.Blocks) == 0 || snapshot.Blocks[0].ID != blockID {
		t.Fatalf("leading block = %#v, want %s", snapshot.Blocks, blockID)
	}
}

// AssertReadonlyMenuFieldValue fails unless a readonly field has the expected value.
func AssertReadonlyMenuFieldValue(t *testing.T, state *formular.MenuSnapshotState, node uint64, blockID, fieldID string, want any) {
	t.Helper()
	item := ReadonlyMenuField(t, state, node, blockID, fieldID)
	if item.Value != want {
		t.Fatalf("%s.%s value = %#v, want %#v", blockID, fieldID, item.Value, want)
	}
}

// ReadonlyMenuFieldString returns a readonly string field value or fails the test.
func ReadonlyMenuFieldString(t *testing.T, state *formular.MenuSnapshotState, node uint64, blockID, fieldID string) string {
	t.Helper()
	item := ReadonlyMenuField(t, state, node, blockID, fieldID)
	value, ok := item.Value.(string)
	if !ok {
		t.Fatalf("%s.%s value = %T, want string", blockID, fieldID, item.Value)
	}
	return value
}

// ReadonlyMenuField returns a readonly menu field or fails the test.
func ReadonlyMenuField(t *testing.T, state *formular.MenuSnapshotState, node uint64, blockID, fieldID string) formular.Item {
	t.Helper()
	snapshot, ok := state.Snapshot(pasta.NodeMenuID(node))
	if !ok {
		t.Fatalf("menu snapshot missing for node %d", node)
	}
	item, ok := FindItem(MustFindBlock(t, snapshot, blockID), fieldID)
	if !ok {
		t.Fatalf("%s.%s item missing", blockID, fieldID)
	}
	if item.Field == nil {
		t.Fatalf("%s.%s field missing", blockID, fieldID)
	}
	if !item.Readonly {
		t.Fatalf("%s.%s readonly = false, want true", blockID, fieldID)
	}
	return item
}

// AssertMenuItemStatus fails unless a config menu item has the expected status.
func AssertMenuItemStatus(t *testing.T, state *formular.MenuSnapshotState, node uint64, fieldID string, status string) {
	t.Helper()
	snapshot, ok := state.Snapshot(pasta.NodeMenuID(node))
	if !ok {
		t.Fatalf("menu snapshot missing for node %d", node)
	}
	item, ok := FindItem(MustFindBlock(t, snapshot, "config"), fieldID)
	if !ok {
		t.Fatalf("%s item missing", fieldID)
	}
	if item.Status != status {
		t.Fatalf("%s status = %q, want %q", fieldID, item.Status, status)
	}
}

// Eventually waits until condition returns true or fails the test.
func Eventually(t *testing.T, condition func() bool, format string, args ...any) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if condition() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for "+format, args...)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
