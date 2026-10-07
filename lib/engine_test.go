package almagest

import (
	"context"
	"crypto/tls"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/formular"
	"github.com/asciimoth/gonnect"
	"github.com/asciimoth/gonnect/dns"
	sysnetdebug "github.com/asciimoth/gonnect/sysnet/debug"
	"github.com/asciimoth/pasta/pasta"

	"github.com/asciimoth/almagest/lib/logging"
	"github.com/asciimoth/almagest/lib/storage"
)

func TestEngineRejectsUnknownWorkspaceAcrossAPI(t *testing.T) {
	engineAPI, err := BuildEngine(
		context.Background(), configer.NewMemory(nil), logging.NewDebugLogFactory(1),
		newEngineTestDebugSystem(), nil, nil, nil, nil,
		[]pasta.NodeClass{engineBatchNodeClass{}}, nil, false,
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = engineAPI.Close() })

	const missing = "missing"
	engineAPI.RemoveNode(missing, 1)
	engineAPI.RemoveNodes(missing, []uint64{1})
	engineAPI.RemoveNodesByClass(missing, "test/EngineBatchNode")
	engineAPI.RemoveLink(missing, 1)
	if _, ok := engineAPI.WorkspaceSnapshot(missing); ok {
		t.Fatal("WorkspaceSnapshot accepted an unknown workspace")
	}
	if _, ok := engineAPI.NodeClassSnapshot(missing, "test/EngineBatchNode"); ok {
		t.Fatal("NodeClassSnapshot accepted an unknown workspace")
	}
	if _, ok := engineAPI.NodeSnapshot(missing, 1); ok {
		t.Fatal("NodeSnapshot accepted an unknown workspace")
	}
	if _, ok := engineAPI.PortSnapshot(missing, 1); ok {
		t.Fatal("PortSnapshot accepted an unknown workspace")
	}
	if _, ok := engineAPI.LinkSnapshot(missing, 1); ok {
		t.Fatal("LinkSnapshot accepted an unknown workspace")
	}
	if _, ok := engineAPI.NodeClassLongDescription(missing, "test/EngineBatchNode"); ok {
		t.Fatal("NodeClassLongDescription accepted an unknown workspace")
	}
	if _, err := engineAPI.AddNode(missing, "test/EngineBatchNode", "node"); !errors.Is(err, ErrNoWorkspace) {
		t.Fatalf("AddNode error = %v, want ErrNoWorkspace", err)
	}
	if err := engineAPI.ReplaceNodeWithPlaceholder(missing, 1); !errors.Is(err, ErrNoWorkspace) {
		t.Fatalf("ReplaceNodeWithPlaceholder error = %v, want ErrNoWorkspace", err)
	}
	if _, _, err := engineAPI.AddLink(missing, 1, 2); !errors.Is(err, ErrNoWorkspace) {
		t.Fatalf("AddLink error = %v, want ErrNoWorkspace", err)
	}
	if _, ok := engineAPI.NodeByName(missing, "node"); ok {
		t.Fatal("NodeByName accepted an unknown workspace")
	}
	if engineAPI.PortsConnected(missing, 1, 2) || engineAPI.NodesConnected(missing, 1, 2) {
		t.Fatal("connection query accepted an unknown workspace")
	}
	if _, _, ok := engineAPI.LinkByPorts(missing, 1, 2); ok {
		t.Fatal("LinkByPorts accepted an unknown workspace")
	}
	if got := engineAPI.LinksByNodes(missing, 1, 2); len(got) != 0 {
		t.Fatalf("LinksByNodes = %#v, want empty", got)
	}
	engineAPI.SetNodePosition(missing, 1, "1 2")
	engineAPI.MoveNodes(missing, map[uint64]string{1: "1 2"})
	engineAPI.SetNodeName(missing, 1, "node")
	engineAPI.RemoveNodePopup(missing, 1, 1)
	engineAPI.RemoveNodePopups(missing, 1)
	engineAPI.RemoveNodePopupsByType(missing, 1, "test")
	if got := engineAPI.Copy(missing, []uint64{1}); got != "" {
		t.Fatalf("Copy = %q, want empty", got)
	}
	if got := engineAPI.Paste(missing, "{}"); len(got) != 0 {
		t.Fatalf("Paste = %#v, want empty", got)
	}
	engineAPI.SendInbox(missing, pasta.InboxMessage{})
	if err := engineAPI.Trigger(missing, 1); !errors.Is(err, ErrNoWorkspace) {
		t.Fatalf("Trigger error = %v, want ErrNoWorkspace", err)
	}
	engineAPI.Undo(missing)
	engineAPI.Redo(missing)
	engineAPI.SendNodeFormularMsg(missing, 1, nil)

	engineAPI.Lock()
	defer engineAPI.Unlock()
	_ = engineAPI.PoolLocked()
	_ = engineAPI.AllocIPLocked()
	_ = engineAPI.AllocSubnetLocked()
	engineAPI.RemoveNodeLocked(missing, 1)
	engineAPI.RemoveNodesLocked(missing, []uint64{1})
	engineAPI.RemoveNodesByClassLocked(missing, "test/EngineBatchNode")
	engineAPI.RemoveLinkLocked(missing, 1)
	_, _ = engineAPI.WorkspaceSnapshotLocked(missing)
	_, _ = engineAPI.NodeClassSnapshotLocked(missing, "test/EngineBatchNode")
	_, _ = engineAPI.NodeSnapshotLocked(missing, 1)
	_, _ = engineAPI.PortSnapshotLocked(missing, 1)
	_, _ = engineAPI.LinkSnapshotLocked(missing, 1)
	_, _ = engineAPI.NodeClassLongDescriptionLocked(missing, "test/EngineBatchNode")
	_, _ = engineAPI.AddNodeLocked(missing, "test/EngineBatchNode", "node")
	_ = engineAPI.ReplaceNodeWithPlaceholderLocked(missing, 1)
	_, _, _ = engineAPI.AddLinkLocked(missing, 1, 2)
	_, _ = engineAPI.NodeByNameLocked(missing, "node")
	_ = engineAPI.PortsConnectedLocked(missing, 1, 2)
	_ = engineAPI.NodesConnectedLocked(missing, 1, 2)
	_, _, _ = engineAPI.LinkByPortsLocked(missing, 1, 2)
	_ = engineAPI.LinksByNodesLocked(missing, 1, 2)
	engineAPI.SetNodePositionLocked(missing, 1, "1 2")
	engineAPI.MoveNodesLocked(missing, map[uint64]string{1: "1 2"})
	engineAPI.SetNodeNameLocked(missing, 1, "node")
	engineAPI.RemoveNodePopupLocked(missing, 1, 1)
	engineAPI.RemoveNodePopupsLocked(missing, 1)
	engineAPI.RemoveNodePopupsByTypeLocked(missing, 1, "test")
	_ = engineAPI.CopyLocked(missing, []uint64{1})
	_ = engineAPI.PasteLocked(missing, "{}")
	engineAPI.SendInboxLocked(missing, pasta.InboxMessage{})
	_ = engineAPI.TriggerLocked(missing, 1)
	engineAPI.UndoLocked(missing)
	engineAPI.RedoLocked(missing)
	engineAPI.SendNodeFormularMsgLocked(missing, 1, nil)
}

