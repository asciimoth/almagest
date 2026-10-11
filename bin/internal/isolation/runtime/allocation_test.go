package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/netip"
	"sync/atomic"
	"testing"
	"time"

	"github.com/asciimoth/almagest/bin/internal/isolation/protocol"
	"github.com/asciimoth/almagest/bin/internal/isolation/relay"
	"github.com/asciimoth/almagest/bin/internal/isolation/transport"
)

func allocationTestClient(t *testing.T, handler transport.Handler) *Client {
	t.Helper()
	c := ruleTestClient(t, handler)
	c.opens = make(chan struct{}, protocol.MaxConcurrentRequests)
	a, z := net.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	data, err := relay.New(ctx, a, c.session)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = data.Close(); _ = z.Close() })
	c.data = data
	return c
}
func allocationReply(body json.RawMessage) protocol.ResourceResult {
	var req protocol.ReserveAddressRequest
	_ = json.Unmarshal(body, &req)
	ref := protocol.ResourceRef{ID: protocol.ResourceID{2}, SessionID: req.Meta.SessionID, Type: protocol.ResourceAllocation, Generation: 1, GraphEpoch: req.Meta.GraphEpoch}
	return protocol.ResourceResult{MutationResult: protocol.MutationResult{OperationID: req.Meta.OperationID, Outcome: protocol.OutcomeCommitted, CurrentResource: ref}, Resource: ref, State: protocol.ResourceState{Up: true, Revision: 1, Allocation: protocol.AllocationState{Kind: protocol.AddressIP, Address: netip.MustParseAddr("192.0.2.1"), Subnet: netip.MustParsePrefix("192.0.2.0/24")}}}
}

func TestAllocatorRejectsMalformedReceipts(t *testing.T) {
	for name, change := range map[string]func(*protocol.ResourceResult){
		"id":                func(r *protocol.ResourceResult) { r.Resource.ID = protocol.ResourceID{} },
		"session":           func(r *protocol.ResourceResult) { r.Resource.SessionID[0]++ },
		"epoch":             func(r *protocol.ResourceResult) { r.Resource.GraphEpoch++ },
		"generation":        func(r *protocol.ResourceResult) { r.Resource.Generation++ },
		"resource_type":     func(r *protocol.ResourceResult) { r.Resource.Type = protocol.ResourceTun },
		"current_reference": func(r *protocol.ResourceResult) { r.CurrentResource.ID[0]++ },
		"operation":         func(r *protocol.ResourceResult) { r.OperationID = "other" },
		"allocation_kind":   func(r *protocol.ResourceResult) { r.State.Allocation.Kind = protocol.AddressSubnet },
		"allocation_parent": func(r *protocol.ResourceResult) { r.State.Allocation.Subnet = netip.MustParsePrefix("198.51.100.0/24") },
		"not_ready":         func(r *protocol.ResourceResult) { r.State.Up = false },
		"closed":            func(r *protocol.ResourceResult) { r.State.Closed = true },
		"uncertain":         func(r *protocol.ResourceResult) { r.State.RecoveryRequired = true },
	} {
		t.Run(name, func(t *testing.T) {
			c := allocationTestClient(t, func(_ context.Context, _ protocol.Operation, body json.RawMessage) (any, error) {
				r := allocationReply(body)
				change(&r)
				return r, nil
			})
			a := c.Allocator()
			if ip, _ := a.AllocIP4(); ip != nil || a.Err() == nil {
				t.Fatal("malformed successful allocation accepted")
			}
			select {
			case <-c.peer.Done():
			case <-time.After(time.Second):
				t.Fatal("malformed allocation did not revoke session")
			}
		})
	}
}

func TestAllocatorKeepsRejectedFamiliesIndependent(t *testing.T) {
	c := allocationTestClient(t, func(_ context.Context, op protocol.Operation, body json.RawMessage) (any, error) {
		if op == protocol.OperationReserveSubnet {
			var req protocol.ReserveAddressRequest
			if err := transport.Decode(body, &req); err != nil {
				return nil, err
			}
			return protocol.ResourceResult{MutationResult: protocol.MutationResult{OperationID: req.Meta.OperationID, Outcome: protocol.OutcomeRejected, Code: "unsupported"}}, nil
		}
		return allocationReply(body), nil
	})
	a := c.Allocator()
	if n := a.AllocSubnet6(64); n != nil || !errors.Is(a.Err(), protocol.ErrCapabilityUnsupported) {
		t.Fatalf("unsupported allocation=%v %v", n, a.Err())
	}
	if ip, _ := a.AllocIP4(); ip == nil || a.Err() != nil {
		t.Fatalf("unsupported subnet disabled IP allocation: %v %v", ip, a.Err())
	}
}

