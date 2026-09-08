package links

import (
	"errors"
	"net/netip"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/asciimoth/almagest/lib/logging"
	"github.com/asciimoth/gonnect/tun"
	"github.com/asciimoth/pasta/pasta"
)

func TestTunPayloadClose(t *testing.T) {
	base, peer := tun.Pipe(1, 1500, 0, 0)
	defer closeTestTun(base)
	defer closeTestTun(peer)

	wrapper := tun.Detach(base, nil, nil)
	if err := (TunPayload{Tun: wrapper}).Close(); err != nil {
		t.Fatalf("Close() error = %v, want nil", err)
	}
	if _, err := wrapper.MTU(); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("wrapper MTU() error = %v, want os.ErrClosed", err)
	}
	if mtu, err := base.MTU(); err != nil || mtu != 1500 {
		t.Fatalf("base MTU() = %d, %v; want 1500, nil", mtu, err)
	}

	if err := (TunPayload{}).Close(); err != nil {
		t.Fatalf("nil payload Close() error = %v, want nil", err)
	}
}

func TestTunConfigCloneAndIsZero(t *testing.T) {
	sourceRoute := TunSourceRoute{
		Destination: netip.MustParsePrefix("10.0.0.0/24"),
		Source:      netip.MustParseAddr("10.0.0.2"),
	}
	cfg := TunConfig{
		Addrs:        []string{"10.0.0.2/32"},
		Routes:       []string{"10.0.0.0/24"},
		SourceRoutes: []TunSourceRoute{sourceRoute},
		MTU:          1500,
		DNS:          "8.8.8.8",
	}
	clone := cfg.Clone()
	cfg.Addrs[0] = "10.0.0.3/32"
	cfg.Routes[0] = "10.0.1.0/24"
	cfg.SourceRoutes[0].Source = netip.MustParseAddr("10.0.0.3")
	if got, want := clone.Addrs, []string{"10.0.0.2/32"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Clone().Addrs = %#v, want %#v", got, want)
	}
	if got, want := clone.Routes, []string{"10.0.0.0/24"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Clone().Routes = %#v, want %#v", got, want)
	}
	if got, want := clone.SourceRoutes, []TunSourceRoute{sourceRoute}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Clone().SourceRoutes = %#v, want %#v", got, want)
	}
	if clone.DNS != "8.8.8.8" {
		t.Fatalf("Clone().DNS = %q, want 8.8.8.8", clone.DNS)
	}
	withoutDNS := clone.WithoutDNS()
	if withoutDNS.DNS != "" {
		t.Fatalf("WithoutDNS().DNS = %q, want empty", withoutDNS.DNS)
	}
	withoutDNS.Addrs[0] = "mutated"
	if clone.Addrs[0] != "10.0.0.2/32" {
		t.Fatal("WithoutDNS() did not copy Addrs")
	}
	if clone.IsZero() {
		t.Fatal("non-empty config IsZero() = true, want false")
	}
	if (TunConfig{Routes: []string{"10.0.0.0/24"}}).IsZero() {
		t.Fatal("routes-only config IsZero() = true, want false")
	}
	if (TunConfig{SourceRoutes: []TunSourceRoute{sourceRoute}}).IsZero() {
		t.Fatal("source-routes-only config IsZero() = true, want false")
	}
	if (TunConfig{DNS: "8.8.8.8"}).IsZero() {
		t.Fatal("DNS-only config IsZero() = true, want false")
	}
	if !((TunConfig{}).IsZero()) {
		t.Fatal("empty config IsZero() = false, want true")
	}
}

