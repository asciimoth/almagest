package almagest

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/asciimoth/bufpool"
	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/formular"
	"github.com/asciimoth/gonnect"
	"github.com/asciimoth/gonnect/sysnet/debug"
	"github.com/asciimoth/pasta/pasta"
)

// E2EWorkspace is the default workspace used by the engine e2e helper.
const E2EWorkspace = "main"

// NodeE2E owns a debug engine and common helpers for engine e2e tests.
type NodeE2E struct {
	T          *testing.T
	Engine     Engine
	LogFactory *DebugLogFactory

	mu    sync.Mutex
	menus *formular.MenuSnapshotState
	logs  []DebugLogEntry
}

func newE2EDebugSystem() *sysnetdebug.System {
	out := gonnect.NewLoopbackNetwok()
	out.AllowAnyHost = true
	local := gonnect.NewLoopbackNetwok()
	local.AllowAnyHost = true
	return &sysnetdebug.System{OutNetwork: out, LocalNetwork: local}
}

// NewNodeE2E builds a debug engine and registers test cleanup hooks.
func NewNodeE2E(t *testing.T) *NodeE2E {
	t.Helper()

	pool := bufpool.NewTestDebugPool(t)
	t.Cleanup(func() { pool.Close() })
	logf := NewDebugLogFactory(4096)
	engine, err := BuildEngine(
		context.Background(),
		configer.NewMemory(nil),
		logf,
		newE2EDebugSystem(),
		nil,
		pool,
		true,
	)
	if err != nil {
		t.Fatalf("BuildEngine error = %v, want nil", err)
	}
	t.Cleanup(func() {
		if err := engine.Close(); err != nil {
			t.Errorf("engine.Close error = %v, want nil", err)
		}
	})

	e := &NodeE2E{T: t, Engine: engine, LogFactory: logf, menus: formular.NewMenuSnapshotState()}
	sub := engine.SubscribeNotifications(func(notification EngineNotification) {
		if notification.Kind != NotificationWorkspace || notification.Workspace == nil {
			return
		}
		if notification.Workspace.Kind == pasta.NotificationNodeMenu {
			e.mu.Lock()
			defer e.mu.Unlock()
			e.menus.Apply(notification.Workspace.Formular)
		}
	})
	if sub == 0 {
		t.Fatal("SubscribeNotifications returned 0")
	}
	t.Cleanup(func() { engine.UnsubscribeNotifications(sub) })
	return e
}

// AddNode adds a node to the e2e workspace and fails the test on error.
func (e *NodeE2E) AddNode(class, name string) uint64 {
	e.T.Helper()
	id, err := e.Engine.AddNode(E2EWorkspace, class, name)
	if err != nil {
		e.T.Fatalf("AddNode %s: %v", class, err)
	}
	return id
}

// SubscribeMenus subscribes to menu snapshots for the given nodes.
func (e *NodeE2E) SubscribeMenus(nodes ...uint64) {
	e.T.Helper()
	sub := e.Engine.SubscribeNotifications(func(notification EngineNotification) {
		if notification.Kind != NotificationWorkspace || notification.Workspace == nil {
			return
		}
		if notification.Workspace.Kind == pasta.NotificationNodeMenu {
			e.mu.Lock()
			defer e.mu.Unlock()
			e.menus.Apply(notification.Workspace.Formular)
		}
	})
	if sub == 0 {
		e.T.Fatal("SubscribeNotifications for menu watcher returned 0")
	}
	e.T.Cleanup(func() { e.Engine.UnsubscribeNotifications(sub) })
	for _, node := range nodes {
		e.Engine.SubscribeNodeMenu(E2EWorkspace, node, sub)
	}
}

// AddLinkByPortName connects ports found by node, direction, and name.
func (e *NodeE2E) AddLinkByPortName(leftNode uint64, leftDir, leftName string, rightNode uint64, rightDir, rightName string) (uint64, string) {
	e.T.Helper()
	return e.AddLinkByPorts(e.NodePort(leftNode, leftDir, leftName), e.NodePort(rightNode, rightDir, rightName))
}

// MustAddTypedLinkByPortName connects named ports and fails unless the link type matches.
func (e *NodeE2E) MustAddTypedLinkByPortName(wantType string, leftNode uint64, leftDir, leftName string, rightNode uint64, rightDir, rightName string) uint64 {
	e.T.Helper()
	link, linkType := e.AddLinkByPortName(leftNode, leftDir, leftName, rightNode, rightDir, rightName)
	if linkType != wantType {
		e.T.Fatalf("AddLink type = %q, want %q", linkType, wantType)
	}
	return link
}

