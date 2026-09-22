package tunnodes

import (
	"bytes"
	"net/netip"
	"testing"

	"github.com/asciimoth/almagest/lib/testkit"
	"github.com/asciimoth/almagest/lib/testkit/networktest"
	"github.com/asciimoth/configer/configer"
	sysnetdebug "github.com/asciimoth/gonnect/sysnet/debug"
	"github.com/asciimoth/gonnect/tun"
	"github.com/asciimoth/pasta/pasta"
)

func TestAutoTunSplitterNodeMaintainsLinkedSlotsAndOneFreeSlot(t *testing.T) {
	w := newTunSplitterTestWorkspace(t)
	splitter, err := w.AddNodeByClass(NodeTypeAutoTunSplitter, "auto splitter")
	if err != nil {
		t.Fatalf("AddNodeByClass AutoTunSplitter: %v", err)
	}
	assertTunSplitterRightPorts(t, w, splitter, []string{"slot 1"})

	consumer1 := &tunPayloadReceiver{events: make(chan TunPayload, 2)}
	if _, err := w.AddNode(consumer1, "test/TunReceiver", "consumer 1"); err != nil {
		t.Fatalf("AddNode consumer 1: %v", err)
	}
	snapshot := testkit.MustNodeSnapshot(t, w, splitter)
	link1, _, err := w.AddLink(consumer1.in, snapshot.RightPorts[0])
	if err != nil {
		t.Fatalf("AddLink slot 1: %v", err)
	}
	assertTunSplitterRightPorts(t, w, splitter, []string{"slot 1", "slot 2"})

	consumer2 := &tunPayloadReceiver{events: make(chan TunPayload, 2)}
	if _, err := w.AddNode(consumer2, "test/TunReceiver", "consumer 2"); err != nil {
		t.Fatalf("AddNode consumer 2: %v", err)
	}
	snapshot = testkit.MustNodeSnapshot(t, w, splitter)
	link2, _, err := w.AddLink(consumer2.in, snapshot.RightPorts[1])
	if err != nil {
		t.Fatalf("AddLink slot 2: %v", err)
	}
	assertTunSplitterRightPorts(t, w, splitter, []string{"slot 1", "slot 2", "slot 3"})

	w.RemoveLink(link1)
	assertTunSplitterRightPorts(t, w, splitter, []string{"slot 1", "slot 2"})
	w.RemoveLink(link2)
	assertTunSplitterRightPorts(t, w, splitter, []string{"slot 1"})
}

func TestAutoTunSplitterNodeRejectsConfigFromReplacedSlotLink(t *testing.T) {
	w := newTunSplitterTestWorkspace(t)
	runtime := &tunSplitterTestRuntime{sys: &sysnetdebug.System{}}
	node := newTunSplitterNode(runtime, configer.NewMemory(nil), true)
	splitter, err := w.AddNode(node, "test/AutoTunSplitter", "auto splitter")
	if err != nil {
		t.Fatalf("AddNode AutoTunSplitter: %v", err)
	}

	defaultConsumer := &tunPayloadReceiver{events: make(chan TunPayload, 2)}
	if _, err := w.AddNode(defaultConsumer, "test/TunReceiver", "default consumer"); err != nil {
		t.Fatalf("AddNode default consumer: %v", err)
	}
	snapshot := testkit.MustNodeSnapshot(t, w, splitter)
	if _, _, err := w.AddLink(defaultConsumer.in, snapshot.RightPorts[0]); err != nil {
		t.Fatalf("AddLink default slot: %v", err)
	}

	oldConsumer := &tunPayloadReceiver{events: make(chan TunPayload, 2)}
	oldConsumerID, err := w.AddNode(oldConsumer, "test/TunReceiver", "old consumer")
	if err != nil {
		t.Fatalf("AddNode old consumer: %v", err)
	}
	snapshot = testkit.MustNodeSnapshot(t, w, splitter)
	slot2Port := snapshot.RightPorts[1]
	oldLink, _, err := w.AddLink(oldConsumer.in, slot2Port)
	if err != nil {
		t.Fatalf("AddLink old slot 2 consumer: %v", err)
	}
	w.EmitEvent(oldConsumerID, oldLink, TunConfig{Routes: []string{"192.0.2.0/24"}})

	w.RemoveLink(oldLink)
	replacement := &tunPayloadReceiver{events: make(chan TunPayload, 2)}
	replacementID, err := w.AddNode(replacement, "test/TunReceiver", "replacement consumer")
	if err != nil {
		t.Fatalf("AddNode replacement consumer: %v", err)
	}
	snapshot = testkit.MustNodeSnapshot(t, w, splitter)
	slot2Port = snapshot.RightPorts[1]
	replacementLink, _, err := w.AddLink(replacement.in, slot2Port)
	if err != nil {
		t.Fatalf("AddLink replacement slot 2 consumer: %v", err)
	}
	w.EmitEvent(replacementID, replacementLink, TunConfig{Routes: []string{"198.51.100.0/24"}})

	// Simulate a delayed workspace event that was queued before oldLink was
	// removed. It must not overwrite the replacement link's route snapshot.
	if err := node.OnEvent(pasta.Event{
		ReceiverPort: slot2Port,
		Link:         oldLink,
		Payload:      TunConfig{Routes: []string{"203.0.113.0/24"}},
	}, TypeTun, nil, "right"); err != nil {
		t.Fatalf("OnEvent stale config: %v", err)
	}

	source := netip.MustParseAddr("10.0.0.1")
	if got := routeAutoTunPacket(node.autoRouter, autoTunTestPacket(
		source,
		netip.MustParseAddr("198.51.100.9"),
	), 0); got != 2 {
		t.Fatalf("replacement prefix route = slot %d, want 2", got)
	}
	if got := routeAutoTunPacket(node.autoRouter, autoTunTestPacket(
		source,
		netip.MustParseAddr("203.0.113.9"),
	), 0); got != 1 {
		t.Fatalf("stale prefix route = slot %d, want fallback slot 1", got)
	}
}

