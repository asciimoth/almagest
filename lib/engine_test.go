package almagest

import (
	"context"
	"crypto/tls"
	"sync"
	"testing"
	"time"

	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/gonnect"
	"github.com/asciimoth/gonnect/dns"
	sysnetdebug "github.com/asciimoth/gonnect/sysnet/debug"
	"github.com/asciimoth/pasta/pasta"

	"github.com/asciimoth/almagest/lib/logging"
	"github.com/asciimoth/almagest/lib/storage"
)

func newEngineTestDebugSystem() *sysnetdebug.System {
	out := gonnect.NewLoopbackNetwork()
	out.AllowAnyHost = true
	local := gonnect.NewLoopbackNetwork()
	local.AllowAnyHost = true
	return &sysnetdebug.System{OutNetwork: out, LocalNetwork: local}
}

func TestEngineNotificationTransactionPublishesOnlyFinalState(t *testing.T) {
	engineAPI, err := BuildEngine(
		context.Background(),
		configer.NewMemory(nil),
		logging.NewDebugLogFactory(1),
		newEngineTestDebugSystem(),
		nil,
		nil,
		nil,
		nil,
		[]pasta.NodeClass{engineBatchNodeClass{}},
		nil,
		false,
	)
	if err != nil {
		t.Fatalf("BuildEngine error = %v, want nil", err)
	}
	t.Cleanup(func() {
		if err := engineAPI.Close(); err != nil {
			t.Errorf("engine.Close error = %v, want nil", err)
		}
	})

	var mu sync.Mutex
	var notifications []EngineNotification
	published := make(chan struct{})
	var publishedOnce sync.Once
	awaitPublication := false
	subscription := engineAPI.SubscribeNotifications(func(notification EngineNotification) {
		mu.Lock()
		notifications = append(notifications, notification)
		if awaitPublication && notification.Worker != nil && notification.Worker.Kind == WorkerNotificationSnapshot {
			publishedOnce.Do(func() { close(published) })
		}
		mu.Unlock()
	})
	if subscription == 0 {
		t.Fatal("SubscribeNotifications returned 0")
	}
	t.Cleanup(func() { engineAPI.UnsubscribeNotifications(subscription) })
	mu.Lock()
	notifications = nil
	mu.Unlock()

	outer := engineAPI.BeginNotificationTransaction()
	inner := engineAPI.BeginNotificationTransaction()
	first, err := engineAPI.AddNode(MainWorkspace, "test/EngineBatchNode", "temporary")
	if err != nil {
		t.Fatal(err)
	}
	engineAPI.SetNodeName(MainWorkspace, first, "renamed")
	engineAPI.MoveNodes(MainWorkspace, map[uint64]string{first: "10 20"})
	engineAPI.RemoveNode(MainWorkspace, first)
	finalID, err := engineAPI.AddNode(MainWorkspace, "test/EngineBatchNode", "restored")
	if err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	if len(notifications) != 0 {
		t.Fatalf("notifications during transaction = %#v, want none", notifications)
	}
	mu.Unlock()
	outer.Publish()
	mu.Lock()
	if len(notifications) != 0 {
		t.Fatalf("notifications before outermost publish = %#v, want none", notifications)
	}
	awaitPublication = true
	mu.Unlock()

	inner.Publish()
	inner.Publish() // Publish is intentionally idempotent.
	assertNoDeadlock(t, published, "notification transaction publication")
	mu.Lock()
	got := append([]EngineNotification(nil), notifications...)
	mu.Unlock()
	if len(got) != 2 {
		t.Fatalf("published notifications = %#v, want workspace and worker snapshots", got)
	}
	if got[0].Kind != NotificationWorkspace || got[0].Workspace == nil ||
		got[0].Workspace.Kind != pasta.NotificationWorkspaceSnapshot || got[0].Workspace.Snapshot == nil {
		t.Fatalf("first published notification = %#v, want full workspace snapshot", got[0])
	}
	if _, present := got[0].Workspace.Snapshot.Nodes[first]; present {
		t.Fatalf("published workspace contains removed temporary node %d", first)
	}
	finalNode, present := got[0].Workspace.Snapshot.Nodes[finalID]
	if !present || finalNode.Name != "restored" {
		t.Fatalf("published final node = %#v, present=%t", finalNode, present)
	}
	if got[1].Kind != NotificationWorker || got[1].Worker == nil ||
		got[1].Worker.Kind != WorkerNotificationSnapshot {
		t.Fatalf("second published notification = %#v, want full worker snapshot", got[1])
	}
}

