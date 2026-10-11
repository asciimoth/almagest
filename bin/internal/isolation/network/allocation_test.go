package network

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/netip"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/asciimoth/almagest/bin/internal/isolation/policy"
	"github.com/asciimoth/almagest/bin/internal/isolation/protocol"
	"github.com/asciimoth/gonnect/subnet"
	"github.com/asciimoth/gonnect/sysnet"
)

type allocationBackend struct {
	*fakeBackend
	calls   atomic.Int32
	reserve func(context.Context, protocol.ReserveAddressRequest) (OwnedAllocation, error)
}

func (f *allocationBackend) ReserveAddress(ctx context.Context, req protocol.ReserveAddressRequest) (OwnedAllocation, error) {
	f.calls.Add(1)
	if f.reserve != nil {
		return f.reserve(ctx, req)
	}
	state := protocol.AllocationState{Kind: req.Kind}
	if req.Kind == protocol.AddressIP {
		if req.Reserve {
			state.Address = req.Address
		} else {
			state.Address = netip.MustParseAddr("10.23.0.2")
			state.Subnet = netip.MustParsePrefix("10.23.0.0/24")
		}
	} else {
		if req.Reserve {
			state.Subnet = req.Subnet.Masked()
		} else {
			state.Subnet = netip.PrefixFrom(netip.MustParseAddr("10.23.0.0"), int(req.PrefixLength)).Masked()
		}
	}
	return &fakeAllocation{state: state}, nil
}

type fakeAllocation struct {
	state    protocol.AllocationState
	closes   atomic.Int32
	closeErr error
}

func (a *fakeAllocation) State() protocol.AllocationState { return a.state }
func (a *fakeAllocation) Close(context.Context) error     { a.closes.Add(1); return a.closeErr }
func allocationBroker(t *testing.T) (*Broker, *allocationBackend) {
	t.Helper()
	b, base := testBroker(t)
	f := &allocationBackend{fakeBackend: base}
	b.backend = f
	return b, f
}
func reserveRequest(b *Broker, id string) protocol.ReserveAddressRequest {
	return protocol.ReserveAddressRequest{Meta: meta(b, id), Kind: protocol.AddressIP, Family: "ipv4", Reserve: true, Address: netip.MustParseAddr("10.23.0.2")}
}

func TestAllocationOwnershipReceiptsAndCleanup(t *testing.T) {
	b, backend := allocationBroker(t)
	owned := &fakeAllocation{state: protocol.AllocationState{Kind: protocol.AddressIP, Address: netip.MustParseAddr("10.23.0.2")}}
	backend.reserve = func(context.Context, protocol.ReserveAddressRequest) (OwnedAllocation, error) { return owned, nil }
	req := reserveRequest(b, "reserve")
	result, err := b.ReserveAddress(context.Background(), req)
	if err != nil || result.Outcome != protocol.OutcomeCommitted || result.Resource.Type != protocol.ResourceAllocation {
		t.Fatalf("reserve=%+v %v", result, err)
	}
	req.Meta.RequestID++
	replay, err := b.ReserveAddress(context.Background(), req)
	if err != nil || replay.Resource != result.Resource || backend.calls.Load() != 1 {
		t.Fatalf("replay=%+v %v", replay, err)
	}
	receipt, err := b.GetResult(context.Background(), protocol.OperationResultRequest{Meta: meta(b, ""), OperationID: "reserve"})
	if err != nil || !reflect.DeepEqual(receipt, replay) {
		t.Fatalf("lost reply receipt=%+v %v", receipt, err)
	}
	conflict := req
	conflict.Address = netip.MustParseAddr("10.23.0.3")
	if _, err := b.ReserveAddress(context.Background(), conflict); !errors.Is(err, protocol.ErrOperationConflict) {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*protocol.ResourceRef){
		"foreign_session": func(r *protocol.ResourceRef) { r.SessionID[0]++ },
		"foreign_id":      func(r *protocol.ResourceRef) { r.ID[0]++ },
		"generation":      func(r *protocol.ResourceRef) { r.Generation++ },
		"graph":           func(r *protocol.ResourceRef) { r.GraphEpoch++ },
		"type":            func(r *protocol.ResourceRef) { r.Type = protocol.ResourceTun },
	} {
		t.Run(name, func(t *testing.T) {
			ref := result.Resource
			mutate(&ref)
			if _, err := b.ReleaseAllocation(context.Background(), protocol.ResourceRequest{Meta: meta(b, name), Resource: ref}); err == nil {
				t.Fatal("foreign release accepted")
			}
			if owned.closes.Load() != 0 {
				t.Fatal("foreign release reached backend")
			}
		})
	}
	if _, err := b.GetResourceState(context.Background(), protocol.ResourceRequest{Meta: meta(b, ""), Resource: result.Resource}); !errors.Is(err, protocol.ErrResourceType) {
		t.Fatalf("allocation used as a TUN: %v", err)
	}
	release := protocol.ResourceRequest{Meta: meta(b, "release"), Resource: result.Resource}
	closed, err := b.ReleaseAllocation(context.Background(), release)
	if err != nil || closed.Outcome != protocol.OutcomeCommitted || owned.closes.Load() != 1 {
		t.Fatalf("release=%+v %v", closed, err)
	}
	release.Meta = meta(b, "release-again")
	if _, err := b.ReleaseAllocation(context.Background(), release); err != nil || owned.closes.Load() != 1 {
		t.Fatalf("idempotent close=%v", err)
	}
	if err := b.Shutdown(context.Background()); err != nil || owned.closes.Load() != 1 {
		t.Fatalf("shutdown=%v", err)
	}
}

