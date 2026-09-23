package tunnodes

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/asciimoth/almagest/lib/logging"
	"github.com/asciimoth/almagest/lib/storage"
	"github.com/asciimoth/almagest/lib/testkit"
	"github.com/asciimoth/almagest/lib/testkit/networktest"
	"github.com/asciimoth/bufpool"
	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/formular"
	"github.com/asciimoth/gonnect"
	"github.com/asciimoth/gonnect/dns"
	"github.com/asciimoth/gonnect/sysnet"
	sysnetdebug "github.com/asciimoth/gonnect/sysnet/debug"
	"github.com/asciimoth/gonnect/tun"
	"github.com/asciimoth/pasta/pasta"
	"github.com/asciimoth/pasta/pasta/std"
)

func TestTunSplitterNodeReviewsSlotPortsFromRulesLinksAndFreeSlot(t *testing.T) {
	w := newTunSplitterTestWorkspace(t)

	splitter, err := w.AddNodeByClass(NodeTypeTunSplitter, "splitter")
	if err != nil {
		t.Fatalf("AddNodeByClass TunSplitter: %v", err)
	}
	assertTunSplitterRightPorts(t, w, splitter, []string{"slot 1"})

	w.SendNodeFormularMsg(splitter, formular.FormApplyMessage{
		MessageBase: testkit.FormMessageBase(formular.MessageFormApply, splitter),
		BlockID:     "config",
		Values:      map[string]any{"rules": "TRUE\nSLOT 3\n"},
	})
	assertTunSplitterRightPorts(t, w, splitter, []string{"slot 1", "slot 3"})

	receiver := &tunPayloadReceiver{events: make(chan TunPayload, 2)}
	if _, err := w.AddNode(receiver, "test/TunReceiver", "receiver"); err != nil {
		t.Fatalf("AddNode receiver: %v", err)
	}
	snapshot := testkit.MustNodeSnapshot(t, w, splitter)
	if _, _, err := w.AddLink(receiver.in, snapshot.RightPorts[0]); err != nil {
		t.Fatalf("AddLink receiver->splitter slot 1: %v", err)
	}

	w.SendNodeFormularMsg(splitter, formular.FormApplyMessage{
		MessageBase: testkit.FormMessageBase(formular.MessageFormApply, splitter),
		BlockID:     "config",
		Values:      map[string]any{"rules": "TRUE\nSLOT 4\n"},
	})
	assertTunSplitterRightPorts(t, w, splitter, []string{"slot 1", "slot 2", "slot 4"})
}

func TestTunSplitterNodeRoutesPacketsThroughFrontend(t *testing.T) {
	w := newTunSplitterTestWorkspace(t)
	providers := newTestTunProviderRegistry()
	if err := w.AddNodeClass(testTunProviderClass{registry: providers}); err != nil {
		t.Fatalf("AddNodeClass provider: %v", err)
	}

	splitter, err := w.AddNodeByClass(NodeTypeTunSplitter, "splitter")
	if err != nil {
		t.Fatalf("AddNodeByClass TunSplitter: %v", err)
	}
	provider, err := w.AddNodeByClass(testTunProviderNodeType, "provider")
	if err != nil {
		t.Fatalf("AddNodeByClass provider: %v", err)
	}
	receiver := &tunPayloadReceiver{events: make(chan TunPayload, 2)}
	if _, err := w.AddNode(receiver, "test/TunReceiver", "receiver"); err != nil {
		t.Fatalf("AddNode receiver: %v", err)
	}

	w.SendNodeFormularMsg(splitter, formular.FormApplyMessage{
		MessageBase: testkit.FormMessageBase(formular.MessageFormApply, splitter),
		BlockID:     "config",
		Values:      map[string]any{"rules": "TRUE\nSLOT 1\n"},
	})
	splitterSnapshot := testkit.MustNodeSnapshot(t, w, splitter)
	providerSnapshot := testkit.MustNodeSnapshot(t, w, provider)
	if _, _, err := w.AddLink(splitterSnapshot.LeftPorts[0], providerSnapshot.RightPorts[0]); err != nil {
		t.Fatalf("AddLink provider->splitter: %v", err)
	}
	if _, _, err := w.AddLink(receiver.in, splitterSnapshot.RightPorts[0]); err != nil {
		t.Fatalf("AddLink splitter->receiver: %v", err)
	}

	frontend := receiveTunPayload(t, receiver.events).Tun
	peer := providers.peer(t, provider)
	packet := testIPv4Packet([4]byte{10, 0, 0, 1}, [4]byte{10, 0, 0, 2})

	writeTunPacket(t, peer, packet)
	if got := readTunPacket(t, frontend); !bytes.Equal(got, packet) {
		t.Fatalf("frontend packet = %x, want %x", got, packet)
	}
}

