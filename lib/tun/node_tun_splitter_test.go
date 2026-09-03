package tunnodes

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/asciimoth/almagest/lib/logging"
	"github.com/asciimoth/almagest/lib/storage"
	"github.com/asciimoth/almagest/lib/testkit"
	"github.com/asciimoth/bufpool"
	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/formular"
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

	outputLink, _, err := w.AddLink(consumer.in, splitterSnapshot.RightPorts[0])
	if err != nil {
		t.Fatalf("AddLink consumer->splitter: %v", err)
	}
	w.EmitEvent(consumerID, outputLink, TunConfig{Addrs: []string{"10.0.0.2/32"}, MTU: 1400, DNS: "8.8.8.8"})
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
	if err := w.AddNodeClass(TunSplitterClass{Runtime: &tunSplitterTestRuntime{sys: &sysnetdebug.System{}, dns: manager}}); err != nil {
		t.Fatalf("AddNodeClass TunSplitter: %v", err)
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

type testTunProviderClass struct {
	registry *testTunProviderRegistry
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
	return &testTunProviderNode{registry: c.registry}, nil
}

type testTunProviderNode struct {
	NodeBase

	registry *testTunProviderRegistry
	out      uint64

	source  tun.Tun
	peer    tun.Tun
	outputs TunOutputCache
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
}

func (n *testTunProviderNode) ensurePipe() {
	if n.source != nil && n.peer != nil {
		return
	}
	n.source, n.peer = tun.Pipe(4, 1500, 0, 0)
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
