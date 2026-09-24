package links

import (
	"testing"
	"time"

	"github.com/asciimoth/almagest/lib/logging"
	"github.com/asciimoth/gonnect"
	"github.com/asciimoth/pasta/pasta"
)

func TestAcceptNetworkPayloadReplacesReadySameSourceWrapper(t *testing.T) {
	base := gonnect.NewLoopbackNetwork()
	current := &testNetworkWrapper{
		NetworkCloser: gonnect.DetachNetwork(base, nil, nil),
		wrapped:       base,
		up:            true,
	}
	replacement := &testNetworkWrapper{
		NetworkCloser: gonnect.DetachNetwork(base, nil, nil),
		wrapped:       base,
		up:            true,
	}
	defer closeTestNetwork(replacement)

	next, changed := AcceptNetworkPayload(nil, 0, pasta.Event{
		Link:    1,
		Payload: NetworkPayload{Network: replacement},
	}, current)
	if !changed {
		t.Fatal("AcceptNetworkPayload() changed = false, want true for fresh same-source wrapper")
	}
	if !SameNetworkInstance(next, replacement) {
		t.Fatal("AcceptNetworkPayload() did not return replacement")
	}
	if !current.closed {
		t.Fatal("current network was not closed")
	}
	if !NetworkIsReady(replacement) {
		t.Fatal("replacement network is not ready")
	}
}

func TestAcceptNetworkPayloadReplacesStaleCurrentSameSource(t *testing.T) {
	// Generic network consumers must accept fresh same-source wrappers because
	// selector nodes can close previously routed wrapper instances.
	base := gonnect.NewLoopbackNetwork()
	current := &testNetworkWrapper{
		NetworkCloser: gonnect.DetachNetwork(base, nil, nil),
		wrapped:       base,
		up:            true,
	}
	closeTestNetwork(current)
	replacement := &testNetworkWrapper{
		NetworkCloser: gonnect.DetachNetwork(base, nil, nil),
		wrapped:       base,
		up:            true,
	}
	defer closeTestNetwork(replacement)

	next, changed := AcceptNetworkPayload(nil, 0, pasta.Event{
		Link:    1,
		Payload: NetworkPayload{Network: replacement},
	}, current)
	if !changed {
		t.Fatal("AcceptNetworkPayload() changed = false, want true for stale current")
	}
	if !SameNetworkInstance(next, replacement) {
		t.Fatal("AcceptNetworkPayload() did not return replacement")
	}
	if !NetworkIsReady(replacement) {
		t.Fatal("replacement network is not ready")
	}
}

func TestAcceptNetworkPayloadUnavailableClosesCurrent(t *testing.T) {
	base := gonnect.NewLoopbackNetwork()
	current := &testNetworkWrapper{
		NetworkCloser: gonnect.DetachNetwork(base, nil, nil),
		wrapped:       base,
		up:            true,
	}

	next, changed := AcceptNetworkPayloadValue(pasta.Event{Payload: NetworkPayload{}}, current)
	if !changed {
		t.Fatal("AcceptNetworkPayloadValue() changed = false, want true for unavailable payload")
	}
	if next != nil {
		t.Fatalf("AcceptNetworkPayloadValue() next = %T, want nil", next)
	}
	if !current.closed {
		t.Fatal("current network was not closed after unavailable payload")
	}

	next, changed = AcceptNetworkPayloadValue(pasta.Event{Payload: NetworkPayload{}}, nil)
	if changed || next != nil {
		t.Fatalf("repeated unavailable payload = (%T, %v), want (nil, false)", next, changed)
	}
}

