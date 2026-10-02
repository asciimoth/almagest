package tailscalenodes

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/asciimoth/bufpool"
	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/formular"
	"github.com/asciimoth/gonnect"
	"github.com/asciimoth/gonnect/dns"
	"github.com/asciimoth/gonnect/sysnet"
	"github.com/asciimoth/pasta/pasta"
	"github.com/asciimoth/pasta/pasta/std"
	wgotailscale "github.com/asciimoth/wgo-tailscale"
	wgdevice "github.com/asciimoth/wgo/device"

	dnsnodes "github.com/asciimoth/almagest/lib/dns"
	"github.com/asciimoth/almagest/lib/logging"
	"github.com/asciimoth/almagest/lib/nodekit"
	"github.com/asciimoth/almagest/lib/storage"
	"github.com/asciimoth/almagest/lib/testkit"
	wireguardnodes "github.com/asciimoth/almagest/lib/wireguard"
)

func TestTailscaleLinksAttachDetachReattach(t *testing.T) {
	runtime, stopRuntime := newTailscaleLifecycleRuntime()
	factory := &tailscaleTestClientFactory{snapshot: wgotailscale.Snapshot{
		State: wgotailscale.StateNeedsAuthentication,
		Interaction: &wgotailscale.Interaction{
			ID:      11,
			Kind:    wgotailscale.InteractionAuthenticate,
			URL:     "https://login.example.test/register/node",
			Message: "Authorize this test node.",
		},
	}}
	class := &recordingTailscaleClass{base: TailscaleCtrlClass{Runtime: runtime, NewClient: factory.newClient}}
	w, err := pasta.WorkspaceFromConfig(
		[]pasta.NodeClass{class},
		configer.NewMemory(map[string]any{
			"tailscale": map[string]any{
				"Class":         NodeTypeTailscaleCtrl,
				fieldControlURL: "https://control.example.test",
				fieldHostname:   "alpha",
			},
		}),
		logging.NewDebugLogFactory(256),
	)
	if err != nil {
		stopRuntime()
		t.Fatalf("build workspace: %v", err)
	}
	node := class.onlyNode(t)
	t.Cleanup(func() {
		w.Close()
		stopRuntime()
	})

	wgPeer := &tailscaleLinkPeer{}
	wgPeerID, err := w.AddNode(wgPeer, "test/TailscaleWGProvider", "WireGuard provider")
	if err != nil {
		t.Fatalf("add WireGuard provider: %v", err)
	}
	wgPort, err := w.AddPort(pasta.Port{Node: wgPeerID, Direction: "left", Name: "WGCtrl", Types: []string{wireguardnodes.TypeWGCtrl}})
	if err != nil {
		t.Fatalf("add WireGuard port: %v", err)
	}
	networkPeer := &tailscaleLinkPeer{}
	networkPeerID, err := w.AddNode(networkPeer, "test/TailscaleNetworkProvider", "Network provider")
	if err != nil {
		t.Fatalf("add Network provider: %v", err)
	}
	networkPort, err := w.AddPort(pasta.Port{Node: networkPeerID, Direction: "left", Name: "Network", Types: []string{nodekit.TypeNetwork}})
	if err != nil {
		t.Fatalf("add Network port: %v", err)
	}

	device := tailscaleTestDevice(t)
	firstController := wgdevice.DetachDevice(device)
	firstNetwork := gonnect.DetachNetwork(gonnect.NewLoopbackNetwork(), nil, nil)
	firstWGLink, _, err := w.AddLink(wgPort, node.ctrlp)
	if err != nil {
		t.Fatalf("attach WireGuard link: %v", err)
	}
	firstNetworkLink, _, err := w.AddLink(networkPort, node.netp)
	if err != nil {
		t.Fatalf("attach Network link: %v", err)
	}
	w.EmitEvent(wgPeerID, firstWGLink, wireguardnodes.WGCtrlPayload{API: firstController})
	w.EmitEvent(networkPeerID, firstNetworkLink, nodekit.NetworkPayload{Network: firstNetwork})
	eventuallyTailscale(t, func() bool { return factory.count() == 1 }, "first Tailscale client")
	eventuallyTailscale(t, func() bool {
		snapshot, ok := w.NodeSnapshot(node.ID)
		return ok && snapshot.Label == "needs authentication" && len(snapshot.Popups) == 1
	}, "authentication state and warning popup")

	w.Lock()
	authBlock, ok := node.authenticationBlock()
	w.Unlock()
	if !ok || authBlock.Order != 0 || len(authBlock.Items) == 0 || authBlock.Items[0].Format != formular.TextMarkdown ||
		!strings.Contains(authBlock.Items[0].Text, "https://login.example.test/register/node") {
		t.Fatalf("authentication block = %#v", authBlock)
	}
	w.SendNodeFormularMsg(node.ID, formular.ButtonPressMessage{
		MessageBase: formular.MessageBase{
			Type:   formular.MessageButtonPress,
			MenuID: pasta.NodeMenuID(node.ID),
		},
		BlockID:  authBlockID,
		ButtonID: authResumeID,
	})
	eventuallyTailscale(t, func() bool { return factory.client(0).resumed(11) }, "authorization resume")
	firstCall := factory.onlyCall(t)
	if wrapped, ok := firstCall.network.(*tailscaleNetwork); !ok || wrapped.Network != firstNetwork ||
		firstCall.controller != firstController {
		t.Fatal("first client received the wrong graph inputs")
	}

	w.RemoveLink(firstWGLink)
	w.RemoveLink(firstNetworkLink)
	eventuallyTailscale(t, func() bool { return factory.client(0).isClosed() }, "first client shutdown")
	eventuallyTailscale(t, func() bool {
		select {
		case <-firstController.Wait():
			return true
		default:
			return false
		}
	}, "first detached WireGuard controller closure")
	eventuallyTailscale(t, func() bool {
		snapshot, ok := w.NodeSnapshot(node.ID)
		return ok && snapshot.Label == "waiting for WireGuard" && len(snapshot.Popups) == 0
	}, "detached state")

	secondController := wgdevice.DetachDevice(device)
	secondNetwork := gonnect.DetachNetwork(gonnect.NewLoopbackNetwork(), nil, nil)
	secondWGLink, _, err := w.AddLink(wgPort, node.ctrlp)
	if err != nil {
		t.Fatalf("reattach WireGuard link: %v", err)
	}
	secondNetworkLink, _, err := w.AddLink(networkPort, node.netp)
	if err != nil {
		t.Fatalf("reattach Network link: %v", err)
	}
	w.EmitEvent(wgPeerID, secondWGLink, wireguardnodes.WGCtrlPayload{API: secondController})
	w.EmitEvent(networkPeerID, secondNetworkLink, nodekit.NetworkPayload{Network: secondNetwork})
	eventuallyTailscale(t, func() bool { return factory.count() == 2 }, "replacement Tailscale client")
	if secondWGLink == firstWGLink || secondNetworkLink == firstNetworkLink {
		t.Fatal("reattach reused a removed link ID")
	}
	secondCall := factory.clientCall(t, 1)
	if wrapped, ok := secondCall.network.(*tailscaleNetwork); !ok || wrapped.Network != secondNetwork ||
		secondCall.controller != secondController {
		t.Fatal("replacement client received stale graph inputs")
	}
	if secondCall.options.TransportID != firstCall.options.TransportID {
		t.Fatalf("transport ID changed across reattach: %q then %q", firstCall.options.TransportID, secondCall.options.TransportID)
	}
}

