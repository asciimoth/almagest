package almagest

import (
	"maps"
	"slices"
	"sync"

	"github.com/asciimoth/pasta/pasta"
)

// engineNotificationDelivery holds a notification until the authoritative
// transaction snapshot has reached the same subscriber. It stores the public
// subscription ID instead of a callback so an unsubscribe takes effect before
// a queued delivery starts.
type engineNotificationDelivery struct {
	subscriptionID uint64
	notification   EngineNotification
}

type engineNotificationTransaction struct {
	once   sync.Once
	engine *engine
}

func (t *engineNotificationTransaction) Publish() {
	if t == nil {
		return
	}
	t.once.Do(func() {
		if t.engine != nil {
			t.engine.endNotificationTransaction()
		}
	})
}

func (e *engine) BeginNotificationTransaction() EngineNotificationTransaction {
	transaction := &engineNotificationTransaction{engine: e}
	if e == nil {
		return transaction
	}

	e.notificationMu.Lock()
	e.notificationTransactionDepth++
	// A transaction can start from a callback that publishes the previous
	// transaction. In that case, let the active publisher finish its callback
	// and replace its result with a newer full snapshot.
	if e.notificationFinishing {
		e.notificationRepublish = true
	}
	e.notificationMu.Unlock()
	return transaction
}

func (e *engine) endNotificationTransaction() {
	e.notificationMu.Lock()
	if e.notificationTransactionDepth == 0 {
		e.notificationMu.Unlock()
		return
	}
	e.notificationTransactionDepth--
	if e.notificationTransactionDepth != 0 {
		e.notificationMu.Unlock()
		return
	}
	if e.notificationFinishing {
		e.notificationRepublish = true
		e.notificationMu.Unlock()
		return
	}
	e.notificationFinishing = true
	e.notificationMu.Unlock()

	// A Pasta notification callback can be blocked in subscriber code. Finish
	// on a separate goroutine so a restore never waits for that client before it
	// can return and release its mutation locks.
	go e.finishNotificationTransaction()
}

// finishNotificationTransaction waits until workspace notifications queued
// before the barrier have reached the engine. This prevents an old removal or
// rename from being delivered after the final snapshot. It then captures the
// workspace and worker states while both sources are locked, publishes those
// states, and drains only changes that happened after the capture point.
func (e *engine) finishNotificationTransaction() {
	for {
		if !e.awaitWorkspaceNotificationBarrier() {
			e.stopNotificationFinisher()
			return
		}

		workspaceSnapshot, workerSnapshot, workerSequence, subscriberIDs, ok := e.captureNotificationPublication()
		if !ok {
			return
		}

		e.publishNotificationSnapshots(workspaceSnapshot, workerSnapshot, workerSequence, subscriberIDs)
		if !e.drainNotificationPublication() {
			return
		}
	}
}

// awaitWorkspaceNotificationBarrier uses the engine's private workspace
// observer. A requested full snapshot is queued after older workspace events,
// so its delivery proves that those events reached the engine suppression
// boundary. The engine context also releases the wait during shutdown.
func (e *engine) awaitWorkspaceNotificationBarrier() bool {
	if e == nil || e.mainWorkspace == nil || e.workspaceObserverSubscription == 0 {
		return false
	}

	e.Lock()
	workspaceSubscriptions := make(map[uint64]uint64, len(e.subscriptionIDMapping))
	for id, mapping := range e.subscriptionIDMapping {
		workspaceSubscriptions[id] = mapping.MainWorkspace
	}
	e.Unlock()

	barrier := make(chan struct{})
	e.notificationMu.Lock()
	e.workspaceBarrier = barrier
	e.workspaceBarrierPending = make(map[uint64]struct{}, len(workspaceSubscriptions)+1)
	e.workspaceBarrierPending[0] = struct{}{}
	for id := range workspaceSubscriptions {
		e.workspaceBarrierPending[id] = struct{}{}
	}
	e.notificationMu.Unlock()

	if !e.mainWorkspace.RequestFullSnapshot(e.workspaceObserverSubscription) {
		e.satisfyWorkspaceBarrier(0, barrier)
	}
	for id, workspaceID := range workspaceSubscriptions {
		if !e.mainWorkspace.RequestFullSnapshot(workspaceID) {
			e.satisfyWorkspaceBarrier(id, barrier)
		}
	}

	select {
	case <-barrier:
		return e.ctx.Err() == nil
	case <-e.ctx.Done():
		e.clearWorkspaceBarrier(barrier)
		return false
	}
}

func (e *engine) observeWorkspaceBarrier(id uint64, notification pasta.WorkspaceNotification) bool {
	if notification.Kind != pasta.NotificationWorkspaceSnapshot {
		return false
	}
	return e.satisfyWorkspaceBarrier(id, nil)
}