func TestEngineNotificationTransactionInitializesNewSubscriberAtomically(t *testing.T) {
	engineAPI, err := BuildEngine(
		context.Background(),
		configer.NewMemory(nil),
		logging.NewDebugLogFactory(1),
		newEngineTestDebugSystem(),
		nil,
		nil,
		nil,
		nil,
		[]pasta.NodeClass{engineBatchNodeClass{}},
		nil,
		false,
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = engineAPI.Close() })

	transaction := engineAPI.BeginNotificationTransaction()
	published := make(chan struct{})
	var notifications []EngineNotification
	subscription := engineAPI.SubscribeNotifications(func(notification EngineNotification) {
		notifications = append(notifications, notification)
		if notification.Worker != nil && notification.Worker.Kind == WorkerNotificationSnapshot {
			select {
			case <-published:
			default:
				close(published)
			}
		}
	})
	if subscription == 0 {
		t.Fatal("SubscribeNotifications returned 0")
	}
	t.Cleanup(func() { engineAPI.UnsubscribeNotifications(subscription) })
	if len(notifications) != 1 || notifications[0].Kind != NotificationColorMap {
		t.Fatalf("notifications before publish = %#v, want only colormap", notifications)
	}
	if _, err := engineAPI.AddNode(MainWorkspace, "test/EngineBatchNode", "final"); err != nil {
		t.Fatal(err)
	}
	transaction.Publish()
	assertNoDeadlock(t, published, "new subscriber transaction publication")

	if len(notifications) != 3 {
		t.Fatalf("notifications after publish = %#v, want colormap and two snapshots", notifications)
	}
	if notifications[1].Workspace == nil ||
		notifications[1].Workspace.Kind != pasta.NotificationWorkspaceSnapshot {
		t.Fatalf("workspace publication = %#v, want full snapshot", notifications[1])
	}
	if notifications[2].Worker == nil || notifications[2].Worker.Kind != WorkerNotificationSnapshot {
		t.Fatalf("worker publication = %#v, want full snapshot", notifications[2])
	}
}

func TestEngineNotificationTransactionDrainsDelayedWorkspaceEvents(t *testing.T) {
	engineAPI, err := BuildEngine(
		context.Background(),
		configer.NewMemory(nil),
		logging.NewDebugLogFactory(1),
		newEngineTestDebugSystem(),
		nil,
		nil,
		nil,
		nil,
		[]pasta.NodeClass{engineBatchNodeClass{}},
		nil,
		false,
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = engineAPI.Close() })

	nodeID, err := engineAPI.AddNode(MainWorkspace, "test/EngineBatchNode", "old")
	if err != nil {
		t.Fatal(err)
	}
	callbackEntered := make(chan struct{})
	releaseCallback := make(chan struct{})
	blockingID := engineAPI.SubscribeNotifications(func(notification EngineNotification) {
		if notification.Workspace == nil || notification.Workspace.Kind != pasta.NotificationNodeUpdated ||
			notification.Workspace.ID != nodeID {
			return
		}
		select {
		case <-callbackEntered:
			return
		default:
			close(callbackEntered)
			<-releaseCallback
		}
	})
	if blockingID == 0 {
		t.Fatal("blocking subscription failed")
	}
	t.Cleanup(func() { engineAPI.UnsubscribeNotifications(blockingID) })

	var mu sync.Mutex
	var notifications []EngineNotification
	finalSnapshot := make(chan struct{})
	var finalSnapshotOnce sync.Once
	awaitFinalSnapshot := false
	collectorID := engineAPI.SubscribeNotifications(func(notification EngineNotification) {
		if notification.Kind == NotificationWorkspace || notification.Kind == NotificationWorker {
			mu.Lock()
			notifications = append(notifications, notification)
			if awaitFinalSnapshot && notification.Worker != nil && notification.Worker.Kind == WorkerNotificationSnapshot {
				finalSnapshotOnce.Do(func() { close(finalSnapshot) })
			}
			mu.Unlock()
		}
	})
	if collectorID == 0 {
		t.Fatal("collector subscription failed")
	}
	t.Cleanup(func() { engineAPI.UnsubscribeNotifications(collectorID) })
	mu.Lock()
	notifications = nil
	awaitFinalSnapshot = true
	mu.Unlock()

	mutationDone := make(chan struct{})
	go func() {
		engineAPI.SetNodePosition(MainWorkspace, nodeID, "1 2")
		close(mutationDone)
	}()
	<-callbackEntered

	transaction := engineAPI.BeginNotificationTransaction()
	mu.Lock()
	notifications = nil
	mu.Unlock()
	engineAPI.SetNodeName(MainWorkspace, nodeID, "transitional")
	engineAPI.RemoveNode(MainWorkspace, nodeID)
	finalID, err := engineAPI.AddNode(MainWorkspace, "test/EngineBatchNode", "final")
	if err != nil {
		t.Fatal(err)
	}
	transaction.Publish()
	select {
	case <-finalSnapshot:
		close(releaseCallback)
		<-mutationDone
		t.Fatal("final snapshot arrived before an older workspace callback drained")
	case <-time.After(20 * time.Millisecond):
	}
	close(releaseCallback)
	<-mutationDone
	assertNoDeadlock(t, finalSnapshot, "notification transaction publication")

	mu.Lock()
	got := append([]EngineNotification(nil), notifications...)
	mu.Unlock()
	if len(got) != 2 {
		t.Fatalf("notifications after delayed transition = %#v, want two full snapshots", got)
	}
	if got[0].Workspace == nil || got[0].Workspace.Kind != pasta.NotificationWorkspaceSnapshot ||
		got[0].Workspace.Snapshot == nil {
		t.Fatalf("workspace notification = %#v, want full snapshot", got[0])
	}
	if _, present := got[0].Workspace.Snapshot.Nodes[nodeID]; present {
		t.Fatalf("final snapshot contains removed node %d", nodeID)
	}
	if finalNode, present := got[0].Workspace.Snapshot.Nodes[finalID]; !present || finalNode.Name != "final" {
		t.Fatalf("final snapshot node = %#v, present=%t", finalNode, present)
	}
}

