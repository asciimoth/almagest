package tunnodes

import (
	"encoding/binary"
	"net/netip"
	"slices"
	"sort"
	"sync"
	"sync/atomic"
)

// autoTunSplitterRouter is the Gonnect split callback used by
// AutoTunSplitter. It converts downstream TunConfig snapshots into one
// immutable packet-routing table.
//
// Gonnect calls Lock before each read batch and Unlock after it. A config
// update takes the write lock, so all packets in one batch use the same table.
// Route also reads the table through an atomic pointer. This keeps direct calls
// safe and avoids mutable maps or slices in the packet path.
type autoTunSplitterRouter struct {
	mu sync.RWMutex

	configs    [tunSplitterMaxSlots + 1]TunConfig
	configured [tunSplitterMaxSlots + 1]bool
	table      atomic.Pointer[autoTunSplitterRouteTable]
}

// autoTunSplitterRouteTable is immutable after publication. The routes are
// ordered by decreasing prefix length. Equal route claims use their associated
// source addresses as tie-breakers, then select the lower slot number. This
// makes conflicting downstream suggestions deterministic without letting an
// interface address claim unrelated destinations.
type autoTunSplitterRouteTable struct {
	routes []autoTunSplitterRoute
}

type autoTunSplitterRoute struct {
	prefix  netip.Prefix
	slot    int
	sources []netip.Addr
}

func newAutoTunSplitterRouter() *autoTunSplitterRouter {
	router := &autoTunSplitterRouter{}
	router.table.Store(&autoTunSplitterRouteTable{})
	return router
}

// Lock keeps the current routing table unchanged for one Gonnect read batch.
func (r *autoTunSplitterRouter) Lock() {
	r.mu.RLock()
}

// Unlock permits routing-table updates after one Gonnect read batch.
func (r *autoTunSplitterRouter) Unlock() {
	r.mu.RUnlock()
}

// Route returns the learned slot for one complete IPv4 or IPv6 packet.
//
// The longest destination-prefix match wins. A source address can select
// between slots that claim the same longest prefix, but it cannot select a
// slot that does not claim the packet destination. This distinction matters
// when an upstream userspace stack uses one branch address as its fallback
// source for otherwise unmatched destinations. Packets without a destination
// match use slot 1. Malformed or incomplete IP packets are dropped by
// returning slot 0.
func (r *autoTunSplitterRouter) Route(buf []byte, offset int, _ bool) int {
	source, destination, ok := autoTunPacketAddresses(buf, offset)
	if !ok {
		return 0
	}
	table := r.table.Load()
	if table == nil {
		return 1
	}
	matchedBits := -1
	matchedSlot := 0
	for _, route := range table.routes {
		bits := route.prefix.Bits()
		if matchedBits >= 0 && bits < matchedBits {
			break
		}
		if !route.prefix.Contains(destination) {
			continue
		}
		if matchedBits < 0 {
			matchedBits = bits
		}
		if slices.Contains(route.sources, source) {
			return route.slot
		}
		if matchedSlot == 0 || route.slot < matchedSlot {
			matchedSlot = route.slot
		}
	}
	if matchedSlot != 0 {
		return matchedSlot
	}
	return 1
}

// SetConfig replaces the complete learned configuration for slot. The copy
// prevents later caller mutations from changing a published route table.
func (r *autoTunSplitterRouter) SetConfig(slot int, cfg TunConfig) {
	if slot < 1 || slot > tunSplitterMaxSlots {
		return
	}
	r.mu.Lock()
	r.configs[slot] = cfg.Clone()
	r.configured[slot] = true
	r.rebuildLocked()
	r.mu.Unlock()
}

// ClearConfig removes all addresses and routes learned for slot.
func (r *autoTunSplitterRouter) ClearConfig(slot int) {
	if slot < 1 || slot > tunSplitterMaxSlots {
		return
	}
	r.mu.Lock()
	r.configs[slot] = TunConfig{}
	r.configured[slot] = false
	r.rebuildLocked()
	r.mu.Unlock()
}

// Reset removes every learned route. The node uses it during shutdown to
// remove references to configuration data from the callback.
func (r *autoTunSplitterRouter) Reset() {
	r.mu.Lock()
	r.configs = [tunSplitterMaxSlots + 1]TunConfig{}
	r.configured = [tunSplitterMaxSlots + 1]bool{}
	r.table.Store(&autoTunSplitterRouteTable{})
	r.mu.Unlock()
}