func TestOpenResourceAcceptedCompletionSurvivesCallerCancellation(t *testing.T) {
	var releases atomic.Int32
	c := allocationTestClient(t, func(_ context.Context, op protocol.Operation, body json.RawMessage) (any, error) {
		if op == protocol.OperationReleaseAllocation {
			var req protocol.ResourceRequest
			if err := transport.Decode(body, &req); err != nil {
				return nil, err
			}
			releases.Add(1)
			return protocol.MutationResult{OperationID: req.Meta.OperationID, Outcome: protocol.OutcomeCommitted, CurrentResource: req.Resource}, nil
		}
		return allocationReply(body), nil
	})
	for index := range 200 {
		ctx, cancel := context.WithCancel(context.Background())
		request := protocol.ReserveAddressRequest{Meta: c.meta(true), Kind: protocol.AddressIP, Family: "ipv4"}
		result, err := c.openResource(ctx, protocol.OperationReserveIP, request)
		cancel()
		if err != nil || result.Outcome != protocol.OutcomeCommitted {
			t.Fatalf("completion %d = %+v, %v", index, result, err)
		}
	}
	time.Sleep(20 * time.Millisecond)
	if got := releases.Load(); got != 0 {
		t.Fatalf("caller cancellation released %d accepted resources", got)
	}
}

func TestAllocatorFailedReleaseRetainsOwnership(t *testing.T) {
	var releases atomic.Int32
	c := allocationTestClient(t, func(_ context.Context, op protocol.Operation, body json.RawMessage) (any, error) {
		if op == protocol.OperationReleaseAllocation {
			var req protocol.ResourceRequest
			if err := transport.Decode(body, &req); err != nil {
				return nil, err
			}
			releases.Add(1)
			return protocol.MutationResult{OperationID: req.Meta.OperationID, Outcome: protocol.OutcomeUncertain, Code: "recovery", CurrentResource: req.Resource}, nil
		}
		return allocationReply(body), nil
	})
	a := c.Allocator()
	ip, _ := a.AllocIP4()
	a.FreeIP(ip)
	if !errors.Is(a.Err(), protocol.ErrRecoveryRequired) || len(a.owned) != 1 {
		t.Fatalf("failed release was discarded: %v", a.Err())
	}
	if next, _ := a.AllocIP4(); next != nil {
		t.Fatal("allocated after uncertain release")
	}
	a.FreeAllIP()
	if releases.Load() != 2 || len(a.owned) != 1 {
		t.Fatal("cleanup did not retain and retry the owned reservation")
	}
}

func TestAllocatorInvalidNativeArgumentsAreNoOps(t *testing.T) {
	var calls atomic.Int32
	c := allocationTestClient(t, func(context.Context, protocol.Operation, json.RawMessage) (any, error) {
		calls.Add(1)
		return nil, protocol.ErrInvalidContract
	})
	a := c.Allocator()
	for _, ip := range []net.IP{nil, {1}, {1, 2, 3}} {
		a.ReserveIP(ip)
		a.FreeIP(ip)
	}
	for _, n := range []*net.IPNet{nil, {}, {IP: net.IPv4(192, 0, 2, 1), Mask: net.IPMask{255, 1, 255, 0}}} {
		a.ReserveSubnet(n)
		a.FreeSubnet(n)
	}
	for _, bits := range []int{-1, 129, 1000} {
		if a.AllocSubnet4(bits) != nil || a.AllocSubnet6(bits) != nil {
			t.Fatal("invalid prefix allocated")
		}
	}
	if a.AllocSubnet4(33) != nil {
		t.Fatal("invalid IPv4 prefix allocated")
	}
	a.FreeIP(net.IPv4(192, 0, 2, 1))
	a.FreeSubnet(allocatorSubnet(netip.MustParsePrefix("192.0.2.0/24")))
	a.FreeAllIP()
	a.FreeAllSubnets()
	if calls.Load() != 0 || a.Err() != nil {
		t.Fatalf("invalid input reached N: %d %v", calls.Load(), a.Err())
	}
}

func FuzzAllocatorNativePrefixes(f *testing.F) {
	f.Add([]byte{192, 0, 2, 7}, []byte{255, 255, 255, 0})
	f.Add([]byte{1}, []byte{255})
	f.Fuzz(func(t *testing.T, ip, mask []byte) {
		if len(ip) > 128 || len(mask) > 128 {
			return
		}
		p := allocatorPrefix(&net.IPNet{IP: net.IP(ip), Mask: net.IPMask(mask)})
		if !p.IsValid() {
			return
		}
		n := allocatorSubnet(p)
		if n == nil || allocatorPrefix(n) != p || !n.Contains(n.IP) {
			t.Fatal("subnet changed after normalization")
		}
		// Mutation of the returned object cannot alter an existing netip key.
		n.IP[0] ^= 255
		if p != p.Masked() {
			t.Fatal("unmasked prefix accepted")
		}
	})
}