func TestAllocationDispatchAndLimits(t *testing.T) {
	b, backend := allocationBroker(t)
	b.policy.Limits.MaxLiveResources = 1
	req := reserveRequest(b, "one")
	body, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.Handle(context.Background(), protocol.OperationReserveSubnet, body); !errors.Is(err, protocol.ErrInvalidContract) {
		t.Fatal(err)
	}
	raw, err := b.Handle(context.Background(), protocol.OperationReserveIP, body)
	if err != nil {
		t.Fatal(err)
	}
	result := raw.(protocol.ResourceResult)
	req.Meta = meta(b, "two")
	if _, err := b.ReserveAddress(context.Background(), req); !errors.Is(err, protocol.ErrLimitExceeded) {
		t.Fatalf("limit=%v", err)
	}
	if backend.calls.Load() != 1 {
		t.Fatal("limit did not precede native work")
	}
	body, err = json.Marshal(protocol.ResourceRequest{Meta: meta(b, "free"), Resource: result.Resource})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.Handle(context.Background(), protocol.OperationReleaseAllocation, body); err != nil {
		t.Fatal(err)
	}
	req.Meta = meta(b, "three")
	if _, err := b.ReserveAddress(context.Background(), req); err != nil {
		t.Fatalf("released capacity was not reusable: %v", err)
	}
	for _, op := range []protocol.Operation{protocol.OperationReserveIP, protocol.OperationReserveSubnet, protocol.OperationReleaseAllocation} {
		for _, body := range []string{`{`, `{"Kind":"ip","Kind":"subnet"}`, `{"path":"/etc/passwd"}`, `{"PrefixLength":-1}`} {
			if _, err := b.Handle(context.Background(), op, json.RawMessage(body)); err == nil {
				t.Fatalf("accepted %s", body)
			}
		}
	}
}

func TestAllocationFailuresAndLateCompletion(t *testing.T) {
	t.Run("unsupported", func(t *testing.T) {
		b, _ := testBroker(t)
		if _, err := b.ReserveAddress(context.Background(), reserveRequest(b, "x")); !errors.Is(err, protocol.ErrCapabilityUnsupported) {
			t.Fatal(err)
		}
	})
	for _, failure := range []error{protocol.ErrAllocationExhausted, protocol.ErrCapabilityUnsupported, context.Canceled} {
		b, backend := allocationBroker(t)
		backend.reserve = func(context.Context, protocol.ReserveAddressRequest) (OwnedAllocation, error) { return nil, failure }
		result, err := b.ReserveAddress(context.Background(), reserveRequest(b, "x"))
		if !errors.Is(err, failure) || result.Outcome != protocol.OutcomeRejected {
			t.Fatalf("failure=%+v %v", result, err)
		}
	}
	for _, mode := range []string{"partial", "malformed", "cancel", "cleanup_failure"} {
		t.Run(mode, func(t *testing.T) {
			b, backend := allocationBroker(t)
			owned := &fakeAllocation{state: protocol.AllocationState{Kind: protocol.AddressIP, Address: netip.MustParseAddr("10.23.0.2")}}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			backend.reserve = func(context.Context, protocol.ReserveAddressRequest) (OwnedAllocation, error) {
				switch mode {
				case "cancel":
					cancel()
				case "malformed":
					owned.state.Kind = protocol.AddressSubnet
				case "cleanup_failure":
					owned.closeErr = errors.New("cannot release")
					return owned, context.Canceled
				case "partial":
					return owned, context.Canceled
				}
				return owned, nil
			}
			_, _ = b.ReserveAddress(ctx, reserveRequest(b, "x"))
			// Cancellation can return before the compensating worker finishes.
			b.gate <- struct{}{}
			<-b.gate
			result, err := b.GetResult(context.Background(), protocol.OperationResultRequest{Meta: meta(b, ""), OperationID: "x"})
			if err == nil || owned.closes.Load() != 1 {
				t.Fatalf("compensation=%+v %v closes=%d", result, err, owned.closes.Load())
			}
			want := protocol.OutcomeCompensated
			if mode == "cleanup_failure" {
				want = protocol.OutcomeUncertain
			}
			if result.Outcome != want {
				t.Fatalf("outcome=%s want=%s", result.Outcome, want)
			}
		})
	}
	t.Run("canceled_before_start", func(t *testing.T) {
		b, backend := allocationBroker(t)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, _ = b.ReserveAddress(ctx, reserveRequest(b, "x"))
		b.gate <- struct{}{}
		<-b.gate
		if backend.calls.Load() != 0 {
			t.Fatal("pre-canceled allocation reached backend")
		}
	})
}

