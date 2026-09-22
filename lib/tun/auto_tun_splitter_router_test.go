package tunnodes

import (
	"encoding/binary"
	"net/netip"
	"sync"
	"testing"
)

func TestAutoTunSplitterRouterRoutesByLearnedAddressesAndPrefixes(t *testing.T) {
	router := newAutoTunSplitterRouter()
	router.SetConfig(2, TunConfig{
		Addrs: []string{"100.64.0.2/10", "fd7a:115c:a1e0::2/48"},
		Routes: []string{
			"100.64.0.0/10",
			"fd7a:115c:a1e0::/48",
			"invalid route",
		},
	})
	router.SetConfig(3, TunConfig{
		Addrs: []string{"not an address", "192.0.2.3"},
		Routes: []string{
			"100.80.0.0/16",
			"fd7a:115c:a1e0:1234::/64",
			"203.0.113.9",
		},
		SourceRoutes: []TunSourceRoute{{
			Destination: netip.MustParsePrefix("198.51.100.0/24"),
			Source:      netip.MustParseAddr("192.0.2.3"),
		}},
	})
	router.SetConfig(4, TunConfig{
		Addrs:  []string{"192.0.2.4"},
		Routes: []string{"198.51.100.0/24"},
	})
	router.SetConfig(5, TunConfig{
		Addrs: []string{"203.0.113.5"},
	})

	tests := []struct {
		name        string
		source      netip.Addr
		destination netip.Addr
		want        int
	}{
		{
			name:        "more-specific IPv4 destination overrides source association",
			source:      netip.MustParseAddr("100.64.0.2"),
			destination: netip.MustParseAddr("100.80.0.1"),
			want:        3,
		},
		{
			name:        "address CIDR does not claim neighboring sources",
			source:      netip.MustParseAddr("100.64.0.3"),
			destination: netip.MustParseAddr("100.80.0.1"),
			want:        3,
		},
		{
			name:        "longest IPv4 destination prefix",
			source:      netip.MustParseAddr("10.0.0.1"),
			destination: netip.MustParseAddr("100.80.255.254"),
			want:        3,
		},
		{
			name:        "broader IPv4 destination prefix",
			source:      netip.MustParseAddr("10.0.0.1"),
			destination: netip.MustParseAddr("100.100.0.1"),
			want:        2,
		},
		{
			name:        "bare route address becomes a host prefix",
			source:      netip.MustParseAddr("10.0.0.1"),
			destination: netip.MustParseAddr("203.0.113.9"),
			want:        3,
		},
		{
			name:        "unknown source uses lower slot for equal prefix",
			source:      netip.MustParseAddr("10.0.0.1"),
			destination: netip.MustParseAddr("198.51.100.7"),
			want:        3,
		},
		{
			name:        "associated source breaks equal prefix tie",
			source:      netip.MustParseAddr("192.0.2.4"),
			destination: netip.MustParseAddr("198.51.100.7"),
			want:        4,
		},
		{
			name:        "advertised IPv4 source does not claim unmatched destination",
			source:      netip.MustParseAddr("100.64.0.2"),
			destination: netip.MustParseAddr("8.8.8.8"),
			want:        1,
		},
		{
			name:        "advertised IPv6 source does not claim unmatched destination",
			source:      netip.MustParseAddr("fd7a:115c:a1e0::2"),
			destination: netip.MustParseAddr("2001:db8::1"),
			want:        1,
		},
		{
			name:        "longest IPv6 destination prefix",
			source:      netip.MustParseAddr("2001:db8::2"),
			destination: netip.MustParseAddr("fd7a:115c:a1e0:1234::99"),
			want:        3,
		},
		{
			name:        "address without route does not claim traffic",
			source:      netip.MustParseAddr("203.0.113.5"),
			destination: netip.MustParseAddr("192.0.2.99"),
			want:        1,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			packet := autoTunTestPacket(test.source, test.destination)
			if got := routeAutoTunPacket(router, packet, 0); got != test.want {
				t.Fatalf("Route(%s -> %s) = %d, want %d", test.source, test.destination, got, test.want)
			}
		})
	}
}