// AddLinkByPorts connects two ports in the e2e workspace and fails the test on error.
func (e *NodeE2E) AddLinkByPorts(a, b uint64) (uint64, string) {
	e.T.Helper()
	link, linkType, err := e.Engine.AddLink(E2EWorkspace, a, b)
	if err != nil {
		e.T.Fatalf("AddLink %d <-> %d: %v", a, b, err)
	}
	return link, linkType
}

// MustAddTypedLinkByPorts connects two ports and fails unless the link type matches.
func (e *NodeE2E) MustAddTypedLinkByPorts(wantType string, a, b uint64) uint64 {
	e.T.Helper()
	link, linkType := e.AddLinkByPorts(a, b)
	if linkType != wantType {
		e.T.Fatalf("AddLink type = %q, want %q", linkType, wantType)
	}
	return link
}

// LeftPort returns a left port ID by node and name.
func (e *NodeE2E) LeftPort(node uint64, name string) uint64 {
	e.T.Helper()
	return e.NodePort(node, "left", name)
}

// RightPort returns a right port ID by node and name.
func (e *NodeE2E) RightPort(node uint64, name string) uint64 {
	e.T.Helper()
	return e.NodePort(node, "right", name)
}

// NodePort returns a port ID by node, direction, and name.
func (e *NodeE2E) NodePort(node uint64, direction, name string) uint64 {
	e.T.Helper()
	nodeSnapshot, ok := e.Engine.NodeSnapshot(E2EWorkspace, node)
	if !ok {
		e.T.Fatalf("NodeSnapshot(%d) missing", node)
	}
	ports := nodeSnapshot.LeftPorts
	if direction == "right" {
		ports = nodeSnapshot.RightPorts
	}
	for _, port := range ports {
		portSnapshot, ok := e.Engine.PortSnapshot(E2EWorkspace, port)
		if ok && portSnapshot.Direction == direction && portSnapshot.Name == name {
			return port
		}
	}
	e.T.Fatalf("node %d has no %s port named %q", node, direction, name)
	return 0
}

// Menu returns the latest menu snapshot observed for a node.
func (e *NodeE2E) Menu(node uint64) formular.MenuSnapshotMessage {
	e.mu.Lock()
	defer e.mu.Unlock()
	snapshot, ok := e.menus.Snapshot(pasta.NodeMenuID(node))
	if !ok {
		return formular.MenuSnapshotMessage{}
	}
	return snapshot
}

// MenuBlock returns a block from the latest observed menu snapshot.
func (e *NodeE2E) MenuBlock(node uint64, id string) (formular.Block, bool) {
	return FindBlock(e.Menu(node), id)
}

// MenuItem returns an item from the latest observed menu snapshot.
func (e *NodeE2E) MenuItem(node uint64, blockID, itemID string) (formular.Item, bool) {
	block, ok := e.MenuBlock(node, blockID)
	if !ok {
		return formular.Item{}, false
	}
	return FindItem(block, itemID)
}

// AssertNodeLabel fails the test unless the node has the expected label.
func (e *NodeE2E) AssertNodeLabel(node uint64, want string) {
	e.T.Helper()
	snapshot, ok := e.Engine.NodeSnapshot(E2EWorkspace, node)
	if !ok {
		e.T.Fatalf("NodeSnapshot(%d) missing", node)
	}
	if snapshot.Label != want {
		e.T.Fatalf("node %d label = %q, want %q", node, snapshot.Label, want)
	}
}

// WaitNodeLabel waits until the node has the expected label.
func (e *NodeE2E) WaitNodeLabel(node uint64, want string) {
	e.T.Helper()
	Eventually(e.T, func() bool {
		snapshot, ok := e.Engine.NodeSnapshot(E2EWorkspace, node)
		return ok && snapshot.Label == want
	}, "node %d label %q", node, want)
}

// AssertDebugLog waits until a matching debug log entry is observed.
func (e *NodeE2E) AssertDebugLog(nodeClass, wantPart string) {
	e.T.Helper()
	Eventually(e.T, func() bool {
		e.mu.Lock()
		defer e.mu.Unlock()
		e.logs = append(e.logs, e.LogFactory.Drain()...)
		for _, entry := range e.logs {
			if entry.Source.Name == "node" && entry.Source.NodeClass == nodeClass && strings.Contains(entry.Message, wantPart) {
				return true
			}
		}
		return false
	}, "debug log %s containing %q", nodeClass, wantPart)
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

// FormMessageBase builds a formular message base for a node menu.
func FormMessageBase(messageType string, node uint64) formular.MessageBase {
	return formular.MessageBase{
		Type:           messageType,
		MenuID:         pasta.NodeMenuID(node),
		MenuGeneration: 1,
	}
}