func TestEngineNotificationTransactionRejectsDelayedWorkerEvent(t *testing.T) {
	engineAPI, err := BuildEngine(
		context.Background(),
		configer.NewMemory(nil),
		logging.NewDebugLogFactory(1),
		newEngineTestDebugSystem(),
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		false,
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = engineAPI.Close() })
	engineImpl := engineAPI.(*engine)

	var mu sync.Mutex
	var workers []WorkerNotificationKind
	published := make(chan struct{})
	var publishedOnce sync.Once
	awaitPublication := false
	subscription := engineAPI.SubscribeNotifications(func(notification EngineNotification) {
		if notification.Worker == nil {
			return
		}
		mu.Lock()
		workers = append(workers, notification.Worker.Kind)
		if awaitPublication && notification.Worker.Kind == WorkerNotificationSnapshot {
			publishedOnce.Do(func() { close(published) })
		}
		mu.Unlock()
	})
	if subscription == 0 {
		t.Fatal("SubscribeNotifications returned 0")
	}
	t.Cleanup(func() { engineAPI.UnsubscribeNotifications(subscription) })
	mu.Lock()
	workers = nil
	awaitPublication = true
	mu.Unlock()

	engineImpl.workers.mu.Lock()
	engineImpl.workers.notificationSequence = 10
	engineImpl.workers.mu.Unlock()
	transaction := engineAPI.BeginNotificationTransaction()
	transaction.Publish()
	assertNoDeadlock(t, published, "worker snapshot publication")

	engineImpl.publishEngineNotification(EngineNotification{
		SubscriptionID: subscription,
		Kind:           NotificationWorker,
		WorkspaceID:    MainWorkspace,
		Worker: &WorkerNotification{
			Kind:     WorkerNotificationStopped,
			sequence: 10,
		},
	})
	engineImpl.publishEngineNotification(EngineNotification{
		SubscriptionID: subscription,
		Kind:           NotificationWorker,
		WorkspaceID:    MainWorkspace,
		Worker: &WorkerNotification{
			Kind:     WorkerNotificationSpawned,
			sequence: 11,
		},
	})

	mu.Lock()
	got := append([]WorkerNotificationKind(nil), workers...)
	mu.Unlock()
	if len(got) != 2 || got[0] != WorkerNotificationSnapshot || got[1] != WorkerNotificationSpawned {
		t.Fatalf("worker notifications = %#v, want snapshot then new spawn", got)
	}
}

