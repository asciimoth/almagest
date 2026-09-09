package amnesianodes

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/asciimoth/bufpool"
	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/formular"
	"github.com/asciimoth/gonnect"
	"github.com/asciimoth/gonnect/dns"
	"github.com/asciimoth/gonnect/sysnet"
	"github.com/asciimoth/gonnect/tun"
	"github.com/asciimoth/pasta/pasta"
	"github.com/asciimoth/pasta/pasta/std"

	dnsnodes "github.com/asciimoth/almagest/lib/dns"
	"github.com/asciimoth/almagest/lib/links"
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
	firstMTU := links.NewLayer3DatagramMTUProvider(1500, true, false, links.MTUConstraint{Source: "first carrier"})
	w.EmitEvent(tunProviderID, firstTunLink, nodekit.TunPayload{Tun: firstTun})
	w.EmitEvent(networkProviderID, firstNetworkLink, nodekit.NetworkPayload{
		Network: firstNetwork, DatagramMTU: firstMTU, CapabilityRevision: firstMTU.Revision(),
	})
	spawner.wait()
	if node.tunLink != firstTunLink || node.tun != firstTun {
		t.Fatalf("attached TUN = link %d value %p, want link %d value %p", node.tunLink, node.tun, firstTunLink, firstTun)
	}
	if node.netLink != firstNetworkLink || node.network != firstNetwork {
		t.Fatalf("attached network = link %d value %p, want link %d value %p", node.netLink, node.network, firstNetworkLink, firstNetwork)
	}
	if !links.SameDatagramMTUProvider(node.networkPayload.DatagramMTU, firstMTU) ||
		node.networkPayload.CapabilityRevision != firstMTU.Revision() || node.unsubscribeNetworkMTU == nil {
		t.Fatal("first network did not attach its MTU capability")
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
	if node.networkPayload.Network != nil || node.networkPayload.DatagramMTU != nil ||
		node.networkMTURevision != 0 || node.unsubscribeNetworkMTU != nil {
		t.Fatalf("detached MTU state was not cleared: %+v", node.networkPayload)
	}
	firstMTU.SetMTU(1400, true, false, links.MTUConstraint{Source: "stale carrier update"})
	spawner.wait()
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
	secondMTU := links.NewLayer3DatagramMTUProvider(1350, true, false, links.MTUConstraint{Source: "second carrier"})
	w.EmitEvent(tunProviderID, secondTunLink, nodekit.TunPayload{Tun: secondTun})
	w.EmitEvent(networkProviderID, secondNetworkLink, nodekit.NetworkPayload{
		Network: secondNetwork, DatagramMTU: secondMTU, CapabilityRevision: secondMTU.Revision(),
	})
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
	if !links.SameDatagramMTUProvider(node.networkPayload.DatagramMTU, secondMTU) ||
		node.networkPayload.CapabilityRevision != secondMTU.Revision() || node.unsubscribeNetworkMTU == nil {
		t.Fatal("reattached network did not replace the MTU capability")
	}
	firstMTU.SetMTU(1300, true, false, links.MTUConstraint{Source: "second stale update"})
	spawner.wait()
	if !links.SameDatagramMTUProvider(node.networkPayload.DatagramMTU, secondMTU) ||
		node.networkPayload.CapabilityRevision != secondMTU.Revision() {
		t.Fatal("detached MTU provider changed the reattached network")
	}
	secondMTU.SetMTU(1320, true, false, links.MTUConstraint{Source: "second carrier update"})
	spawner.wait()
	if node.networkPayload.CapabilityRevision != secondMTU.Revision() {
		t.Fatal("reattached MTU provider update was not processed")
	}
}

