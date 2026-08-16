package links

import (
	"testing"

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

type testNetworkWrapper struct {
	NetworkCloser
	wrapped any
	up      bool
	closed  bool
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
