package almagest

import (
	"context"
	"strings"
	"testing"

	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/formular"
	"github.com/asciimoth/gonnect"
	sysnetdebug "github.com/asciimoth/gonnect/sysnet/debug"
	"github.com/asciimoth/pasta/pasta"
)

func TestVTunNodeEmitsNetworkAndTunAndRebuildsOnFormApply(t *testing.T) {
	w, _ := newNetstackNodeTestWorkspace(t)
	vtunID, err := w.AddNodeByClass(NodeTypeVTun, "vtun")
	if err != nil {
		t.Fatalf("AddNodeByClass VTun: %v", err)
	}
	netReceiver := &outNetReceiver{events: make(chan NetworkPayload, 3)}
	if _, err := w.AddNode(netReceiver, "test/NetworkReceiver", "network receiver"); err != nil {
		t.Fatalf("AddNode network receiver: %v", err)
	}
	tunReceiver := &tunPayloadReceiver{events: make(chan TunPayload, 3)}
	if _, err := w.AddNode(tunReceiver, "test/TunReceiver", "tun receiver"); err != nil {
		t.Fatalf("AddNode tun receiver: %v", err)
	}

	vtunSnapshot := mustNodeSnapshot(t, w, vtunID)
	if _, _, err := w.AddLink(vtunSnapshot.LeftPorts[0], netReceiver.in); err != nil {
		t.Fatalf("AddLink network: %v", err)
	}
	if _, _, err := w.AddLink(tunReceiver.in, vtunSnapshot.RightPorts[0]); err != nil {
		t.Fatalf("AddLink tun: %v", err)
	}

	firstNetwork := receiveOutNetPayload(t, netReceiver.events).Network
	firstTun := receiveTunPayload(t, tunReceiver.events).Tun
	firstNetworkWrapper, ok := firstNetwork.(*gonnect.DetachedNetwork)
	if !ok {
		t.Fatalf("first network = %T, want *gonnect.DetachedNetwork", firstNetwork)
	}

	w.SendNodeFormularMsg(vtunID, formular.FormApplyMessage{
		MessageBase: formular.MessageBase{Type: formular.MessageFormApply, MenuID: pasta.NodeMenuID(vtunID)},
		BlockID:     "config",
		Values: map[string]any{
			"name":           "vtun-test",
			"localAddrs":     "10.123.0.2",
			"dnsServers":     "1.1.1.1",
			"mtu":            1300,
			"endpointCh":     16,
			"eventCh":        4,
			"mwo":            1,
			"mro":            2,
			"noLoopbackAddr": true,
			"disableSACK":    true,
		},
	})

	secondNetwork := receiveOutNetPayload(t, netReceiver.events).Network
	secondTun := receiveTunPayload(t, tunReceiver.events).Tun
	secondNetworkWrapper, ok := secondNetwork.(*gonnect.DetachedNetwork)
	if !ok {
		t.Fatalf("second network = %T, want *gonnect.DetachedNetwork", secondNetwork)
	}
	if firstNetworkWrapper.GetWrapped() == secondNetworkWrapper.GetWrapped() {
		t.Fatal("VTun form apply reused wrapped VTun, want rebuilt instance")
	}
	if SameTunSource(firstTun, secondTun) {
		t.Fatal("VTun form apply reused TUN source, want rebuilt instance")
	}
	if up, err := firstNetworkWrapper.IsUp(); err != nil || up {
		t.Fatalf("old network wrapper IsUp = %v, %v; want false, nil", up, err)
	}
}

func TestVTunGeneratedAddrsAreNotSaved(t *testing.T) {
	w, captured := newNetstackNodeTestWorkspace(t)
	state, sub := subscribeMenuState(t, w)
	defer w.UnsubscribeNotifications(sub)

	vtunID, err := w.AddNodeByClass(NodeTypeVTun, "vtun")
	if err != nil {
		t.Fatalf("AddNodeByClass VTun: %v", err)
	}
	w.SubscribeNodeMenu(vtunID, sub)
	assertLeadingEffectiveBlock(t, state, vtunID)
	netReceiver := &outNetReceiver{events: make(chan NetworkPayload, 1)}
	if _, err := w.AddNode(netReceiver, "test/NetworkReceiver", "network receiver"); err != nil {
		t.Fatalf("AddNode network receiver: %v", err)
	}
	vtunSnapshot := mustNodeSnapshot(t, w, vtunID)
	if _, _, err := w.AddLink(vtunSnapshot.LeftPorts[0], netReceiver.in); err != nil {
		t.Fatalf("AddLink network: %v", err)
	}
	if captured.vtun != nil && captured.vtun.formError != "" {
		t.Fatalf("VTun build formError = %q", captured.vtun.formError)
	}
	if captured.vtun != nil && captured.vtun.vtun == nil {
		t.Fatalf("VTun instance = nil after link, generatedAddrs=%v", captured.vtun.generatedAddrs)
	}
	_ = receiveOutNetPayload(t, netReceiver.events)

	if captured.vtun == nil {
		t.Fatal("captured VTun node = nil")
	}
	if len(captured.vtun.generatedAddrs) == 0 {
		t.Fatal("generatedAddrs is empty, want engine allocation cached")
	}
	assertReadonlyMenuFieldValue(t, state, vtunID, "effective", "localAddrs", strings.Join(netipAddrsText(captured.vtun.generatedAddrs), "\n"))
	assertReadonlyMenuFieldValue(t, state, vtunID, "effective", "mtu", defaultVTunMTU)
	cfg := configer.NewMemory(nil)
	if err := captured.vtun.OnSave(cfg); err != nil {
		t.Fatalf("OnSave: %v", err)
	}
	raw, err := cfg.Get(configer.Path{"localAddrs"})
	if err != nil {
		t.Fatalf("saved localAddrs missing: %v", err)
	}
	if raw != "" {
		t.Fatalf("saved localAddrs = %q, want empty generated value omitted", raw)
	}
}