func TestTunSplitterNodeRecoversInputAfterStartupReadFailure(t *testing.T) {
	w := newTunSplitterTestWorkspace(t)
	providers := newTestTunProviderRegistry()
	failed := make(chan struct{})
	recoveryRead := make(chan struct{})
	failRead := make(chan struct{})
	if err := w.AddNodeClass(testTunProviderClass{
		registry: providers,
		wrapSource: func(source tun.Tun) tun.Tun {
			return &failFirstTunRead{
				Tun:                source,
				trigger:            failRead,
				failedSignal:       failed,
				recoveryReadSignal: recoveryRead,
			}
		},
	}); err != nil {
		t.Fatalf("AddNodeClass provider: %v", err)
	}

	splitter, err := w.AddNodeByClass(NodeTypeTunSplitter, "splitter")
	if err != nil {
		t.Fatalf("AddNodeByClass TunSplitter: %v", err)
	}
	provider, err := w.AddNodeByClass(testTunProviderNodeType, "provider")
	if err != nil {
		t.Fatalf("AddNodeByClass provider: %v", err)
	}
	receiver := &tunPayloadReceiver{events: make(chan TunPayload, 4)}
	if _, err := w.AddNode(receiver, "test/TunReceiver", "receiver"); err != nil {
		t.Fatalf("AddNode receiver: %v", err)
	}

	w.SendNodeFormularMsg(splitter, formular.FormApplyMessage{
		MessageBase: testkit.FormMessageBase(formular.MessageFormApply, splitter),
		BlockID:     "config",
		Values:      map[string]any{"rules": "TRUE\nSLOT 1\n"},
	})
	splitterSnapshot := testkit.MustNodeSnapshot(t, w, splitter)
	providerSnapshot := testkit.MustNodeSnapshot(t, w, provider)
	if _, _, err := w.AddLink(splitterSnapshot.LeftPorts[0], providerSnapshot.RightPorts[0]); err != nil {
		t.Fatalf("AddLink provider->splitter: %v", err)
	}
	if _, _, err := w.AddLink(receiver.in, splitterSnapshot.RightPorts[0]); err != nil {
		t.Fatalf("AddLink splitter->receiver: %v", err)
	}

	frontend := receiveTunPayload(t, receiver.events).Tun
	peer := providers.peer(t, provider)
	testkit.Eventually(t, func() bool {
		return testkit.MustNodeSnapshot(t, w, splitter).Label == "splitting"
	}, "splitter attaches its initial input")
	close(failRead)
	select {
	case <-failed:
	case <-time.After(time.Second):
		t.Fatal("injected input read failure did not occur")
	}
	select {
	case <-recoveryRead:
	case <-time.After(time.Second):
		t.Fatal("replacement input did not start reading")
	}
	testkit.Eventually(t, func() bool {
		return providers.sendCount(provider) >= 2 &&
			testkit.MustNodeSnapshot(t, w, splitter).Label == "splitting"
	}, "splitter recovers its failed input attachment")
	testkit.Eventually(t, func() bool {
		for {
			select {
			case payload := <-receiver.events:
				frontend = payload.Tun
			default:
				return TunIsReady(frontend)
			}
		}
	}, "splitter publishes a ready output after input recovery")
	packet := testIPv4Packet([4]byte{10, 0, 0, 1}, [4]byte{10, 0, 0, 2})
	writeTunPacket(t, peer, packet)
	if got := readTunPacket(t, frontend); !bytes.Equal(got, packet) {
		t.Fatalf("frontend packet = %x, want %x", got, packet)
	}
}

func TestTunInputFailureObserverReportsTerminalFailureFromEvent(t *testing.T) {
	base, peer := tun.Pipe(1, 1500, 0, 0)
	root := tun.Detach(&failFirstTunRead{Tun: base}, nil, nil)
	reported := make(chan error, 1)
	observed := newTunInputFailureObserver(root, func(err error) { reported <- err })
	t.Cleanup(func() {
		_ = observed.Close()
		_ = base.Close()
		_ = peer.Close()
		root.Wait()
	})

	events := observed.Events()

waitForDown:
	for {
		select {
		case event, ok := <-events:
			if !ok {
				t.Fatal("observer event stream closed before EventDown")
			}
			if event == tun.EventDown {
				break waitForDown
			}
		case <-time.After(time.Second):
			t.Fatal("observer did not forward terminal EventDown")
		}
	}

	select {
	case err := <-reported:
		if !errors.Is(err, tun.ErrDetachedTunFailed) {
			t.Fatalf("reported error = %v, want ErrDetachedTunFailed", err)
		}
	case <-time.After(time.Second):
		t.Fatal("observer did not report terminal failure from EventDown")
	}
}