func TestEngineLockedWorkspaceAPI(t *testing.T) {
	engineAPI, err := BuildEngine(
		context.Background(), configer.NewMemory(nil), logging.NewDebugLogFactory(1),
		newEngineTestDebugSystem(), nil, nil, nil, nil,
		[]pasta.NodeClass{engineBatchNodeClass{}}, nil, false,
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = engineAPI.Close() })
	implementation := engineAPI.(*engine)
	implementation.mainWorkspace.Lock()
	defer implementation.mainWorkspace.Unlock()

	id, err := engineAPI.AddNodeLocked(MainWorkspace, "test/EngineBatchNode", "node")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := engineAPI.WorkspaceSnapshotLocked(MainWorkspace); !ok {
		t.Fatal("WorkspaceSnapshotLocked did not find main")
	}
	if _, ok := engineAPI.NodeClassSnapshotLocked(MainWorkspace, "test/EngineBatchNode"); !ok {
		t.Fatal("NodeClassSnapshotLocked did not find test class")
	}
	if description, ok := engineAPI.NodeClassLongDescriptionLocked(MainWorkspace, "test/EngineBatchNode"); !ok || description == "" {
		t.Fatalf("NodeClassLongDescriptionLocked = %q, %t", description, ok)
	}
	_, ok := engineAPI.NodeSnapshotLocked(MainWorkspace, id)
	if !ok {
		t.Fatalf("NodeSnapshotLocked did not find node %d", id)
	}
	_, _ = engineAPI.PortSnapshotLocked(MainWorkspace, 0)
	if got, ok := engineAPI.NodeByNameLocked(MainWorkspace, "node"); !ok || got != id {
		t.Fatalf("NodeByNameLocked = %d, %t; want %d, true", got, ok, id)
	}
	engineAPI.SetNodePositionLocked(MainWorkspace, id, "1 2")
	engineAPI.MoveNodesLocked(MainWorkspace, map[uint64]string{id: "3 4"})
	engineAPI.SetNodeNameLocked(MainWorkspace, id, "renamed")
	engineAPI.RemoveNodePopupLocked(MainWorkspace, id, 999)
	engineAPI.RemoveNodePopupsLocked(MainWorkspace, id)
	engineAPI.RemoveNodePopupsByTypeLocked(MainWorkspace, id, "test")
	clipboard := engineAPI.CopyLocked(MainWorkspace, []uint64{id})
	if clipboard == "" {
		t.Fatal("CopyLocked returned empty clipboard")
	}
	pasted := engineAPI.PasteLocked(MainWorkspace, clipboard)
	if len(pasted) != 1 {
		t.Fatalf("PasteLocked returned %v", pasted)
	}
	_ = engineAPI.PortsConnectedLocked(MainWorkspace, 0, 0)
	_ = engineAPI.NodesConnectedLocked(MainWorkspace, id, pasted[0])
	_, _, _ = engineAPI.LinkByPortsLocked(MainWorkspace, 0, 0)
	_ = engineAPI.LinksByNodesLocked(MainWorkspace, id, pasted[0])
	_, _ = engineAPI.LinkSnapshotLocked(MainWorkspace, 0)
	engineAPI.SendInboxLocked(MainWorkspace, pasta.InboxMessage{})
	_ = engineAPI.TriggerLocked(MainWorkspace, id)
	engineAPI.SendNodeFormularMsgLocked(MainWorkspace, id, nil)
	if err := engineAPI.SaveConfigLocked(configer.NewMemory(nil)); err != nil {
		t.Fatalf("SaveConfigLocked error = %v", err)
	}
	engineAPI.RemoveLinkLocked(MainWorkspace, 0)
	engineAPI.RemoveNodesByClassLocked(MainWorkspace, "missing")
	engineAPI.RemoveNodesLocked(MainWorkspace, pasted)
	engineAPI.RemoveNodeLocked(MainWorkspace, id)
}