func TestVTunNodeValidationReportsStatus(t *testing.T) {
	w, _ := newNetstackNodeTestWorkspace(t)
	state, sub := subscribeMenuState(t, w)
	defer w.UnsubscribeNotifications(sub)

	vtunID, err := w.AddNodeByClass(NodeTypeVTun, "vtun")
	if err != nil {
		t.Fatalf("AddNodeByClass VTun: %v", err)
	}
	w.SubscribeNodeMenu(vtunID, sub)

	w.SendNodeFormularMsg(vtunID, formular.FieldValidateMessage{
		MessageBase: formular.MessageBase{Type: formular.MessageFieldValidate, MenuID: pasta.NodeMenuID(vtunID)},
		Field:       formular.FieldRef{BlockID: "config", FieldID: "localAddrs"},
		Value:       "not-an-ip",
	})
	assertMenuItemStatus(t, state, vtunID, "localAddrs", formular.StatusError)

	w.SendNodeFormularMsg(vtunID, formular.FieldValidateMessage{
		MessageBase: formular.MessageBase{Type: formular.MessageFieldValidate, MenuID: pasta.NodeMenuID(vtunID)},
		Field:       formular.FieldRef{BlockID: "config", FieldID: "localAddrs"},
		Value:       "",
	})
	assertMenuItemStatus(t, state, vtunID, "localAddrs", formular.StatusOK)

	w.SendNodeFormularMsg(vtunID, formular.FieldValidateMessage{
		MessageBase: formular.MessageBase{Type: formular.MessageFieldValidate, MenuID: pasta.NodeMenuID(vtunID)},
		Field:       formular.FieldRef{BlockID: "config", FieldID: "mtu"},
		Value:       defaultVTunMTU,
	})
	assertMenuItemStatus(t, state, vtunID, "mtu", formular.StatusOK)
}

func TestSpooferNodeStartsRebuildsAndStopsWithInputs(t *testing.T) {
	w, captured := newNetstackNodeTestWorkspace(t)
	vtunID, err := w.AddNodeByClass(NodeTypeVTun, "vtun")
	if err != nil {
		t.Fatalf("AddNodeByClass VTun: %v", err)
	}
	spooferID, err := w.AddNodeByClass(NodeTypeSpoofer, "spoofer")
	if err != nil {
		t.Fatalf("AddNodeByClass Spoofer: %v", err)
	}
	mockID, err := w.AddNodeByClass(NodeTypeMockNetwork, "mock")
	if err != nil {
		t.Fatalf("AddNodeByClass MockNetwork: %v", err)
	}
	vtunSnapshot := mustNodeSnapshot(t, w, vtunID)
	spooferSnapshot := mustNodeSnapshot(t, w, spooferID)
	mockSnapshot := mustNodeSnapshot(t, w, mockID)

	if _, _, err := w.AddLink(spooferSnapshot.LeftPorts[0], vtunSnapshot.RightPorts[0]); err != nil {
		t.Fatalf("AddLink tun: %v", err)
	}
	networkLink, _, err := w.AddLink(mockSnapshot.LeftPorts[0], spooferSnapshot.RightPorts[0])
	if err != nil {
		t.Fatalf("AddLink network: %v", err)
	}

	if captured.spoofer == nil {
		t.Fatal("captured Spoofer node = nil")
	}
	first := captured.spoofer.runtime
	if first == nil {
		t.Fatal("Spoofer runtime = nil, want running stack")
	}

	w.SendNodeFormularMsg(spooferID, formular.FormApplyMessage{
		MessageBase: formular.MessageBase{Type: formular.MessageFormApply, MenuID: pasta.NodeMenuID(spooferID)},
		BlockID:     "config",
		Values: map[string]any{
			"mtu":                  1300,
			"queueLength":          64,
			"disableSACK":          true,
			"ttl":                  64,
			"icmpBurst":            8,
			"icmpLimit":            1.5,
			"tcpSendBufferSize":    4096,
			"tcpReceiveBufferSize": 4096,
			"congestionControlAlg": "",
			"disableNagle":         true,
			"disableTCPModRecBuff": true,
			"tcpKeepAlive":         true,
			"tcpKeepAliveIdle":     10,
			"tcpKeepAliveInterval": 5,
			"tcpKeepAliveCount":    3,
			"tcpForwardWnd":        1024,
			"tcpForwardAttempts":   16,
		},
	})
	second := captured.spoofer.runtime
	if second == nil {
		t.Fatal("Spoofer runtime after form apply = nil, want running stack")
	}
	if second == first {
		t.Fatal("Spoofer form apply reused runtime, want rebuilt stack")
	}

	w.RemoveLink(networkLink)
	if captured.spoofer.runtime != nil {
		t.Fatal("Spoofer runtime after network detach != nil, want stopped")
	}
}