func TestTunSplitterNodeForwardsMergedTunConfig(t *testing.T) {
	w := newTunSplitterTestWorkspace(t)
	provider := &tunConfigReceiverNode{configs: make(chan TunConfig, 8)}
	_, err := w.AddNode(provider, "test/TunConfigReceiver", "provider")
	if err != nil {
		t.Fatalf("AddNode provider: %v", err)
	}
	splitter, err := w.AddNodeByClass(NodeTypeTunSplitter, "splitter")
	if err != nil {
		t.Fatalf("AddNodeByClass TunSplitter: %v", err)
	}
	defaultConsumer := &tunPayloadReceiver{events: make(chan TunPayload, 2)}
	defaultConsumerID, err := w.AddNode(defaultConsumer, "test/TunReceiver", "default consumer")
	if err != nil {
		t.Fatalf("AddNode default consumer: %v", err)
	}
	tailnetConsumer := &tunPayloadReceiver{events: make(chan TunPayload, 2)}
	tailnetConsumerID, err := w.AddNode(tailnetConsumer, "test/TunReceiver", "tailnet consumer")
	if err != nil {
		t.Fatalf("AddNode tailnet consumer: %v", err)
	}

	w.SendNodeFormularMsg(splitter, formular.FormApplyMessage{
		MessageBase: testkit.FormMessageBase(formular.MessageFormApply, splitter),
		BlockID:     "config",
		Values:      map[string]any{"rules": "TRUE\nSLOT 1\nTRUE\nSLOT 2\n"},
	})
	splitterSnapshot := testkit.MustNodeSnapshot(t, w, splitter)
	inputLink, _, err := w.AddLink(splitterSnapshot.LeftPorts[0], provider.out)
	if err != nil {
		t.Fatalf("AddLink provider->splitter: %v", err)
	}
	discardTunConfig(t, provider.configs)

	defaultLink, _, err := w.AddLink(defaultConsumer.in, splitterSnapshot.RightPorts[0])
	if err != nil {
		t.Fatalf("AddLink default consumer->splitter: %v", err)
	}
	tailnetLink, _, err := w.AddLink(tailnetConsumer.in, splitterSnapshot.RightPorts[1])
	if err != nil {
		t.Fatalf("AddLink tailnet consumer->splitter: %v", err)
	}

	defaultConfig := TunConfig{
		Addrs:  []string{"10.20.0.2/32", "2001:db8:ffff::2/128"},
		Routes: []string{"0.0.0.0/0", "::/0"},
		MTU:    1400,
		DNS:    "8.8.8.8",
	}.WithInferredSourceRoutes()
	w.EmitEvent(defaultConsumerID, defaultLink, TunConfig{
		Addrs:  defaultConfig.Addrs,
		Routes: defaultConfig.Routes,
		MTU:    defaultConfig.MTU,
		DNS:    defaultConfig.DNS,
	})
	assertTunConfigReceived(t, provider.configs, defaultConfig.WithoutDNS())

	tailnetSpecific := TunSourceRoute{
		Destination: netip.MustParsePrefix("100.80.0.0/16"),
		Source:      netip.MustParseAddr("100.80.0.2"),
	}
	tailnetConfig := TunConfig{
		Addrs: []string{
			"100.64.0.2/32",
			"100.80.0.2/32",
			"fd7a:115c:a1e0::2/128",
		},
		Routes: []string{
			"100.64.0.0/10",
			"100.80.0.0/16",
			"fd7a:115c:a1e0::/48",
		},
		SourceRoutes: []TunSourceRoute{tailnetSpecific},
		MTU:          1280,
		DNS:          "100.100.100.100",
	}.WithInferredSourceRoutes()
	w.EmitEvent(tailnetConsumerID, tailnetLink, TunConfig{
		Addrs:        tailnetConfig.Addrs,
		Routes:       tailnetConfig.Routes,
		SourceRoutes: []TunSourceRoute{tailnetSpecific},
		MTU:          tailnetConfig.MTU,
		DNS:          tailnetConfig.DNS,
	})
	wantMerged := TunConfigByLink{
		1: defaultConfig.WithoutDNS(),
		2: tailnetConfig.WithoutDNS(),
	}.Merge()
	assertTunConfigReceived(t, provider.configs, wantMerged)

	w.RemoveLink(tailnetLink)
	assertTunConfigReceived(t, provider.configs, defaultConfig.WithoutDNS())

	w.RemoveLink(inputLink)
	w.RemoveLink(defaultLink)
}

func TestVTunTunSplitterSelectsSourceAddressForComplexDestinationRoutes(t *testing.T) {
	w := newVTunTunSplitterTestWorkspace(t)
	vtunID, err := w.AddNodeByClass(NodeTypeVTun, "vtun")
	if err != nil {
		t.Fatalf("AddNodeByClass VTun: %v", err)
	}
	splitterID, err := w.AddNodeByClass(NodeTypeTunSplitter, "splitter")
	if err != nil {
		t.Fatalf("AddNodeByClass TunSplitter: %v", err)
	}
	netReceiver := networktest.NewReceiver(4)
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

	w.SendNodeFormularMsg(splitterID, formular.FormApplyMessage{
		MessageBase: testkit.FormMessageBase(formular.MessageFormApply, splitterID),
		BlockID:     TunSplitterConfigBlockID,
		Values: map[string]any{"rules": "SNET4 100.64.0.0/10\nSLOT 2\n\n" +
			"SNET6 fd7a:115c:a1e0::/48\nSLOT 2\n\nTRUE\nSLOT 1\n"},
	})
	vtunSnapshot := testkit.MustNodeSnapshot(t, w, vtunID)
	splitterSnapshot := testkit.MustNodeSnapshot(t, w, splitterID)
	if _, _, err := w.AddLink(vtunSnapshot.LeftPorts[0], netReceiver.In); err != nil {
		t.Fatalf("AddLink VTun network: %v", err)
	}
	_ = networktest.ReceivePayload(t, netReceiver.Events)
	if _, _, err := w.AddLink(splitterSnapshot.LeftPorts[0], vtunSnapshot.RightPorts[0]); err != nil {
		t.Fatalf("AddLink VTun->TunSplitter: %v", err)
	}
	defaultLink, _, err := w.AddLink(defaultConsumer.in, splitterSnapshot.RightPorts[0])
	if err != nil {
		t.Fatalf("AddLink default consumer: %v", err)
	}
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
			name:        "IPv4 Tailnet route",
			destination: netip.MustParseAddr("100.97.143.122"),
			wantSource:  carrier4,
			frontend:    tailnetFrontend,
		},
		{
			name:        "IPv4 longest-prefix route",
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
			name:        "IPv6 Tailnet route",
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

	// Change only the source policy. VTun must update the existing stack, and
	// new flows must use the new source without a Network or TUN replacement.
	tailnetConfig.SourceRoutes = []TunSourceRoute{
		{Destination: netip.MustParsePrefix("100.80.0.0/16"), Source: specific4},
		{Destination: netip.MustParsePrefix("100.64.0.0/10"), Source: specific4},
	}
	w.EmitEvent(tailnetConsumerID, tailnetLink, tailnetConfig)
	networktest.AssertNoEvent(t, netReceiver.Events)
	writeVTunUDPAndAssertPacket(
		t,
		network,
		tailnetFrontend,
		netip.MustParseAddr("100.97.143.122"),
		specific4,
	)
}

