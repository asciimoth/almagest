package tunnodes

import (
	"fmt"
	"net/netip"
	"testing"
)

var autoTunSplitterBenchmarkSlot int

func BenchmarkAutoTunSplitterRouter(b *testing.B) {
	benchmarks := []struct {
		name   string
		router *autoTunSplitterRouter
		packet []byte
	}{
		{
			name:   "IPv4SmallMatch",
			router: autoTunSplitterSmallBenchmarkRouter(),
			packet: autoTunTestPacket(netip.MustParseAddr("192.0.2.1"), netip.MustParseAddr("10.1.2.3")),
		},
		{
			name:   "IPv4LargeLateMatch",
			router: autoTunSplitterLargeBenchmarkRouter(false),
			packet: autoTunTestPacket(netip.MustParseAddr("192.0.2.1"), netip.MustParseAddr("10.15.15.1")),
		},
		{
			name:   "IPv4LargeMiss",
			router: autoTunSplitterLargeBenchmarkRouter(false),
			packet: autoTunTestPacket(netip.MustParseAddr("192.0.2.1"), netip.MustParseAddr("192.0.2.2")),
		},
		{
			name:   "IPv6LargeLateMatch",
			router: autoTunSplitterLargeBenchmarkRouter(true),
			packet: autoTunTestPacket(netip.MustParseAddr("2001:db8::1"), netip.MustParseAddr("fd00:f:f::1")),
		},
		{
			name:   "IPv4EqualPrefixSourceMatch",
			router: autoTunSplitterSourceBenchmarkRouter(),
			packet: autoTunTestPacket(netip.MustParseAddr("192.0.2.16"), netip.MustParseAddr("10.1.2.3")),
		},
	}

	for _, benchmark := range benchmarks {
		b.Run(benchmark.name, func(b *testing.B) {
			benchmark.router.Lock()
			defer benchmark.router.Unlock()
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				autoTunSplitterBenchmarkSlot = benchmark.router.Route(benchmark.packet, 0, false)
			}
		})
	}
}

func autoTunSplitterSmallBenchmarkRouter() *autoTunSplitterRouter {
	router := newAutoTunSplitterRouter()
	router.SetConfig(2, TunConfig{Routes: []string{"10.0.0.0/8"}})
	router.SetConfig(3, TunConfig{Routes: []string{"10.1.0.0/16"}})
	return router
}

func autoTunSplitterLargeBenchmarkRouter(ipv6 bool) *autoTunSplitterRouter {
	router := newAutoTunSplitterRouter()
	for slot := 1; slot <= tunSplitterMaxSlots; slot++ {
		routes := make([]string, tunSplitterMaxSlots)
		for route := range tunSplitterMaxSlots {
			if ipv6 {
				routes[route] = fmt.Sprintf("fd00:%x:%x::/48", slot-1, route)
			} else {
				routes[route] = fmt.Sprintf("10.%d.%d.0/24", slot-1, route)
			}
		}
		router.SetConfig(slot, TunConfig{Routes: routes})
	}
	return router
}

func autoTunSplitterSourceBenchmarkRouter() *autoTunSplitterRouter {
	router := newAutoTunSplitterRouter()
	for slot := 1; slot <= tunSplitterMaxSlots; slot++ {
		router.SetConfig(slot, TunConfig{
			Addrs:  []string{fmt.Sprintf("192.0.2.%d/32", slot)},
			Routes: []string{"10.0.0.0/8"},
		})
	}
	return router
}
