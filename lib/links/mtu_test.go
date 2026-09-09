package links

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/asciimoth/gonnect"
)

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
