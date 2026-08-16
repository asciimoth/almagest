package almagest

import (
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/asciimoth/pasta/pasta"

	"github.com/asciimoth/almagest/lib/logging"
)

var (
	// ErrWorkerNil reports that a nil worker function was passed to a worker
	// spawn method.
	ErrWorkerNil = errors.New("worker nil")
	// ErrWorkerWaitGroupNil reports that a nil wait group was passed to
	// EngineWorkerSpawner.SpawnWg.
	ErrWorkerWaitGroupNil = errors.New("waitgroup nil")
	// ErrWorkerSpawnerUnavailable reports that a zero-value or detached spawner
	// cannot start a worker.
	ErrWorkerSpawnerUnavailable = errors.New("worker spawner unavailable")
)

// WorkerNotificationKind identifies the engine worker lifecycle event.
type WorkerNotificationKind string

const (
	WorkerNotificationSnapshot WorkerNotificationKind = "worker_snapshot"
	WorkerNotificationSpawned  WorkerNotificationKind = "worker_spawned"
	WorkerNotificationStopped  WorkerNotificationKind = "worker_stopped"
	WorkerNotificationFailed   WorkerNotificationKind = "worker_failed"
)

// WorkerSnapshot describes one live background worker owned by the engine.
//
// It intentionally mirrors the old pasta worker JSON shape so existing
// worker UIs can consume engine workers without a model migration.
type WorkerSnapshot struct {
	ID         uint64    `json:"id"`
	Node       uint64    `json:"node"`
	Class      string    `json:"class"`
	Name       string    `json:"name"`
	WorkerName string    `json:"worker_name,omitempty"`
	StartedAt  time.Time `json:"started_at"`
	Orphan     bool      `json:"orphan"`
	Error      string    `json:"error,omitempty"`
}

// WorkerNotification describes one worker-system state change.
type WorkerNotification struct {
	SubscriptionID uint64 `json:"-"`

	Kind WorkerNotificationKind `json:"kind"`
	ID   uint64                 `json:"id,omitempty"`

	Snapshot map[uint64]WorkerSnapshot `json:"snapshot,omitempty"`
	Worker   *WorkerSnapshot           `json:"worker,omitempty"`
}

// WorkerNotificationCallback receives worker lifecycle notifications.
type WorkerNotificationCallback func(WorkerNotification)

// EngineWorkerSpawner spawns workers attached to a single workspace node.
type EngineWorkerSpawner struct {
	tracker *workerTracker
	node    uint64
}

// Spawn runs worker in a new engine-tracked goroutine attached to the spawner's
// node. Name is optional worker metadata.
func (s EngineWorkerSpawner) Spawn(worker func(), name string) (uint64, error) {
	if s.tracker == nil {
		return 0, ErrWorkerSpawnerUnavailable
	}
	return s.tracker.spawn(s.node, worker, nil, name)
}

// SpawnWg runs worker in a new engine-tracked goroutine attached to the
// spawner's node and also tracks it in waitgroup. Name is optional worker
// metadata.
func (s EngineWorkerSpawner) SpawnWg(worker func(), waitgroup *sync.WaitGroup, name string) (uint64, error) {
	if waitgroup == nil {
		return 0, ErrWorkerWaitGroupNil
	}
	if s.tracker == nil {
		return 0, ErrWorkerSpawnerUnavailable
	}
	return s.tracker.spawn(s.node, worker, waitgroup, name)
}

type workerRecord struct {
	ID         uint64
	Node       uint64
	Class      string
	Name       string
	WorkerName string
	StartedAt  time.Time
}

type workerTracker struct {
	mu sync.Mutex
	wg sync.WaitGroup

	nextWorkerID       uint64
	nextSubscriptionID uint64

	closed        bool
	nodes         map[uint64]pasta.NodeSnapshot
	workers       map[uint64]workerRecord
	subscriptions map[uint64]WorkerNotificationCallback
	workspace     func() *pasta.Workspace
	logger        logging.Logger
	onFailure     func(WorkerSnapshot)
}

type workerNotificationDelivery struct {
	callback     WorkerNotificationCallback
	notification WorkerNotification
}

func newWorkerTracker(workspace func() *pasta.Workspace, logger logging.Logger) *workerTracker {
	return &workerTracker{
		nextWorkerID:       1,
		nextSubscriptionID: 1,
		nodes:              make(map[uint64]pasta.NodeSnapshot),
		workers:            make(map[uint64]workerRecord),
		subscriptions:      make(map[uint64]WorkerNotificationCallback),
		workspace:          workspace,
		logger:             logger,
	}
}

func (t *workerTracker) spawner(node uint64) EngineWorkerSpawner {
	if t == nil {
		return EngineWorkerSpawner{}
	}
	return EngineWorkerSpawner{tracker: t, node: node}
}

