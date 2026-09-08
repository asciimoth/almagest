package wireguardnodes

import (
	"errors"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	conn "github.com/asciimoth/batchudp"
	"github.com/asciimoth/pasta/pasta"
	"github.com/asciimoth/pasta/pasta/std"
	wgdevice "github.com/asciimoth/wgo/device"

	"github.com/asciimoth/almagest/lib/logging"
)

func TestWGCtrlPortAndLinkCardinality(t *testing.T) {
	port := WGCtrlPort("left", "Controllers")
	if port.Direction != "left" || port.Name != "Controllers" || len(port.Types) != 1 || port.Types[0] != TypeWGCtrl {
		t.Fatalf("WGCtrlPort() = %#v", port)
	}

	w := pasta.NewWorkspace(logging.NewDebugLogFactory(1024))
	defer w.Close()
	firstProvider := addWGCtrlTestNode(t, w, "left", nil)
	secondProvider := addWGCtrlTestNode(t, w, "left", nil)
	controller := addWGCtrlTestNode(t, w, "right", nil)

	if _, _, err := w.AddLink(firstProvider.port, controller.port); err != nil {
		t.Fatalf("AddLink first provider: %v", err)
	}
	if _, _, err := w.AddLink(secondProvider.port, controller.port); !errors.Is(err, ErrWGCtrlPortBusy) {
		t.Fatalf("AddLink second provider error = %v, want %v", err, ErrWGCtrlPortBusy)
	}

	secondController := addWGCtrlTestNode(t, w, "right", nil)
	if _, _, err := w.AddLink(firstProvider.port, secondController.port); err != nil {
		t.Fatalf("AddLink second controller: %v", err)
	}

	w.Lock()
	err := WGCtrlPreLinkAdd(w, firstProvider.port, "example.com/other", "left")
	w.Unlock()
	if !errors.Is(err, pasta.ErrLinkType) {
		t.Fatalf("wrong type error = %v, want pasta.ErrLinkType", err)
	}
}

func TestWGCtrlTypedNilDeviceIsNotReady(t *testing.T) {
	var device *wgdevice.Device
	var api WGCtrlAPI = device

	if WGCtrlIsReady(api) {
		t.Fatal("typed nil WireGuard controller is ready")
	}
	if _, ok := WGCtrlPublicKey(api); ok {
		t.Fatal("typed nil WireGuard controller has a public key")
	}
	if !SameWGCtrlInstance(api, nil) {
		t.Fatal("typed nil and nil WireGuard controllers differ")
	}
	CloseWGCtrl(api)
	if err := (WGCtrlPayload{API: api}).Close(); err != nil {
		t.Fatalf("close typed nil payload: %v", err)
	}
}

