package main

import (
	"sync"

	almagest "github.com/asciimoth/almagest/lib"
	"github.com/asciimoth/almagest/lib/logging"
	"github.com/asciimoth/pasta/pasta"
)

type secretNodeNameSync struct {
	manager *bboltKVManager
	logger  logging.Logger

	mu    sync.Mutex
	nodes map[uint64]string
}

// startSecretNodeNameSync keeps secret allow lists keyed by the current
// workspace node names. It observes engine notifications from almagestd without
// adding secrets storage to lib.Engine.
func (a *AdminAPI) startSecretNodeNameSync(engine almagest.Engine, logger logging.Logger) uint64 {
	if a == nil || a.secrets == nil || engine == nil {
		return 0
	}
	syncer := &secretNodeNameSync{
		manager: a.secrets,
		logger:  logger,
		nodes:   map[uint64]string{},
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
			s.deleteReferences(oldName)
			continue
		}
		if oldName != newName {
			s.renameReferences(oldName, newName)
		}
	}
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
		s.deleteReferences(name)
	}
}

func (s *secretNodeNameSync) renameReferences(oldName, newName string) {
	if err := s.manager.renameSecretNodeReferences(oldName, newName); err != nil && s.logger != nil {
		s.logger.Errf("failed to rename secret node references %q -> %q: %v", oldName, newName, err)
	}
}

func (s *secretNodeNameSync) deleteReferences(name string) {
	if err := s.manager.deleteSecretNodeReferences(name); err != nil && s.logger != nil {
		s.logger.Errf("failed to delete secret node references for %q: %v", name, err)
	}
}
