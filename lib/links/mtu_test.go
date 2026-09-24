package links

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/asciimoth/gonnect"
)

type failingMTUProvider struct{ err error }

func (p failingMTUProvider) MaxUDPPayload(context.Context, string, string) (DatagramMTUResult, error) {
	return DatagramMTUResult{}, p.err
}
func (failingMTUProvider) Revision() uint64                         { return 1 }
func (failingMTUProvider) SubscribeDatagramMTU(func(uint64)) func() { return func() {} }

func TestMTUEdgeCasesAndHelpers(t *testing.T) {
	if (MTUConstraint{}).IsKnown() || !(MTUConstraint{Value: 1}).IsKnown() {
		t.Fatal("MTUConstraint.IsKnown returned an incorrect result")
	}
	unchanged, err := (MTUConstraint{Path: []uint64{1}}).WithNode(0)
	if err != nil || len(unchanged.Path) != 1 {
		t.Fatalf("WithNode(0) = %+v, %v", unchanged, err)
	}
	for _, test := range []struct {
		name string
		spec string
	}{
		{name: "missing close", spec: "<t"},
		{name: "empty", spec: "<>"},
		{name: "byte arguments", spec: "<b>"},
		{name: "odd byte data", spec: "<b abc>"},
		{name: "random arguments", spec: "<r>"},
		{name: "random invalid", spec: "<r no>"},
		{name: "unknown", spec: "<x>"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := AmneziaInitiationPacketSize(test.spec); err == nil {
				t.Fatalf("AmneziaInitiationPacketSize(%q) accepted invalid input", test.spec)
			}
		})
	}

	addresses := []struct {
		value net.Addr
		want  netip.Addr
	}{
		{value: &net.IPAddr{IP: net.ParseIP("192.0.2.1")}, want: netip.MustParseAddr("192.0.2.1")},
		{value: &net.IPNet{IP: net.ParseIP("192.0.2.2")}, want: netip.MustParseAddr("192.0.2.2")},
		{value: &net.TCPAddr{IP: net.ParseIP("192.0.2.3")}, want: netip.MustParseAddr("192.0.2.3")},
		{value: &net.UDPAddr{IP: net.ParseIP("192.0.2.4")}, want: netip.MustParseAddr("192.0.2.4")},
	}
	for _, test := range addresses {
		if got := addrIP(test.value); got != test.want {
			t.Errorf("addrIP(%T) = %v, want %v", test.value, got, test.want)
		}
	}
	if got := addrIP(nil); got.IsValid() {
		t.Fatalf("addrIP(nil) = %v, want invalid", got)
	}
	if mtu, name := routedMTUResult(1500, 1400, "eth0"); mtu != 1400 || name != "eth0 route PMTU" {
		t.Fatalf("routedMTUResult with PMTU = %d, %q", mtu, name)
	}
	if got := resolvedDatagramDestination("host:53", netip.MustParseAddr("192.0.2.1")); got != "192.0.2.1:53" {
		t.Fatalf("resolvedDatagramDestination = %q", got)
	}
	if got := resolvedDatagramDestination("host", netip.MustParseAddr("192.0.2.1")); got != "192.0.2.1" {
		t.Fatalf("resolvedDatagramDestination without port = %q", got)
	}
	if a, b := learnedDatagramKey(" UDP ", "EXAMPLE:53", true), learnedDatagramKey("udp", "example:53", true); a != b {
		t.Fatalf("learnedDatagramKey did not normalize values: %q != %q", a, b)
	}
}

