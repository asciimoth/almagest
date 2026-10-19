package almagest

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/asciimoth/configer/configer"
	sandbox "github.com/asciimoth/gobox"
	"github.com/asciimoth/pasta/pasta"
)

const sandboxOwnerConfigKey = "SandboxOwner"

type sandboxOwnerMetadata struct {
	Key          string
	Epoch        uint64
	NameRevision uint64
}

type sandboxOwnerCoordinator struct {
	service  sandbox.Service
	binder   sandbox.OwnerBinder
	revoker  sandbox.OwnerRevoker
	mu       sync.Mutex
	byNode   map[uint64]sandbox.Owner
	restored map[string]sandboxOwnerMetadata
	// restoredByNode keeps durable identity stable across undo, redo, and other
	// workspace renames that do not provide the previous node name.
	restoredByNode map[uint64]sandboxOwnerMetadata
	failure        error
	closed         bool
}

func newSandboxOwners(service sandbox.Service, nodes configer.Config) (*sandboxOwnerCoordinator, error) {
	binder, ok := service.(sandbox.OwnerBinder)
	if !ok {
		return nil, fmt.Errorf("sandbox service cannot atomically issue and bind owner identities: %w", sandbox.ErrUnavailable)
	}
	c := &sandboxOwnerCoordinator{service: service, byNode: map[uint64]sandbox.Owner{},
		restored: map[string]sandboxOwnerMetadata{}, restoredByNode: map[uint64]sandboxOwnerMetadata{}}
	c.binder = binder
	c.revoker, _ = service.(sandbox.OwnerRevoker)
	if nodes == nil {
		return c, nil
	}
	value, err := nodes.Get(nil)
	if errors.Is(err, configer.ErrNotFound) {
		return c, nil
	}
	if err != nil {
		return nil, err
	}
	object, ok := value.(map[string]any)
	if !ok {
		return c, nil
	}
	for name, raw := range object {
		node, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		owner, ok := node[sandboxOwnerConfigKey].(map[string]any)
		if !ok {
			continue
		}
		metadata, err := decodeSandboxOwner(owner)
		if err != nil {
			return nil, fmt.Errorf("node %q sandbox owner: %w", name, err)
		}
		c.restored[name] = metadata
	}
	return c, nil
}

func (c *sandboxOwnerCoordinator) attachRestored(snapshot pasta.WorkspaceSnapshot) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for id, node := range snapshot.Nodes {
		if metadata, ok := c.restored[node.Name]; ok {
			c.restoredByNode[id] = metadata
			delete(c.restored, node.Name)
		}
	}
}

func decodeSandboxOwner(value map[string]any) (sandboxOwnerMetadata, error) {
	key, ok := value["Key"].(string)
	if !ok || sandbox.ValidateName(key) != nil {
		return sandboxOwnerMetadata{}, sandbox.ErrInvalid
	}
	epoch, ok := configUint64(value["Epoch"])
	if !ok || epoch == 0 {
		return sandboxOwnerMetadata{}, sandbox.ErrInvalid
	}
	revision, ok := configUint64(value["NameRevision"])
	if !ok || revision == 0 {
		return sandboxOwnerMetadata{}, sandbox.ErrInvalid
	}
	return sandboxOwnerMetadata{Key: key, Epoch: epoch, NameRevision: revision}, nil
}

func configUint64(value any) (uint64, bool) {
	switch value := value.(type) {
	case uint64:
		return value, true
	case int64:
		if value > 0 {
			return uint64(value), true
		}
	case int:
		if value > 0 {
			return uint64(value), true
		}
	case float64:
		if value > 0 && value == float64(uint64(value)) {
			return uint64(value), true
		}
	}
	return 0, false
}

func (c *sandboxOwnerCoordinator) bind(ctx context.Context, node uint64, class, name string) (sandbox.OwnerSandboxes, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil, false, sandbox.ErrClosed
	}
	if c.failure != nil {
		return nil, false, errors.Join(sandbox.ErrUnavailable, c.failure)
	}
	if owner, ok := c.byNode[node]; ok {
		view, err := c.service.BindOwner(ctx, owner)
		return view, false, err
	}
	proposed := sandbox.Owner{Workspace: MainWorkspace, Class: class, Name: name}
	metadata, restoring := c.restoredByNode[node]
	if !restoring {
		metadata, restoring = c.restored[name]
	}
	if restoring {
		proposed.Key = metadata.Key
		proposed.Epoch = metadata.Epoch
		proposed.NameRevision = metadata.NameRevision
	}
	var owner sandbox.Owner
	var view sandbox.OwnerSandboxes
	var err error
	owner, view, err = c.binder.IssueAndBindOwner(ctx, proposed)
	// Imported or copied graph metadata cannot claim an existing owner. If the
	// durable registry rejects the restored identity, create a new identity for
	// this logical node instead of attaching the old persistent state.
	if restoring && (errors.Is(err, sandbox.ErrDenied) || errors.Is(err, sandbox.ErrStale)) {
		proposed.Key = ""
		proposed.Epoch = 0
		proposed.NameRevision = 0
		owner, view, err = c.binder.IssueAndBindOwner(ctx, proposed)
	}
	if err != nil {
		return nil, false, err
	}
	if c.closed {
		_ = c.service.ReleaseOwner(context.Background(), owner)
		return nil, false, sandbox.ErrClosed
	}
	c.byNode[node] = owner
	delete(c.restored, name)
	delete(c.restoredByNode, node)
	return view, true, nil
}