func (t *workerTracker) spawn(node uint64, worker func(), waitgroup *sync.WaitGroup, name string) (uint64, error) {
	if worker == nil {
		return 0, ErrWorkerNil
	}
	if node < 1 {
		return 0, pasta.ErrNoNode
	}
	record, err := t.prepare(node, name)
	if err != nil {
		return 0, err
	}

	run := t.run(record, worker)
	if waitgroup != nil {
		waitgroup.Go(run)
	} else {
		go run()
	}
	return record.ID, nil
}

func (t *workerTracker) prepare(node uint64, name string) (workerRecord, error) {
	nodeSnapshot, ok := t.cachedNodeSnapshot(node)
	if !ok {
		nodeSnapshot, ok = t.loadNodeSnapshot(node)
		if !ok {
			return workerRecord{}, pasta.ErrNoNode
		}
	}

	t.mu.Lock()

	if t.closed {
		t.mu.Unlock()
		return workerRecord{}, pasta.ErrWorkspaceClosed
	}
	t.nodes[node] = nodeSnapshot
	id := t.nextWorkerID
	if id < 1 {
		id = 1
	}
	t.nextWorkerID = id + 1
	record := workerRecord{
		ID:         id,
		Node:       node,
		Class:      nodeSnapshot.Class,
		Name:       nodeSnapshot.Name,
		WorkerName: name,
		StartedAt:  time.Now().UTC(),
	}
	t.workers[id] = record
	t.wg.Add(1)
	if t.logger != nil {
		t.logger.Debugf("engine worker spawned worker=%d node=%d class=%s name=%q worker_name=%q", id, node, record.Class, record.Name, name)
	}
	deliveries := t.deliveriesLocked(WorkerNotification{
		Kind:   WorkerNotificationSpawned,
		ID:     id,
		Worker: ptr(t.workerSnapshotLocked(record, "")),
	})
	t.mu.Unlock()
	deliverWorkerNotifications(deliveries)
	return record, nil
}

func (t *workerTracker) run(record workerRecord, run func()) func() {
	return func() {
		normalStop := false
		defer func() {
			if r := recover(); r != nil {
				t.fail(record, pasta.ErrNodePanic)
			} else {
				normalStop = true
			}
			t.finish(record, normalStop)
		}()
		run()
	}
}

func (t *workerTracker) finish(record workerRecord, normalStop bool) {
	var notify WorkerNotification
	var deliver bool
	t.mu.Lock()
	if normalStop {
		if t.logger != nil {
			t.logger.Debugf("engine worker stopped worker=%d node=%d class=%s name=%q worker_name=%q", record.ID, record.Node, record.Class, record.Name, record.WorkerName)
		}
		notify = WorkerNotification{
			Kind:   WorkerNotificationStopped,
			ID:     record.ID,
			Worker: ptr(t.workerSnapshotLocked(record, "")),
		}
		deliver = true
	}
	delete(t.workers, record.ID)
	var deliveries []workerNotificationDelivery
	if deliver {
		deliveries = t.deliveriesLocked(notify)
	}
	t.mu.Unlock()
	deliverWorkerNotifications(deliveries)
	t.wg.Done()
}

func (t *workerTracker) fail(record workerRecord, cause error) {
	t.mu.Lock()
	snapshot := t.workerSnapshotLocked(record, causeString(cause))
	deliveries := t.deliveriesLocked(WorkerNotification{
		Kind:   WorkerNotificationFailed,
		ID:     record.ID,
		Worker: &snapshot,
	})
	if t.logger != nil {
		if snapshot.Orphan {
			t.logger.Errf(
				"engine worker failed after node was unavailable worker=%d node=%d class=%s name=%q worker_name=%q cause=%v",
				record.ID,
				record.Node,
				record.Class,
				record.Name,
				record.WorkerName,
				cause,
			)
		} else {
			t.logger.Errf(
				"engine worker failed worker=%d node=%d class=%s name=%q worker_name=%q cause=%v",
				record.ID,
				record.Node,
				record.Class,
				record.Name,
				record.WorkerName,
				cause,
			)
		}
	}
	onFailure := t.onFailure
	t.mu.Unlock()
	deliverWorkerNotifications(deliveries)
	if onFailure != nil {
		onFailure(snapshot)
	}
}

func (t *workerTracker) close() {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		t.wg.Wait()
		return
	}
	t.closed = true
	for _, worker := range t.workers {
		if t.logger != nil {
			snapshot := t.workerSnapshotLocked(worker, "")
			t.logger.Debugf(
				"waiting for engine worker worker=%d node=%d class=%s name=%q worker_name=%q orphan=%t",
				snapshot.ID,
				snapshot.Node,
				snapshot.Class,
				snapshot.Name,
				snapshot.WorkerName,
				snapshot.Orphan,
			)
		}
	}
	t.mu.Unlock()
	t.wg.Wait()
	t.mu.Lock()
	t.subscriptions = make(map[uint64]WorkerNotificationCallback)
	t.mu.Unlock()
}