func TestEngineRemoveNodesByClassIsOneBatchMutation(t *testing.T) {
	engineAPI, err := BuildEngine(
		context.Background(), configer.NewMemory(nil), logging.NewDebugLogFactory(1),
		newEngineTestDebugSystem(), nil, nil, nil, nil,
		[]pasta.NodeClass{engineBatchNodeClass{}}, nil, false,
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = engineAPI.Close() })

	ids := make([]uint64, 2)
	for i := range ids {
		ids[i], err = engineAPI.AddNode(MainWorkspace, "test/EngineBatchNode", "")
		if err != nil {
			t.Fatal(err)
		}
	}

	var notificationMu sync.Mutex
	var notifications []pasta.WorkspaceNotification
	notificationReady := make(chan struct{}, 16)
	subscription := engineAPI.SubscribeNotifications(func(notification EngineNotification) {
		if notification.Workspace != nil {
			notificationMu.Lock()
			notifications = append(notifications, *notification.Workspace)
			notificationMu.Unlock()
			select {
			case notificationReady <- struct{}{}:
			default:
			}
		}
	})
	if subscription == 0 {
		t.Fatal("SubscribeNotifications returned 0")
	}
	t.Cleanup(func() { engineAPI.UnsubscribeNotifications(subscription) })
	takeNotifications := func() []pasta.WorkspaceNotification {
		t.Helper()
		notificationMu.Lock()
		defer notificationMu.Unlock()
		result := append([]pasta.WorkspaceNotification(nil), notifications...)
		notifications = nil
		return result
	}
	waitNotification := func() []pasta.WorkspaceNotification {
		t.Helper()
		select {
		case <-notificationReady:
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for workspace notification")
		}
		return takeNotifications()
	}
	for len(notificationReady) > 0 {
		<-notificationReady
	}
	_ = takeNotifications()

	engineAPI.RemoveNodesByClass(MainWorkspace, "test/EngineBatchNode")
	removedNotifications := waitNotification()
	if len(removedNotifications) != 1 || removedNotifications[0].Kind != pasta.NotificationNodesRemoved {
		t.Fatalf("removal notifications = %#v, want one nodes_removed notification", removedNotifications)
	}
	if len(removedNotifications[0].Nodes) != len(ids) {
		t.Fatalf("removed nodes = %#v, want IDs %v", removedNotifications[0].Nodes, ids)
	}
	for _, id := range ids {
		if _, ok := removedNotifications[0].Nodes[id]; !ok {
			t.Fatalf("removed nodes do not contain %d: %#v", id, removedNotifications[0].Nodes)
		}
	}

	engineAPI.Undo(MainWorkspace)
	undoNotifications := waitNotification()
	if len(undoNotifications) != 1 || undoNotifications[0].Kind != pasta.NotificationWorkspaceSnapshot {
		t.Fatalf("undo notifications = %#v, want one workspace snapshot", undoNotifications)
	}
	for _, id := range ids {
		if _, ok := engineAPI.NodeSnapshot(MainWorkspace, id); !ok {
			t.Fatalf("batch undo did not restore node %d", id)
		}
	}
	engineAPI.Redo(MainWorkspace)
	redoNotifications := waitNotification()
	if len(redoNotifications) != 1 || redoNotifications[0].Kind != pasta.NotificationWorkspaceSnapshot {
		t.Fatalf("redo notifications = %#v, want one workspace snapshot", redoNotifications)
	}
	for _, id := range ids {
		if _, ok := engineAPI.NodeSnapshot(MainWorkspace, id); ok {
			t.Fatalf("batch redo did not remove node %d", id)
		}
	}
}