func TestAutoTunSplitterRouterUpdatesAreCopiedAndDeterministic(t *testing.T) {
	router := newAutoTunSplitterRouter()
	cfg := TunConfig{
		Addrs:  []string{"192.0.2.2/24"},
		Routes: []string{"10.0.0.0/8"},
	}
	router.SetConfig(3, cfg)
	cfg.Addrs[0] = "192.0.2.4/24"
	cfg.Routes[0] = "172.16.0.0/12"

	packet := autoTunTestPacket(
		netip.MustParseAddr("192.0.2.2"),
		netip.MustParseAddr("10.1.2.3"),
	)
	if got := routeAutoTunPacket(router, packet, 0); got != 3 {
		t.Fatalf("Route after caller mutation = %d, want copied slot 3 config", got)
	}

	// The lower slot wins when equal route claims have the same associated
	// source address.
	router.SetConfig(2, TunConfig{
		Addrs:  []string{"192.0.2.2/32"},
		Routes: []string{"10.0.0.0/8"},
	})
	if got := routeAutoTunPacket(router, packet, 0); got != 2 {
		t.Fatalf("Route with equal learned data = %d, want lower slot 2", got)
	}

	router.ClearConfig(2)
	if got := routeAutoTunPacket(router, packet, 0); got != 3 {
		t.Fatalf("Route after ClearConfig(2) = %d, want slot 3", got)
	}
	router.ClearConfig(3)
	if got := routeAutoTunPacket(router, packet, 0); got != 1 {
		t.Fatalf("Route after clearing all configs = %d, want fallback slot 1", got)
	}

	// Invalid slot operations must not change the fallback table.
	router.SetConfig(0, TunConfig{Routes: []string{"0.0.0.0/0"}})
	router.SetConfig(tunSplitterMaxSlots+1, TunConfig{Routes: []string{"0.0.0.0/0"}})
	if got := routeAutoTunPacket(router, packet, 0); got != 1 {
		t.Fatalf("Route after invalid slot updates = %d, want fallback slot 1", got)
	}
}

func TestAutoTunSplitterRouterNormalizesPrefixesAndAssociatedSources(t *testing.T) {
	router := newAutoTunSplitterRouter()
	router.SetConfig(2, TunConfig{
		SourceRoutes: []TunSourceRoute{{
			Destination: netip.MustParsePrefix("::ffff:198.51.100.129/120"),
			Source:      netip.MustParseAddr("::ffff:192.0.2.2"),
		}},
	})
	router.SetConfig(3, TunConfig{
		SourceRoutes: []TunSourceRoute{{
			Destination: netip.MustParsePrefix("198.51.100.0/24"),
			Source:      netip.MustParseAddr("192.0.2.3"),
		}},
	})
	router.SetConfig(4, TunConfig{
		Routes: []string{"10.1.2.3/8"},
	})
	router.SetConfig(5, TunConfig{
		SourceRoutes: []TunSourceRoute{{
			Destination: netip.MustParsePrefix("203.0.113.128/25"),
			Source:      netip.MustParseAddr("2001:db8::5"),
		}},
	})

	tests := []struct {
		name        string
		source      string
		destination string
		want        int
	}{
		{
			name:        "mapped IPv4 prefix and source become native IPv4",
			source:      "192.0.2.2",
			destination: "198.51.100.200",
			want:        2,
		},
		{
			name:        "native source selects another equal prefix",
			source:      "192.0.2.3",
			destination: "198.51.100.200",
			want:        3,
		},
		{
			name:        "unknown source uses lower slot for normalized equal prefix",
			source:      "192.0.2.99",
			destination: "198.51.100.200",
			want:        2,
		},
		{
			name:        "non-network route address is masked",
			source:      "192.0.2.99",
			destination: "10.200.0.1",
			want:        4,
		},
		{
			name:        "wrong-family source is ignored but destination remains",
			source:      "192.0.2.99",
			destination: "203.0.113.200",
			want:        5,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			packet := autoTunTestPacket(
				netip.MustParseAddr(test.source),
				netip.MustParseAddr(test.destination),
			)
			if got := routeAutoTunPacket(router, packet, 0); got != test.want {
				t.Fatalf("Route(%s -> %s) = %d, want %d", test.source, test.destination, got, test.want)
			}
		})
	}
}