func TestDatagramMTUProviderNilAndErrorPaths(t *testing.T) {
	var minimum *MinimumDatagramMTUProvider
	minimum.SetProvider(1, nil)
	minimum.RemoveProvider(1)
	minimum.Close()
	minimum.LearnDatagramMTU("udp", "host:1", false, 100)
	if minimum.Revision() != 0 {
		t.Fatal("nil minimum provider has a revision")
	}
	if result, err := minimum.MaxUDPPayload(context.Background(), "udp", "host:1"); err != nil || result.Known {
		t.Fatalf("nil minimum result = %+v, %v", result, err)
	}
	minimum.SubscribeDatagramMTU(nil)()

	combined := NewMinimumDatagramMTUProvider()
	combined.RemoveProvider(99)
	wantErr := errors.New("query failed")
	combined.SetProvider(1, failingMTUProvider{err: wantErr})
	if _, err := combined.MaxUDPPayload(context.Background(), "udp", "host:1"); !errors.Is(err, wantErr) {
		t.Fatalf("MaxUDPPayload error = %v, want %v", err, wantErr)
	}
	combined.Close()

	var layer3 *Layer3DatagramMTUProvider
	layer3.SetMTU(1500, true, true, MTUConstraint{})
	if layer3.Revision() != 0 {
		t.Fatal("nil layer-3 provider has a revision")
	}
	layer3.SubscribeDatagramMTU(nil)()
	if result, err := layer3.MaxUDPPayload(context.Background(), "udp", "host:1"); err != nil || result.Known {
		t.Fatalf("nil layer-3 result = %+v, %v", result, err)
	}

	provider := NewLayer3DatagramMTUProvider(1500, true, false, MTUConstraint{})
	if result, _ := provider.MaxUDPPayload(context.Background(), "udp6", "host:1"); result.Known {
		t.Fatalf("IPv6-only query unexpectedly known: %+v", result)
	}
	provider.SetMTU(1500, false, true, MTUConstraint{})
	if result, _ := provider.MaxUDPPayload(context.Background(), "udp4", "host:1"); result.Known {
		t.Fatalf("IPv4-only query unexpectedly known: %+v", result)
	}
	provider.SetMTU(0, true, true, MTUConstraint{})
	if result, _ := provider.MaxUDPPayload(context.Background(), "udp", "host:1"); result.Known {
		t.Fatalf("zero-MTU query unexpectedly known: %+v", result)
	}
}

func TestNativeDatagramMTULearningAndResolution(t *testing.T) {
	network := (gonnect.NativeConfig{}).Build()
	provider := NewNativeDatagramMTUProvider(network)
	if provider.Revision() != 1 {
		t.Fatalf("initial revision = %d, want 1", provider.Revision())
	}
	changes := make(chan uint64, 2)
	unsubscribe := provider.SubscribeDatagramMTU(func(revision uint64) { changes <- revision })
	defer unsubscribe()
	provider.LearnDatagramMTU("udp4", "127.0.0.1:9", false, 1200)
	select {
	case revision := <-changes:
		if revision != 2 {
			t.Fatalf("learned revision = %d, want 2", revision)
		}
	case <-time.After(time.Second):
		t.Fatal("learning did not notify")
	}
	provider.LearnDatagramMTU("udp4", "127.0.0.1:9", false, 1300)
	provider.LearnDatagramMTU("udp4", "127.0.0.1:9", false, 1)
	result, err := provider.MaxUDPPayload(context.Background(), "udp4", "127.0.0.1:9")
	if err != nil || !result.Known || result.MaxPayload <= 0 || result.IPv6 {
		t.Fatalf("native IPv4 result = %+v, %v", result, err)
	}

	ips, destination, err := resolveDatagramDestination(context.Background(), network, "udp4", "127.0.0.1:9")
	if err != nil || destination != "127.0.0.1:9" || len(ips) != 1 || ips[0] != netip.MustParseAddr("127.0.0.1") {
		t.Fatalf("literal resolution = %v, %q, %v", ips, destination, err)
	}
	if _, _, err := resolveDatagramDestination(context.Background(), network, "udp", "invalid.invalid:9"); err == nil {
		t.Fatal("invalid host resolution succeeded")
	}

	key := learnedDatagramKey("udp4", "expired:9", false)
	provider.mu.Lock()
	provider.sequence++
	entry := learnedDatagramLimit{value: 1000, expires: time.Now().Add(-time.Second), sequence: provider.sequence}
	provider.learned[key] = entry
	provider.mu.Unlock()
	provider.expireLearnedLimitAfter(key, entry)
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		provider.mu.RLock()
		_, found := provider.learned[key]
		provider.mu.RUnlock()
		if !found {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("expired learned limit was not removed")
}

func TestNativeDatagramMTUNilProvider(t *testing.T) {
	var provider *NativeDatagramMTUProvider
	if provider.Revision() != 0 {
		t.Fatal("nil native provider has a revision")
	}
	provider.SubscribeDatagramMTU(nil)()
	provider.LearnDatagramMTU("udp", "host:1", false, 100)
	if result, err := provider.MaxUDPPayload(context.Background(), "udp", "host:1"); err != nil || result.Known {
		t.Fatalf("nil native result = %+v, %v", result, err)
	}
	withoutNetwork := NewNativeDatagramMTUProvider(nil)
	if result, err := withoutNetwork.MaxUDPPayload(context.Background(), "udp", "host:1"); err != nil || result.Known {
		t.Fatalf("networkless native result = %+v, %v", result, err)
	}
}

func TestMTUCalculations(t *testing.T) {
	tests := []struct {
		name string
		got  int
		want int
	}{
		{name: "positive minimum", got: PositiveMinimum(0, 1400, -1, 1280), want: 1280},
		{name: "unknown minimum", got: PositiveMinimum(0, -1), want: 0},
		{name: "IPv4 UDP", got: UDPPayloadFromPathMTU(1500, false), want: 1472},
		{name: "IPv6 UDP", got: UDPPayloadFromPathMTU(1500, true), want: 1452},
		{name: "plain WireGuard", got: WireGuardInnerMTU(1472, 0), want: 1440},
		{name: "Amnezia S4", got: WireGuardInnerMTU(1472, 16), want: 1424},
		{name: "unknown WireGuard", got: WireGuardInnerMTU(0, 16), want: 0},
		{name: "invalid S4", got: WireGuardInnerMTU(1472, -1), want: 0},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if test.got != test.want {
				t.Fatalf("result = %d, want %d", test.got, test.want)
			}
		})
	}
}