func TestNetworkPayloadAndEmitGuardBranches(t *testing.T) {
	if err := (NetworkPayload{}).Close(); err != nil {
		t.Fatalf("empty payload close: %v", err)
	}
	source := gonnect.NewLoopbackNetwork()
	wrapper := gonnect.DetachNetwork(source, nil, nil)
	if err := (NetworkPayload{Network: wrapper}).Close(); err != nil {
		t.Fatal(err)
	}
	if got := EmitDetachedNetwork(nil, 1, 1, source); got != nil {
		t.Fatalf("nil-workspace emit = %T", got)
	}
	w := pasta.NewWorkspace(logging.NewDebugLogFactory(8))
	defer w.Close()
	w.Lock()
	if got := EmitDetachedNetwork(w, 1, 0, source); got != nil {
		w.Unlock()
		t.Fatalf("zero-link emit = %T", got)
	}
	if got := EmitDetachedNetwork(w, 1, 99, source); got != nil {
		w.Unlock()
		t.Fatalf("missing-link emit = %T", got)
	}
	w.Unlock()
	var cache NetworkOutputCache
	if cache.SendResolved(nil, 1, 1, source, nil) {
		t.Fatal("SendResolved accepted nil workspace")
	}
}

func TestAcceptNetworkPayloadUpdateUnavailableClearsCapability(t *testing.T) {
	base := gonnect.NewLoopbackNetwork()
	current := &testNetworkWrapper{
		NetworkCloser: gonnect.DetachNetwork(base, nil, nil),
		wrapped:       base,
		up:            true,
	}
	provider := NewLayer3DatagramMTUProvider(1500, true, false, MTUConstraint{Source: "test"})

	next, changed := AcceptNetworkPayloadUpdate(
		pasta.Event{Payload: NetworkPayload{}},
		NetworkPayload{Network: current, DatagramMTU: provider, CapabilityRevision: provider.Revision()},
	)
	if !changed {
		t.Fatal("AcceptNetworkPayloadUpdate() changed = false, want true for unavailable payload")
	}
	if next.Network != nil || next.DatagramMTU != nil || next.CapabilityRevision != 0 {
		t.Fatalf("AcceptNetworkPayloadUpdate() = %+v, want zero payload", next)
	}
	if !current.closed {
		t.Fatal("current network was not closed after unavailable payload")
	}
}

func TestAcceptNetworkPayloadUpdateAcceptsSameNetworkCapabilityRevision(t *testing.T) {
	base := gonnect.NewLoopbackNetwork()
	network := gonnect.DetachNetwork(base, nil, nil)
	defer closeTestNetwork(network)
	provider := NewLayer3DatagramMTUProvider(1500, true, false, MTUConstraint{Source: "test"})
	current := NetworkPayload{Network: network, DatagramMTU: provider, CapabilityRevision: provider.Revision()}
	provider.SetMTU(1400, true, false, MTUConstraint{Source: "test update"})
	next, changed := AcceptNetworkPayloadUpdate(pasta.Event{Payload: NetworkPayload{
		Network: network, DatagramMTU: provider, CapabilityRevision: provider.Revision(),
	}}, current)
	if !changed {
		t.Fatal("AcceptNetworkPayloadUpdate() ignored a capability revision")
	}
	if !SameNetworkInstance(next.Network, network) || next.CapabilityRevision != provider.Revision() {
		t.Fatalf("AcceptNetworkPayloadUpdate() = %+v", next)
	}
	if !NetworkIsReady(network) {
		t.Fatal("same-network capability update closed the network")
	}
}