func (r *autoTunSplitterRouter) rebuildLocked() {
	table := &autoTunSplitterRouteTable{}
	routeIndexes := make(map[autoTunSplitterRouteKey]int)
	for slot := 1; slot <= tunSplitterMaxSlots; slot++ {
		if !r.configured[slot] {
			continue
		}
		// Use the same inferred destination-to-source associations that this
		// node propagates upstream. The packet router and the upstream source
		// selector must therefore make compatible decisions.
		cfg := r.configs[slot].WithInferredSourceRoutes()
		for _, value := range cfg.Routes {
			if prefix, ok := autoTunConfigPrefix(value); ok {
				addAutoTunSplitterRoute(table, routeIndexes, prefix, slot, netip.Addr{})
			}
		}
		for _, sourceRoute := range cfg.SourceRoutes {
			if prefix, ok := normalizeAutoTunPrefix(sourceRoute.Destination); ok {
				source := normalizeAutoTunSource(sourceRoute.Source, prefix)
				addAutoTunSplitterRoute(table, routeIndexes, prefix, slot, source)
			}
		}
	}
	sort.SliceStable(table.routes, func(i, j int) bool {
		left, right := table.routes[i], table.routes[j]
		if left.prefix.Bits() != right.prefix.Bits() {
			return left.prefix.Bits() > right.prefix.Bits()
		}
		return left.slot < right.slot
	})
	r.table.Store(table)
}

type autoTunSplitterRouteKey struct {
	prefix netip.Prefix
	slot   int
}

func addAutoTunSplitterRoute(
	table *autoTunSplitterRouteTable,
	indexes map[autoTunSplitterRouteKey]int,
	prefix netip.Prefix,
	slot int,
	source netip.Addr,
) {
	key := autoTunSplitterRouteKey{prefix: prefix, slot: slot}
	index, exists := indexes[key]
	if !exists {
		index = len(table.routes)
		indexes[key] = index
		table.routes = append(table.routes, autoTunSplitterRoute{
			prefix: prefix,
			slot:   slot,
		})
	}
	if source.IsValid() && !slices.Contains(table.routes[index].sources, source) {
		table.routes[index].sources = append(table.routes[index].sources, source)
	}
}

func normalizeAutoTunSource(source netip.Addr, destination netip.Prefix) netip.Addr {
	if !source.IsValid() {
		return netip.Addr{}
	}
	source = source.Unmap()
	if source.Is4() != destination.Addr().Is4() {
		return netip.Addr{}
	}
	return source
}

func autoTunConfigPrefix(value string) (netip.Prefix, bool) {
	prefix, err := netip.ParsePrefix(value)
	if err != nil {
		addr, addrErr := netip.ParseAddr(value)
		if addrErr != nil {
			return netip.Prefix{}, false
		}
		prefix = netip.PrefixFrom(addr, addr.BitLen())
	}
	return normalizeAutoTunPrefix(prefix)
}

func normalizeAutoTunPrefix(prefix netip.Prefix) (netip.Prefix, bool) {
	if !prefix.IsValid() {
		return netip.Prefix{}, false
	}
	addr := prefix.Addr()
	bits := prefix.Bits()
	if addr.Is4In6() && bits >= 96 {
		addr = addr.Unmap()
		bits -= 96
	}
	return netip.PrefixFrom(addr, bits).Masked(), true
}

// autoTunPacketAddresses validates enough of the outer IP header to reject
// truncated packets and returns normalized source and destination addresses.
// Transport headers are intentionally not parsed because automatic routing
// depends only on TunConfig address and prefix data.
func autoTunPacketAddresses(buf []byte, offset int) (netip.Addr, netip.Addr, bool) {
	if offset < 0 || offset >= len(buf) {
		return netip.Addr{}, netip.Addr{}, false
	}
	packet := buf[offset:]
	switch packet[0] >> 4 {
	case 4:
		if len(packet) < 20 {
			return netip.Addr{}, netip.Addr{}, false
		}
		headerLength := int(packet[0]&0x0f) * 4
		totalLength := int(binary.BigEndian.Uint16(packet[2:4]))
		if headerLength < 20 || totalLength < headerLength || totalLength > len(packet) {
			return netip.Addr{}, netip.Addr{}, false
		}
		var source, destination [4]byte
		copy(source[:], packet[12:16])
		copy(destination[:], packet[16:20])
		return netip.AddrFrom4(source), netip.AddrFrom4(destination), true
	case 6:
		if len(packet) < 40 {
			return netip.Addr{}, netip.Addr{}, false
		}
		totalLength := 40 + int(binary.BigEndian.Uint16(packet[4:6]))
		if totalLength > len(packet) {
			return netip.Addr{}, netip.Addr{}, false
		}
		var source, destination [16]byte
		copy(source[:], packet[8:24])
		copy(destination[:], packet[24:40])
		return netip.AddrFrom16(source), netip.AddrFrom16(destination), true
	default:
		return netip.Addr{}, netip.Addr{}, false
	}
}
