package links

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"os"
	"reflect"
	"sync"
	"sync/atomic"
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

func TestTunConfigMTUConstraintAttachDetachReattachCompatibility(t *testing.T) {
	configs := TunConfigByLink{}
	configs.Set(1, TunConfig{MTU: 1400, Addrs: []string{"192.0.2.1/32"}})
	constraint := MTUConstraint{Value: 1300, Source: "nested tunnel", Path: []uint64{10, 20}}
	configs.Set(2, TunConfig{MTU: 1300, MTUConstraint: &constraint})

	constraint.Source = "mutated caller value"
	constraint.Path[0] = 99
	merged := configs.Merge()
	if merged.MTU != 1300 || merged.MTUConstraint == nil {
		t.Fatalf("Merge() = %+v, want constrained MTU 1300", merged)
	}
	if merged.MTUConstraint.Source != "nested tunnel" || !reflect.DeepEqual(merged.MTUConstraint.Path, []uint64{10, 20}) {
		t.Fatalf("stored constraint = %+v, want an independent copy", merged.MTUConstraint)
	}
	merged.MTUConstraint.Path[0] = 77
	if next := configs.Merge(); next.MTUConstraint == nil || next.MTUConstraint.Path[0] != 10 {
		t.Fatal("Merge() returned a constraint that aliases stored state")
	}

	configs.Delete(2)
	legacy := configs.Merge()
	if legacy.MTU != 1400 || legacy.MTUConstraint != nil {
		t.Fatalf("Merge() after detach = %+v, want legacy MTU 1400 without provenance", legacy)
	}

	replacement := MTUConstraint{Value: 1350, Source: "reattached tunnel", Path: []uint64{30}}
	configs.Set(2, TunConfig{MTU: 1350, MTUConstraint: &replacement})
	reattached := configs.Merge()
	if reattached.MTU != 1350 || reattached.MTUConstraint == nil || reattached.MTUConstraint.Source != "reattached tunnel" {
		t.Fatalf("Merge() after reattach = %+v, want constrained MTU 1350", reattached)
	}

	configs.Delete(1)
	configs.Delete(2)
	if final := configs.Merge(); !final.IsZero() {
		t.Fatalf("Merge() after all links detach = %+v, want zero config", final)
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

func TestTunOutputCacheReplacementDoesNotStartSecondSourceRead(t *testing.T) {
	w, node, link, events := newTunOutputCacheTestLink(t)

	base, peer := tun.Pipe(1, 1500, 0, 0)
	source := newExclusiveReadTun(base)
	var cache TunOutputCache
	t.Cleanup(func() {
		cache.CloseAll()
		closeTestTun(base)
		closeTestTun(peer)
		cache.Wait()
	})

	if !cache.Send(w, node, link, source, nil) {
		t.Fatal("Send() = false, want true")
	}
	first := receiveTunOutputCachePayload(t, events)
	select {
	case <-source.readStarted:
	case <-time.After(time.Second):
		t.Fatal("source read did not start")
	}

	// Model a runtime attachment failure. The consumer closes its payload and
	// asks the provider for a fresh wrapper on the same graph link.
	closeTestTun(first)
	w.Lock()
	refreshed := cache.RefreshLocked(w, node, link, source, nil)
	w.Unlock()
	if !refreshed {
		t.Fatal("RefreshLocked() = false, want true")
	}
	second := receiveTunOutputCachePayload(t, events)

	packet := []byte{0x45, 0, 0, 4}
	result := make(chan error, 1)
	go func() {
		buf := make([]byte, 64)
		sizes := []int{0}
		n, err := second.Read([][]byte{buf}, sizes, 0)
		if err != nil {
			result <- err
			return
		}
		if n != 1 || !bytes.Equal(buf[:sizes[0]], packet) {
			result <- fmt.Errorf("read = %d, %v; want 1, %v", n, buf[:sizes[0]], packet)
			return
		}
		result <- nil
	}()
	if _, err := peer.Write([][]byte{packet}, 0); err != nil {
		t.Fatalf("peer.Write() error = %v", err)
	}
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("replacement Read() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("replacement did not receive the first source packet")
	}
	if source.hadConcurrentRead() {
		t.Fatal("replacement started a concurrent read on the same source")
	}
}

func TestTunOutputCacheReplacesSourcePumpAfterReadFailure(t *testing.T) {
	w, node, link, events := newTunOutputCacheTestLink(t)

	base, peer := tun.Pipe(1, 1500, 0, 0)
	source := &failNextReadTun{
		Tun:         base,
		readStarted: make(chan struct{}),
	}
	var cache TunOutputCache
	t.Cleanup(func() {
		cache.CloseAll()
		closeTestTun(base)
		closeTestTun(peer)
		cache.Wait()
	})

	if !cache.Send(w, node, link, source, nil) {
		t.Fatal("Send() = false, want true")
	}
	first := receiveTunOutputCachePayload(t, events)
	select {
	case <-source.readStarted:
	case <-time.After(time.Second):
		t.Fatal("source Read() did not start")
	}

	// Complete the current source read. The next source read fails and makes
	// the detached root unhealthy.
	source.failNext.Store(true)
	if _, err := peer.Write([][]byte{{0x45, 0, 0, 1}}, 0); err != nil {
		t.Fatalf("peer.Write() error = %v", err)
	}
	eventually(t, func() bool {
		return len(cache.sources) == 1 && !TunIsReady(cache.sources[0].root)
	})

	// A consumer closes the failed payload before it requests a replacement.
	// The stable source root must also be replaced because its read pump ended.
	closeTestTun(first)
	w.Lock()
	refreshed := cache.RefreshLocked(w, node, link, source, nil)
	w.Unlock()
	if !refreshed {
		t.Fatal("RefreshLocked() = false, want true")
	}
	second := receiveTunOutputCachePayload(t, events)

	packet := []byte{0x45, 0, 0, 4}
	result := make(chan error, 1)
	go func() {
		buf := make([]byte, 64)
		sizes := []int{0}
		n, err := second.Read([][]byte{buf}, sizes, 0)
		if err == nil && (n != 1 || !bytes.Equal(buf[:sizes[0]], packet)) {
			err = fmt.Errorf("read = %d, %v; want 1, %v", n, buf[:sizes[0]], packet)
		}
		result <- err
	}()
	if _, err := peer.Write([][]byte{packet}, 0); err != nil {
		t.Fatalf("peer.Write() error = %v", err)
	}
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("replacement Read() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("replacement source pump did not recover")
	}
}

func TestTunOutputCacheReplacesSourcePumpAfterWriteFailure(t *testing.T) {
	w, node, link, events := newTunOutputCacheTestLink(t)

	base, peer := tun.Pipe(1, 1500, 0, 0)
	source := &failOnceWriteTun{Tun: base}
	var cache TunOutputCache
	t.Cleanup(func() {
		cache.CloseAll()
		closeTestTun(base)
		closeTestTun(peer)
		cache.Wait()
	})

	if !cache.Send(w, node, link, source, nil) {
		t.Fatal("Send() = false, want true")
	}
	first := receiveTunOutputCachePayload(t, events)
	packet := []byte{0x45, 0, 0, 4}
	if _, err := first.Write([][]byte{packet}, 0); err == nil {
		t.Fatal("first Write() error = nil, want injected failure")
	}
	eventually(t, func() bool {
		return len(cache.sources) == 1 && !TunIsReady(cache.sources[0].root)
	})

	closeTestTun(first)
	w.Lock()
	refreshed := cache.RefreshLocked(w, node, link, source, nil)
	w.Unlock()
	if !refreshed {
		t.Fatal("RefreshLocked() = false, want true")
	}
	second := receiveTunOutputCachePayload(t, events)

	result := make(chan error, 1)
	go func() {
		buf := make([]byte, 64)
		sizes := []int{0}
		n, err := peer.Read([][]byte{buf}, sizes, 0)
		if err == nil && (n != 1 || !bytes.Equal(buf[:sizes[0]], packet)) {
			err = fmt.Errorf("read = %d, %v; want 1, %v", n, buf[:sizes[0]], packet)
		}
		result <- err
	}()
	if _, err := second.Write([][]byte{packet}, 0); err != nil {
		t.Fatalf("replacement Write() error = %v", err)
	}
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("peer Read() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("replacement source pump did not recover")
	}
}

func TestTunOutputCacheReadSurvivesSourceMTUGrowth(t *testing.T) {
	tests := []struct {
		name       string
		initialMTU int
		grownMTU   int
		mro        int
		batch      int
		emitEvent  bool
	}{
		{name: "1420 to 1428 batch 1", initialMTU: 1420, grownMTU: 1428, batch: 1, emitEvent: true},
		{name: "1420 to 1500 MRO 16 batch 128", initialMTU: 1420, grownMTU: 1500, mro: 16, batch: 128, emitEvent: true},
		{name: "1280 to 9000 MRO 256 batch 128 without event", initialMTU: 1280, grownMTU: 9000, mro: 256, batch: 128},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			w, node, link, events := newTunOutputCacheTestLink(t)
			source := newGrowingMTUTunWithGeometry(test.initialMTU, test.mro, test.batch)
			var cache TunOutputCache
			t.Cleanup(func() {
				cache.CloseAll()
				closeTestTun(source)
				cache.Wait()
			})

			if !cache.Send(w, node, link, source, nil) {
				t.Fatal("Send() = false, want true")
			}
			output := receiveTunOutputCachePayload(t, events)
			if mtu, err := output.MTU(); err != nil || mtu != test.initialMTU {
				t.Fatalf("output MTU() = %d, %v; want %d, nil", mtu, err, test.initialMTU)
			}
			if got := output.MRO(); got != test.mro {
				t.Fatalf("output MRO() = %d, want %d", got, test.mro)
			}
			if got := output.BatchSize(); got != test.batch {
				t.Fatalf("output BatchSize() = %d, want %d", got, test.batch)
			}
			assertGrowingMTUReadCapacity(t, source, test.initialMTU)
			if test.emitEvent {
				source.setMTU(test.grownMTU)
			} else {
				source.mtu.Store(int64(test.grownMTU))
			}
			if mtu, err := output.MTU(); err != nil || mtu != test.grownMTU {
				t.Fatalf("output MTU() after growth = %d, %v; want %d, nil", mtu, err, test.grownMTU)
			}

			discarded := testGrowingPacket(test.grownMTU, 0x41)
			first := testGrowingPacket(test.grownMTU, 0x51)
			later := testGrowingPacket(test.grownMTU, 0x61)
			result := readGrowingPackets(output, first, later)
			// This packet completes the read that is blocked with the old
			// capacity. Gonnect discards it and grows the same pump.
			sendGrowingPacket(t, source, discarded)
			assertGrowingMTUReadCapacity(t, source, test.grownMTU)
			sendGrowingPacket(t, source, first)
			assertGrowingMTUReadCapacity(t, source, test.grownMTU)
			sendGrowingPacket(t, source, later)
			assertGrowingReadResult(t, result, "MTU growth")

			if len(cache.sources) != 1 || !cache.sources[0].ready() {
				t.Fatal("MTU growth replaced or failed the source root")
			}
			if source.concurrentRead.Load() {
				t.Fatal("source had concurrent reads during MTU growth")
			}
		})
	}
}