func TestNetworkOutputCacheReusesReadyWrapperAndReplacesClosedWrapper(t *testing.T) {
	w := pasta.NewWorkspace(logging.NewDebugLogFactory(1024))
	defer w.Close()
	providerID, out := addNetworkOutputCacheTestPort(t, w)
	receiver := &networkPayloadReceiver{events: make(chan NetworkPayload, 3)}
	_, err := w.AddNode(receiver, "test/NetworkReceiver", "receiver")
	if err != nil {
		t.Fatalf("AddNode receiver: %v", err)
	}
	link, _, err := w.AddLink(out, receiver.in)
	if err != nil {
		t.Fatalf("AddLink: %v", err)
	}

	source := gonnect.NewLoopbackNetwork()
	var cache NetworkOutputCache
	if !cache.Send(w, providerID, link, source) {
		t.Fatal("NetworkOutputCache.Send() = false, want true")
	}
	first := receiveOutNetPayload(t, receiver.events).Network
	if !cache.Send(w, providerID, link, source) {
		t.Fatal("NetworkOutputCache.Send() second = false, want true")
	}
	second := receiveOutNetPayload(t, receiver.events).Network
	if !SameNetworkInstance(first, second) {
		t.Fatal("NetworkOutputCache did not reuse ready wrapper")
	}

	_ = first.Close()
	if !cache.Send(w, providerID, link, source) {
		t.Fatal("NetworkOutputCache.Send() after close = false, want true")
	}
	third := receiveOutNetPayload(t, receiver.events).Network
	if SameNetworkInstance(first, third) {
		t.Fatal("NetworkOutputCache reused closed wrapper, want replacement")
	}
	cache.CloseAll()
}

func TestNetworkOutputCacheInvalidateAllNotifiesAndRecovers(t *testing.T) {
	w := pasta.NewWorkspace(logging.NewDebugLogFactory(1024))
	defer w.Close()
	providerID, out := addNetworkOutputCacheTestPort(t, w)
	receiver := &networkPayloadReceiver{events: make(chan NetworkPayload, 3)}
	_, err := w.AddNode(receiver, "test/NetworkReceiver", "receiver")
	if err != nil {
		t.Fatalf("AddNode receiver: %v", err)
	}
	link, _, err := w.AddLink(out, receiver.in)
	if err != nil {
		t.Fatalf("AddLink: %v", err)
	}

	source := gonnect.NewLoopbackNetwork()
	var cache NetworkOutputCache
	if !cache.Send(w, providerID, link, source) {
		t.Fatal("NetworkOutputCache.Send() = false, want true")
	}
	first := receiveOutNetPayload(t, receiver.events).Network

	w.Lock()
	cache.InvalidateAllLocked(w, providerID)
	w.Unlock()
	unavailable := receiveAnyOutNetPayload(t, receiver.events)
	if unavailable.Network != nil || unavailable.DatagramMTU != nil || unavailable.CapabilityRevision != 0 {
		t.Fatalf("invalidated payload = %+v, want zero payload", unavailable)
	}
	if NetworkIsReady(first) {
		t.Fatal("invalidated network wrapper is still ready")
	}

	if !cache.Send(w, providerID, link, source) {
		t.Fatal("NetworkOutputCache.Send() recovery = false, want true")
	}
	replacement := receiveOutNetPayload(t, receiver.events).Network
	if !NetworkIsReady(replacement) || SameNetworkInstance(first, replacement) {
		t.Fatal("recovery did not emit a fresh ready network wrapper")
	}
	cache.CloseAll()
}

func TestNetworkOutputCacheRefreshReplacesReadySameSourceWrapper(t *testing.T) {
	w := pasta.NewWorkspace(logging.NewDebugLogFactory(1024))
	defer w.Close()
	providerID, out := addNetworkOutputCacheTestPort(t, w)
	receiver := &networkPayloadReceiver{events: make(chan NetworkPayload, 2)}
	_, err := w.AddNode(receiver, "test/NetworkReceiver", "receiver")
	if err != nil {
		t.Fatalf("AddNode receiver: %v", err)
	}
	link, _, err := w.AddLink(out, receiver.in)
	if err != nil {
		t.Fatalf("AddLink: %v", err)
	}

	source := gonnect.NewLoopbackNetwork()
	var cache NetworkOutputCache
	if !cache.Send(w, providerID, link, source) {
		t.Fatal("NetworkOutputCache.Send() = false, want true")
	}
	first := receiveOutNetPayload(t, receiver.events).Network
	w.Lock()
	refreshedOK := cache.RefreshLocked(w, providerID, link, source)
	w.Unlock()
	if !refreshedOK {
		t.Fatal("NetworkOutputCache.RefreshLocked() = false, want true")
	}
	second := receiveOutNetPayload(t, receiver.events).Network
	if SameNetworkInstance(first, second) {
		t.Fatal("NetworkOutputCache.RefreshLocked reused wrapper, want fresh detach")
	}
	if NetworkIsReady(first) {
		t.Fatal("NetworkOutputCache.RefreshLocked left previous wrapper ready")
	}
	if !NetworkIsReady(second) {
		t.Fatal("NetworkOutputCache.RefreshLocked emitted non-ready wrapper")
	}
	cache.CloseAll()
}

