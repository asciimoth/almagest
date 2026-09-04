package main

import (
	"container/list"
	"sort"
	"sync"

	"github.com/asciimoth/almagest/lib/logging"
	"github.com/asciimoth/configer/configer"
)

const recentNodeNameCapacity = 1024

// nodeNameIndex reserves names that must not be selected for a new node. Secret
// names are not capacity limited. Recent names use a bounded recency queue.
type nodeNameIndex struct {
	mu sync.RWMutex

	secretNames map[string]struct{}

	recentCapacity int
	recentNames    map[string]*list.Element
	recentOrder    *list.List
}

func newNodeNameIndex(recentCapacity int) *nodeNameIndex {
	return &nodeNameIndex{
		secretNames:    map[string]struct{}{},
		recentCapacity: max(recentCapacity, 0),
		recentNames:    map[string]*list.Element{},
		recentOrder:    list.New(),
	}
}

// available reports whether pasta can use name as a generated node name.
func (i *nodeNameIndex) available(name string) bool {
	if i == nil {
		return true
	}
	i.mu.RLock()
	defer i.mu.RUnlock()
	if _, reserved := i.secretNames[name]; reserved {
		return false
	}
	_, reserved := i.recentNames[name]
	return !reserved
}

// replaceSecretNames replaces the unlimited index of names in secret allow
// lists.
func (i *nodeNameIndex) replaceSecretNames(names []string) {
	if i == nil {
		return
	}
	next := make(map[string]struct{}, len(names))
	for _, name := range names {
		if name != "" {
			next[name] = struct{}{}
		}
	}
	i.mu.Lock()
	i.secretNames = next
	i.mu.Unlock()
}

// remember marks name as most recently used. An existing name moves to the
// back of the queue. The oldest name is removed when the queue is full.
func (i *nodeNameIndex) remember(name string) {
	if i == nil || name == "" || i.recentCapacity == 0 {
		return
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	if element := i.recentNames[name]; element != nil {
		i.recentOrder.MoveToBack(element)
		return
	}
	element := i.recentOrder.PushBack(name)
	i.recentNames[name] = element
	if i.recentOrder.Len() <= i.recentCapacity {
		return
	}
	oldest := i.recentOrder.Front()
	delete(i.recentNames, oldest.Value.(string))
	i.recentOrder.Remove(oldest)
}

// daemonNodeNameIndex keeps the secret-name index synchronized with bbolt.
type daemonNodeNameIndex struct {
	index          *nodeNameIndex
	manager        *bboltKVManager
	subscriptionID uint64
	logger         logging.Logger
}

func newDaemonNodeNameIndex(
	manager *bboltKVManager,
	cfg configer.Config,
	logger logging.Logger,
) (*daemonNodeNameIndex, error) {
	index := newNodeNameIndex(recentNodeNameCapacity)
	for _, name := range configuredNodeNames(cfg) {
		index.remember(name)
	}

	out := &daemonNodeNameIndex{
		index:   index,
		manager: manager,
		logger:  logger,
	}
	if manager == nil {
		return out, nil
	}
	if err := out.refreshSecretNames(); err != nil {
		return nil, err
	}
	out.subscriptionID = manager.subscribeSecretChanges(func(secretChange) {
		if err := out.refreshSecretNames(); err != nil && out.logger != nil {
			out.logger.Errf("failed to refresh reserved secret node names: %v", err)
		}
	})
	return out, nil
}

func (i *daemonNodeNameIndex) refreshSecretNames() error {
	names, err := i.manager.secretNodeNames()
	if err != nil {
		return err
	}
	i.index.replaceSecretNames(names)
	return nil
}

func (i *daemonNodeNameIndex) Close() {
	if i == nil || i.manager == nil || i.subscriptionID == 0 {
		return
	}
	i.manager.unsubscribeSecretChanges(i.subscriptionID)
	i.subscriptionID = 0
}

func configuredNodeNames(cfg configer.Config) []string {
	if cfg == nil {
		return nil
	}
	nodes, ok := configer.MapStringAny(cfg.View(configer.Path{"nodes"}).Snapshot())
	if !ok {
		return nil
	}
	names := make([]string, 0, len(nodes))
	for name := range nodes {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