func TestTunOutputCacheRecoversAfterSourceShortBuffer(t *testing.T) {
	w, node, link, events := newTunOutputCacheTestLink(t)
	source := newGrowingMTUTunWithGeometry(1420, 16, 128)
	var cache TunOutputCache
	t.Cleanup(func() {
		cache.CloseAll()
		closeTestTun(source)
		cache.Wait()
	})

	if !cache.Send(w, node, link, source, nil) {
		t.Fatal("Send() = false, want true")
	}
	output := receiveTunOutputCachePayload(t, events)
	state := cache.sources[0]
	assertGrowingMTUReadCapacity(t, source, 1420)
	source.mtu.Store(9000)
	source.shortBufferNext.Store(true)
	first := testGrowingPacket(9000, 0x72)
	later := testGrowingPacket(9000, 0x73)
	result := readGrowingPackets(output, first, later)

	// The source reports a typed capacity error for the read that used the old
	// visible length. Gonnect polls MTU, grows the pump, and retries.
	sendGrowingPacket(t, source, []byte{0x45})
	assertGrowingMTUReadCapacity(t, source, 9000)
	sendGrowingPacket(t, source, first)
	assertGrowingMTUReadCapacity(t, source, 9000)
	sendGrowingPacket(t, source, later)
	assertGrowingReadResult(t, result, "source short buffer")

	if len(cache.sources) != 1 || cache.sources[0] != state || !state.ready() {
		t.Fatal("short-buffer recovery replaced or failed the source root")
	}
	if source.concurrentRead.Load() {
		t.Fatal("source had concurrent reads during short-buffer recovery")
	}
}

