package amnesianodes

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/asciimoth/bufpool"
	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/gonnect"
	"github.com/asciimoth/gonnect/sysnet"
	"github.com/asciimoth/gonnect/tun"
	"github.com/asciimoth/pasta/pasta"

	"github.com/asciimoth/almagest/lib/logging"
	"github.com/asciimoth/almagest/lib/nodekit"
	"github.com/asciimoth/almagest/lib/storage"
)

func TestManagedAmnesiaLinksAttachDetachReattach(t *testing.T) {
	spawner := &managedAmnesiaLifecycleSpawner{}
	runtime := managedAmnesiaLifecycleRuntime{
		kv:      managedAmnesiaKVManager{store: newAmnesiaMemoryKV()},
		spawner: spawner,
	}
	class := &recordingManagedAmnesiaClass{base: AmnesiaClass{Runtime: runtime}}
	w, err := pasta.WorkspaceFromConfig(
		[]pasta.NodeClass{class},
		managedAmnesiaLifecycleConfig("amnesia"),
		logging.NewDebugLogFactory(256),
	)
	if err != nil {
		t.Fatalf("build workspace: %v", err)
	}
	spawner.wait()
	t.Cleanup(func() {
		w.Close()
		spawner.wait()
	})
	node := class.onlyNode(t)

	tunProvider := &managedAmnesiaLinkPeer{}
	tunProviderID, err := w.AddNode(tunProvider, "test/AmnesiaTunProvider", "tun provider")
	if err != nil {
		t.Fatalf("add TUN provider: %v", err)
	}
	tunPort, err := w.AddPort(pasta.Port{
		Node: tunProviderID, Direction: "right", Name: "Tun", Types: []string{nodekit.TypeTun},
	})
	if err != nil {
		t.Fatalf("add TUN provider port: %v", err)
	}
	networkProvider := &managedAmnesiaLinkPeer{}
	networkProviderID, err := w.AddNode(networkProvider, "test/AmnesiaNetworkProvider", "network provider")
	if err != nil {
		t.Fatalf("add network provider: %v", err)
	}
	networkPort, err := w.AddPort(pasta.Port{
		Node: networkProviderID, Direction: "left", Name: "Network", Types: []string{nodekit.TypeNetwork},
	})
	if err != nil {
		t.Fatalf("add network provider port: %v", err)
	}

	firstTunLink, _, err := w.AddLink(tunPort, node.tunp)
	if err != nil {
		t.Fatalf("attach TUN link: %v", err)
	}
	firstNetworkLink, _, err := w.AddLink(networkPort, node.netp)
	if err != nil {
		t.Fatalf("attach network link: %v", err)
	}
	firstTun, firstTunPeer := tun.Pipe(1, 1500, 0, 0)
	t.Cleanup(func() {
		_ = firstTun.Close()
		_ = firstTunPeer.Close()
	})
	firstNetwork := gonnect.DetachNetwork(gonnect.NewLoopbackNetwork(), nil, nil)
	w.EmitEvent(tunProviderID, firstTunLink, nodekit.TunPayload{Tun: firstTun})
	w.EmitEvent(networkProviderID, firstNetworkLink, nodekit.NetworkPayload{Network: firstNetwork})
	spawner.wait()
	if node.tunLink != firstTunLink || node.tun != firstTun {
		t.Fatalf("attached TUN = link %d value %p, want link %d value %p", node.tunLink, node.tun, firstTunLink, firstTun)
	}
	if node.netLink != firstNetworkLink || node.network != firstNetwork {
		t.Fatalf("attached network = link %d value %p, want link %d value %p", node.netLink, node.network, firstNetworkLink, firstNetwork)
	}

	w.RemoveLink(firstTunLink)
	w.RemoveLink(firstNetworkLink)
	spawner.wait()
	if node.tunLink != 0 || node.tun != nil || node.netLink != 0 || node.network != nil {
		t.Fatalf(
			"detached inputs = tun_link %d tun %p network_link %d network %p",
			node.tunLink, node.tun, node.netLink, node.network,
		)
	}
	secondTunLink, _, err := w.AddLink(tunPort, node.tunp)
	if err != nil {
		t.Fatalf("reattach TUN link: %v", err)
	}
	secondNetworkLink, _, err := w.AddLink(networkPort, node.netp)
	if err != nil {
		t.Fatalf("reattach network link: %v", err)
	}
	secondTun, secondTunPeer := tun.Pipe(1, 1500, 0, 0)
	t.Cleanup(func() {
		_ = secondTun.Close()
		_ = secondTunPeer.Close()
	})
	secondNetwork := gonnect.DetachNetwork(gonnect.NewLoopbackNetwork(), nil, nil)
	w.EmitEvent(tunProviderID, secondTunLink, nodekit.TunPayload{Tun: secondTun})
	w.EmitEvent(networkProviderID, secondNetworkLink, nodekit.NetworkPayload{Network: secondNetwork})
	spawner.wait()
	if secondTunLink == firstTunLink || secondNetworkLink == firstNetworkLink {
		t.Fatal("reattach reused a removed link ID")
	}
	if node.tunLink != secondTunLink || node.tun != secondTun {
		t.Fatalf("reattached TUN = link %d value %p, want link %d value %p", node.tunLink, node.tun, secondTunLink, secondTun)
	}
	if node.netLink != secondNetworkLink || node.network != secondNetwork {
		t.Fatalf("reattached network = link %d value %p, want link %d value %p", node.netLink, node.network, secondNetworkLink, secondNetwork)
	}
}

