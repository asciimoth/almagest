package network

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"sync"

	"github.com/asciimoth/almagest/bin/internal/isolation/protocol"
)

// OwnedAllocation owns one native reserve count. It never closes an allocator
// or frees unrelated reservations. N's death discards this in-memory state;
// there is no host effect requiring a durable recovery-journal entry. Close
// must be idempotent, including after a previous call returned an error.
type OwnedAllocation interface {
	State() protocol.AllocationState
	Close(context.Context) error
}

type AllocationBackend interface {
	ReserveAddress(context.Context, protocol.ReserveAddressRequest) (OwnedAllocation, error)
}

func allocationOperation(kind protocol.AddressKind) protocol.Operation {
	switch kind {
	case protocol.AddressIP:
		return protocol.OperationReserveIP
	case protocol.AddressSubnet:
		return protocol.OperationReserveSubnet
	default:
		return ""
	}
}

func (b *Broker) ReserveAddress(ctx context.Context, req protocol.ReserveAddressRequest) (protocol.ResourceResult, error) {
	input := req
	input.Meta.RequestID = 0
	return b.mutate(ctx, req.Meta, allocationOperation(req.Kind), input, func(ctx context.Context) (protocol.ResourceResult, error) {
		if err := req.Validate(); err != nil {
			return rejected(req.Meta.OperationID, err), err
		}
		backend, ok := b.backend.(AllocationBackend)
		if !ok {
			return rejected(req.Meta.OperationID, nil), protocol.ErrCapabilityUnsupported
		}
		b.mu.Lock()
		live := 0
		for _, r := range b.resources {
			if !r.state.Closed {
				live++
			}
		}
		limited := live >= int(b.policy.Limits.MaxLiveResources) || len(b.resources) >= protocol.MaxOperationReceipts
		recovery := b.recovery
		b.mu.Unlock()
		if recovery {
			return rejected(req.Meta.OperationID, nil), protocol.ErrRecoveryRequired
		}
		if limited {
			return rejected(req.Meta.OperationID, nil), protocol.ErrLimitExceeded
		}
		if err := ctx.Err(); err != nil {
			return rejected(req.Meta.OperationID, err), err
		}
		id, err := protocol.NewResourceID()
		if err != nil {
			return rejected(req.Meta.OperationID, err), err
		}
		b.mu.Lock()
		_, collision := b.resources[id]
		b.mu.Unlock()
		if collision || !id.Valid() {
			return rejected(req.Meta.OperationID, nil), protocol.ErrInvalidContract
		}
		r := &resource{ref: protocol.ResourceRef{ID: id, SessionID: b.session.ID, Type: protocol.ResourceAllocation, Generation: 1, GraphEpoch: req.Meta.GraphEpoch}}
		owned, err := backend.ReserveAddress(ctx, req)
		if owned == nil {
			if err == nil {
				err = protocol.ErrInvalidContract
			}
			// AllocationBackend must return every acquired reservation even on error.
			return rejected(req.Meta.OperationID, err), err
		}
		r.allocation = owned
		state := owned.State()
		if err == nil && state.Validate(req) != nil {
			err = protocol.ErrInvalidContract
		}
		b.mu.Lock()
		b.resources[id] = r
		if err == nil {
			err = b.current(req.Meta)
		}
		if err == nil {
			err = ctx.Err()
		}
		if err == nil {
			r.state = protocol.ResourceState{Allocation: state, Up: true, Revision: 1}
			b.mu.Unlock()
			return b.result(r, protocol.OutcomeCommitted), nil
		}
		b.mu.Unlock()
		cleanup, cancel := context.WithTimeout(context.Background(), b.timeout)
		defer cancel()
		if closeErr := b.closeNative(cleanup, r); closeErr != nil {
			return b.failed(r, req.Meta.OperationID, errors.Join(err, closeErr))
		}
		return b.result(r, protocol.OutcomeCompensated), err
	})
}

