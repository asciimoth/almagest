package nodekit

import (
	"context"
	"errors"
	"sync"

	"github.com/asciimoth/bufpool"
	"github.com/asciimoth/gonnect/sysnet"
	"github.com/asciimoth/pasta/pasta"

	"github.com/asciimoth/almagest/lib/storage"
)

var (
	// ErrWorkerNil reports that a nil worker function was passed to a worker
	// spawn method.
	ErrWorkerNil = errors.New("worker nil")
	// ErrWorkerWaitGroupNil reports that a nil wait group was passed to a
	// worker spawn method.
	ErrWorkerWaitGroupNil = errors.New("waitgroup nil")
	// ErrWorkerSpawnerUnavailable reports that a zero-value or detached spawner
	// cannot start a worker.
	ErrWorkerSpawnerUnavailable = errors.New("worker spawner unavailable")
)

// WorkerSpawner starts engine-tracked background workers for one node.
type WorkerSpawner interface {
	Spawn(worker func(), name string) (uint64, error)
	SpawnWg(worker func(), waitgroup *sync.WaitGroup, name string) (uint64, error)
}

// Runtime exposes engine services used by built-in node implementations.
type Runtime interface {
	Context() context.Context
	Pool() bufpool.Pool
	System() sysnet.System
	KVManager() storage.KVManager
	SecretsStorage() storage.SecretsStorage
	TlsConfigStorage() storage.TlsConfigStorage
	WorkerSpawner(node uint64) WorkerSpawner
	ObserveNodeSnapshot(node uint64, snapshot pasta.NodeSnapshot)
}

// NodeContext is the small part of Base used by shared node helpers.
type NodeContext interface {
	NodeRuntime() Runtime
	NodeWorkspace() *pasta.Workspace
	NodeID() uint64
}

// RestartableWorker wraps a long-lived worker with a stop channel.
type RestartableWorker struct {
	stop chan struct{}
	done chan struct{}
}

// NewRestartableWorker runs run as an engine-tracked restartable worker.
func NewRestartableWorker(spawner WorkerSpawner, run func(stop <-chan struct{}), name string) (*RestartableWorker, error) {
	worker := &RestartableWorker{stop: make(chan struct{}), done: make(chan struct{})}
	if spawner == nil {
		close(worker.done)
		return nil, ErrWorkerSpawnerUnavailable
	}
	if _, err := spawner.Spawn(func() {
		defer close(worker.done)
		run(worker.stop)
	}, name); err != nil {
		close(worker.done)
		return nil, err
	}
	return worker, nil
}

// Stop requests worker shutdown.
func (w *RestartableWorker) Stop() {
	if w == nil {
		return
	}
	close(w.stop)
}

// Done returns a channel that closes when the worker stops.
func (w *RestartableWorker) Done() <-chan struct{} {
	if w == nil {
		closed := make(chan struct{})
		close(closed)
		return closed
	}
	return w.done
}

// Base carries common runtime handles and workspace helpers for external node
// packages.
type Base struct {
	pasta.BasicNode

	Runtime       Runtime
	Logger        pasta.Logger
	W             *pasta.Workspace
	ID            uint64
	WorkerSpawner WorkerSpawner
}

// InitBaseNode stores runtime handles provided by a node's OnInit callback.
func (b *Base) InitBaseNode(w *pasta.Workspace, logger pasta.Logger, id uint64) {
	b.W = w
	b.Logger = logger
	b.ID = id
	if b.Runtime != nil && id != 0 {
		b.WorkerSpawner = b.Runtime.WorkerSpawner(id)
		if w != nil {
			if snapshot, ok := w.NodeSnapshotLocked(id); ok {
				b.Runtime.ObserveNodeSnapshot(id, snapshot)
			}
		}
	}
}

// NodeRuntime returns the runtime behind this node.
func (b *Base) NodeRuntime() Runtime {
	if b == nil {
		return nil
	}
	return b.Runtime
}

// NodeWorkspace returns the node's workspace.
func (b *Base) NodeWorkspace() *pasta.Workspace {
	if b == nil {
		return nil
	}
	return b.W
}

// NodeID returns the node's workspace ID.
func (b *Base) NodeID() uint64 {
	if b == nil {
		return 0
	}
	return b.ID
}

// Context returns the engine context, or context.Background when unavailable.
func (b *Base) Context() context.Context {
	if b != nil && b.Runtime != nil && b.Runtime.Context() != nil {
		return b.Runtime.Context()
	}
	return context.Background()
}

// Pool returns the engine buffer pool.
func (b *Base) Pool() bufpool.Pool {
	if b == nil || b.Runtime == nil {
		return nil
	}
	return b.Runtime.Pool()
}

// System returns the engine system integration.
func (b *Base) System() sysnet.System {
	if b == nil || b.Runtime == nil {
		return nil
	}
	return b.Runtime.System()
}

// SecretsStorage returns the engine secret store.
func (b *Base) SecretsStorage() storage.SecretsStorage {
	if b == nil || b.Runtime == nil {
		return nil
	}
	return b.Runtime.SecretsStorage()
}

// TlsConfigStorage returns the engine TLS config store.
func (b *Base) TlsConfigStorage() storage.TlsConfigStorage {
	if b == nil || b.Runtime == nil {
		return nil
	}
	return b.Runtime.TlsConfigStorage()
}

// SpawnWorker runs worker as an engine-tracked background worker owned by this
// node.
func (b *Base) SpawnWorker(name string, worker func()) error {
	if b == nil || b.WorkerSpawner == nil {
		return ErrWorkerSpawnerUnavailable
	}
	_, err := b.WorkerSpawner.Spawn(worker, name)
	if err != nil && b.Logger != nil {
		b.Logger.Errf("failed to spawn node worker %q: %v", name, err)
	}
	return err
}

// NewRestartableWorker runs run as an engine-tracked restartable worker owned
// by this node.
func (b *Base) NewRestartableWorker(name string, run func(stop <-chan struct{})) (*RestartableWorker, error) {
	if b == nil {
		return nil, ErrWorkerSpawnerUnavailable
	}
	worker, err := NewRestartableWorker(b.WorkerSpawner, run, name)
	if err != nil && b.Logger != nil {
		b.Logger.Errf("failed to spawn node worker %q: %v", name, err)
	}
	return worker, err
}