func TestNetworkOutputCacheReplacesChangedSource(t *testing.T) {
	w := pasta.NewWorkspace(logging.NewDebugLogFactory(1024))
	defer w.Close()
	providerID, out := addNetworkOutputCacheTestPort(t, w)
	receiver := &networkPayloadReceiver{events: make(chan NetworkPayload, 2)}
	_, err := w.AddNode(receiver, "test/NetworkReceiver", "receiver")
	if err != nil {
		t.Fatalf("AddNode receiver: %v", err)
	}
	link, _, err := w.AddLink(out, receiver.in)
	if err != nil {
		t.Fatalf("AddLink: %v", err)
	}

	var cache NetworkOutputCache
	if !cache.Send(w, providerID, link, gonnect.NewLoopbackNetwork()) {
		t.Fatal("NetworkOutputCache.Send() = false, want true")
	}
	first := receiveOutNetPayload(t, receiver.events).Network
	if !cache.Send(w, providerID, link, gonnect.NewLoopbackNetwork()) {
		t.Fatal("NetworkOutputCache.Send() changed source = false, want true")
	}
	second := receiveOutNetPayload(t, receiver.events).Network
	if SameNetworkInstance(first, second) {
		t.Fatal("NetworkOutputCache reused wrapper for changed source")
	}
	if NetworkIsReady(first) {
		t.Fatal("NetworkOutputCache left old wrapper ready after source replacement")
	}
	cache.CloseAll()
}

func TestNetworkOutputCacheMTUCapabilityAttachDetachReattach(t *testing.T) {
	w := pasta.NewWorkspace(logging.NewDebugLogFactory(1024))
	defer w.Close()
	providerID, out := addNetworkOutputCacheTestPort(t, w)
	receiver := &networkPayloadReceiver{events: make(chan NetworkPayload, 4)}
	_, err := w.AddNode(receiver, "test/NetworkReceiver", "receiver")
	if err != nil {
		t.Fatalf("AddNode receiver: %v", err)
	}

	source := gonnect.NewLoopbackNetwork()
	firstProvider := NewLayer3DatagramMTUProvider(1500, true, false, MTUConstraint{Source: "first path"})
	AttachDatagramMTUProvider(source, firstProvider)
	defer DetachDatagramMTUProvider(source)

	firstLink, _, err := w.AddLink(out, receiver.in)
	if err != nil {
		t.Fatalf("AddLink first: %v", err)
	}
	var cache NetworkOutputCache
	defer cache.CloseAll()
	if !cache.Send(w, providerID, firstLink, source) {
		t.Fatal("Send() first link = false, want true")
	}
	first := receiveOutNetPayload(t, receiver.events)
	if !SameDatagramMTUProvider(first.DatagramMTU, firstProvider) {
		t.Fatal("first payload did not carry the attached MTU provider")
	}
	assertDatagramMTUPayload(t, first, 1472)

	firstProvider.SetMTU(1400, true, false, MTUConstraint{Source: "first path update"})
	if !cache.Send(w, providerID, firstLink, source) {
		t.Fatal("Send() capability update = false, want true")
	}
	updated := receiveOutNetPayload(t, receiver.events)
	if !SameNetworkInstance(updated.Network, first.Network) {
		t.Fatal("capability update replaced a ready network wrapper")
	}
	assertDatagramMTUPayload(t, updated, 1372)

	w.RemoveLink(firstLink)
	cache.Close(firstLink)
	if NetworkIsReady(first.Network) {
		t.Fatal("detached link left its network wrapper ready")
	}

	secondProvider := NewLayer3DatagramMTUProvider(1300, true, false, MTUConstraint{Source: "second path"})
	AttachDatagramMTUProvider(source, secondProvider)
	secondLink, _, err := w.AddLink(out, receiver.in)
	if err != nil {
		t.Fatalf("AddLink second: %v", err)
	}
	if !cache.Send(w, providerID, secondLink, source) {
		t.Fatal("Send() reattached link = false, want true")
	}
	second := receiveOutNetPayload(t, receiver.events)
	if SameNetworkInstance(second.Network, first.Network) {
		t.Fatal("reattached link reused the closed network wrapper")
	}
	if !SameDatagramMTUProvider(second.DatagramMTU, secondProvider) {
		t.Fatal("reattached link did not use the new MTU provider")
	}
	assertDatagramMTUPayload(t, second, 1272)

	firstProvider.SetMTU(1200, true, false, MTUConstraint{Source: "stale path update"})
	if !cache.Send(w, providerID, secondLink, source) {
		t.Fatal("Send() after stale update = false, want true")
	}
	afterStaleUpdate := receiveOutNetPayload(t, receiver.events)
	if !SameDatagramMTUProvider(afterStaleUpdate.DatagramMTU, secondProvider) {
		t.Fatal("a detached provider replaced the active provider")
	}
	assertDatagramMTUPayload(t, afterStaleUpdate, 1272)
}