func newVTunTunSplitterTestWorkspace(t *testing.T) *pasta.Workspace {
	t.Helper()
	w := pasta.NewWorkspace(logging.NewDebugLogFactory(1024))
	t.Cleanup(w.Close)
	runtime := &tunSplitterTestRuntime{sys: &sysnetdebug.System{}}
	for _, class := range []pasta.NodeClass{
		VTunClass{Runtime: runtime},
		AutoTunSplitterClass{Runtime: runtime},
		TunSplitterClass{Runtime: runtime},
	} {
		if err := w.AddNodeClass(class); err != nil {
			t.Fatalf("AddNodeClass %s: %v", class.ClassName(), err)
		}
	}
	return w
}

func writeVTunUDPAndAssertPacket(
	t *testing.T,
	network gonnect.Network,
	frontend tun.Tun,
	destination, wantSource netip.Addr,
) {
	t.Helper()
	networkName := "udp4"
	if destination.Is6() {
		networkName = "udp6"
	}
	conn, err := network.DialUDP(
		context.Background(),
		networkName,
		"",
		netip.AddrPortFrom(destination, 42000).String(),
	)
	if err != nil {
		t.Fatalf("DialUDP to %s: %v", destination, err)
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.Write([]byte("vtun-tun-splitter-source-route")); err != nil {
		t.Fatalf("UDP Write to %s: %v", destination, err)
	}
	packet := readTunPacket(t, frontend)
	gotSource, gotDestination := tunPacketAddresses(t, packet)
	if gotDestination != destination {
		t.Fatalf("packet destination = %s, want %s", gotDestination, destination)
	}
	if gotSource != wantSource {
		t.Fatalf("packet source to %s = %s, want %s", destination, gotSource, wantSource)
	}
}

func tunPacketAddresses(t *testing.T, packet []byte) (source, destination netip.Addr) {
	t.Helper()
	if len(packet) == 0 {
		t.Fatal("empty IP packet")
	}
	switch packet[0] >> 4 {
	case 4:
		if len(packet) < 20 {
			t.Fatalf("short IPv4 packet: %d bytes", len(packet))
		}
		source, _ = netip.AddrFromSlice(packet[12:16])
		destination, _ = netip.AddrFromSlice(packet[16:20])
	case 6:
		if len(packet) < 40 {
			t.Fatalf("short IPv6 packet: %d bytes", len(packet))
		}
		source, _ = netip.AddrFromSlice(packet[8:24])
		destination, _ = netip.AddrFromSlice(packet[24:40])
	default:
		t.Fatalf("unsupported IP version %d", packet[0]>>4)
	}
	return source, destination
}

func TestTunSplitterNodeValidationReportsStatus(t *testing.T) {
	w := newTunSplitterTestWorkspace(t)
	state := formular.NewMenuSnapshotState()
	sub := w.SubscribeNotifications(func(notification pasta.WorkspaceNotification) {
		if notification.Kind == pasta.NotificationNodeMenu {
			state.Apply(notification.Formular)
		}
	})
	if sub == 0 {
		t.Fatal("Subscribe returned 0")
	}
	defer w.UnsubscribeNotifications(sub)

	splitter, err := w.AddNodeByClass(NodeTypeTunSplitter, "splitter")
	if err != nil {
		t.Fatalf("AddNodeByClass TunSplitter: %v", err)
	}
	w.SubscribeNodeMenu(splitter, 1)

	w.SendNodeFormularMsg(splitter, formular.FieldValidateMessage{
		MessageBase: formular.MessageBase{Type: formular.MessageFieldValidate, MenuID: pasta.NodeMenuID(splitter)},
		Field:       formular.FieldRef{BlockID: "config", FieldID: "rules"},
		Value:       "DIAL\nSLOT 1\n",
	})
	assertTunSplitterRulesStatus(t, state, splitter, formular.StatusError)

	w.SendNodeFormularMsg(splitter, formular.FieldValidateMessage{
		MessageBase: formular.MessageBase{Type: formular.MessageFieldValidate, MenuID: pasta.NodeMenuID(splitter)},
		Field:       formular.FieldRef{BlockID: "config", FieldID: "rules"},
		Value:       "TRUE\nSLOT 1\n",
	})
	assertTunSplitterRulesStatus(t, state, splitter, formular.StatusOK)

	w.SendNodeFormularMsg(splitter, formular.FieldValidateMessage{
		MessageBase: testkit.FormMessageBase(formular.MessageFieldValidate, splitter),
		Field:       formular.FieldRef{BlockID: tunSplitterConfigBlockID, FieldID: tunSplitterDNSCacheStorageNameField},
		Value:       string([]byte{0xff}),
	})
	assertMenuItemStatus(t, state, splitter, tunSplitterDNSCacheStorageNameField, formular.StatusError)

	w.SendNodeFormularMsg(splitter, formular.FieldValidateMessage{
		MessageBase: testkit.FormMessageBase(formular.MessageFieldValidate, splitter),
		Field:       formular.FieldRef{BlockID: tunSplitterConfigBlockID, FieldID: tunSplitterRulesField},
		Value:       "TRUE\nSLOT 1\n",
	})
	assertTunSplitterRulesStatus(t, state, splitter, formular.StatusOK)
}

func TestTunSplitterNodeMenuAppliesAndSavesRoutingCacheConfig(t *testing.T) {
	manager := newMemoryDNSCacheManager()
	manager.Storage("shared")
	manager.Storage("other")
	w := newTunSplitterTestWorkspaceWithDNSCacheManager(t, manager)
	state := formular.NewMenuSnapshotState()
	sub := w.SubscribeNotifications(func(notification pasta.WorkspaceNotification) {
		if notification.Kind == pasta.NotificationNodeMenu {
			state.Apply(notification.Formular)
		}
	})
	if sub == 0 {
		t.Fatal("Subscribe returned 0")
	}
	defer w.UnsubscribeNotifications(sub)

	splitter, err := w.AddNodeByClass(NodeTypeTunSplitter, "splitter")
	if err != nil {
		t.Fatalf("AddNodeByClass TunSplitter: %v", err)
	}
	w.SubscribeNodeMenu(splitter, 1)
	assertTunSplitterConfigFieldValue(t, state, splitter, "ruleCacheTTL", 0)
	assertTunSplitterConfigFieldValue(t, state, splitter, "ruleCacheMaxEntries", 0)
	assertTunSplitterConfigFieldValue(t, state, splitter, "routeCacheTTL", 0)
	assertTunSplitterConfigFieldValue(t, state, splitter, "routeCacheMaxEntries", 0)
	assertTunSplitterConfigFieldValue(t, state, splitter, tunSplitterDNSCacheStorageNameField, "")
	assertConfigFieldAllowedValues(t, state, splitter, tunSplitterConfigBlockID, tunSplitterDNSCacheStorageNameField, []any{"", "other", "shared"})

	w.SendNodeFormularMsg(splitter, formular.FormApplyMessage{
		MessageBase: testkit.FormMessageBase(formular.MessageFormApply, splitter),
		BlockID:     "config",
		Values: map[string]any{
			"rules":                             "TRUE\nSLOT 1\n",
			"ruleCacheTTL":                      1500,
			"ruleCacheMaxEntries":               32,
			"routeCacheTTL":                     -1,
			"routeCacheMaxEntries":              -1,
			tunSplitterDNSCacheStorageNameField: "shared",
		},
	})
	assertTunSplitterConfigFieldValue(t, state, splitter, "ruleCacheTTL", 1500)
	assertTunSplitterConfigFieldValue(t, state, splitter, "ruleCacheMaxEntries", 32)
	assertTunSplitterConfigFieldValue(t, state, splitter, "routeCacheTTL", -1)
	assertTunSplitterConfigFieldValue(t, state, splitter, "routeCacheMaxEntries", -1)
	assertTunSplitterConfigFieldValue(t, state, splitter, tunSplitterDNSCacheStorageNameField, "shared")

	w.SendNodeFormularMsg(splitter, formular.FormApplyMessage{
		MessageBase: testkit.FormMessageBase(formular.MessageFormApply, splitter),
		BlockID:     "config",
		Values:      map[string]any{"rules": "TRUE\nSLOT 2\n"},
	})
	assertTunSplitterConfigFieldValue(t, state, splitter, "ruleCacheTTL", 1500)
	assertTunSplitterConfigFieldValue(t, state, splitter, "ruleCacheMaxEntries", 32)
	assertTunSplitterConfigFieldValue(t, state, splitter, "routeCacheTTL", -1)
	assertTunSplitterConfigFieldValue(t, state, splitter, "routeCacheMaxEntries", -1)
	assertTunSplitterConfigFieldValue(t, state, splitter, tunSplitterDNSCacheStorageNameField, "shared")

	cfg := configer.NewMemory(nil)
	if err := w.SaveConfig(cfg); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	wantSaved := map[string]any{
		"rules":                             "TRUE\nSLOT 2\n",
		"ruleCacheTTL":                      1500,
		"ruleCacheMaxEntries":               32,
		"routeCacheTTL":                     -1,
		"routeCacheMaxEntries":              -1,
		tunSplitterDNSCacheStorageNameField: "shared",
	}
	for key, want := range wantSaved {
		got, err := cfg.Get(configer.Path{"splitter", key})
		if err != nil {
			t.Fatalf("saved splitter %s missing: %v", key, err)
		}
		if !sameTunSplitterConfigValue(got, want) {
			t.Fatalf("saved splitter %s = %#v, want %#v", key, got, want)
		}
	}
}

func newTunSplitterTestWorkspace(t *testing.T) *pasta.Workspace {
	t.Helper()
	return newTunSplitterTestWorkspaceWithDNSCacheManager(t, nil)
}

func newTunSplitterTestWorkspaceWithDNSCacheManager(t *testing.T, manager storage.DnsCacheManager) *pasta.Workspace {
	t.Helper()
	w := pasta.NewWorkspace(logging.NewDebugLogFactory(1024))
	t.Cleanup(func() { w.Close() })
	runtime := &tunSplitterTestRuntime{sys: &sysnetdebug.System{}, dns: manager}
	for _, class := range []pasta.NodeClass{
		AutoTunSplitterClass{Runtime: runtime},
		TunSplitterClass{Runtime: runtime},
	} {
		if err := w.AddNodeClass(class); err != nil {
			t.Fatalf("AddNodeClass %s: %v", class.ClassName(), err)
		}
	}
	return w
}

func assertTunSplitterRightPorts(t *testing.T, w *pasta.Workspace, node uint64, want []string) {
	t.Helper()
	snapshot := testkit.MustNodeSnapshot(t, w, node)
	got := make([]string, 0, len(snapshot.RightPorts))
	for _, port := range snapshot.RightPorts {
		ps, ok := w.PortSnapshot(port)
		if !ok {
			t.Fatalf("PortSnapshot(%d) missing", port)
		}
		got = append(got, ps.Name)
	}
	testkit.AssertStringSlice(t, got, want)
}

func assertTunSplitterConfigFieldValue(t *testing.T, state *formular.MenuSnapshotState, node uint64, field string, want any) {
	t.Helper()
	snapshot, ok := state.Snapshot(pasta.NodeMenuID(node))
	if !ok {
		t.Fatal("menu snapshot missing")
	}
	item, ok := testkit.FindItem(testkit.MustFindBlock(t, snapshot, "config"), field)
	if !ok {
		t.Fatalf("%s item missing", field)
	}
	if item.Value != want {
		t.Fatalf("%s value = %#v, want %#v", field, item.Value, want)
	}
}

func sameTunSplitterConfigValue(got, want any) bool {
	if got == want {
		return true
	}
	wantInt, wantOK := formular.IntValue(want)
	gotInt, gotOK := formular.IntValue(got)
	return wantOK && gotOK && gotInt == wantInt
}

func assertTunSplitterRulesStatus(t *testing.T, state *formular.MenuSnapshotState, node uint64, want string) {
	t.Helper()
	snapshot, ok := state.Snapshot(pasta.NodeMenuID(node))
	if !ok {
		t.Fatal("menu snapshot missing")
	}
	item, ok := testkit.FindItem(testkit.MustFindBlock(t, snapshot, "config"), "rules")
	if !ok {
		t.Fatal("rules item missing")
	}
	if item.Status != want {
		t.Fatalf("rules status = %q, want %q", item.Status, want)
	}
}

type tunConfigReceiverNode struct {
	pasta.BasicNode
	out     uint64
	configs chan TunConfig
}

func (n *tunConfigReceiverNode) OnInit(w *pasta.Workspace, _ pasta.Logger, id uint64, _ string, restored *pasta.NodeInitData, _, _, _, _ bool) error {
	if restored != nil && len(restored.RightPorts) > 0 {
		n.out = restored.RightPorts[0]
		return nil
	}
	port, err := w.AddPortLocked(pasta.Port{Node: id, Direction: "right", Name: "Tun", Types: []string{TypeTun}})
	if err != nil {
		return err
	}
	n.out = port
	return nil
}

func (n *tunConfigReceiverNode) PreLinkAdd(port uint64, linkType, portDirection string) error {
	if port != n.out || portDirection != "right" {
		return pasta.LinkTypeErr(linkType)
	}
	return TunPreLinkAdd(nil, 0, linkType)
}

func (n *tunConfigReceiverNode) OnEvent(event pasta.Event, linkType string, _ []string, receiverPortDirection string) error {
	if event.ReceiverPort != n.out || receiverPortDirection != "right" || linkType != TypeTun || std.IsRequest(event.Payload) {
		return nil
	}
	cfg, ok := event.Payload.(TunConfig)
	if ok {
		n.configs <- cfg
	}
	return nil
}

func discardTunConfig(t *testing.T, configs <-chan TunConfig) {
	t.Helper()
	select {
	case <-configs:
	default:
	}
}

func assertTunConfigReceived(t *testing.T, configs <-chan TunConfig, want TunConfig) {
	t.Helper()
	select {
	case got := <-configs:
		testkit.AssertStringSlice(t, got.Addrs, want.Addrs)
		testkit.AssertStringSlice(t, got.Routes, want.Routes)
		if !slices.Equal(got.SourceRoutes, want.SourceRoutes) {
			t.Fatalf("TunConfig SourceRoutes = %#v, want %#v", got.SourceRoutes, want.SourceRoutes)
		}
		if got.MTU != want.MTU {
			t.Fatalf("TunConfig MTU = %d, want %d", got.MTU, want.MTU)
		}
		if got.DNS != want.DNS {
			t.Fatalf("TunConfig DNS = %q, want %q", got.DNS, want.DNS)
		}
	default:
		t.Fatalf("no TunConfig received, want %#v", want)
	}
}

func assertMenuItemStatus(t *testing.T, state *formular.MenuSnapshotState, node uint64, fieldID string, status string) {
	t.Helper()
	snapshot, ok := state.Snapshot(pasta.NodeMenuID(node))
	if !ok {
		t.Fatal("menu snapshot missing")
	}
	for _, block := range snapshot.Blocks {
		for _, item := range block.Items {
			if item.ID == fieldID {
				if item.Status != status {
					t.Fatalf("%s status = %q, want %q", fieldID, item.Status, status)
				}
				return
			}
		}
	}
	t.Fatalf("%s item missing", fieldID)
}

func assertConfigFieldAllowedValues(t *testing.T, state *formular.MenuSnapshotState, node uint64, blockID, fieldID string, want []any) {
	t.Helper()
	snapshot, ok := state.Snapshot(pasta.NodeMenuID(node))
	if !ok {
		t.Fatal("menu snapshot missing")
	}
	item, ok := testkit.FindItem(testkit.MustFindBlock(t, snapshot, blockID), fieldID)
	if !ok {
		t.Fatalf("%s item missing", fieldID)
	}
	got := item.AllowedValues
	if len(got) != len(want) {
		t.Fatalf("%s allowed values = %#v, want %#v", fieldID, got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("%s allowed values = %#v, want %#v", fieldID, got, want)
		}
	}
}

type tunSplitterTestRuntime struct {
	sys sysnet.System
	dns storage.DnsCacheManager
}

func (r *tunSplitterTestRuntime) Context() context.Context {
	return context.Background()
}

func (r *tunSplitterTestRuntime) Pool() bufpool.Pool {
	return nil
}

func (r *tunSplitterTestRuntime) System() sysnet.System {
	return r.sys
}

func (r *tunSplitterTestRuntime) KVManager() storage.KVManager {
	return nil
}

func (r *tunSplitterTestRuntime) SecretsStorage() storage.SecretsStorage {
	return nil
}

func (r *tunSplitterTestRuntime) TlsConfigStorage() storage.TlsConfigStorage {
	return nil
}

func (r *tunSplitterTestRuntime) WorkerSpawner(uint64) WorkerSpawner {
	return nil
}

func (r *tunSplitterTestRuntime) ObserveNodeSnapshot(uint64, pasta.NodeSnapshot) {}

func (r *tunSplitterTestRuntime) DnsCacheManager() storage.DnsCacheManager {
	return r.dns
}

type testTunProviderRegistry struct {
	mu    sync.Mutex
	nodes map[uint64]*testTunProviderNode
}

func newTestTunProviderRegistry() *testTunProviderRegistry {
	return &testTunProviderRegistry{nodes: map[uint64]*testTunProviderNode{}}
}

func (r *testTunProviderRegistry) register(id uint64, node *testTunProviderNode) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.nodes[id] = node
}

