package tunnodes

import (
	"encoding/binary"
	"net/netip"
	"sort"
	"sync"
	"sync/atomic"
)

// autoTunSplitterRouter is the Gonnect split callback used by
// AutoTunSplitter. It converts the latest complete TunConfig snapshot from
// each slot into one immutable packet-routing table. Config fields that do not
// affect packet selection, such as MTU and DNS, stay out of the table.
//
// Gonnect calls Lock before each read batch and Unlock after it. A config
// update takes the write lock, so all packets in one batch use the same table.
// Route also reads the table through an atomic pointer. This keeps direct calls
// safe and avoids mutable maps or slices in the packet path. In particular,
// the atomic pointer is a publication mechanism and the lock is a batch
// consistency mechanism; a future implementation must preserve both effects.
type autoTunSplitterRouter struct {
	mu sync.RWMutex

	configs    [tunSplitterMaxSlots + 1]TunConfig
	configured [tunSplitterMaxSlots + 1]bool
	table      atomic.Pointer[autoTunSplitterRouteTable]
}

// autoTunSplitterRouteTable is immutable after publication. It groups prefixes
// by address family and prefix length. Route checks prefix lengths from most to
// least specific and does one exact lookup at each length. This avoids scanning
// every configured prefix for each packet.
type autoTunSplitterRouteTable struct {
	ipv4 []autoTunSplitterIPv4Level
	ipv6 []autoTunSplitterIPv6Level
}

type autoTunSplitterIPv4Level struct {
	bits   uint8
	mask   uint32
	prefix uint32
	route  autoTunSplitterIPv4Route
	routes map[uint32]autoTunSplitterIPv4Route
}

type autoTunSplitterIPv6Level struct {
	bits   uint8
	mask   autoTunSplitterIPv6Address
	prefix autoTunSplitterIPv6Address
	route  autoTunSplitterIPv6Route
	routes map[autoTunSplitterIPv6Address]autoTunSplitterIPv6Route
}

type autoTunSplitterIPv4Route struct {
	defaultSlot uint8
	sources     []autoTunSplitterIPv4Source
	sourceSlots map[uint32]uint8
}

type autoTunSplitterIPv6Route struct {
	defaultSlot uint8
	sources     []autoTunSplitterIPv6Source
	sourceSlots map[autoTunSplitterIPv6Address]uint8
}

type autoTunSplitterIPv4Source struct {
	address uint32
	slot    uint8
}

type autoTunSplitterIPv6Source struct {
	address autoTunSplitterIPv6Address
	slot    uint8
}

type autoTunSplitterIPv6Address struct {
	hi uint64
	lo uint64
}

// autoTunSplitterRouteBuilder combines every slot claim for one normalized
// prefix before the immutable, family-specific lookup tables are built.
type autoTunSplitterRouteBuilder struct {
	defaultSlot uint8
	slots       uint16
	sourceSlots map[netip.Addr]uint8
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
	if offset < 0 || offset >= len(buf) {
		return 0
	}
	packet := buf[offset:]
	switch packet[0] >> 4 {
	case 4:
		if !validAutoTunIPv4Packet(packet) {
			return 0
		}
		table := r.table.Load()
		if table == nil {
			return 1
		}
		return table.routeIPv4(
			binary.BigEndian.Uint32(packet[12:16]),
			binary.BigEndian.Uint32(packet[16:20]),
		)
	case 6:
		if !validAutoTunIPv6Packet(packet) {
			return 0
		}
		table := r.table.Load()
		if table == nil {
			return 1
		}
		return table.routeIPv6(
			autoTunSplitterIPv6Address{
				hi: binary.BigEndian.Uint64(packet[8:16]),
				lo: binary.BigEndian.Uint64(packet[16:24]),
			},
			autoTunSplitterIPv6Address{
				hi: binary.BigEndian.Uint64(packet[24:32]),
				lo: binary.BigEndian.Uint64(packet[32:40]),
			},
		)
	default:
		return 0
	}
}

// SetConfig replaces the complete learned configuration for slot. An empty
// config is still a configured snapshot and removes all former claims for the
// slot. The copy prevents later caller mutations from changing either stored
// input or a published route table.
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

// ClearConfig removes all addresses and routes learned for slot. The node calls
// it on link detach and before a replacement link can publish a new snapshot.
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
	builders := make(map[netip.Prefix]*autoTunSplitterRouteBuilder)
	for slot := 1; slot <= tunSplitterMaxSlots; slot++ {
		if !r.configured[slot] {
			continue
		}
		// Use the same inferred destination-to-source associations that this
		// node propagates upstream. The packet router and the upstream source
		// selector must therefore make compatible decisions. Addrs do not add
		// claims on their own; they only supply sources for Routes.
		cfg := r.configs[slot].WithInferredSourceRoutes()
		for _, value := range cfg.Routes {
			if prefix, ok := autoTunConfigPrefix(value); ok {
				addAutoTunSplitterRoute(builders, prefix, slot, netip.Addr{})
			}
		}
		for _, sourceRoute := range cfg.SourceRoutes {
			if prefix, ok := normalizeAutoTunPrefix(sourceRoute.Destination); ok {
				source := normalizeAutoTunSource(sourceRoute.Source, prefix)
				addAutoTunSplitterRoute(builders, prefix, slot, source)
			}
		}
	}
	r.table.Store(buildAutoTunSplitterRouteTable(builders))
}