func TestAcceptNetworkPayloadUpdateKeepsLegacyPayloadBehavior(t *testing.T) {
	base := gonnect.NewLoopbackNetwork()
	first := &legacyTestNetwork{
		NetworkCloser: gonnect.DetachNetwork(base, nil, nil),
	}
	replacement := &legacyTestNetwork{
		NetworkCloser: gonnect.DetachNetwork(base, nil, nil),
	}
	defer closeTestNetwork(replacement)

	current, changed := AcceptNetworkPayloadUpdate(pasta.Event{Payload: NetworkPayload{Network: first}}, NetworkPayload{})
	if !changed || !SameNetworkInstance(current.Network, first) {
		t.Fatal("legacy payload was not accepted")
	}
	if current.DatagramMTU != nil || current.CapabilityRevision != 0 {
		t.Fatalf("legacy payload gained an unexpected MTU capability: %+v", current)
	}
	unchanged, changed := AcceptNetworkPayloadUpdate(pasta.Event{Payload: NetworkPayload{Network: first}}, current)
	if changed || !SameNetworkInstance(unchanged.Network, first) {
		t.Fatal("duplicate legacy payload changed the current network")
	}
	next, changed := AcceptNetworkPayloadUpdate(pasta.Event{Payload: NetworkPayload{Network: replacement}}, current)
	if !changed || !SameNetworkInstance(next.Network, replacement) {
		t.Fatal("replacement legacy payload was not accepted")
	}
	if NetworkIsReady(first) {
		t.Fatal("replacement legacy payload left the old wrapper ready")
	}
	if next.DatagramMTU != nil || next.CapabilityRevision != 0 {
		t.Fatalf("replacement legacy payload gained an unexpected MTU capability: %+v", next)
	}
}

func assertDatagramMTUPayload(t *testing.T, payload NetworkPayload, want int) {
	t.Helper()
	if payload.DatagramMTU == nil {
		t.Fatal("payload.DatagramMTU = nil")
	}
	if payload.CapabilityRevision != payload.DatagramMTU.Revision() {
		t.Fatalf("payload capability revision = %d, provider revision = %d", payload.CapabilityRevision, payload.DatagramMTU.Revision())
	}
	result, err := payload.DatagramMTU.MaxUDPPayload(t.Context(), "udp4", "192.0.2.1:53")
	if err != nil || !result.Known || result.MaxPayload != want {
		t.Fatalf("MaxUDPPayload() = %+v, %v; want %d", result, err, want)
	}
}

type testNetworkWrapper struct {
	NetworkCloser
	wrapped any
	up      bool
	closed  bool
}