func (r *testTunProviderRegistry) peer(t *testing.T, id uint64) tun.Tun {
	t.Helper()
	var peer tun.Tun
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		r.mu.Lock()
		node := r.nodes[id]
		if node != nil {
			peer = node.peer
		}
		r.mu.Unlock()
		if peer != nil {
			return peer
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("test tun provider %d peer not ready", id)
	return nil
}

func (r *testTunProviderRegistry) sendCount(id uint64) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	if node := r.nodes[id]; node != nil {
		return node.sendCount
	}
	return 0
}

type testTunProviderClass struct {
	registry   *testTunProviderRegistry
	wrapSource func(tun.Tun) tun.Tun
}

const testTunProviderNodeType = "test/TunProvider"

func (testTunProviderClass) ClassName() string        { return testTunProviderNodeType }
func (testTunProviderClass) ShortDescription() string { return "test TUN provider" }
func (testTunProviderClass) LongDescription() string {
	return "Test-only right-directed almagest/tun provider backed by tun.Pipe."
}
func (testTunProviderClass) DefaultNodeParams() pasta.NodeClassParams {
	return pasta.NodeClassParams{PrimaryType: TypeTun, InitialPorts: []pasta.Port{
		TunPort("right", "Tun"),
	}}
}
func (c testTunProviderClass) NewNode(configer.Config, ...*pasta.NodeClassState) (pasta.Node, error) {
	return &testTunProviderNode{registry: c.registry, wrapSource: c.wrapSource}, nil
}