func (t *workerTracker) snapshot() map[uint64]WorkerSnapshot {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.workerSnapshotsLocked()
}

func (t *workerTracker) workerSnapshotsLocked() map[uint64]WorkerSnapshot {
	workers := make(map[uint64]WorkerSnapshot, len(t.workers))
	for id, worker := range t.workers {
		workers[id] = t.workerSnapshotLocked(worker, "")
	}
	return workers
}

func (t *workerTracker) workerSnapshotLocked(worker workerRecord, failure string) WorkerSnapshot {
	_, present := t.nodes[worker.Node]
	return WorkerSnapshot{
		ID:         worker.ID,
		Node:       worker.Node,
		Class:      worker.Class,
		Name:       worker.Name,
		WorkerName: worker.WorkerName,
		StartedAt:  worker.StartedAt,
		Orphan:     !present,
		Error:      failure,
	}
}

func (t *workerTracker) subscribe(callback WorkerNotificationCallback) uint64 {
	if callback == nil {
		return 0
	}
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return 0
	}
	id := t.nextSubscriptionID
	if id < 1 {
		id = 1
	}
	t.nextSubscriptionID = id + 1
	t.subscriptions[id] = callback
	snapshot := t.workerSnapshotsLocked()
	t.mu.Unlock()

	callback(WorkerNotification{
		SubscriptionID: id,
		Kind:           WorkerNotificationSnapshot,
		Snapshot:       snapshot,
	})
	return id
}

func (t *workerTracker) unsubscribe(id uint64) {
	t.mu.Lock()
	delete(t.subscriptions, id)
	t.mu.Unlock()
}

func (t *workerTracker) requestFullSnapshot(id uint64) {
	t.mu.Lock()
	callback, ok := t.subscriptions[id]
	if !ok {
		t.mu.Unlock()
		return
	}
	snapshot := t.workerSnapshotsLocked()
	t.mu.Unlock()
	callback(WorkerNotification{
		SubscriptionID: id,
		Kind:           WorkerNotificationSnapshot,
		Snapshot:       snapshot,
	})
}

func (t *workerTracker) observeWorkspaceNotification(notification pasta.WorkspaceNotification) {
	t.mu.Lock()
	defer t.mu.Unlock()
	switch notification.Kind {
	case pasta.NotificationWorkspaceSnapshot:
		t.nodes = make(map[uint64]pasta.NodeSnapshot)
		if notification.Snapshot == nil {
			return
		}
		for id, node := range notification.Snapshot.Nodes {
			t.nodes[id] = node
		}
	case pasta.NotificationNodeAdded, pasta.NotificationNodeUpdated:
		if notification.ID != 0 && notification.Node != nil {
			t.nodes[notification.ID] = *notification.Node
		}
	case pasta.NotificationNodeRemoved:
		delete(t.nodes, notification.ID)
	case pasta.NotificationWorkspaceStopped:
		t.nodes = make(map[uint64]pasta.NodeSnapshot)
	}
}

func (t *workerTracker) observeNodeSnapshot(id uint64, snapshot pasta.NodeSnapshot) {
	if id == 0 {
		return
	}
	t.mu.Lock()
	if t.nodes != nil {
		t.nodes[id] = snapshot
	}
	t.mu.Unlock()
}

func (t *workerTracker) cachedNodeSnapshot(node uint64) (pasta.NodeSnapshot, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	snapshot, ok := t.nodes[node]
	return snapshot, ok
}

func (t *workerTracker) loadNodeSnapshot(node uint64) (pasta.NodeSnapshot, bool) {
	if t.workspace == nil {
		return pasta.NodeSnapshot{}, false
	}
	w := t.workspace()
	if w == nil {
		return pasta.NodeSnapshot{}, false
	}
	return w.NodeSnapshot(node)
}

func (t *workerTracker) deliveriesLocked(notification WorkerNotification) []workerNotificationDelivery {
	if len(t.subscriptions) == 0 {
		return nil
	}
	deliveries := make([]workerNotificationDelivery, 0, len(t.subscriptions))
	for id, callback := range t.subscriptions {
		if callback == nil {
			continue
		}
		notification.SubscriptionID = id
		deliveries = append(deliveries, workerNotificationDelivery{
			callback:     callback,
			notification: notification,
		})
	}
	return deliveries
}

func deliverWorkerNotifications(deliveries []workerNotificationDelivery) {
	for _, delivery := range deliveries {
		if delivery.callback != nil {
			delivery.callback(delivery.notification)
		}
	}
}

func ptr[T any](value T) *T {
	return &value
}

func causeString(cause error) string {
	if cause == nil {
		return ""
	}
	return cause.Error()
}

func workerFailureText(worker WorkerSnapshot) string {
	if worker.Error == "" {
		return fmt.Sprintf("worker %d failed", worker.ID)
	}
	return fmt.Sprintf("worker %d failed: %s", worker.ID, worker.Error)
}