func TestManagedAmnesiaDNSLinkAndCountryChangeLifecycle(t *testing.T) {
	spawner := &managedAmnesiaLifecycleSpawner{}
	runtime := managedAmnesiaLifecycleRuntime{
		kv: managedAmnesiaKVManager{store: newAmnesiaMemoryKV()}, spawner: spawner,
	}
	class := &recordingManagedAmnesiaClass{base: AmnesiaClass{Runtime: runtime}}
	w, err := pasta.WorkspaceFromConfig(
		[]pasta.NodeClass{class}, managedAmnesiaLifecycleConfig("amnesia DNS"),
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
	firstSource := newManagedAmnesiaTestDNS()
	replacementSource := newManagedAmnesiaTestDNS()
	t.Cleanup(func() {
		_ = firstSource.Close()
		_ = replacementSource.Close()
	})
	w.Lock()
	node.runtimeReady = true
	node.dnsProvider = firstSource
	w.Unlock()

	consumer := &managedAmnesiaDNSPeer{}
	consumerID, err := w.AddNode(consumer, "test/AmnesiaDNSConsumer", "DNS consumer")
	if err != nil {
		t.Fatalf("add DNS consumer: %v", err)
	}
	consumerPort, err := w.AddPort(pasta.Port{
		Node: consumerID, Direction: "right", Name: "DNS", Types: []string{dnsnodes.TypeDNS},
	})
	if err != nil {
		t.Fatalf("add DNS consumer port: %v", err)
	}
	firstLink, _, err := w.AddLink(node.dnsp, consumerPort)
	if err != nil {
		t.Fatalf("attach first DNS link: %v", err)
	}
	firstOutput := consumer.latest(t)
	if !dnsnodes.DNSIsReady(firstOutput) || !dnsnodes.SameDNSSource(firstOutput, firstSource) {
		t.Fatal("first DNS link did not receive a ready detached resolver")
	}

	generationBeforeRelink := node.generation.Load()
	w.RemoveLink(firstLink)
	if dnsnodes.DNSIsReady(firstOutput) {
		t.Fatal("removed DNS link left its detached resolver ready")
	}
	if !dnsnodes.DNSIsReady(firstSource) {
		t.Fatal("removing a DNS link closed the runtime resolver")
	}
	secondLink, _, err := w.AddLink(node.dnsp, consumerPort)
	if err != nil {
		t.Fatalf("reattach DNS link: %v", err)
	}
	secondOutput := consumer.latest(t)
	if secondLink == firstLink || dnsnodes.SameDNSInstance(firstOutput, secondOutput) ||
		!dnsnodes.SameDNSSource(secondOutput, firstSource) {
		t.Fatal("reattached DNS link did not receive a fresh detached resolver")
	}
	if node.generation.Load() != generationBeforeRelink {
		t.Fatal("DNS-only detach and reattach rebuilt the VPN runtime")
	}

	// Apply a country change through the node menu. The old output must close
	// before negotiation starts, so downstream nodes cannot use stale profile
	// DNS servers during the replacement.
	w.SendNodeFormularMsg(node.ID, formular.FormApplyMessage{
		MessageBase: formular.MessageBase{
			Type: formular.MessageFormApply, MenuID: pasta.NodeMenuID(node.ID),
		},
		BlockID: "config",
		Values:  map[string]any{fieldServerCountryCode: "de"},
	})
	if node.config.serverCountryCode != "de" {
		t.Fatalf("country after menu apply = %q, want de", node.config.serverCountryCode)
	}
	if dnsnodes.DNSIsReady(secondOutput) {
		t.Fatal("country change left the old detached resolver ready")
	}
	spawner.wait()

	// Model the ready result from the replacement runtime. The existing link
	// must receive a new wrapper without detach and reattach operations.
	w.Lock()
	node.handleRuntimeResult(managedAmnesiaRuntimeResult{
		generation: node.generation.Load(), ready: true, dns: replacementSource,
	})
	w.Unlock()
	replacementOutput := consumer.latest(t)
	if dnsnodes.SameDNSInstance(replacementOutput, secondOutput) ||
		!dnsnodes.DNSIsReady(replacementOutput) ||
		!dnsnodes.SameDNSSource(replacementOutput, replacementSource) {
		t.Fatal("ready country replacement did not refresh the attached DNS link")
	}
	if !dnsnodes.DNSIsReady(firstSource) {
		t.Fatal("country replacement output handling closed the old runtime source")
	}

	std.Request(w, consumerID, secondLink)
	refreshedOutput := consumer.latest(t)
	if dnsnodes.DNSIsReady(replacementOutput) ||
		dnsnodes.SameDNSInstance(refreshedOutput, replacementOutput) ||
		!dnsnodes.SameDNSSource(refreshedOutput, replacementSource) {
		t.Fatal("DNS value request did not replace the per-link wrapper")
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
	if !ok || len(snapshot.LeftPorts) != 2 || len(snapshot.RightPorts) != 1 {
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

type managedAmnesiaDNSPeer struct {
	pasta.BasicNode

	received []dns.Interface
}

func (*managedAmnesiaDNSPeer) PreLinkAdd(uint64, string, string) error { return nil }

func (p *managedAmnesiaDNSPeer) OnEvent(
	event pasta.Event,
	linkType string,
	_ []string,
	receiverPortDirection string,
) error {
	if linkType != dnsnodes.TypeDNS || receiverPortDirection != "right" {
		return nil
	}
	payload, ok := event.Payload.(dnsnodes.DNSPayload)
	if ok && payload.DNS != nil {
		p.received = append(p.received, payload.DNS)
	}
	return nil
}

func (p *managedAmnesiaDNSPeer) latest(t *testing.T) dns.Interface {
	t.Helper()
	if len(p.received) == 0 {
		t.Fatal("DNS consumer received no resolver")
	}
	return p.received[len(p.received)-1]
}

type managedAmnesiaTestDNS struct {
	requests chan dns.Request
	closed   atomic.Bool
}

func newManagedAmnesiaTestDNS() *managedAmnesiaTestDNS {
	return &managedAmnesiaTestDNS{requests: make(chan dns.Request)}
}

func (d *managedAmnesiaTestDNS) Requests() chan<- dns.Request { return d.requests }
func (d *managedAmnesiaTestDNS) Close() error {
	d.closed.Store(true)
	return nil
}
func (d *managedAmnesiaTestDNS) IsUp() (bool, error) { return !d.closed.Load(), nil }

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
