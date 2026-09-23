package main

import (
	"maps"
	"sync"

	almagest "github.com/asciimoth/almagest/lib"
	"github.com/asciimoth/almagest/lib/logging"
	"github.com/asciimoth/pasta/pasta"
)

type tlsNodeNameSync struct {
	manager *bboltKVManager
	engine  almagest.Engine
	logger  logging.Logger

	mu    sync.Mutex
	nodes map[uint64]string
}

// startTLSNodeNameSync keeps TLS policy watcher names in step with workspace
// node renames and removals. Watchers are runtime-only and are not persisted.
func (a *AdminAPI) startTLSNodeNameSync(engine almagest.Engine, logger logging.Logger) uint64 {
	if a == nil || a.tls == nil || engine == nil {
		return 0
	}
	syncer := &tlsNodeNameSync{
		manager: a.tls,
		engine:  engine,
		logger:  logger,
		nodes:   map[uint64]string{},
	}
	return engine.SubscribeNotifications(syncer.handle)
}

func (s *tlsNodeNameSync) handle(notification almagest.EngineNotification) {
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
	case pasta.NotificationWorkspaceStopped:
		s.clearReferences()
	}
}

func (s *tlsNodeNameSync) reconcileSnapshot(nodes map[uint64]pasta.NodeSnapshot) {
	// Another callback can delay delivery until the graph changes again. Use the
	// live graph so an old snapshot cannot remove replacement-node watchers.
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
			s.deleteReferencesIfInactive(oldName)
			continue
		}
		if oldName != newName {
			s.renameReferences(oldName, newName)
		}
	}
}

func (s *tlsNodeNameSync) reconcileNode(id uint64, fallbackName string) {
	// Resolve the ID again because add, update, and remove notifications can be
	// older than the graph by the time this callback runs.
	if name, ok := currentWorkspaceNodeName(s.engine, id); ok {
		s.renameNode(id, name)
		return
	}
	s.deleteNode(id, fallbackName)
}

func (s *tlsNodeNameSync) renameNode(id uint64, newName string) {
	s.mu.Lock()
	oldName := s.nodes[id]
	s.nodes[id] = newName
	s.mu.Unlock()
	if oldName != "" && oldName != newName {
		s.renameReferences(oldName, newName)
	}
}

func (s *tlsNodeNameSync) deleteNode(id uint64, fallbackName string) {
	s.mu.Lock()
	name := s.nodes[id]
	delete(s.nodes, id)
	s.mu.Unlock()
	if name == "" {
		name = fallbackName
	}
	if name != "" {
		s.deleteReferencesIfInactive(name)
	}
}

func (s *tlsNodeNameSync) clearReferences() {
	s.mu.Lock()
	old := s.nodes
	s.nodes = map[uint64]string{}
	s.mu.Unlock()
	for _, name := range old {
		s.deleteReferences(name)
	}
}

func (s *tlsNodeNameSync) deleteReferencesIfInactive(name string) {
	// A rebuilt node can register its watchers before the old removal arrives.
	if workspaceNodeNameActive(s.engine, name) {
		return
	}
	s.deleteReferences(name)
}

func (s *tlsNodeNameSync) renameReferences(oldName, newName string) {
	if err := s.manager.renameTLSPolicyNodeReferences(oldName, newName); err != nil && s.logger != nil {
		s.logger.Errf("failed to rename tls policy watcher %q -> %q: %v", oldName, newName, err)
	}
}

func (s *tlsNodeNameSync) deleteReferences(name string) {
	if err := s.manager.deleteTLSPolicyNodeReferences(name); err != nil && s.logger != nil {
		s.logger.Errf("failed to delete tls policy watchers for %q: %v", name, err)
	}
}