func TestSpooferNodeValidationReportsStatus(t *testing.T) {
	w, _ := newNetstackNodeTestWorkspace(t)
	state, sub := subscribeMenuState(t, w)
	defer w.UnsubscribeNotifications(sub)

	spooferID, err := w.AddNodeByClass(NodeTypeSpoofer, "spoofer")
	if err != nil {
		t.Fatalf("AddNodeByClass Spoofer: %v", err)
	}
	w.SubscribeNodeMenu(spooferID, sub)

	w.SendNodeFormularMsg(spooferID, formular.FieldValidateMessage{
		MessageBase: formular.MessageBase{Type: formular.MessageFieldValidate, MenuID: pasta.NodeMenuID(spooferID)},
		Field:       formular.FieldRef{BlockID: "config", FieldID: "ttl"},
		Value:       300,
	})
	assertMenuItemStatus(t, state, spooferID, "ttl", formular.StatusError)

	w.SendNodeFormularMsg(spooferID, formular.FieldValidateMessage{
		MessageBase: formular.MessageBase{Type: formular.MessageFieldValidate, MenuID: pasta.NodeMenuID(spooferID)},
		Field:       formular.FieldRef{BlockID: "config", FieldID: "ttl"},
		Value:       64,
	})
	assertMenuItemStatus(t, state, spooferID, "ttl", formular.StatusOK)

	w.SendNodeFormularMsg(spooferID, formular.FieldValidateMessage{
		MessageBase: formular.MessageBase{Type: formular.MessageFieldValidate, MenuID: pasta.NodeMenuID(spooferID)},
		Field:       formular.FieldRef{BlockID: "config", FieldID: "congestionControlAlg"},
		Value:       "",
	})
	assertMenuItemStatus(t, state, spooferID, "congestionControlAlg", formular.StatusOK)
}

func newNetstackNodeTestWorkspace(t *testing.T) (*pasta.Workspace, *captureNetstackNodes) {
	t.Helper()
	w := pasta.NewWorkspace(NewDebugLogFactory(1024))
	t.Cleanup(w.Close)
	engine := &engine{ctx: context.Background(), sys: &sysnetdebug.System{}}
	captured := &captureNetstackNodes{}
	if err := w.AddNodeClass(captureVTunClass{engine: engine, captured: captured}); err != nil {
		t.Fatalf("AddNodeClass VTun: %v", err)
	}
	if err := w.AddNodeClass(captureSpooferClass{engine: engine, captured: captured}); err != nil {
		t.Fatalf("AddNodeClass Spoofer: %v", err)
	}
	if err := w.AddNodeClass(mockNetClass{}); err != nil {
		t.Fatalf("AddNodeClass MockNetwork: %v", err)
	}
	return w, captured
}

func subscribeMenuState(t *testing.T, w *pasta.Workspace) (*formular.MenuSnapshotState, uint64) {
	t.Helper()
	state := formular.NewMenuSnapshotState()
	sub := w.SubscribeNotifications(func(notification pasta.WorkspaceNotification) {
		if notification.Kind == pasta.NotificationNodeMenu {
			state.Apply(notification.Formular)
		}
	})
	if sub == 0 {
		t.Fatal("SubscribeNotifications returned 0")
	}
	return state, sub
}

func assertMenuItemStatus(t *testing.T, state *formular.MenuSnapshotState, node uint64, fieldID string, status string) {
	t.Helper()
	snapshot, ok := state.Snapshot(pasta.NodeMenuID(node))
	if !ok {
		t.Fatalf("menu snapshot missing for node %d", node)
	}
	item, ok := FindItem(mustFindBlock(t, snapshot, "config"), fieldID)
	if !ok {
		t.Fatalf("%s item missing", fieldID)
	}
	if item.Status != status {
		t.Fatalf("%s status = %q, want %q", fieldID, item.Status, status)
	}
}