func TestEmitWGCtrlInterfaceConfigFlowsFromController(t *testing.T) {
	w := pasta.NewWorkspace(logging.NewDebugLogFactory(1024))
	defer w.Close()
	configs := make(chan WGCtrlInterfaceConfig, 1)
	provider := addWGCtrlTestNode(t, w, "left", nil)
	provider.configs = configs
	controller := addWGCtrlTestNode(t, w, "right", nil)
	link := addWGCtrlTestLink(t, w, provider.port, controller.port)

	cfg := WGCtrlInterfaceConfig{
		OwnedIPs: []string{"10.44.0.2/32"},
		Routes:   []string{"10.80.0.0/16", "fd00:80::/64"},
		DNS:      "8.8.8.8",
	}
	w.Lock()
	if !EmitWGCtrlInterfaceConfig(w, controller.id, link, cfg) {
		w.Unlock()
		t.Fatal("EmitWGCtrlInterfaceConfig() = false, want true")
	}
	if EmitWGCtrlInterfaceConfig(w, provider.id, link, cfg) {
		w.Unlock()
		t.Fatal("provider emitted a controller config")
	}
	w.Unlock()
	cfg.OwnedIPs[0] = "10.44.0.99/32"
	cfg.Routes[0] = "10.99.0.0/16"
	cfg.DNS = "1.1.1.1"

	select {
	case got := <-configs:
		if len(got.OwnedIPs) != 1 || got.OwnedIPs[0] != "10.44.0.2/32" ||
			!slices.Equal(got.Routes, []string{"10.80.0.0/16", "fd00:80::/64"}) || got.DNS != "8.8.8.8" {
			t.Fatalf("received config = %#v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for WGCtrl interface config")
	}
}

func TestWGCtrlInterfaceConfigZeroIncludesDNS(t *testing.T) {
	if !(WGCtrlInterfaceConfig{}).IsZero() {
		t.Fatal("empty WGCtrl interface config is not zero")
	}
	if (WGCtrlInterfaceConfig{DNS: "8.8.8.8"}).IsZero() {
		t.Fatal("WGCtrl interface config with DNS is zero")
	}
	if (WGCtrlInterfaceConfig{Routes: []string{"10.0.0.0/8"}}).IsZero() {
		t.Fatal("WGCtrl interface config with routes is zero")
	}
}

func TestWGCtrlDetachedAPIsTrackPeersTransportsAndGraphLifecycle(t *testing.T) {
	w := pasta.NewWorkspace(logging.NewDebugLogFactory(1024))
	defer w.Close()
	provider := addWGCtrlTestNode(t, w, "left", nil)
	firstEvents := make(chan WGCtrlAPI, 2)
	secondEvents := make(chan WGCtrlAPI, 1)
	firstController := addWGCtrlTestNode(t, w, "right", firstEvents)
	secondController := addWGCtrlTestNode(t, w, "right", secondEvents)
	firstLink := addWGCtrlTestLink(t, w, provider.port, firstController.port)
	secondLink := addWGCtrlTestLink(t, w, provider.port, secondController.port)

	source := wgdevice.NewDevice(nil, nil, wgdevice.NopLogger{}, nil, wgdevice.DeviceOptions{BatchSize: conn.IdealBatchSize})
	defer source.Close()
	privateKey, err := wgdevice.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("GeneratePrivateKey: %v", err)
	}
	if err := source.SetPrivateKey(privateKey); err != nil {
		t.Fatalf("SetPrivateKey: %v", err)
	}

	w.Lock()
	firstPayload := EmitDetachedWGCtrl(w, provider.id, firstLink, source)
	secondPayload := EmitDetachedWGCtrl(w, provider.id, secondLink, source)
	w.Unlock()
	if !WGCtrlIsReady(firstPayload.API) || !WGCtrlIsReady(secondPayload.API) {
		t.Fatal("EmitDetachedWGCtrl() returned a closed or nil API")
	}
	if SameWGCtrlInstance(firstPayload.API, secondPayload.API) {
		t.Fatal("controller links received the same detached API instance")
	}
	if got := receiveWGCtrlAPI(t, firstEvents); !SameWGCtrlInstance(got, firstPayload.API) {
		t.Fatal("first controller did not accept its detached API")
	}
	if got := receiveWGCtrlAPI(t, secondEvents); !SameWGCtrlInstance(got, secondPayload.API) {
		t.Fatal("second controller did not accept its detached API")
	}

	publicKey, ok := WGCtrlPublicKey(firstPayload.API)
	if !ok || !publicKey.Equals(privateKey.PublicKey()) {
		t.Fatalf("WGCtrlPublicKey() = %v, %t; want source public key", publicKey, ok)
	}

	firstPeer := generateWGCtrlPeerKey(t)
	secondPeer := generateWGCtrlPeerKey(t)
	if err := firstPayload.API.UpsertTrackedPeer(wgdevice.PeerSpec{PublicKey: firstPeer, ProtocolVersion: 1}); err != nil {
		t.Fatalf("first UpsertTrackedPeer: %v", err)
	}
	if err := secondPayload.API.UpsertTrackedPeer(wgdevice.PeerSpec{PublicKey: secondPeer, ProtocolVersion: 1}); err != nil {
		t.Fatalf("second UpsertTrackedPeer: %v", err)
	}

	transport := &wgCtrlTestBind{}
	transportID := wgdevice.TransportID("controller-one/udp")
	if err := firstPayload.API.AddTrackedTransport(transportID, wgdevice.TransportConfig{
		Bind: transport,
	}); err != nil {
		t.Fatalf("AddTrackedTransport: %v", err)
	}
	if _, ok := source.TransportInfo(transportID); !ok {
		t.Fatal("source does not contain the tracked transport")
	}

	w.Lock()
	if !EmitWGCtrlPayload(w, provider.id, firstLink, firstPayload) {
		t.Fatal("EmitWGCtrlPayload() = false, want true")
	}
	w.Unlock()
	if !SameWGCtrlInstance(firstController.current, firstPayload.API) {
		t.Fatal("re-emission changed the first controller API")
	}

	w.RemoveLink(firstLink)
	waitWGCtrlCondition(t, func() bool { return !WGCtrlIsReady(firstPayload.API) })
	if _, ok := source.PeerSpec(firstPeer); ok {
		t.Fatal("first tracked peer remains after its link was removed")
	}
	if _, ok := source.TransportInfo(transportID); ok {
		t.Fatal("tracked transport remains after its link was removed")
	}
	if !transport.closed.Load() {
		t.Fatal("tracked transport bind was not closed")
	}
	if _, ok := source.PeerSpec(secondPeer); !ok {
		t.Fatal("first link removal deleted the second controller peer")
	}
	if !WGCtrlIsReady(source) || !WGCtrlIsReady(secondPayload.API) {
		t.Fatal("first link removal closed the shared device or second controller API")
	}

	w.RemoveNode(secondController.id)
	waitWGCtrlCondition(t, func() bool { return !WGCtrlIsReady(secondPayload.API) })
	if _, ok := source.PeerSpec(secondPeer); ok {
		t.Fatal("second tracked peer remains after its controller node was removed")
	}
}