type allocatorSystem struct {
	sysnet.System
	ip  subnet.IPAllocator
	sub subnet.SubnetAllocator
}

func (s allocatorSystem) AllocIP() subnet.IPAllocator         { return s.ip }
func (s allocatorSystem) AllocSubnet() subnet.SubnetAllocator { return s.sub }
func allocationFixture(t *testing.T) (*Broker, *subnet.CombinedAllocator) {
	t.Helper()
	pool := subnet.NewCombinedAllocator(nativeSubnet(netip.MustParsePrefix("10.23.0.0/24")), nativeSubnet(netip.MustParsePrefix("fd23::/64")), 29, 124, nil, nil, nil, nil, nil)
	b, _ := testBroker(t)
	b.backend = &SystemBackend{system: allocatorSystem{ip: pool, sub: pool}}
	return b, pool
}

func TestAllocationRuntimeNativeAllocatorParity(t *testing.T) {
	b, native := allocationFixture(t)
	c := startClient(t, b)
	a := c.Allocator()
	if c.Allocator() != a {
		t.Fatal("allocator identity changed within an epoch")
	}
	// Native reservations not owned by R must survive both forms of FreeAll.
	foreign := net.ParseIP("10.23.0.2")
	native.ReserveIP(foreign)
	block := nativeSubnet(netip.MustParsePrefix("10.23.0.128/25"))
	native.ReserveSubnet(block)
	var ips []net.IP
	for _, allocate := range []func() (net.IP, *net.IPNet){a.AllocIP4, a.AllocIP6} {
		ip, parent := allocate()
		if ip == nil || parent == nil || !parent.Contains(ip) {
			t.Fatalf("IP=%v parent=%v error=%v", ip, parent, a.Err())
		}
		ips = append(ips, ip)
		a.ReserveIP(ip)
		a.FreeIP(ip) // Exactly one reserve count remains.
	}
	for _, allocate := range []func(int) *net.IPNet{a.AllocSubnet4, a.AllocSubnet6} {
		bits := 28
		if len(ips) == 3 {
			bits = 120
		}
		n := allocate(bits)
		if n == nil {
			t.Fatalf("subnet error: %v", a.Err())
		}
		a.ReserveSubnet(n)
		a.FreeSubnet(n)
		ips = append(ips, nil)
	}
	a.FreeAllIP()
	a.FreeAllSubnets()
	if err := a.Err(); err != nil {
		t.Fatal(err)
	}
	status, err := c.Status(context.Background())
	if err != nil || status.LiveResources != 0 {
		t.Fatalf("status=%+v %v", status, err)
	}
	// Allocate every free address; the foreign native reservation must never
	// become available, even after R released all of its own reservations.
	for range 130 {
		ip, _ := native.AllocIP4()
		if ip == nil {
			break
		}
		if ip.Equal(foreign) || block.Contains(ip) {
			t.Fatalf("runtime released native ownership of %v", ip)
		}
	}
}