func TestEngineNotificationTransactionAllowsUnsubscribeDuringBarrier(t *testing.T) {
	engineAPI, err := BuildEngine(
		context.Background(),
		configer.NewMemory(nil),
		logging.NewDebugLogFactory(1),
		newEngineTestDebugSystem(),
		nil,
		nil,
		nil,
		nil,
		[]pasta.NodeClass{engineBatchNodeClass{}},
		nil,
		false,
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = engineAPI.Close() })
	engineImpl := engineAPI.(*engine)
	nodeID, err := engineAPI.AddNode(MainWorkspace, "test/EngineBatchNode", "node")
	if err != nil {
		t.Fatal(err)
	}

	callbackEntered := make(chan struct{})
	releaseCallback := make(chan struct{})
	published := make(chan struct{})
	var publishedOnce sync.Once
	awaitPublication := false
	blockingID := engineAPI.SubscribeNotifications(func(notification EngineNotification) {
		if notification.Workspace != nil && notification.Workspace.Kind == pasta.NotificationNodeUpdated &&
			notification.Workspace.ID == nodeID {
			select {
			case <-callbackEntered:
			default:
				close(callbackEntered)
				<-releaseCallback
			}
		}
		if awaitPublication && notification.Worker != nil && notification.Worker.Kind == WorkerNotificationSnapshot {
			publishedOnce.Do(func() { close(published) })
		}
	})
	if blockingID == 0 {
		t.Fatal("blocking subscription failed")
	}
	t.Cleanup(func() { engineAPI.UnsubscribeNotifications(blockingID) })

	unsubscribedID := engineAPI.SubscribeNotifications(func(EngineNotification) {})
	if unsubscribedID == 0 {
		t.Fatal("second subscription failed")
	}
	mutationDone := make(chan struct{})
	go func() {
		engineAPI.SetNodePosition(MainWorkspace, nodeID, "1 2")
		close(mutationDone)
	}()
	<-callbackEntered

	transaction := engineAPI.BeginNotificationTransaction()
	engineAPI.SetNodeName(MainWorkspace, nodeID, "final")
	awaitPublication = true
	transaction.Publish()

	deadline := time.Now().Add(2 * time.Second)
	for {
		engineImpl.notificationMu.Lock()
		_, pending := engineImpl.workspaceBarrierPending[unsubscribedID]
		engineImpl.notificationMu.Unlock()
		if pending {
			break
		}
		if time.Now().After(deadline) {
			close(releaseCallback)
			<-mutationDone
			t.Fatal("subscriber did not enter the workspace barrier")
		}
		time.Sleep(time.Millisecond)
	}

	engineAPI.UnsubscribeNotifications(unsubscribedID)
	close(releaseCallback)
	<-mutationDone
	assertNoDeadlock(t, published, "publication after barrier unsubscribe")
}

func TestEngineUnsubscribeNotificationsRemovesSubscriptionMapping(t *testing.T) {
	engineAPI, err := BuildEngine(
		context.Background(),
		configer.NewMemory(nil),
		logging.NewDebugLogFactory(1),
		newEngineTestDebugSystem(),
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		true,
	)

	if err != nil {
		t.Fatalf("BuildEngine error = %v, want nil", err)
	}
	t.Cleanup(func() {
		if err := engineAPI.Close(); err != nil {
			t.Errorf("engine.Close error = %v, want nil", err)
		}
	})

	engineImpl := engineAPI.(*engine)
	for range 10 {
		id := engineAPI.SubscribeNotifications(func(EngineNotification) {})
		if id == 0 {
			t.Fatal("SubscribeNotifications returned 0")
		}
		if got := len(engineImpl.subscriptionIDMapping); got != 1 {
			t.Fatalf("subscription mapping count after subscribe = %d, want 1", got)
		}

		engineAPI.UnsubscribeNotifications(id)
		if got := len(engineImpl.subscriptionIDMapping); got != 0 {
			t.Fatalf("subscription mapping count after unsubscribe = %d, want 0", got)
		}
	}
}