func TestMinimumMTUConstraintAndCycle(t *testing.T) {
	first := MTUConstraint{Value: 1400, Source: "first", Path: []uint64{1}}
	second := MTUConstraint{Value: 1280, Source: "second", Path: []uint64{2}}
	selected := MinimumMTUConstraint(MTUConstraint{}, first, second)
	if selected.Value != 1280 || selected.Source != "second" {
		t.Fatalf("MinimumMTUConstraint() = %+v, want second constraint", selected)
	}
	selected.Path[0] = 9
	if second.Path[0] != 2 {
		t.Fatal("MinimumMTUConstraint() did not clone the dependency path")
	}
	if _, err := first.WithNode(1); err == nil {
		t.Fatal("WithNode() accepted an MTU dependency cycle")
	}
	appended, err := first.WithNode(3)
	if err != nil || len(appended.Path) != 2 || appended.Path[1] != 3 {
		t.Fatalf("WithNode(3) = %+v, %v", appended, err)
	}
}

func TestLayer3DatagramMTUProviderFamiliesAndRevision(t *testing.T) {
	provider := NewLayer3DatagramMTUProvider(1500, true, true, MTUConstraint{Source: "test"})
	ipv4, err := provider.MaxUDPPayload(context.Background(), "udp", "192.0.2.1:53")
	if err != nil || !ipv4.Known || ipv4.MaxPayload != 1472 || ipv4.IPv6 {
		t.Fatalf("IPv4 result = %+v, %v", ipv4, err)
	}
	ipv6, err := provider.MaxUDPPayload(context.Background(), "udp", "[2001:db8::1]:53")
	if err != nil || !ipv6.Known || ipv6.MaxPayload != 1452 || !ipv6.IPv6 {
		t.Fatalf("IPv6 result = %+v, %v", ipv6, err)
	}
	host, err := provider.MaxUDPPayload(context.Background(), "udp", "example.invalid:53")
	if err != nil || !host.Known || host.MaxPayload != 1452 {
		t.Fatalf("mixed-family result = %+v, %v", host, err)
	}
	revision := provider.Revision()
	changes := make(chan uint64, 1)
	unsubscribe := provider.SubscribeDatagramMTU(func(next uint64) { changes <- next })
	defer unsubscribe()
	provider.SetMTU(1400, true, true, MTUConstraint{Source: "smaller"})
	if next := <-changes; next <= revision {
		t.Fatalf("revision = %d after %d", next, revision)
	}
}