func TestAllocationRuntimeEpochAndPeerLoss(t *testing.T) {
	b, _ := allocationFixture(t)
	c := startClient(t, b)
	old := c.Allocator()
	ip, _ := old.AllocIP4()
	if ip == nil {
		t.Fatal(old.Err())
	}
	if err := c.ReleaseGraphEpoch(context.Background()); err != nil {
		t.Fatal(err)
	}
	current := c.Allocator()
	if current == old {
		t.Fatal("old allocator acquired a new epoch")
	}
	current.ReserveIP(ip)
	old.FreeIP(ip)
	old.FreeAllIP()
	if ip, _ := old.AllocIP4(); ip != nil || !errors.Is(old.Err(), protocol.ErrStaleGraphEpoch) {
		t.Fatal("old graph retained allocator authority")
	}
	status, err := c.Status(context.Background())
	if err != nil || status.LiveResources != 1 {
		t.Fatalf("old allocator released current reservation: %+v %v", status, err)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if ip, _ := current.AllocIP4(); ip != nil || current.Err() == nil {
		t.Fatal("dead broker supplied an address")
	}
}

func TestAllocationRuntimeExhaustionAndReferenceCounts(t *testing.T) {
	// /30 has exactly two usable addresses. Reference-counted reservations must
	// survive one Free and become available after the matching second Free.
	pool := subnet.NewIPAllocator(nativeSubnet(netip.MustParsePrefix("192.0.2.0/30")), nil)
	b, _ := testBroker(t)
	b.backend = &SystemBackend{system: allocatorSystem{ip: pool}}
	c := startClient(t, b)
	a := c.Allocator()
	first := net.ParseIP("192.0.2.1")
	a.ReserveIP(first)
	a.ReserveIP(first)
	other, _ := a.AllocIP4()
	if other == nil || other.Equal(first) {
		t.Fatal("reserved address was allocated")
	}
	a.FreeIP(first)
	if ip, _ := a.AllocIP4(); ip != nil || a.Err() != nil {
		t.Fatalf("exhaustion=%v %v", ip, a.Err())
	}
	a.FreeIP(first)
	reused, _ := a.AllocIP4()
	if !reused.Equal(first) {
		t.Fatalf("address was not released: %v %v", reused, a.Err())
	}
	a.FreeAllIP()
}

func TestAllocationRuntimeConcurrentReservations(t *testing.T) {
	b, _ := allocationFixture(t)
	c := startClient(t, b)
	a := c.Allocator()
	var wg sync.WaitGroup
	ips := make(chan net.IP, 16)
	for range 16 {
		wg.Go(func() { ip, _ := a.AllocIP4(); ips <- ip })
	}
	wg.Wait()
	close(ips)
	seen := map[string]bool{}
	for ip := range ips {
		if ip == nil || seen[ip.String()] {
			t.Fatalf("duplicate or missing address %v: %v", ip, a.Err())
		}
		seen[ip.String()] = true
	}
	a.FreeAllIP()
}

func TestAllocationRuntimeFailedReserveStopsNewAllocations(t *testing.T) {
	b, backend := allocationBroker(t)
	c := startClient(t, b)
	a := c.Allocator()
	backend.reserve = func(context.Context, protocol.ReserveAddressRequest) (OwnedAllocation, error) {
		return nil, protocol.ErrCapabilityUnavailable
	}
	a.ReserveIP(net.ParseIP("192.0.2.1"))
	if a.Err() == nil {
		t.Fatal("failed void reservation was hidden")
	}
	if ip, _ := a.AllocIP4(); ip != nil {
		t.Fatal("allocated after failed reservation")
	}
	if backend.calls.Load() != 1 {
		t.Fatal("failed allocator kept making requests")
	}
}

func TestAllocationShutdownReleasesReservations(t *testing.T) {
	b, backend := allocationBroker(t)
	owned := &fakeAllocation{state: protocol.AllocationState{Kind: protocol.AddressIP, Address: netip.MustParseAddr("10.23.0.2")}}
	backend.reserve = func(context.Context, protocol.ReserveAddressRequest) (OwnedAllocation, error) { return owned, nil }
	if _, err := b.ReserveAddress(context.Background(), reserveRequest(b, "x")); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := b.Shutdown(ctx); err != nil || owned.closes.Load() != 1 {
		t.Fatalf("shutdown=%v close=%d", err, owned.closes.Load())
	}
}

func TestAllocationAuthorizationPrecedesBackend(t *testing.T) {
	for name, change := range map[string]func(*Broker, *protocol.ReserveAddressRequest){
		"session":       func(_ *Broker, r *protocol.ReserveAddressRequest) { r.Meta.SessionID[0]++ },
		"session_epoch": func(_ *Broker, r *protocol.ReserveAddressRequest) { r.Meta.SessionEpoch++ },
		"graph":         func(_ *Broker, r *protocol.ReserveAddressRequest) { r.Meta.GraphEpoch++ },
		"operation_id":  func(_ *Broker, r *protocol.ReserveAddressRequest) { r.Meta.OperationID = "" },
		"request_id":    func(_ *Broker, r *protocol.ReserveAddressRequest) { r.Meta.RequestID = 0 },
		"receipt_limit": func(b *Broker, _ *protocol.ReserveAddressRequest) { b.policy.Limits.MaxOperationReceipts = 0 },
		"recovery":      func(b *Broker, _ *protocol.ReserveAddressRequest) { b.recovery = true },
		"closed":        func(b *Broker, _ *protocol.ReserveAddressRequest) { b.closed = true },
		"malformed":     func(_ *Broker, r *protocol.ReserveAddressRequest) { r.Family = "host" },
	} {
		t.Run(name, func(t *testing.T) {
			b, backend := allocationBroker(t)
			req := reserveRequest(b, "one")
			change(b, &req)
			if _, err := b.ReserveAddress(context.Background(), req); err == nil {
				t.Fatal("unauthorized request accepted")
			}
			if backend.calls.Load() != 0 {
				t.Fatal("unauthorized allocation reached backend")
			}
		})
	}
}

func TestAllocationTimeoutKeepsControlResponsive(t *testing.T) {
	b, backend := allocationBroker(t)
	b.timeout = 30 * time.Millisecond
	started, release := make(chan struct{}), make(chan struct{})
	var stop sync.Once
	t.Cleanup(func() { stop.Do(func() { close(release) }) })
	owned := &fakeAllocation{state: protocol.AllocationState{Kind: protocol.AddressIP, Address: netip.MustParseAddr("10.23.0.2")}}
	backend.reserve = func(context.Context, protocol.ReserveAddressRequest) (OwnedAllocation, error) {
		close(started)
		<-release
		return owned, nil
	}
	done := make(chan error, 1)
	go func() { _, err := b.ReserveAddress(context.Background(), reserveRequest(b, "blocked")); done <- err }()
	<-started
	select {
	case err := <-done:
		if !errors.Is(err, protocol.ErrRecoveryRequired) {
			t.Fatalf("unproved timeout=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("unbounded caller wait")
	}
	status, err := b.Status(context.Background(), protocol.ReadRequest{Meta: meta(b, "")})
	if err != nil || !status.RecoveryRequired {
		t.Fatalf("status blocked or hid uncertain work: %+v %v", status, err)
	}
	if _, err := b.ReserveAddress(context.Background(), reserveRequest(b, "queued")); err == nil {
		t.Fatal("queued request accepted behind a stuck worker")
	}
	if backend.calls.Load() != 1 {
		t.Fatal("timeout created a second native worker")
	}
	stop.Do(func() { close(release) })
	b.gate <- struct{}{}
	<-b.gate
	result, err := b.GetResult(context.Background(), protocol.OperationResultRequest{Meta: meta(b, ""), OperationID: "blocked"})
	if err == nil || result.Outcome != protocol.OutcomeCompensated || owned.closes.Load() != 1 {
		t.Fatalf("late result not compensated: %+v %v", result, err)
	}
}

func TestAllocationOptionalPolicy(t *testing.T) {
	for _, kind := range []protocol.AddressKind{protocol.AddressIP, protocol.AddressSubnet} {
		t.Run(string(kind), func(t *testing.T) {
			b, backend := allocationBroker(t)
			b.policy.NetworkRestrictions = &policy.NetworkRestrictions{DeniedOperations: []protocol.Operation{allocationOperation(kind)}}
			req := reserveRequest(b, "denied")
			req.Kind = kind
			if kind == protocol.AddressSubnet {
				req.Address = netip.Addr{}
				req.Subnet = netip.MustParsePrefix("10.23.0.0/24")
			}
			if _, err := b.ReserveAddress(context.Background(), req); !errors.Is(err, protocol.ErrPolicyDenied) {
				t.Fatalf("restricted allocation=%v", err)
			}
			if backend.calls.Load() != 0 {
				t.Fatal("restricted request reached native allocator")
			}
		})
	}
}