func TestAutoTunSplitterNodeRoutesPacketsAndForgetsDetachedConfig(t *testing.T) {
	w := newTunSplitterTestWorkspace(t)
	providers := newTestTunProviderRegistry()
	if err := w.AddNodeClass(testTunProviderClass{registry: providers}); err != nil {
		t.Fatalf("AddNodeClass provider: %v", err)
	}

	splitter, err := w.AddNodeByClass(NodeTypeAutoTunSplitter, "auto splitter")
	if err != nil {
		t.Fatalf("AddNodeByClass AutoTunSplitter: %v", err)
	}
	provider, err := w.AddNodeByClass(testTunProviderNodeType, "provider")
	if err != nil {
		t.Fatalf("AddNodeByClass provider: %v", err)
	}
	defaultConsumer := &tunPayloadReceiver{events: make(chan TunPayload, 4)}
	defaultConsumerID, err := w.AddNode(defaultConsumer, "test/TunReceiver", "default consumer")
	if err != nil {
		t.Fatalf("AddNode default consumer: %v", err)
	}
	specificConsumer := &tunPayloadReceiver{events: make(chan TunPayload, 4)}
	specificConsumerID, err := w.AddNode(specificConsumer, "test/TunReceiver", "specific consumer")
	if err != nil {
		t.Fatalf("AddNode specific consumer: %v", err)
	}

	splitterSnapshot := testkit.MustNodeSnapshot(t, w, splitter)
	providerSnapshot := testkit.MustNodeSnapshot(t, w, provider)
	if _, _, err := w.AddLink(splitterSnapshot.LeftPorts[0], providerSnapshot.RightPorts[0]); err != nil {
		t.Fatalf("AddLink provider->splitter: %v", err)
	}
	defaultLink, _, err := w.AddLink(defaultConsumer.in, splitterSnapshot.RightPorts[0])
	if err != nil {
		t.Fatalf("AddLink default slot: %v", err)
	}
	splitterSnapshot = testkit.MustNodeSnapshot(t, w, splitter)
	specificLink, _, err := w.AddLink(specificConsumer.in, splitterSnapshot.RightPorts[1])
	if err != nil {
		t.Fatalf("AddLink specific slot: %v", err)
	}
	defaultFrontend := receiveTunPayload(t, defaultConsumer.events).Tun
	specificFrontend := receiveTunPayload(t, specificConsumer.events).Tun

	w.EmitEvent(defaultConsumerID, defaultLink, TunConfig{
		Addrs:  []string{"10.20.0.2/32"},
		Routes: []string{"0.0.0.0/0"},
	})
	w.EmitEvent(specificConsumerID, specificLink, TunConfig{
		Addrs:  []string{"100.64.0.2/32"},
		Routes: []string{"100.64.0.0/10"},
	})

	peer := providers.peer(t, provider)
	assertAutoTunPacketRoute(
		t,
		peer,
		defaultFrontend,
		netip.MustParseAddr("10.20.0.2"),
		netip.MustParseAddr("203.0.113.1"),
	)
	assertAutoTunPacketRoute(
		t,
		peer,
		specificFrontend,
		netip.MustParseAddr("192.0.2.1"),
		netip.MustParseAddr("100.100.0.1"),
	)
	// A learned source address identifies its slot even when the destination
	// would otherwise follow the default route.
	assertAutoTunPacketRoute(
		t,
		peer,
		specificFrontend,
		netip.MustParseAddr("100.64.0.2"),
		netip.MustParseAddr("203.0.113.2"),
	)

	// Replacing a complete config removes routes that the new snapshot omits.
	w.EmitEvent(specificConsumerID, specificLink, TunConfig{
		Addrs:  []string{"198.18.0.2/32"},
		Routes: []string{"198.18.0.0/15"},
	})
	assertAutoTunPacketRoute(
		t,
		peer,
		defaultFrontend,
		netip.MustParseAddr("10.20.0.2"),
		netip.MustParseAddr("100.100.0.1"),
	)
	assertAutoTunPacketRoute(
		t,
		peer,
		specificFrontend,
		netip.MustParseAddr("192.0.2.1"),
		netip.MustParseAddr("198.19.0.1"),
	)

	w.RemoveLink(specificLink)
	assertAutoTunPacketRoute(
		t,
		peer,
		defaultFrontend,
		netip.MustParseAddr("10.20.0.2"),
		netip.MustParseAddr("198.19.0.1"),
	)
}