func TestAutoTunSplitterRouterRejectsMalformedPacketsAndHonorsOffset(t *testing.T) {
	router := newAutoTunSplitterRouter()
	router.SetConfig(2, TunConfig{Routes: []string{"10.0.0.0/8"}})
	valid := autoTunTestPacket(
		netip.MustParseAddr("192.0.2.1"),
		netip.MustParseAddr("10.0.0.1"),
	)
	withOffset := append([]byte{1, 2, 3, 4}, valid...)
	if got := routeAutoTunPacket(router, withOffset, 4); got != 2 {
		t.Fatalf("Route with offset = %d, want 2", got)
	}

	badIPv4Length := append([]byte(nil), valid...)
	binary.BigEndian.PutUint16(badIPv4Length[2:4], uint16(len(valid)+1))
	badIPv4Header := append([]byte(nil), valid...)
	badIPv4Header[0] = 0x44
	badIPv4TotalLength := append([]byte(nil), valid...)
	binary.BigEndian.PutUint16(badIPv4TotalLength[2:4], 19)
	validIPv6 := autoTunTestPacket(
		netip.MustParseAddr("2001:db8::1"),
		netip.MustParseAddr("2001:db8::2"),
	)
	badIPv6Length := append([]byte(nil), validIPv6...)
	binary.BigEndian.PutUint16(badIPv6Length[4:6], 1)

	tests := []struct {
		name   string
		packet []byte
		offset int
	}{
		{name: "nil", packet: nil, offset: 0},
		{name: "negative offset", packet: valid, offset: -1},
		{name: "offset at end", packet: valid, offset: len(valid)},
		{name: "unknown version", packet: []byte{0x10}, offset: 0},
		{name: "short IPv4", packet: valid[:19], offset: 0},
		{name: "small IPv4 header", packet: badIPv4Header, offset: 0},
		{name: "IPv4 total length smaller than header", packet: badIPv4TotalLength, offset: 0},
		{name: "truncated IPv4 total length", packet: badIPv4Length, offset: 0},
		{name: "short IPv6", packet: validIPv6[:39], offset: 0},
		{name: "truncated IPv6 payload", packet: badIPv6Length, offset: 0},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := routeAutoTunPacket(router, test.packet, test.offset); got != 0 {
				t.Fatalf("Route malformed packet = %d, want drop slot 0", got)
			}
		})
	}
}

func TestAutoTunSplitterRouterPinsEachBatchDuringConcurrentUpdates(t *testing.T) {
	router := newAutoTunSplitterRouter()
	router.SetConfig(2, TunConfig{Routes: []string{"10.0.0.0/8"}})
	packet := autoTunTestPacket(
		netip.MustParseAddr("192.0.2.1"),
		netip.MustParseAddr("10.1.2.3"),
	)

	router.Lock()
	started := make(chan struct{})
	done := make(chan struct{})
	go func() {
		close(started)
		router.SetConfig(3, TunConfig{Routes: []string{"10.1.0.0/16"}})
		close(done)
	}()
	<-started
	select {
	case <-done:
		t.Fatal("config update completed while a packet batch held the read lock")
	default:
	}
	if got := router.Route(packet, 0, false); got != 2 {
		t.Fatalf("Route in pinned batch = %d, want old slot 2 table", got)
	}
	router.Unlock()
	<-done
	if got := routeAutoTunPacket(router, packet, 0); got != 3 {
		t.Fatalf("Route after concurrent update = %d, want slot 3", got)
	}
}

func TestAutoTunSplitterRouterConcurrentRouteAndConfigReplacement(t *testing.T) {
	router := newAutoTunSplitterRouter()
	packet := autoTunTestPacket(
		netip.MustParseAddr("192.0.2.1"),
		netip.MustParseAddr("10.1.2.3"),
	)

	const iterations = 2000
	var writers sync.WaitGroup
	for writer := 0; writer < 3; writer++ {
		writers.Add(1)
		go func(writer int) {
			defer writers.Done()
			for i := 0; i < iterations; i++ {
				slot := 2 + writer
				if i%3 == 0 {
					router.ClearConfig(slot)
					continue
				}
				router.SetConfig(slot, TunConfig{
					Addrs:  []string{"192.0.2.1/32"},
					Routes: []string{"10.0.0.0/8"},
				})
			}
		}(writer)
	}

	var readers sync.WaitGroup
	for range 4 {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for range iterations {
				slot := routeAutoTunPacket(router, packet, 0)
				if slot < 1 || slot > 4 {
					t.Errorf("Route during concurrent updates = %d, want slot 1..4", slot)
					return
				}
			}
		}()
	}
	writers.Wait()
	readers.Wait()
}

func routeAutoTunPacket(router *autoTunSplitterRouter, packet []byte, offset int) int {
	router.Lock()
	defer router.Unlock()
	return router.Route(packet, offset, false)
}

func autoTunTestPacket(source, destination netip.Addr) []byte {
	if source.Is4() && destination.Is4() {
		packet := make([]byte, 20)
		packet[0] = 0x45
		binary.BigEndian.PutUint16(packet[2:4], uint16(len(packet)))
		sourceBytes := source.As4()
		destinationBytes := destination.As4()
		copy(packet[12:16], sourceBytes[:])
		copy(packet[16:20], destinationBytes[:])
		return packet
	}
	if source.Is6() && destination.Is6() {
		packet := make([]byte, 40)
		packet[0] = 0x60
		sourceBytes := source.As16()
		destinationBytes := destination.As16()
		copy(packet[8:24], sourceBytes[:])
		copy(packet[24:40], destinationBytes[:])
		return packet
	}
	panic("autoTunTestPacket requires source and destination from the same IP family")
}