type testTunProviderNode struct {
	NodeBase

	registry   *testTunProviderRegistry
	out        uint64
	wrapSource func(tun.Tun) tun.Tun

	source    tun.Tun
	peer      tun.Tun
	outputs   TunOutputCache
	sendCount int
}

func (n *testTunProviderNode) OnInit(w *pasta.Workspace, logger pasta.Logger, id uint64, _ string, restored *pasta.NodeInitData, _, _, _, _ bool) error {
	n.InitBaseNode(w, logger, id)
	n.out = restoredRight(restored, 0)
	if n.registry != nil {
		n.registry.register(id, n)
	}
	if err := n.SetPrimary(TypeTun); err != nil {
		return err
	}
	return n.SetLabel("ready")
}

func (n *testTunProviderNode) OnStop() {
	n.outputs.CloseAll()
	n.closePipe()
	n.outputs.Wait()
}

func (n *testTunProviderNode) OnReady() error {
	n.sendAll()
	return nil
}

func (n *testTunProviderNode) PreLinkAdd(port uint64, linkType, portDirection string) error {
	if port != n.out || portDirection != "right" {
		return pasta.LinkTypeErr(linkType)
	}
	return TunPreLinkAdd(n.W, port, linkType)
}

func (n *testTunProviderNode) OnLinkAdd(link, port uint64, linkType, portDirection string) error {
	if port == n.out && portDirection == "right" && linkType == TypeTun {
		n.sendTunToLink(link)
	}
	return nil
}

