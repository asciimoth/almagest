package tunnodes

import (
	"encoding/binary"
	"net/netip"
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

// autoTunSplitterRouteTable is immutable after publication. Address ties and
// equal-length route ties select the lower slot number. This makes conflicting
// downstream suggestions deterministic.
type autoTunSplitterRouteTable struct {
	sourceSlots map[netip.Addr]int
	routes      []autoTunSplitterRoute
}

type autoTunSplitterRoute struct {
	prefix netip.Prefix
	slot   int
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
// An exact packet source address has priority because an upstream userspace
// stack selects that address from the source routes propagated by this node.
// Otherwise, the longest destination-prefix match wins. Packets that do not
// match learned data use slot 1. Malformed or incomplete IP packets are
// dropped by returning slot 0.
func (r *autoTunSplitterRouter) Route(buf []byte, offset int, _ bool) int {
	source, destination, ok := autoTunPacketAddresses(buf, offset)
	if !ok {
		return 0
	}
	table := r.table.Load()
	if table == nil {
		return 1
	}
	if slot := table.sourceSlots[source]; slot != 0 {
		return slot
	}
	for _, route := range table.routes {
		if route.prefix.Contains(destination) {
			return route.slot
		}
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
	table := &autoTunSplitterRouteTable{
		sourceSlots: make(map[netip.Addr]int),
	}
	for slot := 1; slot <= tunSplitterMaxSlots; slot++ {
		if !r.configured[slot] {
			continue
		}
		cfg := r.configs[slot]
		for _, value := range cfg.Addrs {
			addr, ok := autoTunConfigAddr(value)
			if !ok {
				continue
			}
			if _, exists := table.sourceSlots[addr]; !exists {
				table.sourceSlots[addr] = slot
			}
		}
		for _, value := range cfg.Routes {
			if prefix, ok := autoTunConfigPrefix(value); ok {
				table.routes = append(table.routes, autoTunSplitterRoute{
					prefix: prefix,
					slot:   slot,
				})
			}
		}
		for _, sourceRoute := range cfg.SourceRoutes {
			if prefix, ok := normalizeAutoTunPrefix(sourceRoute.Destination); ok {
				table.routes = append(table.routes, autoTunSplitterRoute{
					prefix: prefix,
					slot:   slot,
				})
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

func autoTunConfigAddr(value string) (netip.Addr, bool) {
	if prefix, err := netip.ParsePrefix(value); err == nil {
		return prefix.Addr().Unmap(), true
	}
	addr, err := netip.ParseAddr(value)
	if err != nil {
		return netip.Addr{}, false
	}
	return addr.Unmap(), true
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