func TestTunOutputCacheMTUGrowthThroughEightNestedDetachedWrappers(t *testing.T) {
	w, node, link, events := newTunOutputCacheTestLink(t)
	source := newGrowingMTUTunWithGeometry(1420, 16, 128)
	var cache TunOutputCache
	var nested []*tun.DetachedTun
	t.Cleanup(func() {
		for i := len(nested) - 1; i >= 0; i-- {
			closeTestTun(nested[i])
		}
		cache.CloseAll()
		closeTestTun(source)
		for i := len(nested) - 1; i >= 0; i-- {
			nested[i].Wait()
		}
		cache.Wait()
	})

	if !cache.Send(w, node, link, source, nil) {
		t.Fatal("Send() = false, want true")
	}
	output := receiveTunOutputCachePayload(t, events)
	for range 8 {
		detached := tun.Detach(output, nil, nil)
		nested = append(nested, detached)
		output = detached
	}
	assertGrowingMTUReadCapacity(t, source, 1420)
	source.mtu.Store(9000)
	first := testGrowingPacket(9000, 0x81)
	later := testGrowingPacket(9000, 0x82)
	result := readGrowingPackets(output, first, later)
	sendGrowingPacket(t, source, testGrowingPacket(9000, 0x80))
	assertGrowingMTUReadCapacity(t, source, 9000)
	sendGrowingPacket(t, source, first)
	assertGrowingMTUReadCapacity(t, source, 9000)
	sendGrowingPacket(t, source, later)
	assertGrowingReadResult(t, result, "eight nested detached wrappers")

	if source.concurrentRead.Load() {
		t.Fatal("nested wrappers started concurrent source reads")
	}
	if len(cache.sources) != 1 || !cache.sources[0].ready() {
		t.Fatal("nested MTU growth replaced or failed the source root")
	}
}

