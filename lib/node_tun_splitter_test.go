package almagest

import (
	"bytes"
	"testing"

	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/formular"
	sysnetdebug "github.com/asciimoth/gonnect/sysnet/debug"
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
		MessageBase: FormMessageBase(formular.MessageFormApply, splitter),
		BlockID:     "config",
		Values:      map[string]any{"rules": "TRUE\nSLOT 3\n"},
	})
	assertTunSplitterRightPorts(t, w, splitter, []string{"slot 1", "slot 3"})

	receiver := &tunPayloadReceiver{events: make(chan TunPayload, 2)}
	if _, err := w.AddNode(receiver, "test/TunReceiver", "receiver"); err != nil {
		t.Fatalf("AddNode receiver: %v", err)
	}
	snapshot := mustNodeSnapshot(t, w, splitter)
	if _, _, err := w.AddLink(receiver.in, snapshot.RightPorts[0]); err != nil {
		t.Fatalf("AddLink receiver->splitter slot 1: %v", err)
	}

	w.SendNodeFormularMsg(splitter, formular.FormApplyMessage{
		MessageBase: FormMessageBase(formular.MessageFormApply, splitter),
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
		MessageBase: FormMessageBase(formular.MessageFormApply, splitter),
		BlockID:     "config",
		Values:      map[string]any{"rules": "TRUE\nSLOT 1\n"},
	})
	splitterSnapshot := mustNodeSnapshot(t, w, splitter)
	providerSnapshot := mustNodeSnapshot(t, w, provider)
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
	consumer := &tunPayloadReceiver{events: make(chan TunPayload, 2)}
	consumerID, err := w.AddNode(consumer, "test/TunReceiver", "consumer")
	if err != nil {
		t.Fatalf("AddNode consumer: %v", err)
	}

	w.SendNodeFormularMsg(splitter, formular.FormApplyMessage{
		MessageBase: FormMessageBase(formular.MessageFormApply, splitter),
		BlockID:     "config",
		Values:      map[string]any{"rules": "TRUE\nSLOT 1\nTRUE\nSLOT 2\n"},
	})
	splitterSnapshot := mustNodeSnapshot(t, w, splitter)
	inputLink, _, err := w.AddLink(splitterSnapshot.LeftPorts[0], provider.out)
	if err != nil {
		t.Fatalf("AddLink provider->splitter: %v", err)
	}
	discardTunConfig(t, provider.configs)

	outputLink, _, err := w.AddLink(consumer.in, splitterSnapshot.RightPorts[0])
	if err != nil {
		t.Fatalf("AddLink consumer->splitter: %v", err)
	}
	w.EmitEvent(consumerID, outputLink, TunConfig{Addrs: []string{"10.0.0.2/32"}, MTU: 1400})
	assertTunConfigReceived(t, provider.configs, TunConfig{Addrs: []string{"10.0.0.2/32"}, MTU: 1400})

	w.RemoveLink(inputLink)
	w.RemoveLink(outputLink)
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
}

func TestTunSplitterNodeMenuAppliesAndSavesRoutingCacheConfig(t *testing.T) {
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
	assertTunSplitterConfigFieldValue(t, state, splitter, "ruleCacheTTL", 0)
	assertTunSplitterConfigFieldValue(t, state, splitter, "ruleCacheMaxEntries", 0)
	assertTunSplitterConfigFieldValue(t, state, splitter, "routeCacheTTL", 0)
	assertTunSplitterConfigFieldValue(t, state, splitter, "routeCacheMaxEntries", 0)

	w.SendNodeFormularMsg(splitter, formular.FormApplyMessage{
		MessageBase: FormMessageBase(formular.MessageFormApply, splitter),
		BlockID:     "config",
		Values: map[string]any{
			"rules":                "TRUE\nSLOT 1\n",
			"ruleCacheTTL":         1500,
			"ruleCacheMaxEntries":  32,
			"routeCacheTTL":        -1,
			"routeCacheMaxEntries": -1,
		},
	})
	assertTunSplitterConfigFieldValue(t, state, splitter, "ruleCacheTTL", 1500)
	assertTunSplitterConfigFieldValue(t, state, splitter, "ruleCacheMaxEntries", 32)
	assertTunSplitterConfigFieldValue(t, state, splitter, "routeCacheTTL", -1)
	assertTunSplitterConfigFieldValue(t, state, splitter, "routeCacheMaxEntries", -1)

	w.SendNodeFormularMsg(splitter, formular.FormApplyMessage{
		MessageBase: FormMessageBase(formular.MessageFormApply, splitter),
		BlockID:     "config",
		Values:      map[string]any{"rules": "TRUE\nSLOT 2\n"},
	})
	assertTunSplitterConfigFieldValue(t, state, splitter, "ruleCacheTTL", 1500)
	assertTunSplitterConfigFieldValue(t, state, splitter, "ruleCacheMaxEntries", 32)
	assertTunSplitterConfigFieldValue(t, state, splitter, "routeCacheTTL", -1)
	assertTunSplitterConfigFieldValue(t, state, splitter, "routeCacheMaxEntries", -1)

	cfg := configer.NewMemory(nil)
	if err := w.SaveConfig(cfg); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	wantSaved := map[string]any{
		"rules":                "TRUE\nSLOT 2\n",
		"ruleCacheTTL":         1500,
		"ruleCacheMaxEntries":  32,
		"routeCacheTTL":        -1,
		"routeCacheMaxEntries": -1,
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
	w := pasta.NewWorkspace(NewDebugLogFactory(1024))
	t.Cleanup(func() { w.Close() })
	if err := w.AddNodeClass(tunSplitterClass{engine: &engine{sys: &sysnetdebug.System{}}}); err != nil {
		t.Fatalf("AddNodeClass TunSplitter: %v", err)
	}
	return w
}

func assertTunSplitterRightPorts(t *testing.T, w *pasta.Workspace, node uint64, want []string) {
	t.Helper()
	snapshot := mustNodeSnapshot(t, w, node)
	got := make([]string, 0, len(snapshot.RightPorts))
	for _, port := range snapshot.RightPorts {
		ps, ok := w.PortSnapshot(port)
		if !ok {
			t.Fatalf("PortSnapshot(%d) missing", port)
		}
		got = append(got, ps.Name)
	}
	assertStringSlice(t, got, want)
}

func assertTunSplitterConfigFieldValue(t *testing.T, state *formular.MenuSnapshotState, node uint64, field string, want any) {
	t.Helper()
	snapshot, ok := state.Snapshot(pasta.NodeMenuID(node))
	if !ok {
		t.Fatal("menu snapshot missing")
	}
	item, ok := FindItem(mustFindBlock(t, snapshot, "config"), field)
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
	item, ok := FindItem(mustFindBlock(t, snapshot, "config"), "rules")
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
		assertStringSlice(t, got.Addrs, want.Addrs)
		assertStringSlice(t, got.Routes, want.Routes)
		if got.MTU != want.MTU {
			t.Fatalf("TunConfig MTU = %d, want %d", got.MTU, want.MTU)
		}
	default:
		t.Fatalf("no TunConfig received, want %#v", want)
	}
}