func TestVTunAutoTunSplitterLearnsDualStackAndSourceRoutes(t *testing.T) {
	w := newVTunTunSplitterTestWorkspace(t)
	vtunID, err := w.AddNodeByClass(NodeTypeVTun, "vtun")
	if err != nil {
		t.Fatalf("AddNodeByClass VTun: %v", err)
	}
	splitterID, err := w.AddNodeByClass(NodeTypeAutoTunSplitter, "auto splitter")
	if err != nil {
		t.Fatalf("AddNodeByClass AutoTunSplitter: %v", err)
	}
	netReceiver := networktest.NewReceiver(8)
	if _, err := w.AddNode(netReceiver, "test/NetworkReceiver", "network receiver"); err != nil {
		t.Fatalf("AddNode network receiver: %v", err)
	}
	defaultConsumer := &tunPayloadReceiver{events: make(chan TunPayload, 8)}
	defaultConsumerID, err := w.AddNode(defaultConsumer, "test/TunReceiver", "default consumer")
	if err != nil {
		t.Fatalf("AddNode default consumer: %v", err)
	}
	tailnetConsumer := &tunPayloadReceiver{events: make(chan TunPayload, 8)}
	tailnetConsumerID, err := w.AddNode(tailnetConsumer, "test/TunReceiver", "tailnet consumer")
	if err != nil {
		t.Fatalf("AddNode tailnet consumer: %v", err)
	}

	vtunSnapshot := testkit.MustNodeSnapshot(t, w, vtunID)
	splitterSnapshot := testkit.MustNodeSnapshot(t, w, splitterID)
	if _, _, err := w.AddLink(vtunSnapshot.LeftPorts[0], netReceiver.In); err != nil {
		t.Fatalf("AddLink VTun network: %v", err)
	}
	_ = networktest.ReceivePayload(t, netReceiver.Events)
	if _, _, err := w.AddLink(splitterSnapshot.LeftPorts[0], vtunSnapshot.RightPorts[0]); err != nil {
		t.Fatalf("AddLink VTun->AutoTunSplitter: %v", err)
	}
	defaultLink, _, err := w.AddLink(defaultConsumer.in, splitterSnapshot.RightPorts[0])
	if err != nil {
		t.Fatalf("AddLink default consumer: %v", err)
	}
	splitterSnapshot = testkit.MustNodeSnapshot(t, w, splitterID)
	tailnetLink, _, err := w.AddLink(tailnetConsumer.in, splitterSnapshot.RightPorts[1])
	if err != nil {
		t.Fatalf("AddLink tailnet consumer: %v", err)
	}
	defaultFrontend := receiveTunPayload(t, defaultConsumer.events).Tun
	tailnetFrontend := receiveTunPayload(t, tailnetConsumer.events).Tun

	default4 := netip.MustParseAddr("10.20.0.2")
	default6 := netip.MustParseAddr("2001:db8:ffff::2")
	carrier4 := netip.MustParseAddr("100.64.0.2")
	specific4 := netip.MustParseAddr("100.80.0.2")
	tailnet6 := netip.MustParseAddr("fd7a:115c:a1e0::2")
	w.EmitEvent(defaultConsumerID, defaultLink, TunConfig{
		Addrs:  []string{default4.String() + "/32", default6.String() + "/128"},
		Routes: []string{"0.0.0.0/0", "::/0"},
	})
	_ = networktest.ReceivePayload(t, netReceiver.Events)
	tailnetConfig := TunConfig{
		Addrs: []string{
			carrier4.String() + "/32",
			specific4.String() + "/32",
			tailnet6.String() + "/128",
		},
		Routes: []string{
			"100.64.0.0/10",
			"100.80.0.0/16",
			"fd7a:115c:a1e0::/48",
		},
		SourceRoutes: []TunSourceRoute{{
			Destination: netip.MustParsePrefix("100.80.0.0/16"),
			Source:      specific4,
		}},
	}
	w.EmitEvent(tailnetConsumerID, tailnetLink, tailnetConfig)
	network := networktest.ReceivePayload(t, netReceiver.Events).Network

	tests := []struct {
		name        string
		destination netip.Addr
		wantSource  netip.Addr
		frontend    tun.Tun
	}{
		{
			name:        "IPv4 default route",
			destination: netip.MustParseAddr("203.0.113.1"),
			wantSource:  default4,
			frontend:    defaultFrontend,
		},
		{
			name:        "IPv4 learned route",
			destination: netip.MustParseAddr("100.97.143.122"),
			wantSource:  carrier4,
			frontend:    tailnetFrontend,
		},
		{
			name:        "IPv4 most-specific source route",
			destination: netip.MustParseAddr("100.80.0.1"),
			wantSource:  specific4,
			frontend:    tailnetFrontend,
		},
		{
			name:        "IPv6 default route",
			destination: netip.MustParseAddr("2001:db8:1234::1"),
			wantSource:  default6,
			frontend:    defaultFrontend,
		},
		{
			name:        "IPv6 learned route",
			destination: netip.MustParseAddr("fd7a:115c:a1e0::99"),
			wantSource:  tailnet6,
			frontend:    tailnetFrontend,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			writeVTunUDPAndAssertPacket(t, network, test.frontend, test.destination, test.wantSource)
		})
	}

	// The router and VTun source policy both update from the replacement
	// TunConfig without replacing the active Network or TUN frontends.
	tailnetConfig.Routes = []string{"198.18.0.0/15"}
	tailnetConfig.SourceRoutes = []TunSourceRoute{{
		Destination: netip.MustParsePrefix("198.18.0.0/15"),
		Source:      specific4,
	}}
	w.EmitEvent(tailnetConsumerID, tailnetLink, tailnetConfig)
	networktest.AssertNoEvent(t, netReceiver.Events)
	writeVTunUDPAndAssertPacket(
		t,
		network,
		defaultFrontend,
		netip.MustParseAddr("100.97.143.122"),
		default4,
	)
	writeVTunUDPAndAssertPacket(
		t,
		network,
		tailnetFrontend,
		netip.MustParseAddr("198.19.0.1"),
		specific4,
	)
}

func assertAutoTunPacketRoute(
	t *testing.T,
	peer, frontend tun.Tun,
	source, destination netip.Addr,
) {
	t.Helper()
	packet := autoTunTestPacket(source, destination)
	writeTunPacket(t, peer, packet)
	if got := readTunPacket(t, frontend); !bytes.Equal(got, packet) {
		t.Fatalf("frontend packet = %x, want %x", got, packet)
	}
}
