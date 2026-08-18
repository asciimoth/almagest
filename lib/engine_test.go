package almagest

import (
	"context"
	"crypto/tls"
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