func (b *Broker) ReleaseAllocation(ctx context.Context, req protocol.ResourceRequest) (protocol.MutationResult, error) {
	if req.Resource.Type != protocol.ResourceAllocation {
		return protocol.MutationResult{}, protocol.ErrResourceType
	}
	return b.closeResource(ctx, req, protocol.OperationReleaseAllocation)
}

func (b *Broker) closeAllocation(ctx context.Context, r *resource) error {
	if r.allocation == nil {
		return protocol.ErrRecoveryRequired
	}
	if err := r.allocation.Close(ctx); err != nil {
		return err
	}
	b.mu.Lock()
	r.allocation = nil
	r.state.Up = false
	r.state.Closed = true
	r.state.RecoveryRequired = false
	r.state.Revision++
	b.mu.Unlock()
	return nil
}

type systemAllocation struct {
	state protocol.AllocationState
	free  func()
	once  sync.Once
}

func (a *systemAllocation) State() protocol.AllocationState { return a.state }
func (a *systemAllocation) Close(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	a.once.Do(a.free)
	return nil
}

func nativeSubnet(p netip.Prefix) *net.IPNet {
	return &net.IPNet{IP: net.IP(p.Masked().Addr().AsSlice()), Mask: net.CIDRMask(p.Bits(), p.Addr().BitLen())}
}
func allocationPrefix(n *net.IPNet) netip.Prefix {
	if n == nil {
		return netip.Prefix{}
	}
	ip, ok := netip.AddrFromSlice(n.IP)
	if !ok {
		return netip.Prefix{}
	}
	bits, size := n.Mask.Size()
	ip = ip.Unmap()
	if size != ip.BitLen() {
		return netip.Prefix{}
	}
	return netip.PrefixFrom(ip, bits).Masked()
}

func (s *SystemBackend) ReserveAddress(ctx context.Context, req protocol.ReserveAddressRequest) (OwnedAllocation, error) {
	if err := req.Validate(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	state := protocol.AllocationState{Kind: req.Kind}
	if req.Kind == protocol.AddressIP {
		allocator := s.system.AllocIP()
		if allocator == nil {
			return nil, protocol.ErrCapabilityUnsupported
		}
		var ip net.IP
		var parent *net.IPNet
		if req.Reserve {
			ip = net.IP(req.Address.AsSlice())
			allocator.ReserveIP(ip)
		} else if req.Family == "ipv4" {
			ip, parent = allocator.AllocIP4()
		} else {
			ip, parent = allocator.AllocIP6()
		}
		if ip == nil {
			return nil, protocol.ErrAllocationExhausted
		}
		// Keep a private copy so a native allocator cannot change our release key.
		ip = append(net.IP(nil), ip...)
		state.Address, _ = netip.AddrFromSlice(ip)
		state.Address = state.Address.Unmap()
		if !req.Reserve {
			state.Subnet = allocationPrefix(parent)
		}
		return &systemAllocation{state: state, free: func() { allocator.FreeIP(ip) }}, nil
	}
	allocator := s.system.AllocSubnet()
	if allocator == nil {
		return nil, protocol.ErrCapabilityUnsupported
	}
	var subnet *net.IPNet
	if req.Reserve {
		subnet = nativeSubnet(req.Subnet)
		allocator.ReserveSubnet(subnet)
	} else if req.Family == "ipv4" {
		subnet = allocator.AllocSubnet4(int(req.PrefixLength))
	} else {
		subnet = allocator.AllocSubnet6(int(req.PrefixLength))
	}
	if subnet == nil {
		return nil, protocol.ErrAllocationExhausted
	}
	subnet = &net.IPNet{IP: append(net.IP(nil), subnet.IP...), Mask: append(net.IPMask(nil), subnet.Mask...)}
	state.Subnet = allocationPrefix(subnet)
	return &systemAllocation{state: state, free: func() { allocator.FreeSubnet(subnet) }}, nil
}