func TestTunConfigWithInferredSourceRoutesDualStackAndExplicitOverride(t *testing.T) {
	explicit := TunSourceRoute{
		Destination: netip.MustParsePrefix("100.80.0.0/16"),
		Source:      netip.MustParseAddr("100.80.0.2"),
	}
	cfg := TunConfig{
		Addrs: []string{
			"invalid",
			"fd7a:115c:a1e0::2/128",
			"100.64.0.2/32",
			"100.80.0.2/32",
		},
		Routes: []string{
			"100.127.255.255/10",
			"100.80.1.1/16",
			"fd7a:115c:a1e0:ffff::1/48",
			"not-a-prefix",
		},
		SourceRoutes: []TunSourceRoute{explicit},
	}

	got := cfg.WithInferredSourceRoutes()
	want := []TunSourceRoute{
		explicit,
		{
			Destination: netip.MustParsePrefix("100.64.0.0/10"),
			Source:      netip.MustParseAddr("100.64.0.2"),
		},
		{
			Destination: netip.MustParsePrefix("fd7a:115c:a1e0::/48"),
			Source:      netip.MustParseAddr("fd7a:115c:a1e0::2"),
		},
	}
	if !reflect.DeepEqual(got.SourceRoutes, want) {
		t.Fatalf("WithInferredSourceRoutes().SourceRoutes = %#v, want %#v", got.SourceRoutes, want)
	}
	if !reflect.DeepEqual(cfg.SourceRoutes, []TunSourceRoute{explicit}) {
		t.Fatalf("input SourceRoutes changed to %#v", cfg.SourceRoutes)
	}
}

func TestTunPortAndPreLinkAdd(t *testing.T) {
	port := TunPort("right", "Tun")
	if port.Direction != "right" || port.Name != "Tun" ||
		!reflect.DeepEqual(port.Types, []string{TypeTun}) {
		t.Fatalf("TunPort() = %#v", port)
	}

	w, leftNode, leftPort, rightNode, rightPort := newTunTypeLinkedWorkspace(t)
	_ = leftNode
	_ = rightNode

	if err := TunPreLinkAdd(w, leftPort, "example.com/other"); !errors.Is(err, pasta.ErrLinkType) {
		t.Fatalf("wrong type error = %v, want pasta.ErrLinkType", err)
	}
	if err := TunPreLinkAdd(w, leftPort, TypeTun); !errors.Is(err, ErrTunPortBusy) {
		t.Fatalf("busy left port error = %v, want ErrTunPortBusy", err)
	}
	if err := TunPreLinkAdd(w, rightPort, TypeTun); !errors.Is(err, ErrTunPortBusy) {
		t.Fatalf("busy right port error = %v, want ErrTunPortBusy", err)
	}

	w.RemoveLink(onlyLink(t, w, leftPort, rightPort))
	if err := TunPreLinkAdd(w, leftPort, TypeTun); err != nil {
		t.Fatalf("unlinked left port error = %v, want nil", err)
	}
	if TunPortHasLink(w, leftPort) {
		t.Fatal("TunPortHasLink() after link removal = true, want false")
	}
}

func TestDetachTunForLinkBindsWrapperResource(t *testing.T) {
	w, node, _, _, rightPort := newTunTypeLinkedWorkspace(t)
	link := onlyPortLink(t, w, rightPort)

	base, peer := tun.Pipe(1, 1500, 0, 0)
	defer closeTestTun(base)
	defer closeTestTun(peer)

	wrapper := DetachTunForLink(w, node, link, base)
	if wrapper == nil {
		t.Fatal("DetachTunForLink() = nil, want wrapper")
	}
	if SameTunInstance(base, wrapper) {
		t.Fatal("DetachTunForLink() returned original Tun instance, want detached wrapper")
	}

	w.RemoveLink(link)
	eventually(t, func() bool {
		_, err := wrapper.MTU()
		return errors.Is(err, os.ErrClosed)
	})
	if mtu, err := base.MTU(); err != nil || mtu != 1500 {
		t.Fatalf("base MTU() after wrapper close = %d, %v; want 1500, nil", mtu, err)
	}
}

func TestAcceptTunPayloadReplacesReadySameSourceWrapper(t *testing.T) {
	w, node, _, _, rightPort := newTunTypeLinkedWorkspace(t)
	link := onlyPortLink(t, w, rightPort)

	base, peer := tun.Pipe(1, 1500, 0, 0)
	defer closeTestTun(base)
	defer closeTestTun(peer)

	current := testTunWrapper{Tun: tun.Detach(base, nil, nil), wrapped: base}
	replacement := testTunWrapper{Tun: tun.Detach(base, nil, nil), wrapped: base}
	defer closeTestTun(replacement)

	next, changed := AcceptTunPayload(w, node, pasta.Event{
		Link:    link,
		Payload: TunPayload{Tun: replacement},
	}, link, current)
	if !changed {
		t.Fatal("AcceptTunPayload() changed = false, want true for fresh same-source wrapper")
	}
	if !SameTunInstance(next, replacement) {
		t.Fatal("AcceptTunPayload() did not return replacement")
	}
	if _, err := current.MTU(); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("current MTU() error = %v, want os.ErrClosed", err)
	}
	if mtu, err := replacement.MTU(); err != nil || mtu != 1500 {
		t.Fatalf("replacement MTU() = %d, %v; want 1500, nil", mtu, err)
	}
}