func addAutoTunSplitterRoute(
	builders map[netip.Prefix]*autoTunSplitterRouteBuilder,
	prefix netip.Prefix,
	slot int,
	source netip.Addr,
) {
	builder := builders[prefix]
	if builder == nil {
		builder = &autoTunSplitterRouteBuilder{}
		builders[prefix] = builder
	}
	slotValue := uint8(slot)
	if builder.defaultSlot == 0 || slotValue < builder.defaultSlot {
		builder.defaultSlot = slotValue
	}
	builder.slots |= uint16(1) << (slotValue - 1)
	if !source.IsValid() {
		return
	}
	if builder.sourceSlots == nil {
		builder.sourceSlots = make(map[netip.Addr]uint8)
	}
	if current := builder.sourceSlots[source]; current == 0 || slotValue < current {
		builder.sourceSlots[source] = slotValue
	}
}

func buildAutoTunSplitterRouteTable(
	builders map[netip.Prefix]*autoTunSplitterRouteBuilder,
) *autoTunSplitterRouteTable {
	ipv4 := make(map[int]map[uint32]autoTunSplitterIPv4Route)
	ipv6 := make(map[int]map[autoTunSplitterIPv6Address]autoTunSplitterIPv6Route)
	for prefix, builder := range builders {
		bits := prefix.Bits()
		if prefix.Addr().Is4() {
			routes := ipv4[bits]
			if routes == nil {
				routes = make(map[uint32]autoTunSplitterIPv4Route)
				ipv4[bits] = routes
			}
			addr := prefix.Addr().As4()
			routes[binary.BigEndian.Uint32(addr[:])] = builder.ipv4Route()
			continue
		}
		routes := ipv6[bits]
		if routes == nil {
			routes = make(map[autoTunSplitterIPv6Address]autoTunSplitterIPv6Route)
			ipv6[bits] = routes
		}
		routes[autoTunSplitterIPv6AddressFromAddr(prefix.Addr())] = builder.ipv6Route()
	}

	table := &autoTunSplitterRouteTable{
		ipv4: make([]autoTunSplitterIPv4Level, 0, len(ipv4)),
		ipv6: make([]autoTunSplitterIPv6Level, 0, len(ipv6)),
	}
	for bits, routes := range ipv4 {
		level := autoTunSplitterIPv4Level{
			bits: uint8(bits),
			mask: autoTunSplitterIPv4Mask(bits),
		}
		if len(routes) == 1 {
			for prefix, route := range routes {
				level.prefix = prefix
				level.route = route
			}
		} else {
			level.routes = routes
		}
		table.ipv4 = append(table.ipv4, level)
	}
	for bits, routes := range ipv6 {
		level := autoTunSplitterIPv6Level{
			bits: uint8(bits),
			mask: autoTunSplitterIPv6Mask(bits),
		}
		if len(routes) == 1 {
			for prefix, route := range routes {
				level.prefix = prefix
				level.route = route
			}
		} else {
			level.routes = routes
		}
		table.ipv6 = append(table.ipv6, level)
	}
	sort.Slice(table.ipv4, func(i, j int) bool {
		return table.ipv4[i].bits > table.ipv4[j].bits
	})
	sort.Slice(table.ipv6, func(i, j int) bool {
		return table.ipv6[i].bits > table.ipv6[j].bits
	})
	return table
}

const autoTunSplitterLinearSourceLimit = 4

func (b *autoTunSplitterRouteBuilder) ipv4Route() autoTunSplitterIPv4Route {
	route := autoTunSplitterIPv4Route{defaultSlot: b.defaultSlot}
	if !b.hasSlotTie() {
		return route
	}
	if len(b.sourceSlots) > autoTunSplitterLinearSourceLimit {
		route.sourceSlots = make(map[uint32]uint8, len(b.sourceSlots))
		for source, slot := range b.sourceSlots {
			addr := source.As4()
			route.sourceSlots[binary.BigEndian.Uint32(addr[:])] = slot
		}
		return route
	}
	for source, slot := range b.sourceSlots {
		addr := source.As4()
		route.sources = append(route.sources, autoTunSplitterIPv4Source{
			address: binary.BigEndian.Uint32(addr[:]),
			slot:    slot,
		})
	}
	sort.Slice(route.sources, func(i, j int) bool {
		return route.sources[i].address < route.sources[j].address
	})
	return route
}