func TestAcceptWGCtrlPayloadRejectsRequestsStaleLinksAndClosedAPIs(t *testing.T) {
	source := wgdevice.NewDevice(nil, nil, wgdevice.NopLogger{}, nil, wgdevice.DeviceOptions{})
	defer source.Close()
	api := wgdevice.DetachDevice(source)
	payload := WGCtrlPayload{API: api, resource: &wgCtrlResource{api: api}}

	if got, changed := AcceptWGCtrlPayloadValue(pasta.Event{
		Link:    1,
		Payload: std.RequestValue{},
	}, 1, nil); changed || got != nil {
		t.Fatalf("request result = %v, %t; want nil, false", got, changed)
	}
	if got, changed := AcceptWGCtrlPayloadValue(pasta.Event{
		Link:    2,
		Payload: payload,
	}, 1, nil); changed || got != nil {
		t.Fatalf("stale link result = %v, %t; want nil, false", got, changed)
	}

	if err := payload.Close(); err != nil {
		t.Fatalf("payload.Close(): %v", err)
	}
	if got, changed := AcceptWGCtrlPayloadValue(pasta.Event{
		Link:    1,
		Payload: payload,
	}, 1, nil); changed || got != nil {
		t.Fatalf("closed API result = %v, %t; want nil, false", got, changed)
	}
	if !WGCtrlIsReady(source) {
		t.Fatal("payload.Close() closed the shared device")
	}
}

func TestAcceptWGCtrlPayloadAllowsLiveReplacementForMiddlewareSwitch(t *testing.T) {
	source := wgdevice.NewDevice(nil, nil, wgdevice.NopLogger{}, nil, wgdevice.DeviceOptions{})
	defer source.Close()
	current := wgdevice.DetachDevice(source)
	defer current.Close()
	replacement := wgdevice.DetachDevice(source)
	defer replacement.Close()

	got, changed := AcceptWGCtrlPayloadValue(pasta.Event{
		Link:    7,
		Payload: WGCtrlPayload{API: replacement, resource: &wgCtrlResource{api: replacement}},
	}, 7, current)
	if !changed || !SameWGCtrlInstance(got, replacement) {
		t.Fatalf("live replacement = (%v, %t), want replacement, true", got, changed)
	}
	if !WGCtrlIsReady(current) {
		t.Fatal("accepting replacement closed current API inside callback")
	}
}