func (n *testTunProviderNode) OnLinkRemoved(link uint64, port uint64, linkType, portDirection string) error {
	if port == n.out && portDirection == "right" && linkType == TypeTun {
		n.outputs.Close(link)
		n.closePipe()
	}
	return nil
}

func (n *testTunProviderNode) OnEvent(event pasta.Event, linkType string, _ []string, receiverPortDirection string) error {
	if event.ReceiverPort != n.out || receiverPortDirection != "right" || linkType != TypeTun || !std.IsRequest(event.Payload) {
		return nil
	}
	n.sendTunToLink(event.Link)
	return nil
}

func (n *testTunProviderNode) sendAll() {
	n.ForEachPortLink(n.out, func(link uint64) {
		n.sendTunToLink(link)
	})
}

func (n *testTunProviderNode) sendTunToLink(link uint64) {
	n.ensurePipe()
	n.outputs.SendLocked(n.W, n.ID, link, n.source, nil)
	if n.registry != nil {
		n.registry.mu.Lock()
		n.sendCount++
		n.registry.mu.Unlock()
	}
}

func (n *testTunProviderNode) ensurePipe() {
	if n.source != nil && n.peer != nil {
		return
	}
	n.source, n.peer = tun.Pipe(4, 1500, 0, 0)
	if n.wrapSource != nil {
		n.source = n.wrapSource(n.source)
	}
}