func TestEngineSendInboxesAndWaitWaitsForConcurrentWorkspaceDelivery(t *testing.T) {
	received := make(chan int, 2)
	engineAPI, err := BuildEngine(
		context.Background(), configer.NewMemory(nil), logging.NewDebugLogFactory(1),
		newEngineTestDebugSystem(), nil, nil, nil, nil,
		[]pasta.NodeClass{engineInboxNodeClass{received: received}}, nil, false,
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = engineAPI.Close() })
	node, err := engineAPI.AddNode(MainWorkspace, "test/EngineInboxNode", "inbox")
	if err != nil {
		t.Fatal(err)
	}

	implementation := engineAPI.(*engine)
	workspaceBusy := make(chan struct{})
	releaseWorkspace := make(chan struct{})
	busyDone := make(chan struct{})
	go func() {
		implementation.mainWorkspace.AddPendingOp(func() {
			close(workspaceBusy)
			<-releaseWorkspace
		})
		close(busyDone)
	}()
	<-workspaceBusy

	deliveryDone := make(chan bool, 1)
	go func() {
		deliveryDone <- engineAPI.SendInboxesAndWait(MainWorkspace, []pasta.InboxMessage{
			{ReceiverNode: node, Payload: 1},
			{ReceiverNode: node, Payload: 2},
		})
	}()
	select {
	case ok := <-deliveryDone:
		t.Fatalf("SendInboxesAndWait returned %v before the workspace could deliver messages", ok)
	case <-time.After(25 * time.Millisecond):
	}
	select {
	case value := <-received:
		t.Fatalf("node received %d while the workspace was blocked", value)
	default:
	}

	close(releaseWorkspace)
	assertNoDeadlock(t, busyDone, "blocked workspace operation")
	select {
	case ok := <-deliveryDone:
		if !ok {
			t.Fatal("SendInboxesAndWait returned false for the main workspace")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("SendInboxesAndWait did not return after inbox delivery")
	}
	for want := 1; want <= 2; want++ {
		select {
		case got := <-received:
			if got != want {
				t.Fatalf("inbox delivery order = %d, want %d", got, want)
			}
		default:
			t.Fatalf("missing delivered inbox value %d", want)
		}
	}
	if engineAPI.SendInboxesAndWait("missing", []pasta.InboxMessage{{ReceiverNode: node}}) {
		t.Fatal("SendInboxesAndWait accepted an unknown workspace")
	}
}