type wgCtrlTestNode struct {
	pasta.BasicNode

	id        uint64
	w         *pasta.Workspace
	direction string
	port      uint64
	link      uint64
	current   WGCtrlAPI
	events    chan<- WGCtrlAPI
	configs   chan<- WGCtrlInterfaceConfig
}

type wgCtrlTestBind struct {
	closed atomic.Bool
}

func (b *wgCtrlTestBind) Open(uint16) ([]conn.ReceiveFunc, uint16, error) {
	b.closed.Store(false)
	return nil, 0, nil
}

func (b *wgCtrlTestBind) Close() error {
	b.closed.Store(true)
	return nil
}

func (*wgCtrlTestBind) SetMark(uint32) error { return nil }

func (*wgCtrlTestBind) Send([][]byte, conn.Endpoint) error { return nil }

func (*wgCtrlTestBind) ParseEndpoint(string) (conn.Endpoint, error) {
	return nil, errors.New("test bind has no endpoints")
}

func (*wgCtrlTestBind) BatchSize() int { return 1 }

func addWGCtrlTestNode(t *testing.T, w *pasta.Workspace, direction string, events chan<- WGCtrlAPI) *wgCtrlTestNode {
	t.Helper()
	node := &wgCtrlTestNode{direction: direction, events: events}
	id, err := w.AddNode(node, "test/WGCtrl"+direction)
	if err != nil {
		t.Fatalf("AddNode %s: %v", direction, err)
	}
	if node.id != id {
		t.Fatalf("initialized node ID = %d, want %d", node.id, id)
	}
	return node
}

func (n *wgCtrlTestNode) OnInit(w *pasta.Workspace, _ pasta.Logger, id uint64, _ string, _ *pasta.NodeInitData, _, _, _, _ bool) error {
	n.id = id
	n.w = w
	declaration := WGCtrlPort(n.direction, "WireGuard controller")
	declaration.Node = id
	port, err := w.AddPortLocked(declaration)
	if err != nil {
		return err
	}
	n.port = port
	return nil
}

func (n *wgCtrlTestNode) PreLinkAdd(port uint64, linkType, portDirection string) error {
	if port != n.port || portDirection != n.direction {
		return pasta.LinkTypeErr(linkType)
	}
	return WGCtrlPreLinkAdd(n.w, port, linkType, portDirection)
}

func (n *wgCtrlTestNode) OnLinkAdd(link, port uint64, linkType, portDirection string) error {
	if port == n.port && linkType == TypeWGCtrl && portDirection == n.direction {
		n.link = link
	}
	return nil
}

func (n *wgCtrlTestNode) OnEvent(event pasta.Event, linkType string, _ []string, receiverPortDirection string) error {
	if event.ReceiverPort != n.port || linkType != TypeWGCtrl || receiverPortDirection != n.direction {
		return nil
	}
	if n.direction == "left" {
		if cfg, ok := event.Payload.(WGCtrlInterfaceConfig); ok && n.configs != nil {
			n.configs <- cfg.Clone()
		}
		return nil
	}
	next, changed := AcceptWGCtrlPayload(n.w, event, n.link, n.current)
	if !changed {
		return nil
	}
	n.current = next
	if n.events != nil {
		n.events <- next
	}
	return nil
}

func addWGCtrlTestLink(t *testing.T, w *pasta.Workspace, left, right uint64) uint64 {
	t.Helper()
	link, linkType, err := w.AddLink(left, right)
	if err != nil {
		t.Fatalf("AddLink: %v", err)
	}
	if linkType != TypeWGCtrl {
		t.Fatalf("link type = %q, want %q", linkType, TypeWGCtrl)
	}
	return link
}

func receiveWGCtrlAPI(t *testing.T, events <-chan WGCtrlAPI) WGCtrlAPI {
	t.Helper()
	select {
	case api := <-events:
		return api
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for WireGuard controller API")
		return nil
	}
}

func generateWGCtrlPeerKey(t *testing.T) wgdevice.NoisePublicKey {
	t.Helper()
	privateKey, err := wgdevice.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("GeneratePrivateKey: %v", err)
	}
	return privateKey.PublicKey()
}

func waitWGCtrlCondition(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("condition did not become true")
}
