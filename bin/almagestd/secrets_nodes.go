package main

import (
	"maps"
	"sync"

	almagest "github.com/asciimoth/almagest/lib"
	"github.com/asciimoth/almagest/lib/logging"
	"github.com/asciimoth/pasta/pasta"
)

type secretNodeNameSync struct {
	secrets   secretNodeReferenceStore
	kv        kvOwnerReferenceStore
	nodeNames *nodeNameIndex
	engine    almagest.Engine
	logger    logging.Logger

	mu    sync.Mutex
	nodes map[uint64]string
}

type secretNodeReferenceStore interface {
	renameSecretNodeReferences(oldName, newName string) error
	deleteSecretNodeReferences(name string) error
	reconcileSecretNodeReferences(active map[string]struct{}) error
}

type kvOwnerReferenceStore interface {
	renameKVOwnerReferences(oldName, newName string) error
	deleteKVOwnerEntries(name string) error
	reconcileKVOwnerEntries(active map[string]struct{}) error
}

// startSecretNodeNameSync keeps all persisted node-name references keyed by
// current workspace names. Secret allow lists are rewritten on rename and
// cleared on delete. KV ownership is rewritten on rename, and owned entries are
// deleted with their node. It observes daemon engine notifications without
// adding backend-specific lifecycle methods to lib.Engine.
func (a *AdminAPI) startSecretNodeNameSync(engine almagest.Engine, logger logging.Logger) uint64 {
	if a == nil || (a.secrets == nil && a.kv == nil && a.nodeNames == nil) || engine == nil {
		return 0
	}
	syncer := &secretNodeNameSync{
		nodeNames: a.nodeNames,
		engine:    engine,
		logger:    logger,
		nodes:     map[uint64]string{},
	}
	if a.secrets != nil {
		syncer.secrets = a.secrets
	}
	if a.kv != nil {
		syncer.kv = a.kv
	}
	return engine.SubscribeNotifications(syncer.handle)
}

func (s *secretNodeNameSync) handle(notification almagest.EngineNotification) {
	if notification.Kind != almagest.NotificationWorkspace || notification.Workspace == nil {
		return
	}
	wn := notification.Workspace
	switch wn.Kind {
	case pasta.NotificationWorkspaceSnapshot:
		if wn.Snapshot != nil {
			s.reconcileSnapshot(wn.Snapshot.Nodes)
		}
	case pasta.NotificationNodeAdded:
		if wn.Node != nil {
			s.reconcileNode(wn.ID, wn.Node.Name)
		}
	case pasta.NotificationNodeUpdated:
		if wn.Node != nil {
			s.reconcileNode(wn.ID, wn.Node.Name)
		}
	case pasta.NotificationNodeRemoved:
		name := ""
		if wn.Node != nil {
			name = wn.Node.Name
		}
		s.reconcileNode(wn.ID, name)
	case pasta.NotificationNodesUpdated:
		if wn.Snapshot != nil {
			s.reconcileSnapshot(wn.Snapshot.Nodes)
			return
		}
		for id, node := range wn.Nodes {
			s.reconcileNode(id, node.Name)
		}
	case pasta.NotificationNodesRemoved:
		if wn.Snapshot != nil {
			s.reconcileSnapshot(wn.Snapshot.Nodes)
			return
		}
		for id, node := range wn.Nodes {
			s.reconcileNode(id, node.Name)
		}
	}
}

func (s *secretNodeNameSync) reconcileSnapshot(nodes map[uint64]pasta.NodeSnapshot) {
	// Another callback can delay delivery until the graph changes again. Use the
	// live graph so an old snapshot cannot remove state from a replacement node.
	if current, ok := currentWorkspaceNodes(s.engine); ok {
		nodes = current
	}
	next := make(map[uint64]string, len(nodes))
	for id, node := range nodes {
		next[id] = node.Name
	}

	s.mu.Lock()
	old := s.nodes
	// Keep next private after the lock is released. Notification callbacks can
	// concurrently update s.nodes, while the reconciliation below still reads
	// next.
	s.nodes = maps.Clone(next)
	s.mu.Unlock()

	for id, oldName := range old {
		newName, ok := next[id]
		if !ok {
			s.nodeNames.remember(oldName)
			continue
		}
		if oldName != newName {
			s.nodeNames.remember(oldName)
			s.renameReferences(oldName, newName)
		}
	}
	s.reconcileReferences(next)
}

