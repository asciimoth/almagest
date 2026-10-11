package runtime

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"sync"
	"time"

	"github.com/asciimoth/almagest/bin/internal/isolation/protocol"
	"github.com/asciimoth/gonnect/subnet"
)

const allocationTimeout = 5 * time.Second

type allocationKey struct {
	kind   protocol.AddressKind
	prefix netip.Prefix
}

// Allocator implements the native allocator interfaces for one graph epoch.
// The broker owns each reserve count. An old allocator never acquires authority
// for a replacement graph, even if that graph later uses the same address.
type Allocator struct {
	client *Client
	epoch  uint64
	mu     sync.Mutex
	owned  map[allocationKey][]protocol.ResourceRef
	err    error
	failed bool
}

// Allocator returns the current graph's allocator. Callers must retain it with
// the addresses they allocate, including for their eventual Free calls.
func (c *Client) Allocator() *Allocator {
	c.mu.Lock()
	defer c.mu.Unlock()
	epoch := c.epoch.Load()
	if c.allocator == nil || c.allocator.epoch != epoch {
		c.allocator = &Allocator{client: c, epoch: epoch, owned: make(map[allocationKey][]protocol.ResourceRef)}
	}
	return c.allocator
}

// Err reports the last allocation, reservation, or release failure. Native
// allocator interfaces have no error return. After a failed required Reserve
// or release, this adapter refuses new allocations instead of silently
// treating that reservation as held.
// Pool exhaustion is a normal nil allocation, not a permanent error.
func (a *Allocator) Err() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.epoch != a.client.epoch.Load() {
		return protocol.ErrStaleGraphEpoch
	}
	return a.err
}

func (a *Allocator) reserve(req protocol.ReserveAddressRequest) protocol.AllocationState {
	if a.epoch != a.client.epoch.Load() || a.failed {
		return protocol.AllocationState{}
	}
	ctx, cancel := context.WithTimeout(context.Background(), allocationTimeout)
	defer cancel()
	req.Meta = a.client.meta(true)
	req.Meta.GraphEpoch = a.epoch
	op := protocol.OperationReserveIP
	if req.Kind == protocol.AddressSubnet {
		op = protocol.OperationReserveSubnet
	}
	result, err := a.client.openResource(ctx, op, req)
	if err == nil && result.Outcome != protocol.OutcomeCommitted {
		err = decodeError(result.Code)
		if err == nil {
			err = protocol.ErrRecoveryRequired
		}
	}
	if errors.Is(err, protocol.ErrAllocationExhausted) && !req.Reserve {
		a.err = nil
		return protocol.AllocationState{}
	}
	if err != nil {
		a.err = err
		// A rejected optional allocation has no effects and must not disable
		// another address family. A failed required Reserve cannot be ignored.
		a.failed = req.Reserve || result.Outcome != protocol.OutcomeRejected
		return protocol.AllocationState{}
	}
	state := result.State.Allocation
	err = result.Resource.Validate(a.client.session, protocol.ResourceAllocation, a.epoch, 1)
	if err == nil {
		err = state.Validate(req)
	}
	if err == nil && (result.CurrentResource != result.Resource || result.OperationID != req.Meta.OperationID || !result.State.Up || result.State.Closed || result.State.RecoveryRequired) {
		err = protocol.ErrInvalidContract
	}
	if err != nil {
		a.err, a.failed = err, true
		// A malformed successful receipt cannot identify a safe individual release.
		// Peer loss revokes the complete session rather than leaking that authority.
		_ = a.client.Close()
		return protocol.AllocationState{}
	}
	if a.epoch != a.client.epoch.Load() {
		return protocol.AllocationState{}
	}
	a.err = nil
	key := allocationStateKey(state)
	a.owned[key] = append(a.owned[key], result.Resource)
	return state
}