func TestEngineSendNodeFormularMsgAndWaitWaitsForConcurrentWorkspaceDelivery(t *testing.T) {
	received := make(chan any, 1)
	engineAPI, err := BuildEngine(
		context.Background(), configer.NewMemory(nil), logging.NewDebugLogFactory(1),
		newEngineTestDebugSystem(), nil, nil, nil, nil,
		[]pasta.NodeClass{engineInboxNodeClass{formularReceived: received}}, nil, false,
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = engineAPI.Close() })
	node, err := engineAPI.AddNode(MainWorkspace, "test/EngineInboxNode", "formular")
	if err != nil {
		t.Fatal(err)
	}

	implementation := engineAPI.(*engine)
	workspaceBusy := make(chan struct{})
	releaseWorkspace := make(chan struct{})
	busyDone := make(chan struct{})
	go func() {
		implementation.mainWorkspace.AddPendingOp(func() {
			close(workspaceBusy)
			<-releaseWorkspace
		})
		close(busyDone)
	}()
	<-workspaceBusy

	deliveryDone := make(chan bool, 1)
	want := "form value"
	go func() {
		deliveryDone <- engineAPI.SendNodeFormularMsgAndWait(MainWorkspace, node, want)
	}()
	select {
	case ok := <-deliveryDone:
		t.Fatalf("SendNodeFormularMsgAndWait returned %v before delivery", ok)
	case <-time.After(25 * time.Millisecond):
	}

	close(releaseWorkspace)
	assertNoDeadlock(t, busyDone, "blocked workspace operation")
	select {
	case ok := <-deliveryDone:
		if !ok {
			t.Fatal("SendNodeFormularMsgAndWait returned false for the main workspace")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("SendNodeFormularMsgAndWait did not return after delivery")
	}
	select {
	case got := <-received:
		if got != want {
			t.Fatalf("form message = %#v, want %#v", got, want)
		}
	default:
		t.Fatal("form message was not delivered")
	}
	if engineAPI.SendNodeFormularMsgAndWait("missing", node, nil) {
		t.Fatal("SendNodeFormularMsgAndWait accepted an unknown workspace")
	}
}