func (b *autoTunSplitterRouteBuilder) ipv6Route() autoTunSplitterIPv6Route {
	route := autoTunSplitterIPv6Route{defaultSlot: b.defaultSlot}
	if !b.hasSlotTie() {
		return route
	}
	if len(b.sourceSlots) > autoTunSplitterLinearSourceLimit {
		route.sourceSlots = make(map[autoTunSplitterIPv6Address]uint8, len(b.sourceSlots))
		for source, slot := range b.sourceSlots {
			route.sourceSlots[autoTunSplitterIPv6AddressFromAddr(source)] = slot
		}
		return route
	}
	for source, slot := range b.sourceSlots {
		route.sources = append(route.sources, autoTunSplitterIPv6Source{
			address: autoTunSplitterIPv6AddressFromAddr(source),
			slot:    slot,
		})
	}
	sort.Slice(route.sources, func(i, j int) bool {
		left, right := route.sources[i].address, route.sources[j].address
		return left.hi < right.hi || left.hi == right.hi && left.lo < right.lo
	})
	return route
}

func (b *autoTunSplitterRouteBuilder) hasSlotTie() bool {
	return b.slots != 0 && b.slots&(b.slots-1) != 0
}

func (t *autoTunSplitterRouteTable) routeIPv4(source, destination uint32) int {
	for i := range t.ipv4 {
		level := &t.ipv4[i]
		prefix := destination & level.mask
		if level.routes == nil {
			if prefix == level.prefix {
				return level.route.selectSlot(source)
			}
			continue
		}
		if route, ok := level.routes[prefix]; ok {
			return route.selectSlot(source)
		}
	}
	return 1
}

func (t *autoTunSplitterRouteTable) routeIPv6(
	source autoTunSplitterIPv6Address,
	destination autoTunSplitterIPv6Address,
) int {
	for i := range t.ipv6 {
		level := &t.ipv6[i]
		prefix := destination.mask(level.mask)
		if level.routes == nil {
			if prefix == level.prefix {
				return level.route.selectSlot(source)
			}
			continue
		}
		if route, ok := level.routes[prefix]; ok {
			return route.selectSlot(source)
		}
	}
	return 1
}

func (r autoTunSplitterIPv4Route) selectSlot(source uint32) int {
	if r.sourceSlots != nil {
		if slot := r.sourceSlots[source]; slot != 0 {
			return int(slot)
		}
	} else {
		for _, candidate := range r.sources {
			if candidate.address == source {
				return int(candidate.slot)
			}
		}
	}
	return int(r.defaultSlot)
}

func (r autoTunSplitterIPv6Route) selectSlot(source autoTunSplitterIPv6Address) int {
	if r.sourceSlots != nil {
		if slot := r.sourceSlots[source]; slot != 0 {
			return int(slot)
		}
	} else {
		for _, candidate := range r.sources {
			if candidate.address == source {
				return int(candidate.slot)
			}
		}
	}
	return int(r.defaultSlot)
}

func autoTunSplitterIPv4Mask(bits int) uint32 {
	if bits == 0 {
		return 0
	}
	return ^uint32(0) << (32 - bits)
}

func autoTunSplitterIPv6Mask(bits int) autoTunSplitterIPv6Address {
	switch {
	case bits == 0:
		return autoTunSplitterIPv6Address{}
	case bits <= 64:
		return autoTunSplitterIPv6Address{hi: ^uint64(0) << (64 - bits)}
	default:
		return autoTunSplitterIPv6Address{
			hi: ^uint64(0),
			lo: ^uint64(0) << (128 - bits),
		}
	}
}

func autoTunSplitterIPv6AddressFromAddr(addr netip.Addr) autoTunSplitterIPv6Address {
	value := addr.As16()
	return autoTunSplitterIPv6Address{
		hi: binary.BigEndian.Uint64(value[:8]),
		lo: binary.BigEndian.Uint64(value[8:]),
	}
}

func (a autoTunSplitterIPv6Address) mask(mask autoTunSplitterIPv6Address) autoTunSplitterIPv6Address {
	return autoTunSplitterIPv6Address{hi: a.hi & mask.hi, lo: a.lo & mask.lo}
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

// Packet validation deliberately stops at the outer IP header. Automatic
// routing depends only on its source and destination addresses.
func validAutoTunIPv4Packet(packet []byte) bool {
	if len(packet) < 20 {
		return false
	}
	headerLength := int(packet[0]&0x0f) * 4
	totalLength := int(binary.BigEndian.Uint16(packet[2:4]))
	return headerLength >= 20 && totalLength >= headerLength && totalLength <= len(packet)
}

func validAutoTunIPv6Packet(packet []byte) bool {
	if len(packet) < 40 {
		return false
	}
	totalLength := 40 + int(binary.BigEndian.Uint16(packet[4:6]))
	return totalLength <= len(packet)
}
