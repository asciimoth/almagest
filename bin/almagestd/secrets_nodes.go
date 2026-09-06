package main

import (
	"sync"

	almagest "github.com/asciimoth/almagest/lib"
	"github.com/asciimoth/almagest/lib/logging"
	"github.com/asciimoth/pasta/pasta"
)

type secretNodeNameSync struct {
	secrets   *bboltKVManager
	kv        *bboltKVManager
	nodeNames *nodeNameIndex
	logger    logging.Logger

	mu    sync.Mutex
	nodes map[uint64]string
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
		secrets:   a.secrets,
		kv:        a.kv,
		nodeNames: a.nodeNames,
		logger:    logger,
		nodes:     map[uint64]string{},
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
			s.setNodeName(wn.ID, wn.Node.Name)
		}
	case pasta.NotificationNodeUpdated:
		if wn.Node != nil {
			s.renameNode(wn.ID, wn.Node.Name)
		}
	case pasta.NotificationNodeRemoved:
		name := ""
		if wn.Node != nil {
			name = wn.Node.Name
		}
		s.deleteNode(wn.ID, name)
	case pasta.NotificationNodesUpdated:
		if wn.Snapshot != nil {
			s.reconcileSnapshot(wn.Snapshot.Nodes)
			return
		}
		for id, node := range wn.Nodes {
			s.renameNode(id, node.Name)
		}
	case pasta.NotificationNodesRemoved:
		if wn.Snapshot != nil {
			s.reconcileSnapshot(wn.Snapshot.Nodes)
			return
		}
		for id, node := range wn.Nodes {
			s.deleteNode(id, node.Name)
		}
	}
}

func (s *secretNodeNameSync) reconcileSnapshot(nodes map[uint64]pasta.NodeSnapshot) {
	next := make(map[uint64]string, len(nodes))
	for id, node := range nodes {
		next[id] = node.Name
	}

	s.mu.Lock()
	old := s.nodes
	s.nodes = next
	s.mu.Unlock()

	for id, oldName := range old {
		newName, ok := next[id]
		if !ok {
			s.nodeNames.remember(oldName)
			s.deleteReferences(oldName)
			continue
		}
		if oldName != newName {
			s.nodeNames.remember(oldName)
			s.renameReferences(oldName, newName)
		}
	}
	s.reconcileReferences(next)
}

func (s *secretNodeNameSync) setNodeName(id uint64, name string) {
	s.mu.Lock()
	s.nodes[id] = name
	s.mu.Unlock()
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
		s.deleteReferences(name)
	}
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