func TestEngineSubscribeNotificationsCallbackCanReenterEngine(t *testing.T) {
	engineAPI, err := BuildEngine(
		context.Background(),
		configer.NewMemory(nil),
		logging.NewDebugLogFactory(1),
		newEngineTestDebugSystem(),
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		true,
	)

	if err != nil {
		t.Fatalf("BuildEngine error = %v, want nil", err)
	}
	t.Cleanup(func() {
		if err := engineAPI.Close(); err != nil {
			t.Errorf("engine.Close error = %v, want nil", err)
		}
	})

	done := make(chan struct{})
	go func() {
		id := engineAPI.SubscribeNotifications(func(notification EngineNotification) {
			if notification.Kind == NotificationColorMap {
				engineAPI.RequestFullSnapshot(notification.SubscriptionID)
			}
		})
		if id != 0 {
			engineAPI.UnsubscribeNotifications(id)
		}
		close(done)
	}()

	assertNoDeadlock(t, done, "SubscribeNotifications callback re-entered engine")
}

func TestEngineLockedWorkspaceWrappersFromNodeCallback(t *testing.T) {
	engineAPI, err := BuildEngine(
		context.Background(),
		configer.NewMemory(nil),
		logging.NewDebugLogFactory(1),
		newEngineTestDebugSystem(),
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		true,
	)

	if err != nil {
		t.Fatalf("BuildEngine error = %v, want nil", err)
	}
	t.Cleanup(func() {
		if err := engineAPI.Close(); err != nil {
			t.Errorf("engine.Close error = %v, want nil", err)
		}
	})
	engineImpl := engineAPI.(*engine)
	if err := engineImpl.mainWorkspace.AddNodeClass(engineLockedCallbackClass{engine: engineImpl}); err != nil {
		t.Fatalf("AddNodeClass error = %v, want nil", err)
	}

	done := make(chan error, 1)
	go func() {
		_, err := engineAPI.AddNode(MainWorkspace, "test/EngineLockedCallback", "locked-callback")
		done <- err
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("AddNode error = %v, want nil", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("AddNode deadlocked while node callback used engine Locked wrappers")
	}
}

func TestEngineBatchNodeRemovalAndMoveUseWorkspaceBatchMutations(t *testing.T) {
	engineAPI, err := BuildEngine(
		context.Background(),
		configer.NewMemory(nil),
		logging.NewDebugLogFactory(1),
		newEngineTestDebugSystem(),
		nil,
		nil,
		nil,
		nil,
		[]pasta.NodeClass{engineBatchNodeClass{}},
		nil,
		false,
	)

	if err != nil {
		t.Fatalf("BuildEngine error = %v, want nil", err)
	}
	t.Cleanup(func() {
		if err := engineAPI.Close(); err != nil {
			t.Errorf("engine.Close error = %v, want nil", err)
		}
	})

	first, err := engineAPI.AddNode(MainWorkspace, "test/EngineBatchNode", "first")
	if err != nil {
		t.Fatalf("AddNode first error = %v, want nil", err)
	}
	second, err := engineAPI.AddNode(MainWorkspace, "test/EngineBatchNode", "second")
	if err != nil {
		t.Fatalf("AddNode second error = %v, want nil", err)
	}
	third, err := engineAPI.AddNode(MainWorkspace, "test/EngineBatchNode", "third")
	if err != nil {
		t.Fatalf("AddNode third error = %v, want nil", err)
	}

	var notifications []pasta.WorkspaceNotification
	sub := engineAPI.SubscribeNotifications(func(notification EngineNotification) {
		if notification.Workspace != nil {
			notifications = append(notifications, *notification.Workspace)
		}
	})
	if sub == 0 {
		t.Fatal("SubscribeNotifications returned 0")
	}
	defer engineAPI.UnsubscribeNotifications(sub)
	notifications = nil

	engineAPI.MoveNodes(MainWorkspace, map[uint64]string{
		0:      "ignored",
		999999: "ignored",
		first:  "10 20",
		second: "30 40",
	})
	if len(notifications) != 1 || notifications[0].Kind != pasta.NotificationNodesUpdated {
		t.Fatalf("move notifications = %#v, want one nodes_updated notification", notifications)
	}
	firstSnapshot, ok := engineAPI.NodeSnapshot(MainWorkspace, first)
	if !ok || firstSnapshot.Position != "10 20" {
		t.Fatalf("first snapshot = %#v, %v; want position 10 20", firstSnapshot, ok)
	}
	secondSnapshot, ok := engineAPI.NodeSnapshot(MainWorkspace, second)
	if !ok || secondSnapshot.Position != "30 40" {
		t.Fatalf("second snapshot = %#v, %v; want position 30 40", secondSnapshot, ok)
	}

	notifications = nil
	engineAPI.RemoveNodes(MainWorkspace, []uint64{0, first, second, first, 999999})
	if len(notifications) != 1 || notifications[0].Kind != pasta.NotificationNodesRemoved {
		t.Fatalf("remove notifications = %#v, want one nodes_removed notification", notifications)
	}
	if _, ok := engineAPI.NodeSnapshot(MainWorkspace, first); ok {
		t.Fatal("first node still exists after batch remove")
	}
	if _, ok := engineAPI.NodeSnapshot(MainWorkspace, second); ok {
		t.Fatal("second node still exists after batch remove")
	}
	if _, ok := engineAPI.NodeSnapshot(MainWorkspace, third); !ok {
		t.Fatal("third node should remain after batch remove")
	}
}

func TestEngineNodeNameAvailableCallbackChecksGeneratedNames(t *testing.T) {
	var checked []string
	engineAPI, err := BuildEngine(
		context.Background(),
		configer.NewMemory(nil),
		logging.NewDebugLogFactory(1),
		newEngineTestDebugSystem(),
		nil,
		nil,
		nil,
		nil,
		[]pasta.NodeClass{engineBatchNodeClass{}},
		nil,
		false,
		WithNodeNameAvailableCallback(func(name string) bool {
			checked = append(checked, name)
			return len(checked) > 1
		}),
	)
	if err != nil {
		t.Fatalf("BuildEngine error = %v, want nil", err)
	}
	t.Cleanup(func() {
		if err := engineAPI.Close(); err != nil {
			t.Errorf("engine.Close error = %v, want nil", err)
		}
	})

	id, err := engineAPI.AddNode(MainWorkspace, "test/EngineBatchNode", "")
	if err != nil {
		t.Fatalf("AddNode error = %v, want nil", err)
	}
	if len(checked) < 2 {
		t.Fatalf("checked names = %q, want at least two generated candidates", checked)
	}
	snapshot, ok := engineAPI.NodeSnapshot(MainWorkspace, id)
	if !ok {
		t.Fatal("generated node was not found")
	}
	if snapshot.Name == checked[0] {
		t.Fatalf("generated node name = %q, want rejected candidate to be skipped", snapshot.Name)
	}
}

func TestBuildEngineRandomSeedControlsGeneratedNodeNames(t *testing.T) {
	firstSeed := int64(42)
	secondSeed := int64(43)
	first := generatedEngineNodeName(t, &firstSeed)
	if got := generatedEngineNodeName(t, &firstSeed); got != first {
		t.Fatalf("same seed generated node name %q, want %q", got, first)
	}
	if got := generatedEngineNodeName(t, &secondSeed); got == first {
		t.Fatalf("different seeds generated the same node name %q", got)
	}

	defaultName := generatedEngineNodeName(t, nil)
	if got := generatedEngineNodeName(t, nil); got != defaultName {
		t.Fatalf("default seeds generated node names %q and %q, want equal names", defaultName, got)
	}
}

func generatedEngineNodeName(t *testing.T, seed *int64) string {
	t.Helper()
	checked := 0
	options := []BuildEngineOption{
		WithNodeNameAvailableCallback(func(string) bool {
			checked++
			return checked > 1
		}),
	}
	if seed != nil {
		options = append(options, WithRandomSeed(*seed))
	}
	engineAPI, err := BuildEngine(
		context.Background(),
		configer.NewMemory(nil),
		logging.NewDebugLogFactory(1),
		newEngineTestDebugSystem(),
		nil,
		nil,
		nil,
		nil,
		[]pasta.NodeClass{engineBatchNodeClass{}},
		nil,
		false,
		options...,
	)
	if err != nil {
		t.Fatalf("BuildEngine error = %v, want nil", err)
	}
	t.Cleanup(func() {
		if err := engineAPI.Close(); err != nil {
			t.Errorf("engine.Close error = %v, want nil", err)
		}
	})

	id, err := engineAPI.AddNode(MainWorkspace, "test/EngineBatchNode", "")
	if err != nil {
		t.Fatalf("AddNode error = %v, want nil", err)
	}
	snapshot, ok := engineAPI.NodeSnapshot(MainWorkspace, id)
	if !ok {
		t.Fatal("generated node was not found")
	}
	return snapshot.Name
}

func TestEngineManagersFromBuildEngine(t *testing.T) {
	kv := testKVManager{}
	dnsCache := testDnsCacheManager{}
	secrets := testSecretsStorage{}
	tlsStorage := testTlsConfigStorage{}
	engineAPI, err := BuildEngine(
		context.Background(),
		configer.NewMemory(nil),
		logging.NewDebugLogFactory(1),
		newEngineTestDebugSystem(),
		kv,
		dnsCache,
		secrets,
		tlsStorage,
		nil,
		nil,
		true,
	)
	if err != nil {
		t.Fatalf("BuildEngine error = %v, want nil", err)
	}
	t.Cleanup(func() {
		if err := engineAPI.Close(); err != nil {
			t.Errorf("engine.Close error = %v, want nil", err)
		}
	})

	if got := engineAPI.KVManager(); got != kv {
		t.Fatalf("KVManager() = %#v, want %#v", got, kv)
	}
	if got := engineAPI.DnsCacheManager(); got != dnsCache {
		t.Fatalf("DnsCacheManager() = %#v, want %#v", got, dnsCache)
	}
	if got := engineAPI.SecretsStorage(); got != secrets {
		t.Fatalf("SecretsStorage() = %#v, want %#v", got, secrets)
	}
	if got := engineAPI.TlsConfigStorage(); got != tlsStorage {
		t.Fatalf("TlsConfigStorage() = %#v, want %#v", got, tlsStorage)
	}
}

type testKVManager struct{}

func (testKVManager) DefaultStore() storage.KVStore {
	return nil
}

func (testKVManager) PluginStore(string) storage.KVStore {
	return nil
}

type testDnsCacheManager struct{}

func (testDnsCacheManager) Storage(string) dns.CacheStorage {
	return nil
}

type testSecretsStorage struct{}

func (testSecretsStorage) GetSecret(string, string) (storage.Secret, error) {
	return storage.Secret{}, storage.ErrSecretAccessDenied
}

func (testSecretsStorage) NewSecret(storage.Secret, string) (string, error) {
	return "", nil
}

type testTlsConfigStorage struct{}

func (testTlsConfigStorage) ListConfigs() []string {
	return nil
}

func (testTlsConfigStorage) GetClientConfig(string, string) (tls.Config, error) {
	return tls.Config{}, nil
}

func (testTlsConfigStorage) GetLocalCA(string) (tls.Certificate, error) {
	return tls.Certificate{}, storage.ErrTlsConfigNotFound
}

type engineLockedCallbackClass struct {
	engine *engine
}

func (c engineLockedCallbackClass) ClassName() string {
	return "test/EngineLockedCallback"
}

func (c engineLockedCallbackClass) ShortDescription() string {
	return "Engine locked callback test"
}

func (c engineLockedCallbackClass) LongDescription() string {
	return "Tests that engine Locked workspace wrappers can be called from node callbacks."
}

func (c engineLockedCallbackClass) DefaultNodeParams() pasta.NodeClassParams {
	return pasta.NodeClassParams{}
}

func (c engineLockedCallbackClass) NewNode(configer.Config, ...*pasta.NodeClassState) (pasta.Node, error) {
	return &engineLockedCallbackNode{engine: c.engine}, nil
}

type engineLockedCallbackNode struct {
	pasta.BasicNode

	engine *engine
}

func (n *engineLockedCallbackNode) OnInit(w *pasta.Workspace, _ pasta.Logger, id uint64, _ string, _ *pasta.NodeInitData, _, _, _, _ bool) error {
	if _, ok := n.engine.NodeSnapshotLocked(MainWorkspace, id); !ok {
		return pasta.ErrNoNode
	}
	n.engine.SetNodePositionLocked(MainWorkspace, id, "callback-position")
	return nil
}

type engineBatchNodeClass struct{}

func (engineBatchNodeClass) ClassName() string {
	return "test/EngineBatchNode"
}

func (engineBatchNodeClass) ShortDescription() string {
	return "Engine batch node test"
}

func (engineBatchNodeClass) LongDescription() string {
	return "Tests Engine batch remove and move workspace wrappers."
}

func (engineBatchNodeClass) DefaultNodeParams() pasta.NodeClassParams {
	return pasta.NodeClassParams{}
}

func (engineBatchNodeClass) NewNode(configer.Config, ...*pasta.NodeClassState) (pasta.Node, error) {
	return &engineBatchNode{}, nil
}

type engineBatchNode struct {
	pasta.BasicNode
	_ byte
}

func assertNoDeadlock(t *testing.T, done <-chan struct{}, context string) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("%s deadlocked", context)
	}
}