func TestMinimumDatagramMTUProvider(t *testing.T) {
	first := NewLayer3DatagramMTUProvider(1500, true, false, MTUConstraint{Source: "first"})
	second := NewLayer3DatagramMTUProvider(1400, true, false, MTUConstraint{Source: "second"})
	combined := NewMinimumDatagramMTUProvider()
	defer combined.Close()
	combined.SetProvider(1, first)
	combined.SetProvider(2, second)
	result, err := combined.MaxUDPPayload(context.Background(), "udp4", "192.0.2.1:53")
	if err != nil || !result.Known || result.MaxPayload != 1372 {
		t.Fatalf("combined result = %+v, %v", result, err)
	}
	combined.SetProvider(3, nil)
	result, err = combined.MaxUDPPayload(context.Background(), "udp4", "192.0.2.1:53")
	if err != nil || result.Known {
		t.Fatalf("combined result with unknown path = %+v, %v, want unknown", result, err)
	}
}

func TestAmneziaInitiationPacketSize(t *testing.T) {
	size, err := AmneziaInitiationPacketSize("ignored<b 0xaa55><rc 3><rd 2><t>")
	if err != nil || size != 11 {
		t.Fatalf("AmneziaInitiationPacketSize() = %d, %v, want 11", size, err)
	}
	if _, err := AmneziaInitiationPacketSize("<r -1>"); err == nil {
		t.Fatal("AmneziaInitiationPacketSize() accepted a negative length")
	}
}

func TestDatagramMTUProvidersConcurrentLifecycle(t *testing.T) {
	first := NewLayer3DatagramMTUProvider(1500, true, true, MTUConstraint{Source: "first"})
	second := NewLayer3DatagramMTUProvider(1400, true, false, MTUConstraint{Source: "second"})
	combined := NewMinimumDatagramMTUProvider()
	defer combined.Close()
	combined.SetProvider(1, first)

	var notifications atomic.Uint64
	unsubscribe := combined.SubscribeDatagramMTU(func(uint64) {
		notifications.Add(1)
	})
	defer unsubscribe()

	network := gonnect.NewLoopbackNetwork()
	defer DetachDatagramMTUProvider(network)
	start := make(chan struct{})
	var workers sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		worker := worker
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			for iteration := 0; iteration < 250; iteration++ {
				switch worker % 4 {
				case 0:
					first.SetMTU(1300+(iteration%100), true, iteration%2 == 0, MTUConstraint{Source: "first update"})
					_, _ = first.MaxUDPPayload(context.Background(), "udp", "example.invalid:53")
				case 1:
					stop := first.SubscribeDatagramMTU(func(uint64) {})
					_ = first.Revision()
					stop()
				case 2:
					second.SetMTU(1200+(iteration%100), true, false, MTUConstraint{Source: "second update"})
					combined.SetProvider(2, second)
					_, _ = combined.MaxUDPPayload(context.Background(), "udp4", "192.0.2.1:53")
					combined.RemoveProvider(2)
				case 3:
					provider := DatagramMTUProvider(first)
					if iteration%2 == 0 {
						provider = second
					}
					AttachDatagramMTUProvider(network, provider)
					_ = DatagramMTUProviderFromNetwork(network)
					DetachDatagramMTUProvider(network)
				}
			}
		}()
	}
	close(start)
	workers.Wait()

	first.SetMTU(1300, true, false, MTUConstraint{Source: "final"})
	combined.RemoveProvider(2)
	combined.SetProvider(1, first)
	AttachDatagramMTUProvider(network, first)
	if !SameDatagramMTUProvider(DatagramMTUProviderFromNetwork(network), first) {
		t.Fatal("provider registry did not keep the final attachment")
	}
	result, err := combined.MaxUDPPayload(context.Background(), "udp4", "192.0.2.1:53")
	if err != nil || !result.Known || result.MaxPayload != 1272 {
		t.Fatalf("final combined result = %+v, %v; want 1272", result, err)
	}
	if notifications.Load() == 0 {
		t.Fatal("concurrent updates did not notify the combined provider")
	}
}