func TestTunOutputCacheRecoversAfterReadExceedsInitialMTU(t *testing.T) {
	w, node, link, events := newTunOutputCacheTestLink(t)

	source := newGrowingMTUTun(1420)
	var cache TunOutputCache
	t.Cleanup(func() {
		cache.CloseAll()
		closeTestTun(source)
		cache.Wait()
	})

	if !cache.Send(w, node, link, source, nil) {
		t.Fatal("Send() = false, want true")
	}
	output := receiveTunOutputCachePayload(t, events)
	state := cache.sources[0]
	assertGrowingMTUReadCapacity(t, source, 1420)
	source.mtu.Store(1600)

	discarded := make([]byte, 1600)
	discarded[0] = 0x45
	want := bytes.Repeat([]byte{0x6b}, 1600)
	want[0] = 0x45
	result := make(chan error, 1)
	go func() {
		buf := make([]byte, len(want))
		sizes := []int{0}
		n, err := output.Read([][]byte{buf}, sizes, 0)
		if err == nil && (n != 1 || sizes[0] != len(want) || !bytes.Equal(buf, want)) {
			err = fmt.Errorf("read = %d, size %d; want 1, size %d", n, sizes[0], len(want))
		}
		result <- err
	}()
	source.packets <- discarded
	assertGrowingMTUReadCapacity(t, source, 1600)
	source.packets <- want
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("Read() after capacity recovery error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Read() after capacity recovery did not return")
	}
	if len(cache.sources) != 1 || cache.sources[0] != state || !state.ready() {
		t.Fatal("capacity recovery replaced or failed the source root")
	}
	if source.concurrentRead.Load() {
		t.Fatal("source had concurrent reads during capacity recovery")
	}
}