func (e *engine) satisfyWorkspaceBarrier(id uint64, expected chan struct{}) bool {
	e.notificationMu.Lock()
	if expected != nil && e.workspaceBarrier != expected {
		e.notificationMu.Unlock()
		return false
	}
	if _, pending := e.workspaceBarrierPending[id]; !pending {
		e.notificationMu.Unlock()
		return false
	}
	delete(e.workspaceBarrierPending, id)
	if len(e.workspaceBarrierPending) != 0 {
		e.notificationMu.Unlock()
		return true
	}
	barrier := e.workspaceBarrier
	e.workspaceBarrier = nil
	e.workspaceBarrierPending = nil
	e.notificationMu.Unlock()
	if barrier != nil {
		close(barrier)
	}
	return true
}

func (e *engine) clearWorkspaceBarrier(barrier chan struct{}) {
	e.notificationMu.Lock()
	if e.workspaceBarrier == barrier {
		e.workspaceBarrier = nil
		e.workspaceBarrierPending = nil
	}
	e.notificationMu.Unlock()
}

// captureNotificationPublication establishes the point between the full state
// and later incremental events. Workspace mutations cannot pass the workspace
// lock, worker mutations cannot pass the worker lock, and notification
// callbacks cannot pass notificationMu while this point is established.
func (e *engine) captureNotificationPublication() (
	pasta.WorkspaceSnapshot,
	map[uint64]WorkerSnapshot,
	uint64,
	[]uint64,
	bool,
) {
	if e == nil || e.mainWorkspace == nil || e.workers == nil {
		e.stopNotificationFinisher()
		return pasta.WorkspaceSnapshot{}, nil, 0, nil, false
	}

	e.mainWorkspace.Lock()
	workspaceSnapshot := e.mainWorkspace.SnapshotLocked()
	e.workers.mu.Lock()
	workerSnapshot := e.workers.workerSnapshotsLocked()
	workerSequence := e.workers.notificationSequence
	e.notificationMu.Lock()
	if e.ctx.Err() != nil || e.notificationTransactionDepth != 0 {
		// Shutdown cancels publication. A concurrent transaction also cancels
		// this capture; its outermost Publish will start a new finisher.
		e.notificationQueue = nil
		e.notificationPublishing = false
		e.notificationFinishing = false
		e.notificationMu.Unlock()
		e.workers.mu.Unlock()
		e.mainWorkspace.Unlock()
		return pasta.WorkspaceSnapshot{}, nil, 0, nil, false
	}

	e.notificationPublishing = true
	e.notificationRepublish = false
	e.notificationQueue = nil
	subscriberIDs := make([]uint64, 0, len(e.notificationCallbacks))
	for id := range e.notificationCallbacks {
		subscriberIDs = append(subscriberIDs, id)
	}
	slices.Sort(subscriberIDs)
	e.notificationMu.Unlock()
	e.workers.mu.Unlock()
	e.mainWorkspace.Unlock()

	return workspaceSnapshot, workerSnapshot, workerSequence, subscriberIDs, true
}

func (e *engine) publishNotificationSnapshots(
	workspaceSnapshot pasta.WorkspaceSnapshot,
	workerSnapshot map[uint64]WorkerSnapshot,
	workerSequence uint64,
	subscriberIDs []uint64,
) {
	for _, id := range subscriberIDs {
		workspace := workspaceSnapshot
		e.deliverEngineNotification(engineNotificationDelivery{
			subscriptionID: id,
			notification: EngineNotification{
				SubscriptionID: id,
				Kind:           NotificationWorkspace,
				WorkspaceID:    MainWorkspace,
				Workspace: &pasta.WorkspaceNotification{
					SubscriptionID: id,
					Kind:           pasta.NotificationWorkspaceSnapshot,
					Snapshot:       &workspace,
				},
			},
		})
		e.deliverEngineNotification(engineNotificationDelivery{
			subscriptionID: id,
			notification: EngineNotification{
				SubscriptionID: id,
				Kind:           NotificationWorker,
				WorkspaceID:    MainWorkspace,
				Worker: &WorkerNotification{
					SubscriptionID: id,
					Kind:           WorkerNotificationSnapshot,
					Snapshot:       maps.Clone(workerSnapshot),
					sequence:       workerSequence,
				},
			},
		})
	}
}