func TestAcceptTunPayloadReplacesStaleCurrentSameSource(t *testing.T) {
	w, node, _, _, rightPort := newTunTypeLinkedWorkspace(t)
	link := onlyPortLink(t, w, rightPort)

	base, peer := tun.Pipe(1, 1500, 0, 0)
	defer closeTestTun(base)
	defer closeTestTun(peer)

	current := testTunWrapper{Tun: tun.Detach(base, nil, nil), wrapped: base}
	closeTestTun(current)
	replacement := testTunWrapper{Tun: tun.Detach(base, nil, nil), wrapped: base}
	defer closeTestTun(replacement)

	next, changed := AcceptTunPayload(w, node, pasta.Event{
		Link:    link,
		Payload: TunPayload{Tun: replacement},
	}, link, current)
	if !changed {
		t.Fatal("AcceptTunPayload() changed = false, want true for stale current")
	}
	if !SameTunInstance(next, replacement) {
		t.Fatal("AcceptTunPayload() did not return replacement")
	}
	if mtu, err := replacement.MTU(); err != nil || mtu != 1500 {
		t.Fatalf("replacement MTU() = %d, %v; want 1500, nil", mtu, err)
	}
}

func TestTunOutputCacheReusesReadyWrapperAndReplacesClosedWrapper(t *testing.T) {
	w, node, link, events := newTunOutputCacheTestLink(t)

	base, peer := tun.Pipe(1, 1500, 0, 0)
	defer closeTestTun(base)
	defer closeTestTun(peer)

	var cache TunOutputCache
	if !cache.Send(w, node, link, base, nil) {
		t.Fatal("Send() = false, want true")
	}
	first := receiveTunOutputCachePayload(t, events)
	if first == nil {
		t.Fatal("output = nil, want wrapper")
	}

	if !cache.Send(w, node, link, base, nil) {
		t.Fatal("second Send() = false, want true")
	}
	second := receiveTunOutputCachePayload(t, events)
	if !SameTunInstance(first, second) {
		t.Fatal("second Send() replaced a ready same-source wrapper")
	}

	_ = first.Close()
	if !cache.Send(w, node, link, base, nil) {
		t.Fatal("third Send() = false, want true")
	}
	third := receiveTunOutputCachePayload(t, events)
	if SameTunInstance(first, third) {
		t.Fatal("third Send() reused a closed wrapper, want replacement")
	}

	cache.CloseAll()
	closeTestTun(base)
	closeTestTun(peer)
	cache.Wait()
}

func TestTunOutputCacheRefreshReplacesReadySameSourceWrapper(t *testing.T) {
	w, node, link, events := newTunOutputCacheTestLink(t)

	base, peer := tun.Pipe(1, 1500, 0, 0)
	defer closeTestTun(base)
	defer closeTestTun(peer)

	var cache TunOutputCache
	if !cache.Send(w, node, link, base, nil) {
		t.Fatal("TunOutputCache.Send() = false, want true")
	}
	first := receiveTunOutputCachePayload(t, events)
	w.Lock()
	refreshedOK := cache.RefreshLocked(w, node, link, base, nil)
	w.Unlock()
	if !refreshedOK {
		t.Fatal("TunOutputCache.RefreshLocked() = false, want true")
	}
	second := receiveTunOutputCachePayload(t, events)
	if SameTunInstance(first, second) {
		t.Fatal("TunOutputCache.RefreshLocked reused wrapper, want fresh detach")
	}
	eventually(t, func() bool {
		_, err := first.MTU()
		return errors.Is(err, os.ErrClosed)
	})
	if mtu, err := second.MTU(); err != nil || mtu != 1500 {
		t.Fatalf("second MTU() = %d, %v; want 1500, nil", mtu, err)
	}
	cache.CloseAll()
	closeTestTun(base)
	closeTestTun(peer)
	cache.Wait()
}