func TestManagedAmnesiaSaveRestorePreservesConfigAndInstallationUUID(t *testing.T) {
	manager := managedAmnesiaKVManager{store: newAmnesiaMemoryKV()}
	spawner := &managedAmnesiaLifecycleSpawner{}
	runtime := managedAmnesiaLifecycleRuntime{kv: manager, spawner: spawner}
	class := &recordingManagedAmnesiaClass{base: AmnesiaClass{Runtime: runtime}}
	w, err := pasta.WorkspaceFromConfig(
		[]pasta.NodeClass{class},
		managedAmnesiaLifecycleConfig("saved amnesia"),
		logging.NewDebugLogFactory(256),
	)
	if err != nil {
		t.Fatalf("build workspace: %v", err)
	}
	spawner.wait()
	node := class.onlyNode(t)
	uuid, persistent, err := node.runtimeInstallationUUID(context.Background())
	if err != nil || !persistent || uuid == "" {
		t.Fatalf("initial installation UUID = %q persistent=%t err=%v", uuid, persistent, err)
	}

	saved := configer.NewMemory(nil)
	if err := w.SaveConfig(saved); err != nil {
		t.Fatalf("save workspace: %v", err)
	}
	w.Close()
	spawner.wait()

	restoredClass := &recordingManagedAmnesiaClass{base: AmnesiaClass{Runtime: runtime}}
	restoredWorkspace, err := pasta.WorkspaceFromConfig(
		[]pasta.NodeClass{restoredClass},
		saved,
		logging.NewDebugLogFactory(256),
	)
	if err != nil {
		t.Fatalf("restore workspace: %v", err)
	}
	spawner.wait()
	t.Cleanup(func() {
		restoredWorkspace.Close()
		spawner.wait()
	})
	restored := restoredClass.onlyNode(t)
	if restored.config != node.config {
		t.Fatalf("restored config = %#v, want %#v", restored.config, node.config)
	}
	restoredUUID, restoredPersistent, err := restored.runtimeInstallationUUID(context.Background())
	if err != nil || !restoredPersistent || restoredUUID != uuid {
		t.Fatalf(
			"restored installation UUID = %q persistent=%t err=%v, want %q",
			restoredUUID, restoredPersistent, err, uuid,
		)
	}
	snapshot, ok := restoredWorkspace.NodeSnapshot(restored.ID)
	if !ok || len(snapshot.LeftPorts) != 1 || len(snapshot.RightPorts) != 1 {
		t.Fatalf("restored Amnesia ports = %#v", snapshot)
	}
}

