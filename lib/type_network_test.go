package almagest

import (
	"testing"

	"github.com/asciimoth/gonnect"
	"github.com/asciimoth/pasta/pasta"
)

func TestAcceptNetworkPayloadClosesReadyDuplicateSource(t *testing.T) {
	base := gonnect.NewLoopbackNetwok()
	current := &testNetworkWrapper{
		NetworkCloser: gonnect.DetachNetwork(base, nil),
		wrapped:       base,
		up:            true,
	}
	defer closeTestNetwork(current)
	duplicate := &testNetworkWrapper{
		NetworkCloser: gonnect.DetachNetwork(base, nil),
		wrapped:       base,
		up:            true,
	}

	next, changed := AcceptNetworkPayload(nil, 0, pasta.Event{
		Link:    1,
		Payload: NetworkPayload{Network: duplicate},
	}, current)
	if changed {
		t.Fatal("AcceptNetworkPayload() changed duplicate same-source wrapper, want unchanged")
	}
	if !SameNetworkInstance(next, current) {
		t.Fatal("AcceptNetworkPayload() returned a replacement for duplicate same-source wrapper")
	}
	if !duplicate.closed {
		t.Fatal("duplicate network was not closed")
	}
}

func TestNetworkOutputCacheReusesReadyWrapperAndReplacesClosedWrapper(t *testing.T) {
	w := pasta.NewWorkspace(NewDebugLogFactory(1024))
	defer w.Close()
	providerID, out := addNetworkOutputCacheTestPort(t, w)
	receiver := &outNetReceiver{events: make(chan NetworkPayload, 3)}
	_, err := w.AddNode(receiver, "test/NetworkReceiver", "receiver")
	if err != nil {
		t.Fatalf("AddNode receiver: %v", err)
	}
	link, _, err := w.AddLink(out, receiver.in)
	if err != nil {
		t.Fatalf("AddLink: %v", err)
	}

	source := gonnect.NewLoopbackNetwok()
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

func TestNetworkOutputCacheReplacesChangedSource(t *testing.T) {
	w := pasta.NewWorkspace(NewDebugLogFactory(1024))
	defer w.Close()
	providerID, out := addNetworkOutputCacheTestPort(t, w)
	receiver := &outNetReceiver{events: make(chan NetworkPayload, 2)}
	_, err := w.AddNode(receiver, "test/NetworkReceiver", "receiver")
	if err != nil {
		t.Fatalf("AddNode receiver: %v", err)
	}
	link, _, err := w.AddLink(out, receiver.in)
	if err != nil {
		t.Fatalf("AddLink: %v", err)
	}

	var cache NetworkOutputCache
	if !cache.Send(w, providerID, link, gonnect.NewLoopbackNetwok()) {
		t.Fatal("NetworkOutputCache.Send() = false, want true")
	}
	first := receiveOutNetPayload(t, receiver.events).Network
	if !cache.Send(w, providerID, link, gonnect.NewLoopbackNetwok()) {
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
	port, err := w.AddPort(pasta.Port{Node: id, Direction: "left", Name: "Network", Types: []string{TypeNetwork}})
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