func (c *sandboxOwnerCoordinator) observe(notification pasta.WorkspaceNotification) {
	switch notification.Kind {
	case pasta.NotificationNodeRemoved:
		if notification.Node != nil {
			c.release(notification.ID)
		}
	case pasta.NotificationNodesRemoved:
		for id := range notification.Nodes {
			c.release(id)
		}
	case pasta.NotificationNodeUpdated:
		if notification.Node != nil {
			c.recordFailure(c.rename(notification.ID, notification.Node.Name))
		}
	case pasta.NotificationNodesUpdated:
		for id, node := range notification.Nodes {
			c.recordFailure(c.rename(id, node.Name))
		}
	}
}

func (c *sandboxOwnerCoordinator) release(node uint64) {
	c.mu.Lock()
	owner, ok := c.byNode[node]
	if ok {
		delete(c.byNode, node)
	}
	delete(c.restoredByNode, node)
	c.mu.Unlock()
	if ok {
		if c.revoker != nil {
			c.recordFailure(c.revoker.RevokeOwner(context.Background(), owner))
		}
		go func() { c.recordFailure(c.service.ReleaseOwner(context.Background(), owner)) }()
	}
}

func (c *sandboxOwnerCoordinator) recordFailure(err error) {
	if err == nil || errors.Is(err, sandbox.ErrClosed) {
		return
	}
	c.mu.Lock()
	c.failure = errors.Join(c.failure, err)
	c.mu.Unlock()
}

func (c *sandboxOwnerCoordinator) rename(node uint64, name string) error {
	return c.renameFrom(node, "", name)
}

// renameFrom also preserves restored metadata when a node is renamed before
// it first asks for sandbox access. oldName is supplied by the engine's
// checked rename path. Notification-only callers leave it empty because they
// do not have a trustworthy previous snapshot.
func (c *sandboxOwnerCoordinator) renameFrom(node uint64, oldName, name string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	owner, ok := c.byNode[node]
	if !ok {
		if oldName != "" && oldName != name {
			if _, restored := c.restoredByNode[node]; restored {
				return nil
			}
			if metadata, restored := c.restored[oldName]; restored {
				if _, conflict := c.restored[name]; conflict {
					return sandbox.ErrConflict
				}
				delete(c.restored, oldName)
				c.restored[name] = metadata
			}
		}
		return nil
	}
	if owner.Name == name {
		return nil
	}
	updated, err := c.service.RebindOwner(context.Background(), sandbox.OwnerRename{Owner: owner, NewName: name, OperationID: fmt.Sprintf("engine-%s-%d-%d", owner.Key, owner.NameRevision, node)})
	if err != nil {
		return err
	}
	if current, ok := c.byNode[node]; ok && current == owner {
		c.byNode[node] = updated
		return nil
	}
	return sandbox.ErrStale
}

func (c *sandboxOwnerCoordinator) save(nodes configer.Config) error {
	c.mu.Lock()
	if c.failure != nil {
		err := errors.Join(sandbox.ErrUnavailable, c.failure)
		c.mu.Unlock()
		return err
	}
	owners := make([]sandbox.Owner, 0, len(c.byNode))
	for _, owner := range c.byNode {
		owners = append(owners, owner)
	}
	c.mu.Unlock()
	for _, owner := range owners {
		value := map[string]any{"Key": owner.Key, "Epoch": owner.Epoch, "NameRevision": owner.NameRevision}
		if err := nodes.Set(configer.Path{owner.Name, sandboxOwnerConfigKey}, value); err != nil {
			return err
		}
	}
	return nil
}

func (c *sandboxOwnerCoordinator) close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	failure := c.failure
	c.byNode = map[uint64]sandbox.Owner{}
	c.restoredByNode = map[uint64]sandboxOwnerMetadata{}
	c.mu.Unlock()
	return errors.Join(failure, c.service.Close())
}

func (e *engine) SandboxesForNode(ctx context.Context, node uint64, _, _ string) (sandbox.OwnerSandboxes, error) {
	if e == nil || e.sandboxOwners == nil {
		return nil, sandbox.ErrUnavailable
	}
	if e.workspaceReady != nil {
		select {
		case <-e.workspaceReady:
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-e.ctx.Done():
			return nil, sandbox.ErrClosed
		}
	}
	if e.mainWorkspace == nil {
		return nil, sandbox.ErrUnavailable
	}
	snapshot, ok := e.mainWorkspace.NodeSnapshot(node)
	if !ok {
		return nil, pasta.ErrNoNode
	}
	view, changed, err := e.sandboxOwners.bind(ctx, node, snapshot.Class, snapshot.Name)
	if err == nil {
		view = trackedOwnerSandboxes{base: view, tracker: e.workers, node: node}
	}
	if err == nil && changed && e.nodeConfigChangeCallback != nil {
		result := e.nodeConfigChangeCallback(node)
		if result != nil {
			go func() { <-result }()
		}
	}
	return view, err
}