func managedAmnesiaLifecycleConfig(name string) configer.Config {
	return configer.NewMemory(map[string]any{
		name: map[string]any{
			"Class":                     NodeTypeAmnesia,
			fieldEnabled:                false,
			fieldConnectionSecret:       "connection-secret",
			fieldServerCountryCode:      "nl",
			nodekit.TLSConfigFieldID:    "default",
			fieldGatewayURL:             "https://gateway.example.test/",
			fieldGatewayPublicKeySecret: "gateway-key-secret",
		},
	})
}

type recordingManagedAmnesiaClass struct {
	base  AmnesiaClass
	nodes []*managedAmnesiaNode
}

func (c *recordingManagedAmnesiaClass) ClassName() string { return c.base.ClassName() }
func (c *recordingManagedAmnesiaClass) ShortDescription() string {
	return c.base.ShortDescription()
}
func (c *recordingManagedAmnesiaClass) LongDescription() string { return c.base.LongDescription() }
func (c *recordingManagedAmnesiaClass) DefaultNodeParams() pasta.NodeClassParams {
	return c.base.DefaultNodeParams()
}
func (c *recordingManagedAmnesiaClass) NewNode(
	cfg configer.Config,
	previous ...*pasta.NodeClassState,
) (pasta.Node, error) {
	raw, err := c.base.NewNode(cfg, previous...)
	if err != nil {
		return nil, err
	}
	node := raw.(*managedAmnesiaNode)
	c.nodes = append(c.nodes, node)
	return node, nil
}

func (c *recordingManagedAmnesiaClass) onlyNode(t *testing.T) *managedAmnesiaNode {
	t.Helper()
	if len(c.nodes) != 1 {
		t.Fatalf("constructed Amnesia nodes = %d, want 1", len(c.nodes))
	}
	return c.nodes[0]
}

type managedAmnesiaLinkPeer struct {
	pasta.BasicNode
	_ byte
}

func (*managedAmnesiaLinkPeer) PreLinkAdd(uint64, string, string) error { return nil }

type managedAmnesiaKVManager struct{ store *amnesiaMemoryKV }

func (m managedAmnesiaKVManager) DefaultStore() storage.KVStore { return m.store }
func (m managedAmnesiaKVManager) PluginStore(name string) storage.KVStore {
	return m.store.Subtree("plugins", name)
}

type managedAmnesiaLifecycleRuntime struct {
	kv      storage.KVManager
	spawner nodekit.WorkerSpawner
}

func (managedAmnesiaLifecycleRuntime) Context() context.Context { return context.Background() }
func (managedAmnesiaLifecycleRuntime) Pool() bufpool.Pool       { return nil }
func (managedAmnesiaLifecycleRuntime) System() sysnet.System    { return nil }
func (r managedAmnesiaLifecycleRuntime) KVManager() storage.KVManager {
	return r.kv
}
func (managedAmnesiaLifecycleRuntime) DnsCacheManager() storage.DnsCacheManager { return nil }
func (managedAmnesiaLifecycleRuntime) SecretsStorage() storage.SecretsStorage   { return nil }
func (managedAmnesiaLifecycleRuntime) TlsConfigStorage() storage.TlsConfigStorage {
	return nil
}
func (r managedAmnesiaLifecycleRuntime) WorkerSpawner(uint64) nodekit.WorkerSpawner {
	return r.spawner
}
func (managedAmnesiaLifecycleRuntime) ObserveNodeSnapshot(uint64, pasta.NodeSnapshot) {
}

type managedAmnesiaLifecycleSpawner struct {
	next atomic.Uint64
	wg   sync.WaitGroup
}

func (s *managedAmnesiaLifecycleSpawner) Spawn(worker func(), _ string) (uint64, error) {
	id := s.next.Add(1)
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		worker()
	}()
	return id, nil
}

func (s *managedAmnesiaLifecycleSpawner) SpawnWg(
	worker func(),
	wg *sync.WaitGroup,
	name string,
) (uint64, error) {
	wg.Add(1)
	return s.Spawn(func() {
		defer wg.Done()
		worker()
	}, name)
}

func (s *managedAmnesiaLifecycleSpawner) wait() { s.wg.Wait() }