func TestEngineWorkerAndSubscriptionEdgeAPI(t *testing.T) {
	engineAPI, err := BuildEngine(
		context.Background(), configer.NewMemory(nil), logging.NewDebugLogFactory(1),
		newEngineTestDebugSystem(), nil, nil, nil, nil, nil, nil, false,
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = engineAPI.Close() })
	if _, ok := engineAPI.WorkersSnapshot("missing"); ok {
		t.Fatal("WorkersSnapshot accepted an unknown workspace")
	}
	if snapshot, ok := engineAPI.WorkersSnapshot(MainWorkspace); !ok || snapshot == nil {
		t.Fatalf("WorkersSnapshot = %#v, %t", snapshot, ok)
	}
	if snapshot, ok := engineAPI.WorkersSnapshotLocked(MainWorkspace); !ok || snapshot == nil {
		t.Fatalf("WorkersSnapshotLocked = %#v, %t", snapshot, ok)
	}
	workerID := engineAPI.SubscribeWorkerNotifications(func(WorkerNotification) {})
	if workerID == 0 {
		t.Fatal("SubscribeWorkerNotifications returned zero")
	}
	engineAPI.RequestWorkerSnapshot(workerID)
	engineAPI.UnsubscribeWorkerNotificationsLocked(workerID)
	workerID = engineAPI.SubscribeWorkerNotificationsLocked(func(WorkerNotification) {})
	engineAPI.RequestWorkerSnapshotLocked(workerID)
	engineAPI.UnsubscribeWorkerNotifications(workerID)
	engineAPI.RequestWorkerSnapshot(999)
	engineAPI.RequestFullSnapshot(999)
	engineAPI.UnsubscribeNotifications(999)
	engineAPI.SubscribeNodeMenu(MainWorkspace, 1, 999)
	engineAPI.UnsubscribeNodeMenu(MainWorkspace, 1, 999)
	engineAPI.SubscribeNodeMenu("missing", 1, 999)
	engineAPI.UnsubscribeNodeMenu("missing", 1, 999)
	engineAPI.Lock()
	engineAPI.RequestFullSnapshotLocked(999)
	engineAPI.UnsubscribeNotificationsLocked(999)
	engineAPI.SubscribeNodeMenuLocked("missing", 1, 999)
	engineAPI.SubscribeNodeMenuLocked(MainWorkspace, 1, 999)
	engineAPI.UnsubscribeNodeMenuLocked("missing", 1, 999)
	engineAPI.UnsubscribeNodeMenuLocked(MainWorkspace, 1, 999)
	engineAPI.Unlock()
}

func TestBuiltInNodeClassMetadata(t *testing.T) {
	classes := packageNodeClasses(&engine{}, true)
	if len(classes) == 0 {
		t.Fatal("packageNodeClasses returned no classes")
	}
	for _, class := range classes {
		if class.ClassName() == "" {
			t.Errorf("%T has an empty class name", class)
		}
		if class.ShortDescription() == "" {
			t.Errorf("%T has an empty short description", class)
		}
		if class.LongDescription() == "" {
			t.Errorf("%T has an empty long description", class)
		}
		_ = class.DefaultNodeParams()
		factory, ok := class.(pasta.NodeClassFactory)
		if !ok {
			t.Errorf("%T does not implement NodeClassFactory", class)
			continue
		}
		if node, err := factory.NewNode(nil); err != nil || node == nil {
			t.Errorf("%T NewNode(nil) = %T, %v", class, node, err)
		}
	}
}

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

