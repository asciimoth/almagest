package main

import (
	"sync"

	almagest "github.com/asciimoth/almagest/lib"
	"github.com/asciimoth/pasta/pasta"
)

type tlsNodeNameSync struct {
	manager *bboltKVManager
	logger  almagest.Logger

	mu    sync.Mutex
	nodes map[uint64]string
}

// startTLSNodeNameSync keeps TLS policy watcher names in step with workspace
// node renames and removals. Watchers are runtime-only and are not persisted.
func (a *AdminAPI) startTLSNodeNameSync(engine almagest.Engine, logger almagest.Logger) uint64 {
	if a == nil || a.tls == nil || engine == nil {
		return 0
	}
	syncer := &tlsNodeNameSync{
		manager: a.tls,
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
		for id, node := range wn.Nodes {
			s.renameNode(id, node.Name)
		}
	case pasta.NotificationNodesRemoved:
		for id, node := range wn.Nodes {
			s.deleteNode(id, node.Name)
		}
	case pasta.NotificationWorkspaceStopped:
		s.reconcileSnapshot(nil)
	}
}

func (s *tlsNodeNameSync) reconcileSnapshot(nodes map[uint64]pasta.NodeSnapshot) {
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
			s.deleteReferences(oldName)
			continue
		}
		if oldName != newName {
			s.renameReferences(oldName, newName)
		}
	}
}

func (s *tlsNodeNameSync) setNodeName(id uint64, name string) {
	s.mu.Lock()
	s.nodes[id] = name
	s.mu.Unlock()
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
		s.deleteReferences(name)
	}
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