func TestTunOutputCacheReplacesChangedSource(t *testing.T) {
	w, node, link, events := newTunOutputCacheTestLink(t)

	firstSource, firstPeer := tun.Pipe(1, 1500, 0, 0)
	defer closeTestTun(firstSource)
	defer closeTestTun(firstPeer)
	secondSource, secondPeer := tun.Pipe(1, 1500, 0, 0)
	defer closeTestTun(secondSource)
	defer closeTestTun(secondPeer)

	var cache TunOutputCache
	if !cache.Send(w, node, link, firstSource, nil) {
		t.Fatal("Send(firstSource) = false, want true")
	}
	first := receiveTunOutputCachePayload(t, events)
	if !cache.Send(w, node, link, secondSource, nil) {
		t.Fatal("Send(secondSource) = false, want true")
	}
	second := receiveTunOutputCachePayload(t, events)
	if SameTunInstance(first, second) {
		t.Fatal("Send(secondSource) reused first source wrapper")
	}
	eventually(t, func() bool {
		_, err := first.MTU()
		return errors.Is(err, os.ErrClosed)
	})

	cache.CloseAll()
	closeTestTun(firstSource)
	closeTestTun(firstPeer)
	closeTestTun(secondSource)
	closeTestTun(secondPeer)
	cache.Wait()
}

func TestTunOutputCacheSendWrappedDecoratesDetachedTun(t *testing.T) {
	w, node, link, events := newTunOutputCacheTestLink(t)

	base, peer := tun.Pipe(1, 1500, 0, 0)
	defer closeTestTun(base)
	defer closeTestTun(peer)

	var cache TunOutputCache
	if !cache.SendWrapped(w, node, link, base, func(t tun.Tun) tun.Tun {
		return testTunWrapper{Tun: t, wrapped: t}
	}, nil) {
		t.Fatal("SendWrapped() = false, want true")
	}
	output := receiveTunOutputCachePayload(t, events)
	wrapped, ok := output.(testTunWrapper)
	if !ok {
		t.Fatalf("output type = %T, want testTunWrapper", output)
	}
	if SameTunInstance(base, output) {
		t.Fatal("SendWrapped() emitted the source directly, want detached wrapper")
	}
	if SameTunInstance(base, wrapped.Tun) {
		t.Fatal("SendWrapped() decorated the source directly, want detached wrapper")
	}

	cache.CloseAll()
	closeTestTun(base)
	closeTestTun(peer)
	cache.Wait()
}

func TestTunSourceAndComparisonHelpers(t *testing.T) {
	base, peer := tun.Pipe(1, 1500, 0, 0)
	defer closeTestTun(base)
	defer closeTestTun(peer)

	first := tun.Detach(base, nil, nil)
	defer closeTestTun(first)
	second := tun.Detach(first, nil, nil)
	defer closeTestTun(second)

	if !SameTunInstance(first, first) {
		t.Fatal("SameTunInstance(first, first) = false, want true")
	}
	if SameTunInstance(first, second) {
		t.Fatal("SameTunInstance(first, second) = true, want false")
	}
	if SameTunSource(first, second) {
		t.Fatal("SameTunSource(first, second) = true, want false")
	}

	wrapped := testTunWrapper{Tun: base, wrapped: base}
	nested := testTunWrapper{Tun: wrapped, wrapped: wrapped}
	if !SameTunSource(wrapped, nested) {
		t.Fatal("SameTunSource(wrapped, nested) = false, want true")
	}
	if SameTunSource(first, peer) {
		t.Fatal("SameTunSource(first, peer) = true, want false")
	}
}

type testTunWrapper struct {
	tun.Tun
	wrapped any
}

func (w testTunWrapper) GetWrapped() any { return w.wrapped }

func (w testTunWrapper) IsUp() (bool, error) {
	updown, ok := w.Tun.(interface {
		IsUp() (bool, error)
	})
	if !ok {
		return true, nil
	}
	return updown.IsUp()
}

func closeTestTun(t tun.Tun) {
	_ = t.Close()
}

func receiveTunOutputCachePayload(t *testing.T, events <-chan TunPayload) tun.Tun {
	t.Helper()
	select {
	case payload := <-events:
		if payload.Tun == nil {
			t.Fatal("payload.Tun = nil, want tun")
		}
		return payload.Tun
	case <-time.After(100 * time.Millisecond):
		t.Fatal("no tun payload received")
		return nil
	}
}