func allocationStateKey(state protocol.AllocationState) allocationKey {
	prefix := state.Subnet
	if state.Kind == protocol.AddressIP {
		prefix = netip.PrefixFrom(state.Address, state.Address.BitLen())
	}
	return allocationKey{kind: state.Kind, prefix: prefix}
}
func family(ip netip.Addr) string {
	if ip.Is4() {
		return "ipv4"
	}
	return "ipv6"
}
func allocatorPrefix(n *net.IPNet) netip.Prefix {
	if n == nil {
		return netip.Prefix{}
	}
	ip, ok := netip.AddrFromSlice(n.IP)
	if !ok {
		return netip.Prefix{}
	}
	ip = ip.Unmap()
	bits, size := n.Mask.Size()
	if size != ip.BitLen() {
		return netip.Prefix{}
	}
	return netip.PrefixFrom(ip, bits).Masked()
}
func allocatorSubnet(p netip.Prefix) *net.IPNet {
	if !p.IsValid() {
		return nil
	}
	return &net.IPNet{IP: net.IP(p.Addr().AsSlice()), Mask: net.CIDRMask(p.Bits(), p.Addr().BitLen())}
}
func (a *Allocator) ReserveIP(ip net.IP) {
	addr, ok := netip.AddrFromSlice(ip)
	if !ok {
		return
	}
	addr = addr.Unmap()
	a.mu.Lock()
	defer a.mu.Unlock()
	a.reserve(protocol.ReserveAddressRequest{Kind: protocol.AddressIP, Family: family(addr), Reserve: true, Address: addr})
}
func (a *Allocator) allocIP(family string) (net.IP, *net.IPNet) {
	a.mu.Lock()
	defer a.mu.Unlock()
	state := a.reserve(protocol.ReserveAddressRequest{Kind: protocol.AddressIP, Family: family})
	if !state.Address.IsValid() {
		return nil, nil
	}
	return net.IP(state.Address.AsSlice()), allocatorSubnet(state.Subnet)
}
func (a *Allocator) AllocIP4() (net.IP, *net.IPNet) { return a.allocIP("ipv4") }
func (a *Allocator) AllocIP6() (net.IP, *net.IPNet) { return a.allocIP("ipv6") }

func (a *Allocator) ReserveSubnet(n *net.IPNet) {
	prefix := allocatorPrefix(n)
	if !prefix.IsValid() {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.reserve(protocol.ReserveAddressRequest{Kind: protocol.AddressSubnet, Family: family(prefix.Addr()), Reserve: true, Subnet: prefix})
}
func (a *Allocator) allocSubnet(family string, bits int) *net.IPNet {
	if bits < 0 || bits > 128 || family == "ipv4" && bits > 32 {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	state := a.reserve(protocol.ReserveAddressRequest{Kind: protocol.AddressSubnet, Family: family, PrefixLength: uint8(bits)})
	return allocatorSubnet(state.Subnet)
}
func (a *Allocator) AllocSubnet4(bits int) *net.IPNet { return a.allocSubnet("ipv4", bits) }
func (a *Allocator) AllocSubnet6(bits int) *net.IPNet { return a.allocSubnet("ipv6", bits) }

func (a *Allocator) release(ctx context.Context, key allocationKey) bool {
	refs := a.owned[key]
	if len(refs) == 0 {
		return true
	}
	ref := refs[len(refs)-1]
	meta := a.client.meta(true)
	meta.GraphEpoch = a.epoch
	var result protocol.MutationResult
	err := a.client.peer.CallSettled(ctx, protocol.OperationReleaseAllocation, protocol.ResourceRequest{Meta: meta, Resource: ref}, &result)
	if err == nil && result.Outcome != protocol.OutcomeCommitted {
		err = decodeError(result.Code)
		if err == nil {
			err = protocol.ErrRecoveryRequired
		}
	}
	if err == nil && (result.CurrentResource != ref || result.OperationID != meta.OperationID) {
		err = protocol.ErrInvalidContract
	}
	if err != nil {
		a.err, a.failed = err, true
		return false
	}
	if len(refs) == 1 {
		delete(a.owned, key)
	} else {
		a.owned[key] = refs[:len(refs)-1]
	}
	return true
}
func (a *Allocator) free(key allocationKey) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.epoch != a.client.epoch.Load() {
		clear(a.owned)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), allocationTimeout)
	defer cancel()
	a.release(ctx, key)
}
func (a *Allocator) FreeIP(ip net.IP) {
	addr, ok := netip.AddrFromSlice(ip)
	if !ok {
		return
	}
	addr = addr.Unmap()
	a.free(allocationKey{kind: protocol.AddressIP, prefix: netip.PrefixFrom(addr, addr.BitLen())})
}
func (a *Allocator) FreeSubnet(n *net.IPNet) {
	p := allocatorPrefix(n)
	if !p.IsValid() {
		return
	}
	a.free(allocationKey{kind: protocol.AddressSubnet, prefix: p})
}
func (a *Allocator) freeAll(kind protocol.AddressKind) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.epoch != a.client.epoch.Load() {
		clear(a.owned)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), allocationTimeout)
	defer cancel()
	for key := range a.owned {
		if key.kind != kind {
			continue
		}
		for len(a.owned[key]) > 0 {
			if !a.release(ctx, key) {
				return
			}
		}
	}
}
func (a *Allocator) FreeAllIP()      { a.freeAll(protocol.AddressIP) }
func (a *Allocator) FreeAllSubnets() { a.freeAll(protocol.AddressSubnet) }

var _ subnet.IPAllocator = (*Allocator)(nil)
var _ subnet.SubnetAllocator = (*Allocator)(nil)
