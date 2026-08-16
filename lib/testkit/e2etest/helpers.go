package e2etest

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	almagest "github.com/asciimoth/almagest/lib"
	"github.com/asciimoth/almagest/lib/logging"
	"github.com/asciimoth/almagest/lib/storage"
	"github.com/asciimoth/almagest/lib/testkit"
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
	T           *testing.T
	Engine      almagest.Engine
	LogFactory  *logging.DebugLogFactory
	DebugSystem *sysnetdebug.System
	Secrets     *e2eSecretsStorage

	mu    sync.Mutex
	menus *formular.MenuSnapshotState
	logs  []logging.DebugLogEntry
}

// TestingT returns the test handle for helper adapters.
func (e *NodeE2E) TestingT() *testing.T {
	return e.T
}

// EngineAPI returns the engine for helper adapters.
func (e *NodeE2E) EngineAPI() almagest.Engine {
	return e.Engine
}

// SendNodeFormularMsg forwards a node form message to the engine.
func (e *NodeE2E) SendNodeFormularMsg(workspace string, node uint64, msg any) {
	e.Engine.SendNodeFormularMsg(workspace, node, msg)
}

// NodeSnapshot returns a node snapshot from the engine.
func (e *NodeE2E) NodeSnapshot(workspace string, id uint64) (pasta.NodeSnapshot, bool) {
	return e.Engine.NodeSnapshot(workspace, id)
}

func newE2EDebugSystem() *sysnetdebug.System {
	out := gonnect.NewLoopbackNetwork()
	out.AllowAnyHost = true
	local := gonnect.NewLoopbackNetwork()
	local.AllowAnyHost = true
	return &sysnetdebug.System{OutNetwork: out, LocalNetwork: local}
}

type e2eDebugPool struct {
	*bufpool.DebugPool
	active atomic.Int64
}

// DebugPool tracks active buffers for e2e tests.
type DebugPool = e2eDebugPool

// NewDebugSystem returns a debug sysnet system for e2e tests.
func NewDebugSystem() *sysnetdebug.System {
	return newE2EDebugSystem()
}

var nextTestPort atomic.Uint32

// TestPort returns a per-process port for local e2e listeners.
func TestPort() int {
	base := uint32(20000 + os.Getpid()%20000)
	return int(base + nextTestPort.Add(1))
}

// NewTestDebugPool returns a tracked debug pool for e2e tests.
func NewTestDebugPool(t *testing.T) *DebugPool {
	return newE2ETestDebugPool(t)
}

func newE2ETestDebugPool(t *testing.T) *e2eDebugPool {
	t.Helper()
	pool := &e2eDebugPool{DebugPool: bufpool.NewTestDebugPool(t)}
	t.Cleanup(func() { pool.close(t) })
	return pool
}

func (p *e2eDebugPool) Get(length int) []byte {
	buf := p.DebugPool.Get(length)
	if cap(buf) > 0 {
		p.active.Add(1)
	}
	return buf
}

func (p *e2eDebugPool) Put(buf []byte) {
	p.DebugPool.Put(buf)
	if cap(buf) > 0 {
		p.active.Add(-1)
	}
}

func (p *e2eDebugPool) close(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var idleSince time.Time
	for time.Now().Before(deadline) {
		if p.active.Load() == 0 {
			if idleSince.IsZero() {
				idleSince = time.Now()
			}
			if time.Since(idleSince) >= 50*time.Millisecond {
				break
			}
		} else {
			idleSince = time.Time{}
		}
		time.Sleep(10 * time.Millisecond)
	}
	p.Close()
}