func newTunOutputCacheTestLink(t *testing.T) (*pasta.Workspace, uint64, uint64, <-chan TunPayload) {
	t.Helper()

	w := pasta.NewWorkspace(logging.NewDebugLogFactory(1024))
	receiver := &tunPayloadReceiver{events: make(chan TunPayload, 8)}
	if _, err := w.AddNode(receiver, "test/TunOutputCacheReceiver", "receiver"); err != nil {
		t.Fatalf("AddNode receiver: %v", err)
	}
	providerNode := &tunOutputCacheTestProvider{}
	provider, err := w.AddNode(providerNode, "test/TunOutputCacheProvider", "provider")
	if err != nil {
		t.Fatalf("AddNode provider: %v", err)
	}
	link, _, err := w.AddLink(receiver.in, providerNode.out)
	if err != nil {
		t.Fatalf("AddLink: %v", err)
	}
	return w, provider, link, receiver.events
}

type tunOutputCacheTestProvider struct {
	pasta.BasicNode
	out uint64
}

func (n *tunOutputCacheTestProvider) OnInit(w *pasta.Workspace, _ pasta.Logger, id uint64, _ string, _ *pasta.NodeInitData, _, _, _, _ bool) error {
	port, err := w.AddPortLocked(pasta.Port{
		Node:      id,
		Direction: "right",
		Name:      "Tun",
		Types:     []string{TypeTun},
	})
	if err != nil {
		return err
	}
	n.out = port
	return nil
}

func (n *tunOutputCacheTestProvider) PreLinkAdd(port uint64, linkType, portDirection string) error {
	if port != n.out || portDirection != "right" {
		return pasta.LinkTypeErr(linkType)
	}
	return TunPreLinkAdd(nil, 0, linkType)
}

type tunPayloadReceiver struct {
	pasta.BasicNode
	in     uint64
	events chan TunPayload
}

func (n *tunPayloadReceiver) OnInit(w *pasta.Workspace, _ pasta.Logger, id uint64, _ string, _ *pasta.NodeInitData, _, _, _, _ bool) error {
	port, err := w.AddPortLocked(pasta.Port{
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
	if ok {
		n.events <- payload
	}
	return nil
}

func newTunTypeLinkedWorkspace(t *testing.T) (*pasta.Workspace, uint64, uint64, uint64, uint64) {
	t.Helper()

	w := pasta.NewWorkspace(logging.NewDebugLogFactory(1024))
	leftNode, err := w.AddPlaceholderNode("test/LeftTun", []pasta.Port{TunPort("left", "Tun")}, "left")
	if err != nil {
		t.Fatalf("AddPlaceholderNode left: %v", err)
	}
	rightNode, err := w.AddPlaceholderNode("test/RightTun", []pasta.Port{TunPort("right", "Tun")}, "right")
	if err != nil {
		t.Fatalf("AddPlaceholderNode right: %v", err)
	}
	snapshot := w.Snapshot()
	leftPort := snapshot.Nodes[leftNode].LeftPorts[0]
	rightPort := snapshot.Nodes[rightNode].RightPorts[0]
	if _, _, err := w.AddLink(leftPort, rightPort); err != nil {
		t.Fatalf("AddLink: %v", err)
	}
	return w, leftNode, leftPort, rightNode, rightPort
}

func onlyLink(t *testing.T, w *pasta.Workspace, leftPort, rightPort uint64) uint64 {
	t.Helper()
	link, _, ok := w.LinkByPorts(leftPort, rightPort)
	if !ok {
		t.Fatalf("LinkByPorts(%d, %d) not found", leftPort, rightPort)
	}
	return link
}

func onlyPortLink(t *testing.T, w *pasta.Workspace, port uint64) uint64 {
	t.Helper()
	snapshot, ok := w.PortSnapshot(port)
	if !ok {
		t.Fatalf("PortSnapshot(%d) not found", port)
	}
	if len(snapshot.Links) != 1 {
		t.Fatalf("port %d links = %#v, want exactly one link", port, snapshot.Links)
	}
	return snapshot.Links[0]
}

func eventually(t *testing.T, check func() bool) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if check() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("condition was not met before timeout")
}