func TestTailscaleDNSAndFirewallOutputLinksDetachAndReattach(t *testing.T) {
	runtime, stopRuntime := newTailscaleLifecycleRuntime()
	class := &recordingTailscaleClass{base: TailscaleCtrlClass{Runtime: runtime}}
	w, err := pasta.WorkspaceFromConfig(
		[]pasta.NodeClass{class},
		configer.NewMemory(map[string]any{
			"tailscale": map[string]any{"Class": NodeTypeTailscaleCtrl, fieldEnabled: false},
		}),
		logging.NewDebugLogFactory(256),
	)
	if err != nil {
		stopRuntime()
		t.Fatalf("build workspace: %v", err)
	}
	node := class.onlyNode(t)
	t.Cleanup(func() {
		w.Close()
		stopRuntime()
	})

	dnsConsumer := &tailscaleDNSConsumer{}
	dnsConsumerID, err := w.AddNode(dnsConsumer, "test/TailscaleDNSConsumer", "DNS consumer")
	if err != nil {
		t.Fatalf("add DNS consumer: %v", err)
	}
	dnsPort, err := w.AddPort(pasta.Port{Node: dnsConsumerID, Direction: "right", Name: "DNS", Types: []string{dnsnodes.TypeDNS}})
	if err != nil {
		t.Fatalf("add DNS consumer port: %v", err)
	}
	objectConsumer := &tailscaleObjectConsumer{}
	objectConsumerID, err := w.AddNode(objectConsumer, "test/TailscaleObjectConsumer", "Object consumer")
	if err != nil {
		t.Fatalf("add object consumer: %v", err)
	}
	objectPort, err := w.AddPort(pasta.Port{Node: objectConsumerID, Direction: "left", Name: "Config", Types: []string{std.TypeObject}})
	if err != nil {
		t.Fatalf("add object consumer port: %v", err)
	}

	firstDNSLink, _, err := w.AddLink(node.dnsp, dnsPort)
	if err != nil {
		t.Fatalf("attach DNS output: %v", err)
	}
	firstObjectLink, _, err := w.AddLink(objectPort, node.firewallp)
	if err != nil {
		t.Fatalf("attach firewall output: %v", err)
	}
	testkit.Eventually(t, func() bool {
		return dnsConsumer.count() == 1 && objectConsumer.count() == 1
	}, "initial Tailscale DNS and firewall output delivery")
	firstDNS := dnsConsumer.latest(t)
	if !dnsnodes.DNSIsReady(firstDNS) || objectConsumer.count() != 1 || objectConsumer.latest(t).JSONString() != "{}" {
		t.Fatalf("first outputs = DNS ready %t, objects %d", dnsnodes.DNSIsReady(firstDNS), objectConsumer.count())
	}

	w.RemoveLink(firstDNSLink)
	w.RemoveLink(firstObjectLink)
	if dnsnodes.DNSIsReady(firstDNS) {
		t.Fatal("removed DNS link left its detached output ready")
	}
	secondDNSLink, _, err := w.AddLink(node.dnsp, dnsPort)
	if err != nil {
		t.Fatalf("reattach DNS output: %v", err)
	}
	secondObjectLink, _, err := w.AddLink(objectPort, node.firewallp)
	if err != nil {
		t.Fatalf("reattach firewall output: %v", err)
	}
	testkit.Eventually(t, func() bool {
		return dnsConsumer.count() == 2 && objectConsumer.count() == 2
	}, "reattached Tailscale DNS and firewall output delivery")
	secondDNS := dnsConsumer.latest(t)
	if secondDNSLink == firstDNSLink || secondObjectLink == firstObjectLink ||
		dnsnodes.SameDNSInstance(firstDNS, secondDNS) || !dnsnodes.DNSIsReady(secondDNS) {
		t.Fatal("reattached outputs did not use fresh links and a fresh DNS wrapper")
	}
	if objectConsumer.count() != 2 || objectConsumer.latest(t).JSONString() != "{}" {
		t.Fatalf("firewall object count = %d, latest %s", objectConsumer.count(), objectConsumer.latest(t).JSONString())
	}
}