// NewNodeE2E builds a debug engine and registers test cleanup hooks.
func NewNodeE2E(t *testing.T) *NodeE2E {
	t.Helper()

	pool := newE2ETestDebugPool(t)
	logf := logging.NewDebugLogFactory(16384)
	sys := newE2EDebugSystem()
	secrets := newE2ESecretsStorage()
	engine, err := almagest.BuildEngine(
		context.Background(),
		configer.NewMemory(nil),
		logf,
		sys,
		nil,
		nil, secrets,
		nil,

		nil,
		pool,
		true)

	if err != nil {
		t.Fatalf("almagest.BuildEngine error = %v, want nil", err)
	}
	t.Cleanup(func() {
		if err := engine.Close(); err != nil {
			t.Errorf("engine.Close error = %v, want nil", err)
		}
	})

	e := &NodeE2E{T: t, Engine: engine, LogFactory: logf, DebugSystem: sys, Secrets: secrets, menus: formular.NewMenuSnapshotState()}
	sub := engine.SubscribeNotifications(func(notification almagest.EngineNotification) {
		if notification.Kind != almagest.NotificationWorkspace || notification.Workspace == nil {
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

type e2eSecretRecord struct {
	secret storage.Secret
	nodes  map[string]struct{}
}

type e2eSecretsStorage struct {
	mu      sync.Mutex
	nextKey int
	records map[string]e2eSecretRecord
}

// SecretsStorage is a small in-memory secret store for e2e tests.
type SecretsStorage = e2eSecretsStorage

// NewSecretsStorage returns an empty e2e secret store.
func NewSecretsStorage() *SecretsStorage {
	return newE2ESecretsStorage()
}

func newE2ESecretsStorage() *e2eSecretsStorage {
	return &e2eSecretsStorage{records: map[string]e2eSecretRecord{}}
}

func (s *e2eSecretsStorage) GetSecret(key string, node string) (storage.Secret, error) {
	if s == nil || key == "" || pasta.ValidateNodeName(node) != nil {
		return storage.Secret{}, storage.ErrSecretAccessDenied
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.records[key]
	if !ok {
		return storage.Secret{}, storage.ErrSecretAccessDenied
	}
	if _, ok := record.nodes[node]; !ok {
		return storage.Secret{}, storage.ErrSecretAccessDenied
	}
	return record.secret, nil
}

func (s *e2eSecretsStorage) NewSecret(sec storage.Secret, node string) (string, error) {
	if s == nil {
		return "", storage.ErrSecretClosed
	}
	if pasta.ValidateNodeName(node) != nil {
		return "", storage.ErrSecretInvalidName
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for {
		s.nextKey++
		key := fmt.Sprintf("secret-%d", s.nextKey)
		if _, exists := s.records[key]; exists {
			continue
		}
		s.records[key] = e2eSecretRecord{
			secret: storage.Secret{Value: sec.Value},
			nodes:  map[string]struct{}{node: {}},
		}
		return key, nil
	}
}

func (s *e2eSecretsStorage) SetSecret(key string, sec storage.Secret, nodes ...string) {
	if s == nil {
		return
	}
	allowed := make(map[string]struct{}, len(nodes))
	for _, node := range nodes {
		if pasta.ValidateNodeName(node) == nil {
			allowed[node] = struct{}{}
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.records[key] = e2eSecretRecord{secret: sec, nodes: allowed}
}

func (s *e2eSecretsStorage) SecretValue(key string) (string, bool) {
	if s == nil {
		return "", false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.records[key]
	if !ok {
		return "", false
	}
	return record.secret.Value, true
}

func (s *e2eSecretsStorage) DeleteSecret(key string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.records, key)
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
	sub := e.Engine.SubscribeNotifications(func(notification almagest.EngineNotification) {
		if notification.Kind != almagest.NotificationWorkspace || notification.Workspace == nil {
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

// ApplyStdConstantValue commits the current value form used by pasta std constant nodes.
func (e *NodeE2E) ApplyStdConstantValue(node uint64, value any) {
	e.T.Helper()
	e.Engine.SendNodeFormularMsg(E2EWorkspace, node, formular.FormApplyMessage{
		MessageBase: FormMessageBase(formular.MessageFormApply, node),
		BlockID:     "state",
		Values:      map[string]any{"value": value},
	})
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
	return testkit.FindBlock(snapshot, id)
}

// FindItem returns a block item by ID.
func FindItem(block formular.Block, id string) (formular.Item, bool) {
	return testkit.FindItem(block, id)
}

// Eventually waits until condition returns true or fails the test.
func Eventually(t *testing.T, condition func() bool, format string, args ...any) {
	testkit.Eventually(t, condition, format, args...)
}

// FormMessageBase builds a formular message base for a node menu.
func FormMessageBase(messageType string, node uint64) formular.MessageBase {
	return testkit.FormMessageBase(messageType, node)
}

// NewNodeE2EFromEngine wraps an existing engine with e2e menu/log helpers.
func NewNodeE2EFromEngine(t *testing.T, engine almagest.Engine, logf *logging.DebugLogFactory, sys *sysnetdebug.System, secrets *SecretsStorage) *NodeE2E {
	t.Helper()
	e := &NodeE2E{T: t, Engine: engine, LogFactory: logf, DebugSystem: sys, Secrets: secrets, menus: formular.NewMenuSnapshotState()}
	sub := engine.SubscribeNotifications(func(notification almagest.EngineNotification) {
		if notification.Kind != almagest.NotificationWorkspace || notification.Workspace == nil {
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

// NewNodeE2EWithConfig builds an e2e engine from cfg.
func NewNodeE2EWithConfig(t *testing.T, cfg configer.Config) *NodeE2E {
	return NewNodeE2EWithConfigAndSecrets(t, cfg, newE2ESecretsStorage())
}

// NewNodeE2EWithConfigAndSecrets builds an e2e engine from cfg and secrets.
func NewNodeE2EWithConfigAndSecrets(t *testing.T, cfg configer.Config, secrets *e2eSecretsStorage) *NodeE2E {
	t.Helper()
	if secrets == nil {
		secrets = newE2ESecretsStorage()
	}
	pool := newE2ETestDebugPool(t)
	logf := logging.NewDebugLogFactory(16384)
	sys := newE2EDebugSystem()
	engine, err := almagest.BuildEngine(context.Background(), cfg, logf, sys, nil, nil, secrets, nil, nil, pool, true)
	if err != nil {
		t.Fatalf("almagest.BuildEngine restore: %v", err)
	}
	t.Cleanup(func() {
		if err := engine.Close(); err != nil {
			t.Errorf("engine.Close error = %v, want nil", err)
		}
	})
	e := &NodeE2E{T: t, Engine: engine, LogFactory: logf, DebugSystem: sys, Secrets: secrets, menus: formular.NewMenuSnapshotState()}
	sub := engine.SubscribeNotifications(func(notification almagest.EngineNotification) {
		if notification.Kind != almagest.NotificationWorkspace || notification.Workspace == nil {
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