// drainNotificationPublication returns true when a transaction started and
// finished during publication, so the caller must replace the just-published
// state with a newer full snapshot. It returns false when publication is done
// or an active transaction will publish later.
func (e *engine) drainNotificationPublication() bool {
	for {
		e.notificationMu.Lock()
		if e.notificationTransactionDepth != 0 {
			e.notificationQueue = nil
			e.notificationPublishing = false
			e.notificationFinishing = false
			e.notificationMu.Unlock()
			return false
		}
		if e.notificationRepublish {
			e.notificationQueue = nil
			e.notificationPublishing = false
			e.notificationRepublish = false
			e.notificationMu.Unlock()
			return true
		}
		if len(e.notificationQueue) == 0 {
			e.notificationPublishing = false
			e.notificationFinishing = false
			e.notificationMu.Unlock()
			return false
		}
		deliveries := e.notificationQueue
		e.notificationQueue = nil
		e.notificationMu.Unlock()

		for _, delivery := range deliveries {
			e.deliverEngineNotification(delivery)
		}
	}
}

func (e *engine) stopNotificationFinisher() {
	if e == nil {
		return
	}
	e.notificationMu.Lock()
	e.notificationQueue = nil
	e.notificationPublishing = false
	e.notificationFinishing = false
	e.notificationMu.Unlock()
}

func (e *engine) addNotificationSubscriber(id uint64, callback NotificationCallback) {
	if e == nil || id == 0 || callback == nil {
		return
	}
	e.notificationMu.Lock()
	e.notificationCallbacks[id] = callback
	e.notificationMu.Unlock()
}

func (e *engine) removeNotificationSubscriber(id uint64) {
	if e == nil || id == 0 {
		return
	}
	e.notificationMu.Lock()
	delete(e.notificationCallbacks, id)
	delete(e.notificationWorkerFloor, id)
	e.notificationMu.Unlock()
	// An unsubscribe can remove a requested Pasta snapshot before delivery. It
	// must also release this subscriber's part of an active drain barrier.
	e.satisfyWorkspaceBarrier(id, nil)
}

func (e *engine) publishEngineNotification(notification EngineNotification) {
	if e == nil || notification.SubscriptionID == 0 {
		return
	}
	delivery := engineNotificationDelivery{
		subscriptionID: notification.SubscriptionID,
		notification:   notification,
	}

	e.notificationMu.Lock()
	if e.oldWorkerNotificationLocked(notification) {
		e.notificationMu.Unlock()
		return
	}
	// Node-menu messages are not represented by the authoritative workspace
	// snapshot published at the end of a transaction. In particular,
	// SubscribeNodeMenu can synchronously emit its cached forced snapshot after
	// Undo or Redo has returned but while the asynchronous publication barrier
	// is still finishing. Dropping that message leaves the subscriber waiting
	// forever because no later workspace snapshot can replace it.
	if notification.Workspace != nil && notification.Workspace.Kind == pasta.NotificationNodeMenu {
		callback := e.notificationCallbacks[notification.SubscriptionID]
		e.notificationMu.Unlock()
		if callback != nil {
			callback(notification)
		}
		return
	}
	if e.notificationTransactionDepth != 0 || (e.notificationFinishing && !e.notificationPublishing) {
		e.notificationMu.Unlock()
		return
	}
	if e.notificationPublishing {
		e.notificationQueue = append(e.notificationQueue, delivery)
		e.notificationMu.Unlock()
		return
	}
	callback := e.notificationCallbacks[notification.SubscriptionID]
	if callback != nil {
		e.recordWorkerNotificationSequenceLocked(notification)
	}
	e.notificationMu.Unlock()

	if callback != nil {
		callback(notification)
	}
}

func (e *engine) deliverEngineNotification(delivery engineNotificationDelivery) {
	e.notificationMu.Lock()
	callback := e.notificationCallbacks[delivery.subscriptionID]
	if e.oldWorkerNotificationLocked(delivery.notification) {
		callback = nil
	}
	if callback != nil {
		e.recordWorkerNotificationSequenceLocked(delivery.notification)
	}
	e.notificationMu.Unlock()
	if callback != nil {
		callback(delivery.notification)
	}
}

func (e *engine) oldWorkerNotificationLocked(notification EngineNotification) bool {
	worker := notification.Worker
	return worker != nil && worker.Kind != WorkerNotificationSnapshot && worker.sequence != 0 &&
		worker.sequence <= e.notificationWorkerFloor[notification.SubscriptionID]
}

func (e *engine) recordWorkerNotificationSequenceLocked(notification EngineNotification) {
	if notification.Worker == nil || notification.Worker.sequence == 0 {
		return
	}
	if notification.Worker.sequence > e.notificationWorkerFloor[notification.SubscriptionID] {
		e.notificationWorkerFloor[notification.SubscriptionID] = notification.Worker.sequence
	}
}