func (s *secretNodeNameSync) reconcileNode(id uint64, fallbackName string) {
	// Resolve the ID again because add, update, and remove notifications can be
	// older than the graph by the time this callback runs.
	if name, ok := currentWorkspaceNodeName(s.engine, id); ok {
		s.renameNode(id, name)
		return
	}
	s.deleteNode(id, fallbackName)
}

func (s *secretNodeNameSync) renameNode(id uint64, newName string) {
	s.mu.Lock()
	oldName := s.nodes[id]
	s.nodes[id] = newName
	s.mu.Unlock()
	if oldName != "" && oldName != newName {
		s.nodeNames.remember(oldName)
		s.renameReferences(oldName, newName)
	}
}

func (s *secretNodeNameSync) deleteNode(id uint64, fallbackName string) {
	s.mu.Lock()
	name := s.nodes[id]
	delete(s.nodes, id)
	s.mu.Unlock()
	if name == "" {
		name = fallbackName
	}
	if name != "" {
		s.nodeNames.remember(name)
		s.deleteReferencesIfInactive(name)
	}
}

func (s *secretNodeNameSync) deleteReferencesIfInactive(name string) {
	// Node IDs change during snapshot restore. A node with this name can already
	// be live again when the removal for the old ID arrives.
	if workspaceNodeNameActive(s.engine, name) {
		return
	}
	s.deleteReferences(name)
}

func (s *secretNodeNameSync) renameReferences(oldName, newName string) {
	if s.secrets != nil {
		if err := s.secrets.renameSecretNodeReferences(oldName, newName); err != nil && s.logger != nil {
			s.logger.Errf("failed to rename secret node references %q -> %q: %v", oldName, newName, err)
		}
	}
	if s.kv != nil {
		if err := s.kv.renameKVOwnerReferences(oldName, newName); err != nil && s.logger != nil {
			s.logger.Errf("failed to rename KV owner %q -> %q: %v", oldName, newName, err)
		}
	}
}

func (s *secretNodeNameSync) deleteReferences(name string) {
	if s.secrets != nil {
		if err := s.secrets.deleteSecretNodeReferences(name); err != nil && s.logger != nil {
			s.logger.Errf("failed to delete secret node references for %q: %v", name, err)
		}
	}
	if s.kv != nil {
		if err := s.kv.deleteKVOwnerEntries(name); err != nil && s.logger != nil {
			s.logger.Errf("failed to delete KV entries owned by %q: %v", name, err)
		}
	}
}

func (s *secretNodeNameSync) reconcileReferences(nodes map[uint64]string) {
	if s.secrets == nil && s.kv == nil {
		return
	}
	active := make(map[string]struct{}, len(nodes))
	for _, name := range nodes {
		active[name] = struct{}{}
	}
	if s.secrets != nil {
		if err := s.secrets.reconcileSecretNodeReferences(active); err != nil && s.logger != nil {
			s.logger.Errf("failed to reconcile secret node references: %v", err)
		}
	}
	if s.kv != nil {
		if err := s.kv.reconcileKVOwnerEntries(active); err != nil && s.logger != nil {
			s.logger.Errf("failed to reconcile KV owners: %v", err)
		}
	}
}

func currentWorkspaceNodes(engine almagest.Engine) (map[uint64]pasta.NodeSnapshot, bool) {
	if engine == nil {
		return nil, false
	}
	snapshot, ok := engine.WorkspaceSnapshot(almagest.MainWorkspace)
	return snapshot.Nodes, ok
}

func workspaceNodeNameActive(engine almagest.Engine, name string) bool {
	if engine == nil || name == "" {
		return false
	}
	_, ok := engine.NodeByName(almagest.MainWorkspace, name)
	return ok
}

func currentWorkspaceNodeName(engine almagest.Engine, id uint64) (string, bool) {
	if engine == nil {
		return "", false
	}
	snapshot, ok := engine.NodeSnapshot(almagest.MainWorkspace, id)
	return snapshot.Name, ok
}