type failFirstTunRead struct {
	tun.Tun
	mu                 sync.Mutex
	failed             bool
	trigger            <-chan struct{}
	failedSignal       chan struct{}
	recoveryOnce       sync.Once
	recoveryReadSignal chan struct{}
}

func (t *failFirstTunRead) Read(bufs [][]byte, sizes []int, offset int) (int, error) {
	t.mu.Lock()
	if !t.failed {
		t.failed = true
		t.mu.Unlock()
		if t.trigger != nil {
			<-t.trigger
		}
		if t.failedSignal != nil {
			close(t.failedSignal)
		}
		return 0, errors.New("injected startup read failure")
	}
	t.mu.Unlock()
	if t.recoveryReadSignal != nil {
		t.recoveryOnce.Do(func() { close(t.recoveryReadSignal) })
	}
	return t.Tun.Read(bufs, sizes, offset)
}

func (n *testTunProviderNode) closePipe() {
	if n.source != nil {
		_ = n.source.Close()
		n.source = nil
	}
	if n.peer != nil {
		_ = n.peer.Close()
		n.peer = nil
	}
}

func writeTunPacket(t *testing.T, target tun.Tun, packet []byte) {
	t.Helper()
	offset := target.MWO()
	buf := make([]byte, offset+len(packet))
	copy(buf[offset:], packet)
	n, err := target.Write([][]byte{buf}, offset)
	if err != nil {
		t.Fatalf("tun Write: %v", err)
	}
	if n != 1 {
		t.Fatalf("tun Write count = %d, want 1", n)
	}
}

func readTunPacket(t *testing.T, source tun.Tun) []byte {
	t.Helper()
	type result struct {
		packet []byte
		err    error
	}
	ch := make(chan result, 1)
	go func() {
		offset := source.MRO()
		buf := make([]byte, offset+2048)
		sizes := []int{0}
		n, err := source.Read([][]byte{buf}, sizes, offset)
		if err != nil {
			ch <- result{err: err}
			return
		}
		if n != 1 {
			ch <- result{err: fmt.Errorf("read packet count %d, want 1", n)}
			return
		}
		packet := append([]byte(nil), buf[offset:offset+sizes[0]]...)
		ch <- result{packet: packet}
	}()
	select {
	case got := <-ch:
		if got.err != nil {
			t.Fatalf("tun Read: %v", got.err)
		}
		return got.packet
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for tun packet")
		return nil
	}
}

func testIPv4Packet(src, dst [4]byte) []byte {
	p := make([]byte, 20)
	p[0] = 0x45
	binary.BigEndian.PutUint16(p[2:4], uint16(len(p)))
	copy(p[12:16], src[:])
	copy(p[16:20], dst[:])
	return p
}

type memoryDNSCacheManager struct {
	mu       sync.Mutex
	storages map[string]dns.CacheStorage
}

func newMemoryDNSCacheManager() *memoryDNSCacheManager {
	return &memoryDNSCacheManager{storages: map[string]dns.CacheStorage{}}
}

func (m *memoryDNSCacheManager) Storage(name string) dns.CacheStorage {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.storages == nil {
		m.storages = map[string]dns.CacheStorage{}
	}
	storage := m.storages[name]
	if storage == nil {
		storage = dns.NewMemoryStorage()
		m.storages[name] = storage
	}
	return storage
}

func (m *memoryDNSCacheManager) StorageNames() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	names := make([]string, 0, len(m.storages))
	for name := range m.storages {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}