func TestTunOutputCacheRebindSourceKeepsReadyWrapper(t *testing.T) {
	w, node, link, events := newTunOutputCacheTestLink(t)

	base, peer := tun.Pipe(1, 1500, 0, 0)
	defer closeTestTun(base)
	defer closeTestTun(peer)
	firstSource := &opaqueTunSource{Tun: base}
	secondSource := &opaqueTunSource{Tun: base}

	var cache TunOutputCache
	if !cache.Send(w, node, link, firstSource, nil) {
		t.Fatal("Send(firstSource) = false, want true")
	}
	first := receiveTunOutputCachePayload(t, events)

	cache.RebindSource(firstSource, secondSource)
	if !cache.Send(w, node, link, secondSource, nil) {
		t.Fatal("Send(secondSource) = false, want true")
	}
	second := receiveTunOutputCachePayload(t, events)
	if !SameTunInstance(first, second) {
		t.Fatal("Send after RebindSource replaced the ready wrapper")
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

type opaqueTunSource struct {
	tun.Tun
}

type exclusiveReadTun struct {
	tun.Tun

	mu             sync.Mutex
	reading        bool
	concurrentRead bool
	readStarted    chan struct{}
	startOnce      sync.Once
}

type failNextReadTun struct {
	tun.Tun
	failNext    atomic.Bool
	readStarted chan struct{}
	startOnce   sync.Once
}

type failOnceWriteTun struct {
	tun.Tun
	failed atomic.Bool
}

type growingMTUTun struct {
	mtu             atomic.Int64
	mro             int
	batch           int
	packets         chan []byte
	readCaps        chan int
	events          chan tun.Event
	closed          chan struct{}
	closeOnce       sync.Once
	readMu          sync.Mutex
	reading         bool
	concurrentRead  atomic.Bool
	shortBufferNext atomic.Bool
}

func newGrowingMTUTun(mtu int) *growingMTUTun {
	return newGrowingMTUTunWithGeometry(mtu, 0, 1)
}

func newGrowingMTUTunWithGeometry(mtu, mro, batch int) *growingMTUTun {
	t := &growingMTUTun{
		mro:      mro,
		batch:    batch,
		packets:  make(chan []byte),
		readCaps: make(chan int, 16),
		events:   make(chan tun.Event, 8),
		closed:   make(chan struct{}),
	}
	t.mtu.Store(int64(mtu))
	t.events <- tun.EventUp
	return t
}

func (t *growingMTUTun) File() *os.File { return nil }

func (t *growingMTUTun) IsNative() bool { return true }

func (t *growingMTUTun) Read(bufs [][]byte, sizes []int, offset int) (int, error) {
	t.readMu.Lock()
	if t.reading {
		t.concurrentRead.Store(true)
		t.readMu.Unlock()
		return 0, errors.New("concurrent source read")
	}
	t.reading = true
	t.readMu.Unlock()
	defer func() {
		t.readMu.Lock()
		t.reading = false
		t.readMu.Unlock()
	}()
	capacity := -1
	if len(bufs) > 0 && offset >= 0 && offset <= len(bufs[0]) {
		capacity = len(bufs[0]) - offset
	}
	select {
	case t.readCaps <- capacity:
	case <-t.closed:
		return 0, os.ErrClosed
	}

	select {
	case <-t.closed:
		return 0, os.ErrClosed
	case packet := <-t.packets:
		if t.shortBufferNext.CompareAndSwap(true, false) {
			return 0, io.ErrShortBuffer
		}
		copy(bufs[0][offset:], packet)
		sizes[0] = len(packet)
		return 1, nil
	}
}

func (t *growingMTUTun) Write(bufs [][]byte, _ int) (int, error) {
	select {
	case <-t.closed:
		return 0, os.ErrClosed
	default:
		return len(bufs), nil
	}
}

func (t *growingMTUTun) MWO() int { return 0 }

func (t *growingMTUTun) MRO() int { return t.mro }

func (t *growingMTUTun) MTU() (int, error) { return int(t.mtu.Load()), nil }

func (t *growingMTUTun) setMTU(mtu int) {
	t.mtu.Store(int64(mtu))
	t.events <- tun.EventMTUUpdate
}

func (t *growingMTUTun) Name() (string, error) { return "growing-mtu", nil }

func (t *growingMTUTun) Events() <-chan tun.Event { return t.events }

func (t *growingMTUTun) Close() error {
	t.closeOnce.Do(func() {
		close(t.closed)
		close(t.events)
	})
	return nil
}

func (t *growingMTUTun) BatchSize() int { return t.batch }

func (t *failNextReadTun) Read(bufs [][]byte, sizes []int, offset int) (int, error) {
	fail := t.failNext.CompareAndSwap(true, false)
	t.startOnce.Do(func() {
		if t.readStarted != nil {
			close(t.readStarted)
		}
	})
	if fail {
		return 0, errors.New("injected source read failure")
	}
	return t.Tun.Read(bufs, sizes, offset)
}

func assertGrowingMTUReadCapacity(t *testing.T, source *growingMTUTun, want int) {
	t.Helper()
	select {
	case got := <-source.readCaps:
		if got != want {
			t.Fatalf("source read capacity = %d, want %d", got, want)
		}
	case <-time.After(time.Second):
		t.Fatalf("source Read() did not start with capacity %d", want)
	}
}

func testGrowingPacket(size int, fill byte) []byte {
	packet := bytes.Repeat([]byte{fill}, size)
	packet[0] = 0x45
	return packet
}

func sendGrowingPacket(t *testing.T, source *growingMTUTun, packet []byte) {
	t.Helper()
	select {
	case source.packets <- packet:
	case <-time.After(time.Second):
		t.Fatal("source did not accept test packet")
	}
}

func readGrowingPackets(output tun.Tun, want ...[]byte) <-chan error {
	result := make(chan error, 1)
	go func() {
		for i, packet := range want {
			offset := output.MRO()
			buf := make([]byte, offset+len(packet))
			sizes := []int{0}
			count, err := output.Read([][]byte{buf}, sizes, offset)
			if err != nil {
				result <- err
				return
			}
			if count != 1 || sizes[0] != len(packet) || !bytes.Equal(buf[offset:], packet) {
				result <- fmt.Errorf("read %d = %d, size %d; want 1, size %d", i, count, sizes[0], len(packet))
				return
			}
		}
		result <- nil
	}()
	return result
}

func assertGrowingReadResult(t *testing.T, result <-chan error, scenario string) {
	t.Helper()
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("Read() after %s error = %v", scenario, err)
		}
	case <-time.After(time.Second):
		t.Fatalf("Read() after %s did not return", scenario)
	}
}

func (t *failOnceWriteTun) Write(bufs [][]byte, offset int) (int, error) {
	if t.failed.CompareAndSwap(false, true) {
		return 0, errors.New("injected source write failure")
	}
	return t.Tun.Write(bufs, offset)
}

func newExclusiveReadTun(t tun.Tun) *exclusiveReadTun {
	return &exclusiveReadTun{Tun: t, readStarted: make(chan struct{})}
}

func (t *exclusiveReadTun) Read(bufs [][]byte, sizes []int, offset int) (int, error) {
	t.mu.Lock()
	if t.reading {
		t.concurrentRead = true
		t.mu.Unlock()
		return 0, errors.New("concurrent source read")
	}
	t.reading = true
	t.startOnce.Do(func() { close(t.readStarted) })
	t.mu.Unlock()
	defer func() {
		t.mu.Lock()
		t.reading = false
		t.mu.Unlock()
	}()
	return t.Tun.Read(bufs, sizes, offset)
}

func (t *exclusiveReadTun) hadConcurrentRead() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.concurrentRead
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