type recordingTailscaleClass struct {
	base  TailscaleCtrlClass
	nodes []*tailscaleNode
}

func (c *recordingTailscaleClass) ClassName() string        { return c.base.ClassName() }
func (c *recordingTailscaleClass) ShortDescription() string { return c.base.ShortDescription() }
func (c *recordingTailscaleClass) LongDescription() string  { return c.base.LongDescription() }
func (c *recordingTailscaleClass) DefaultNodeParams() pasta.NodeClassParams {
	return c.base.DefaultNodeParams()
}
func (c *recordingTailscaleClass) NewNode(cfg configer.Config, previous ...*pasta.NodeClassState) (pasta.Node, error) {
	raw, err := c.base.NewNode(cfg, previous...)
	if err != nil {
		return nil, err
	}
	node := raw.(*tailscaleNode)
	c.nodes = append(c.nodes, node)
	return node, nil
}
func (c *recordingTailscaleClass) onlyNode(t *testing.T) *tailscaleNode {
	t.Helper()
	if len(c.nodes) != 1 {
		t.Fatalf("constructed Tailscale nodes = %d, want 1", len(c.nodes))
	}
	return c.nodes[0]
}

type tailscaleLinkPeer struct {
	pasta.BasicNode
	_ byte
}

func (*tailscaleLinkPeer) PreLinkAdd(uint64, string, string) error { return nil }