type legacyTestNetwork struct {
	NetworkCloser
	closed bool
}

func (*legacyTestNetwork) GetWrapped() any { return nil }

func (*legacyTestNetwork) IsNative() bool { return false }

func (n *legacyTestNetwork) IsUp() (bool, error) { return !n.closed, nil }

func (n *legacyTestNetwork) Close() error {
	n.closed = true
	return n.NetworkCloser.Close()
}

func (w *testNetworkWrapper) GetWrapped() any { return w.wrapped }

func (w *testNetworkWrapper) IsUp() (bool, error) { return w.up, nil }

func (w *testNetworkWrapper) Close() error {
	w.closed = true
	w.up = false
	if w.NetworkCloser == nil {
		return nil
	}
	return w.NetworkCloser.Close()
}

func closeTestNetwork(n NetworkCloser) {
	if n != nil {
		_ = n.Close()
	}
}

func addNetworkOutputCacheTestPort(t *testing.T, w *pasta.Workspace) (uint64, uint64) {
	t.Helper()
	provider := &networkOutputCacheTestProvider{}
	nodeID, err := w.AddNode(provider, "test/NetworkOutputCacheProvider", "provider")
	if err != nil {
		t.Fatalf("AddNode provider: %v", err)
	}
	return nodeID, provider.out
}

type networkOutputCacheTestProvider struct {
	pasta.BasicNode
	out uint64
}

func (n *networkOutputCacheTestProvider) OnInit(w *pasta.Workspace, _ pasta.Logger, id uint64, _ string, _ *pasta.NodeInitData, _, _, _, _ bool) error {
	port, err := w.AddPortLocked(pasta.Port{Node: id, Direction: "left", Name: "Network", Types: []string{TypeNetwork}})
	if err != nil {
		return err
	}
	n.out = port
	return nil
}

func (n *networkOutputCacheTestProvider) PreLinkAdd(port uint64, linkType, portDirection string) error {
	if port != n.out || portDirection != "left" || linkType != TypeNetwork {
		return pasta.LinkTypeErr(linkType)
	}
	return nil
}

type networkPayloadReceiver struct {
	pasta.BasicNode
	in     uint64
	events chan NetworkPayload
}

func (n *networkPayloadReceiver) OnInit(w *pasta.Workspace, _ pasta.Logger, id uint64, _ string, _ *pasta.NodeInitData, _, _, _, _ bool) error {
	port, err := w.AddPortLocked(pasta.Port{
		Node:      id,
		Direction: "right",
		Name:      "Network",
		Types:     []string{TypeNetwork},
	})
	if err != nil {
		return err
	}
	n.in = port
	return nil
}

func (n *networkPayloadReceiver) PreLinkAdd(port uint64, linkType, portDirection string) error {
	if port != n.in || portDirection != "right" || linkType != TypeNetwork {
		return pasta.LinkTypeErr(linkType)
	}
	return nil
}

func (n *networkPayloadReceiver) OnEvent(event pasta.Event, linkType string, _ []string, receiverPortDirection string) error {
	if event.ReceiverPort != n.in || receiverPortDirection != "right" || linkType != TypeNetwork {
		return nil
	}
	payload, ok := event.Payload.(NetworkPayload)
	if ok {
		n.events <- payload
	}
	return nil
}

func receiveOutNetPayload(t *testing.T, events <-chan NetworkPayload) NetworkPayload {
	t.Helper()
	select {
	case payload := <-events:
		if payload.Network == nil {
			t.Fatal("payload.Network = nil, want network")
		}
		return payload
	default:
		t.Fatal("no network payload received")
		return NetworkPayload{}
	}
}

func receiveAnyOutNetPayload(t *testing.T, events <-chan NetworkPayload) NetworkPayload {
	t.Helper()
	select {
	case payload := <-events:
		return payload
	case <-time.After(time.Second):
		t.Fatal("no network payload received")
		return NetworkPayload{}
	}
}
