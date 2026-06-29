package almagest

import (
	"errors"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/asciimoth/gonnect/tun"
	"github.com/asciimoth/pasta/pasta"
)

func TestTunPayloadClose(t *testing.T) {
	base, peer := tun.Pipe(1, 1500, 0, 0)
	defer closeTestTun(base)
	defer closeTestTun(peer)

	wrapper := tun.Detach(base)
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

func TestTunConfigByLinkSetMergeDelete(t *testing.T) {
	store := TunConfigByLink{}
	cfg := TunConfig{
		Addrs: []string{"10.0.0.2/32", "fd00::2/128"},
		MTU:   1420,
	}
	store.Set(2, cfg)
	cfg.Addrs[0] = "mutated"
	store.Set(1, TunConfig{
		Addrs: []string{"10.0.0.3/32", "10.0.0.2/32"},
		MTU:   1280,
	})
	store.Set(0, TunConfig{Addrs: []string{"ignored"}, MTU: 1})

	merged := store.Merge()
	want := TunConfig{
		Addrs: []string{"10.0.0.3/32", "10.0.0.2/32", "fd00::2/128"},
		MTU:   1280,
	}
	if !reflect.DeepEqual(merged, want) {
		t.Fatalf("Merge() = %#v, want %#v", merged, want)
	}

	store.Delete(1)
	merged = store.Merge()
	want = TunConfig{
		Addrs: []string{"10.0.0.2/32", "fd00::2/128"},
		MTU:   1420,
	}
	if !reflect.DeepEqual(merged, want) {
		t.Fatalf("Merge() after Delete = %#v, want %#v", merged, want)
	}
}

func TestTunConfigCloneAndIsZero(t *testing.T) {
	cfg := TunConfig{Addrs: []string{"10.0.0.2/32"}, MTU: 1500}
	clone := cfg.Clone()
	cfg.Addrs[0] = "10.0.0.3/32"
	if got, want := clone.Addrs, []string{"10.0.0.2/32"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Clone().Addrs = %#v, want %#v", got, want)
	}
	if clone.IsZero() {
		t.Fatal("non-empty config IsZero() = true, want false")
	}
	if !((TunConfig{}).IsZero()) {
		t.Fatal("empty config IsZero() = false, want true")
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

func TestAcceptTunPayloadClosesReadyDuplicateSource(t *testing.T) {
	w, node, _, _, rightPort := newTunTypeLinkedWorkspace(t)
	link := onlyPortLink(t, w, rightPort)

	base, peer := tun.Pipe(1, 1500, 0, 0)
	defer closeTestTun(base)
	defer closeTestTun(peer)

	current := testTunWrapper{Tun: tun.Detach(base), wrapped: base}
	defer closeTestTun(current)
	duplicate := testTunWrapper{Tun: tun.Detach(base), wrapped: base}

	next, changed := AcceptTunPayload(w, node, pasta.Event{
		Link:    link,
		Payload: TunPayload{Tun: duplicate},
	}, link, current)
	if changed {
		t.Fatal("AcceptTunPayload() changed duplicate same-source wrapper, want unchanged")
	}
	if !SameTunInstance(next, current) {
		t.Fatal("AcceptTunPayload() returned a replacement for duplicate same-source wrapper")
	}
	if _, err := duplicate.MTU(); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("duplicate MTU() error = %v, want os.ErrClosed", err)
	}
}

func TestAcceptTunPayloadReplacesStaleCurrentSameSource(t *testing.T) {
	w, node, _, _, rightPort := newTunTypeLinkedWorkspace(t)
	link := onlyPortLink(t, w, rightPort)

	base, peer := tun.Pipe(1, 1500, 0, 0)
	defer closeTestTun(base)
	defer closeTestTun(peer)

	current := testTunWrapper{Tun: tun.Detach(base), wrapped: base}
	closeTestTun(current)
	replacement := testTunWrapper{Tun: tun.Detach(base), wrapped: base}
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
	w, node, _, _, rightPort := newTunTypeLinkedWorkspace(t)
	link := onlyPortLink(t, w, rightPort)

	base, peer := tun.Pipe(1, 1500, 0, 0)
	defer closeTestTun(base)
	defer closeTestTun(peer)

	var cache TunOutputCache
	if !cache.Send(w, node, link, base, nil) {
		t.Fatal("Send() = false, want true")
	}
	first := cache.entries[link].output
	if first == nil {
		t.Fatal("cached output = nil, want wrapper")
	}

	if !cache.Send(w, node, link, base, nil) {
		t.Fatal("second Send() = false, want true")
	}
	second := cache.entries[link].output
	if !SameTunInstance(first, second) {
		t.Fatal("second Send() replaced a ready same-source wrapper")
	}

	_ = first.Close()
	if !cache.Send(w, node, link, base, nil) {
		t.Fatal("third Send() = false, want true")
	}
	third := cache.entries[link].output
	if SameTunInstance(first, third) {
		t.Fatal("third Send() reused a closed wrapper, want replacement")
	}

	cache.CloseAll()
	closeTestTun(base)
	closeTestTun(peer)
	cache.Wait()
}

func TestTunOutputCacheReplacesChangedSource(t *testing.T) {
	w, node, _, _, rightPort := newTunTypeLinkedWorkspace(t)
	link := onlyPortLink(t, w, rightPort)

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
	first := cache.entries[link].output
	if !cache.Send(w, node, link, secondSource, nil) {
		t.Fatal("Send(secondSource) = false, want true")
	}
	second := cache.entries[link].output
	if SameTunInstance(first, second) {
		t.Fatal("Send(secondSource) reused first source wrapper")
	}
	if !SameTunInstance(cache.entries[link].source, secondSource) {
		t.Fatal("cached entry source is not second source")
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
	w, node, _, _, rightPort := newTunTypeLinkedWorkspace(t)
	link := onlyPortLink(t, w, rightPort)

	base, peer := tun.Pipe(1, 1500, 0, 0)
	defer closeTestTun(base)
	defer closeTestTun(peer)

	var cache TunOutputCache
	if !cache.SendWrapped(w, node, link, base, func(t tun.Tun) tun.Tun {
		return testTunWrapper{Tun: t, wrapped: t}
	}, nil) {
		t.Fatal("SendWrapped() = false, want true")
	}
	output := cache.entries[link].output
	if _, ok := output.(testTunWrapper); !ok {
		t.Fatalf("cached output type = %T, want testTunWrapper", output)
	}
	if SameTunInstance(base, output) {
		t.Fatal("SendWrapped() emitted the source directly, want detached wrapper")
	}
	if !SameTunInstance(cache.entries[link].source, base) {
		t.Fatal("cached entry source is not original source")
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

	first := tun.Detach(base)
	defer closeTestTun(first)
	second := tun.Detach(first)
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

func newTunTypeLinkedWorkspace(t *testing.T) (*pasta.Workspace, uint64, uint64, uint64, uint64) {
	t.Helper()

	w := pasta.NewWorkspace(NewDebugLogFactory(1024))
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