type tailscaleDNSConsumer struct {
	pasta.BasicNode
	mu       sync.Mutex
	received []dns.Interface
}

func (*tailscaleDNSConsumer) PreLinkAdd(uint64, string, string) error { return nil }
func (c *tailscaleDNSConsumer) OnEvent(event pasta.Event, linkType string, _ []string, direction string) error {
	if linkType == dnsnodes.TypeDNS && direction == "right" {
		if payload, ok := event.Payload.(dnsnodes.DNSPayload); ok && payload.DNS != nil {
			c.mu.Lock()
			c.received = append(c.received, payload.DNS)
			c.mu.Unlock()
		}
	}
	return nil
}
func (c *tailscaleDNSConsumer) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.received)
}
func (c *tailscaleDNSConsumer) latest(t *testing.T) dns.Interface {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.received) == 0 {
		t.Fatal("DNS consumer received no value")
	}
	return c.received[len(c.received)-1]
}

type tailscaleObjectConsumer struct {
	pasta.BasicNode
	mu       sync.Mutex
	received []std.Object
}

func (*tailscaleObjectConsumer) PreLinkAdd(uint64, string, string) error { return nil }
func (c *tailscaleObjectConsumer) OnEvent(event pasta.Event, linkType string, _ []string, direction string) error {
	if linkType == std.TypeObject && direction == "left" {
		if object, ok := std.ObjectFromPayload(event.Payload); ok {
			c.mu.Lock()
			c.received = append(c.received, object)
			c.mu.Unlock()
		}
	}
	return nil
}
func (c *tailscaleObjectConsumer) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.received)
}
func (c *tailscaleObjectConsumer) latest(t *testing.T) std.Object {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.received) == 0 {
		t.Fatal("object consumer received no value")
	}
	return c.received[len(c.received)-1]
}

type tailscaleLifecycleRuntime struct {
	ctx     context.Context
	spawner *tailscaleTestSpawner
}

func newTailscaleLifecycleRuntime() (*tailscaleLifecycleRuntime, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())
	return &tailscaleLifecycleRuntime{ctx: ctx, spawner: &tailscaleTestSpawner{}}, cancel
}
func (r *tailscaleLifecycleRuntime) Context() context.Context { return r.ctx }
func (*tailscaleLifecycleRuntime) Pool() bufpool.Pool         { return nil }
func (*tailscaleLifecycleRuntime) System() sysnet.System      { return nil }
func (*tailscaleLifecycleRuntime) KVManager() storage.KVManager {
	return nil
}
func (*tailscaleLifecycleRuntime) DnsCacheManager() storage.DnsCacheManager { return nil }
func (*tailscaleLifecycleRuntime) SecretsStorage() storage.SecretsStorage   { return nil }
func (*tailscaleLifecycleRuntime) TlsConfigStorage() storage.TlsConfigStorage {
	return nil
}
func (r *tailscaleLifecycleRuntime) WorkerSpawner(uint64) nodekit.WorkerSpawner {
	return r.spawner
}
func (*tailscaleLifecycleRuntime) ObserveNodeSnapshot(uint64, pasta.NodeSnapshot) {}

type tailscaleTestSpawner struct{ next atomic.Uint64 }

func (s *tailscaleTestSpawner) Spawn(worker func(), _ string) (uint64, error) {
	id := s.next.Add(1)
	go worker()
	return id, nil
}
func (s *tailscaleTestSpawner) SpawnWg(worker func(), waitGroup *sync.WaitGroup, _ string) (uint64, error) {
	id := s.next.Add(1)
	waitGroup.Add(1)
	go func() {
		defer waitGroup.Done()
		worker()
	}()
	return id, nil
}

func (f *tailscaleTestClientFactory) clientCall(t *testing.T, index int) tailscaleTestClientCall {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if index < 0 || index >= len(f.calls) {
		t.Fatalf("client call index %d out of %d", index, len(f.calls))
	}
	return f.calls[index]
}