func assertLeadingEffectiveBlock(t *testing.T, state *formular.MenuSnapshotState, node uint64) {
	t.Helper()
	snapshot, ok := state.Snapshot(pasta.NodeMenuID(node))
	if !ok {
		t.Fatalf("menu snapshot missing for node %d", node)
	}
	if len(snapshot.Blocks) == 0 || snapshot.Blocks[0].ID != "effective" {
		t.Fatalf("leading block = %#v, want effective", snapshot.Blocks)
	}
}

func assertReadonlyMenuFieldValue(t *testing.T, state *formular.MenuSnapshotState, node uint64, blockID, fieldID string, want any) {
	t.Helper()
	snapshot, ok := state.Snapshot(pasta.NodeMenuID(node))
	if !ok {
		t.Fatalf("menu snapshot missing for node %d", node)
	}
	item, ok := FindItem(mustFindBlock(t, snapshot, blockID), fieldID)
	if !ok {
		t.Fatalf("%s.%s item missing", blockID, fieldID)
	}
	if item.Field == nil {
		t.Fatalf("%s.%s field missing", blockID, fieldID)
	}
	if !item.Readonly {
		t.Fatalf("%s.%s readonly = false, want true", blockID, fieldID)
	}
	if item.Value != want {
		t.Fatalf("%s.%s value = %#v, want %#v", blockID, fieldID, item.Value, want)
	}
}

type captureNetstackNodes struct {
	vtun    *vtunNode
	spoofer *spooferNode
}

type captureVTunClass struct {
	engine   *engine
	captured *captureNetstackNodes
}

func (c captureVTunClass) ClassName() string        { return NodeTypeVTun }
func (c captureVTunClass) ShortDescription() string { return vtunClass{}.ShortDescription() }
func (c captureVTunClass) LongDescription() string  { return vtunClass{}.LongDescription() }
func (c captureVTunClass) DefaultNodeParams() pasta.NodeClassParams {
	return vtunClass{}.DefaultNodeParams()
}
func (c captureVTunClass) NewNode(cfg configer.Config, _ ...*pasta.NodeClassState) (pasta.Node, error) {
	config := defaultVTunConfig()
	config.read(cfg)
	node := &vtunNode{engine: c.engine, config: config}
	c.captured.vtun = node
	return node, nil
}

type captureSpooferClass struct {
	engine   *engine
	captured *captureNetstackNodes
}

func (c captureSpooferClass) ClassName() string        { return NodeTypeSpoofer }
func (c captureSpooferClass) ShortDescription() string { return spooferClass{}.ShortDescription() }
func (c captureSpooferClass) LongDescription() string  { return spooferClass{}.LongDescription() }
func (c captureSpooferClass) DefaultNodeParams() pasta.NodeClassParams {
	return spooferClass{}.DefaultNodeParams()
}
func (c captureSpooferClass) NewNode(cfg configer.Config, _ ...*pasta.NodeClassState) (pasta.Node, error) {
	config := defaultSpooferConfig()
	config.read(cfg)
	node := &spooferNode{engine: c.engine, pool: enginePool(c.engine), config: config}
	c.captured.spoofer = node
	return node, nil
}

type tunPayloadReceiver struct {
	pasta.BasicNode
	in     uint64
	events chan TunPayload
}

func (n *tunPayloadReceiver) OnInit(w *pasta.Workspace, _ pasta.Logger, id uint64, _ string, _ *pasta.NodeInitData, _, _, _, _ bool) error {
	port, err := w.AddPort(pasta.Port{
		Node:      id,
		Direction: "left",
		Name:      "Tun",
		Types:     []string{TypeTun},
	})
	if err != nil {
		return err
	}
	n.in = port
	return nil
}

func (n *tunPayloadReceiver) PreLinkAdd(port uint64, linkType, portDirection string) error {
	if port != n.in || portDirection != "left" {
		return pasta.LinkTypeErr(linkType)
	}
	return TunPreLinkAdd(nil, 0, linkType)
}

func (n *tunPayloadReceiver) OnEvent(event pasta.Event, linkType string, _ []string, receiverPortDirection string) error {
	if event.ReceiverPort != n.in || receiverPortDirection != "left" || linkType != TypeTun {
		return nil
	}
	payload, ok := event.Payload.(TunPayload)
	if !ok {
		return nil
	}
	n.events <- payload
	return nil
}

func receiveTunPayload(t *testing.T, events <-chan TunPayload) TunPayload {
	t.Helper()
	select {
	case payload := <-events:
		if payload.Tun == nil {
			t.Fatal("payload.Tun = nil, want tun")
		}
		return payload
	default:
		t.Fatal("no tun payload received")
		return TunPayload{}
	}
}