func TestEngineMenuSubscriptionDuringTransactionFinisherGetsForcedSnapshot(t *testing.T) {
	engineAPI, err := BuildEngine(
		context.Background(),
		configer.NewMemory(nil),
		logging.NewDebugLogFactory(1),
		newEngineTestDebugSystem(),
		nil,
		nil,
		nil,
		nil,
		[]pasta.NodeClass{engineMenuNodeClass{}},
		nil,
		false,
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = engineAPI.Close() })
	node, err := engineAPI.AddNode(MainWorkspace, engineMenuNodeClass{}.ClassName(), "menu")
	if err != nil {
		t.Fatal(err)
	}

	menu := make(chan EngineNotification, 1)
	subscription := engineAPI.SubscribeNotifications(func(notification EngineNotification) {
		if notification.Workspace != nil && notification.Workspace.Kind == pasta.NotificationNodeMenu {
			menu <- notification
		}
	})
	if subscription == 0 {
		t.Fatal("SubscribeNotifications returned 0")
	}
	t.Cleanup(func() { engineAPI.UnsubscribeNotifications(subscription) })

	engineImpl := engineAPI.(*engine)
	engineImpl.notificationMu.Lock()
	engineImpl.notificationFinishing = true
	engineImpl.notificationMu.Unlock()
	defer func() {
		engineImpl.notificationMu.Lock()
		engineImpl.notificationFinishing = false
		engineImpl.notificationMu.Unlock()
	}()

	engineAPI.SubscribeNodeMenu(MainWorkspace, node, subscription)
	select {
	case notification := <-menu:
		if notification.Workspace.ID != node || notification.Workspace.Formular == nil {
			t.Fatalf("menu notification = %#v", notification)
		}
	case <-time.After(time.Second):
		t.Fatal("menu snapshot was dropped while transaction publication was finishing")
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
	spawned := make(chan struct{})
	var publishedOnce sync.Once
	var spawnedOnce sync.Once
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
		if awaitPublication && notification.Worker.Kind == WorkerNotificationSpawned {
			spawnedOnce.Do(func() { close(spawned) })
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
	assertNoDeadlock(t, spawned, "queued worker notification publication")

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

type engineMenuNodeClass struct{}

func (engineMenuNodeClass) ClassName() string { return "test/EngineMenuNode" }
func (engineMenuNodeClass) ShortDescription() string {
	return "Engine menu notification test"
}
func (engineMenuNodeClass) LongDescription() string {
	return "Tests engine menu delivery during notification transactions."
}
func (engineMenuNodeClass) DefaultNodeParams() pasta.NodeClassParams {
	return pasta.NodeClassParams{}
}
func (engineMenuNodeClass) NewNode(configer.Config, ...*pasta.NodeClassState) (pasta.Node, error) {
	return &engineMenuNode{}, nil
}

type engineMenuNode struct{ pasta.BasicNode }

func (*engineMenuNode) OnInit(
	w *pasta.Workspace,
	_ pasta.Logger,
	id uint64,
	_ string,
	_ *pasta.NodeInitData,
	_, _, _, _ bool,
) error {
	w.SendNodeMenuMsgLocked(id, formular.MenuSnapshot(pasta.NodeMenuID(id), 1))
	return nil
}

type engineInboxNodeClass struct {
	received         chan<- int
	formularReceived chan<- any
}

func (engineInboxNodeClass) ClassName() string { return "test/EngineInboxNode" }
func (engineInboxNodeClass) ShortDescription() string {
	return "Engine inbox node test"
}
func (engineInboxNodeClass) LongDescription() string {
	return "Tests ordered inbox delivery barriers."
}
func (engineInboxNodeClass) DefaultNodeParams() pasta.NodeClassParams {
	return pasta.NodeClassParams{}
}
func (c engineInboxNodeClass) NewNode(configer.Config, ...*pasta.NodeClassState) (pasta.Node, error) {
	return &engineInboxNode{received: c.received, formularReceived: c.formularReceived}, nil
}

type engineInboxNode struct {
	pasta.BasicNode
	received         chan<- int
	formularReceived chan<- any
}

func (n *engineInboxNode) OnFormularMsg(message any) error {
	if n.formularReceived != nil {
		n.formularReceived <- message
	}
	return nil
}

func (n *engineInboxNode) OnInbox(message pasta.InboxMessage) error {
	if value, ok := message.Payload.(int); ok {
		n.received <- value
	}
	return nil
}

func assertNoDeadlock(t *testing.T, done <-chan struct{}, context string) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("%s deadlocked", context)
	}
}
