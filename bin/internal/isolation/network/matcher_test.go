package network

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/asciimoth/almagest/bin/internal/isolation/policy"
	"github.com/asciimoth/almagest/bin/internal/isolation/protocol"
	"github.com/asciimoth/gonnect/sockowner"
	"github.com/asciimoth/gonnect/sysnet"
)

type matcherBackend struct {
	*fakeBackend
	build func(context.Context, protocol.Rule) (sysnet.Matcher, error)
}

func (b matcherBackend) BuildMatcher(ctx context.Context, rule protocol.Rule) (sysnet.Matcher, error) {
	return b.build(ctx, rule)
}

type fakeMatcher struct {
	match  func(sockowner.FlowTuple) (bool, error)
	close  func() error
	calls  atomic.Int32
	closes atomic.Int32
}

func (m *fakeMatcher) Match(f sockowner.FlowTuple) (bool, error) {
	m.calls.Add(1)
	if m.match != nil {
		return m.match(f)
	}
	return true, nil
}
func (m *fakeMatcher) Close() error {
	m.closes.Add(1)
	if m.close != nil {
		return m.close()
	}
	return nil
}
func matcherBroker(t *testing.T, m *fakeMatcher) *Broker {
	t.Helper()
	b, f := testBroker(t)
	b.backend = matcherBackend{f, func(context.Context, protocol.Rule) (sysnet.Matcher, error) { return m, nil }}
	return b
}
func createMatcher(t *testing.T, b *Broker, id string) protocol.ResourceRef {
	t.Helper()
	r, err := b.CreateMatcher(context.Background(), protocol.CreateMatcherRequest{Meta: meta(b, id), Rule: protocol.Rule{Type: "pid", Value: "42"}})
	if err != nil || r.Outcome != protocol.OutcomeCommitted {
		t.Fatalf("create: %+v %v", r, err)
	}
	return r.Resource
}
func matchRequest(b *Broker, ref protocol.ResourceRef) protocol.MatchRequest {
	return protocol.MatchRequest{Meta: meta(b, ""), Matcher: ref, Flow: protocol.FiveTuple{Protocol: "tcp", Local: netip.MustParseAddrPort("192.0.2.1:1234"), Remote: netip.MustParseAddrPort("192.0.2.2:443")}}
}

func TestMatcherOwnershipAndFreshness(t *testing.T) {
	m := &fakeMatcher{}
	b := matcherBroker(t, m)
	ref := createMatcher(t, b, "one")
	if again := createMatcher(t, b, "one"); again != ref {
		t.Fatal("receipt replay created a matcher")
	}
	q := matchRequest(b, ref)
	for range 3 {
		r, err := b.Match(context.Background(), q)
		if err != nil || !r.Matched || r.Quality != "unknown" {
			t.Fatalf("%+v %v", r, err)
		}
	}
	if m.calls.Load() != 3 {
		t.Fatal("lookup was cached")
	}
	for name, change := range map[string]func(*protocol.MatchRequest){
		"foreign session": func(q *protocol.MatchRequest) { q.Matcher.SessionID[0] ^= 1 },
		"foreign id":      func(q *protocol.MatchRequest) { q.Matcher.ID[0] ^= 1 },
		"zero id":         func(q *protocol.MatchRequest) { q.Matcher.ID = protocol.ResourceID{} },
		"wrong type":      func(q *protocol.MatchRequest) { q.Matcher.Type = protocol.ResourceTun },
		"generation":      func(q *protocol.MatchRequest) { q.Matcher.Generation++ },
		"epoch":           func(q *protocol.MatchRequest) { q.Meta.GraphEpoch++ },
		"resource epoch":  func(q *protocol.MatchRequest) { q.Matcher.GraphEpoch++ },
		"mutation id":     func(q *protocol.MatchRequest) { q.Meta.OperationID = "forbidden" },
	} {
		t.Run(name, func(t *testing.T) {
			bad := q
			change(&bad)
			if _, err := b.Match(context.Background(), bad); err == nil {
				t.Fatal("accepted invalid identity")
			}
		})
	}
	if m.calls.Load() != 3 {
		t.Fatal("invalid request reached native matcher")
	}
	if _, err := b.GetResourceState(context.Background(), protocol.ResourceRequest{Meta: meta(b, ""), Resource: ref}); !errors.Is(err, protocol.ErrResourceType) {
		t.Fatal(err)
	}
	for _, id := range []string{"close", "close", "close-again"} {
		if _, err := b.CloseResource(context.Background(), protocol.ResourceRequest{Meta: meta(b, id), Resource: ref}); err != nil {
			t.Fatal(err)
		}
	}
	if m.closes.Load() != 1 {
		t.Fatal("close was not idempotent")
	}
	if _, err := b.Match(context.Background(), q); !errors.Is(err, net.ErrClosed) {
		t.Fatal(err)
	}
}

func TestMatcherErrorCategories(t *testing.T) {
	for _, cause := range []error{sockowner.ErrNoOwner, sockowner.ErrAUW, sockowner.ErrAEx, os.ErrPermission, context.Canceled, context.DeadlineExceeded, sysnet.ErrNotSupported, sysnet.ErrUnavailable, errors.New("secret process /path pid=42")} {
		t.Run(cause.Error(), func(t *testing.T) {
			m := &fakeMatcher{match: func(sockowner.FlowTuple) (bool, error) { return true, fmt.Errorf("private executable: %w", cause) }}
			b := matcherBroker(t, m)
			q := matchRequest(b, createMatcher(t, b, "one"))
			result, err := b.Match(context.Background(), q)
			if err == nil || result.Matched || strings.Contains(err.Error(), "private") || strings.Contains(err.Error(), "secret") {
				t.Fatalf("%+v %v", result, err)
			}
			want := cause
			if cause == sysnet.ErrNotSupported {
				want = protocol.ErrCapabilityUnsupported
			}
			if cause == sysnet.ErrUnavailable {
				want = protocol.ErrCapabilityUnavailable
			}
			if !strings.Contains(cause.Error(), "secret") && !errors.Is(err, want) {
				t.Fatalf("lost error category: %v", err)
			}
		})
	}
}

func TestMatcherCancellationAndOverload(t *testing.T) {
	started, release := make(chan struct{}, 1), make(chan struct{})
	m := &fakeMatcher{match: func(sockowner.FlowTuple) (bool, error) { started <- struct{}{}; <-release; return true, nil }}
	b := matcherBroker(t, m)
	b.matchSlots = make(chan struct{}, 1)
	q := matchRequest(b, createMatcher(t, b, "one"))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := b.Match(ctx, q); done <- err }()
	<-started
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	// A timed-out native lookup still occupies its slot.
	if _, err := b.Match(context.Background(), q); !errors.Is(err, protocol.ErrLimitExceeded) {
		t.Fatal(err)
	}
	// A stuck lookup must not hold the native control lane.
	create(t, b, "tun", false)
	close(release)
}

func TestMatcherLateCompletion(t *testing.T) {
	for _, action := range []string{"close", "epoch", "shutdown"} {
		t.Run(action, func(t *testing.T) {
			started, release := make(chan struct{}), make(chan struct{})
			m := &fakeMatcher{match: func(sockowner.FlowTuple) (bool, error) { close(started); <-release; return true, nil }, close: func() error { close(release); return nil }}
			b := matcherBroker(t, m)
			ref := createMatcher(t, b, "one")
			done := make(chan error, 1)
			go func() { _, err := b.Match(context.Background(), matchRequest(b, ref)); done <- err }()
			<-started
			var err error
			switch action {
			case "close":
				_, err = b.CloseResource(context.Background(), protocol.ResourceRequest{Meta: meta(b, "close"), Resource: ref})
			case "epoch":
				_, err = b.ReleaseGraphEpoch(context.Background(), protocol.ReleaseGraphEpochRequest{Meta: meta(b, "epoch"), GraphEpoch: 1})
			case "shutdown":
				err = b.Shutdown(context.Background())
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := <-done; err == nil {
				t.Fatal("late result accepted")
			}
			if m.closes.Load() != 1 {
				t.Fatal("native matcher not closed")
			}
		})
	}
}

func TestMatcherCreateFailures(t *testing.T) {
	for _, mode := range []string{"unsupported", "invalid", "policy", "padded-policy", "limit", "receipt", "nil", "build", "partial", "close"} {
		t.Run(mode, func(t *testing.T) {
			m := &fakeMatcher{}
			b := matcherBroker(t, m)
			q := protocol.CreateMatcherRequest{Meta: meta(b, "create"), Rule: protocol.Rule{Type: "pid", Value: "42"}}
			switch mode {
			case "unsupported":
				b.backend = &fakeBackend{}
			case "invalid":
				q.Rule.Type = ""
			case "policy":
				b.policy.NetworkRestrictions = &policy.NetworkRestrictions{DeniedRuleTypes: []string{"pid"}}
			case "padded-policy":
				b.policy.NetworkRestrictions = &policy.NetworkRestrictions{DeniedRuleTypes: []string{"win-pid"}}
				q.Rule.Type = "\t win-pid\u00a0"
			case "limit":
				b.policy.Limits.MaxLiveResources = 0
			case "receipt":
				b.policy.Limits.MaxOperationReceipts = 0
			case "nil":
				b.backend = matcherBackend{&fakeBackend{}, func(context.Context, protocol.Rule) (sysnet.Matcher, error) { return nil, nil }}
			case "build":
				b.backend = matcherBackend{&fakeBackend{}, func(context.Context, protocol.Rule) (sysnet.Matcher, error) { return nil, os.ErrPermission }}
			case "partial", "close":
				b.backend = matcherBackend{&fakeBackend{}, func(context.Context, protocol.Rule) (sysnet.Matcher, error) { return m, os.ErrPermission }}
				if mode == "close" {
					m.close = func() error { return os.ErrPermission }
				}
			}
			r, err := b.CreateMatcher(context.Background(), q)
			if err == nil || r.Outcome == protocol.OutcomeCommitted {
				t.Fatalf("%+v %v", r, err)
			}
			if mode == "partial" && (m.closes.Load() != 1 || r.Outcome != protocol.OutcomeCompensated) {
				t.Fatal("partial creation leaked")
			}
			if mode == "close" && r.Outcome != protocol.OutcomeUncertain {
				t.Fatal("failed cleanup not reported")
			}
		})
	}
}

func TestMatcherWireAndRuleReplacement(t *testing.T) {
	b, f := testBroker(t)
	flows := make(chan sockowner.FlowTuple, 20)
	b.backend = matcherBackend{f, func(_ context.Context, rule protocol.Rule) (sysnet.Matcher, error) {
		return &fakeMatcher{match: func(flow sockowner.FlowTuple) (bool, error) { flows <- flow; return rule.Value == "yes", nil }}, nil
	}}
	c := startClient(t, b)
	for _, value := range []string{"yes", "no"} {
		m, err := c.BuildMatcher(sysnet.Rule{Type: "pid", Rule: value})
		if err != nil {
			t.Fatal(err)
		}
		for _, ip := range []string{"192.0.2.1", "2001:db8::1", "::ffff:192.0.2.1"} {
			for _, proto := range []string{"tcp", "udp"} {
				flow := sockowner.FlowTuple{Proto: proto, LocalIP: net.ParseIP(ip), LocalPort: 123, RemoteIP: net.ParseIP(ip), RemotePort: 456}
				got, err := m.Match(flow)
				if err != nil || got != (value == "yes") {
					t.Fatalf("%v %v", got, err)
				}
				observed := <-flows
				if !observed.LocalIP.Equal(flow.LocalIP) || !observed.RemoteIP.Equal(flow.RemoteIP) || observed.LocalPort != flow.LocalPort || observed.RemotePort != flow.RemotePort || observed.Proto != proto {
					t.Fatalf("direction changed: %+v", observed)
				}
			}
		}
		if err := m.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := m.Match(sockowner.FlowTuple{}); !errors.Is(err, net.ErrClosed) {
			t.Fatal(err)
		}
	}
}

func FuzzMatcherTuple(f *testing.F) {
	f.Add("tcp", "192.0.2.1:123", "192.0.2.2:443")
	f.Add("udp", "[::]:0", "[2001:db8::1]:53")
	f.Add("tcp", "[::ffff:192.0.2.1]:1", "192.0.2.2:2")
	f.Fuzz(func(t *testing.T, proto, local, remote string) {
		l, le := netip.ParseAddrPort(local)
		r, re := netip.ParseAddrPort(remote)
		q := protocol.FiveTuple{Protocol: proto, Local: l, Remote: r}
		native := sockowner.FlowTuple{Proto: proto, LocalIP: net.IP(l.Addr().AsSlice()), RemoteIP: net.IP(r.Addr().AsSlice())}
		_, familyErr := native.FlowFamily()
		valid := le == nil && re == nil && (proto == "tcp" || proto == "udp") && l.Addr().Zone() == "" && r.Addr().Zone() == "" && familyErr == nil
		if (validateTuple(q) == nil) != valid {
			t.Fatalf("validation differs from native tuple: %+v", q)
		}
	})
}

func TestMatcherDeadline(t *testing.T) {
	release := make(chan struct{})
	b := matcherBroker(t, &fakeMatcher{match: func(sockowner.FlowTuple) (bool, error) { <-release; return true, nil }})
	ref := createMatcher(t, b, "one")
	b.timeout = 20 * time.Millisecond
	_, err := b.Match(context.Background(), matchRequest(b, ref))
	close(release)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
}

func TestMatcherBackendChangesAreNotCached(t *testing.T) {
	// The adapter must invoke the backend again for the same tuple after port
	// reuse, process exit/exec, or a native cache/identity decision changes.
	var state atomic.Int32
	m := &fakeMatcher{match: func(sockowner.FlowTuple) (bool, error) {
		switch state.Load() {
		case 0:
			return true, nil
		case 1:
			return false, nil
		case 2:
			return false, sockowner.ErrNoOwner
		default:
			return false, sockowner.ErrAUW
		}
	}}
	b := matcherBroker(t, m)
	c := startClient(t, b)
	adapter, err := c.BuildMatcher(sysnet.Rule{Type: "pid", Rule: "42"})
	if err != nil {
		t.Fatal(err)
	}
	flow := sockowner.FlowTuple{Proto: "udp", LocalIP: net.IPv4(127, 0, 0, 1), LocalPort: 123, RemoteIP: net.IPv4(127, 0, 0, 1), RemotePort: 53}
	for i, wantErr := range []error{nil, nil, sockowner.ErrNoOwner, sockowner.ErrAUW, nil} {
		state.Store(int32(i % 4))
		got, err := adapter.Match(flow)
		if got != (i%4 == 0) || !errors.Is(err, wantErr) {
			t.Fatalf("state %d: %v %v", i, got, err)
		}
	}
	if m.calls.Load() != 5 {
		t.Fatal("adapter bypassed native identity checks")
	}
}

func TestMatcherCanceledCreation(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	b, f := testBroker(t)
	m := &fakeMatcher{}
	b.backend = matcherBackend{f, func(ctx context.Context, _ protocol.Rule) (sysnet.Matcher, error) {
		close(started)
		<-release
		return m, nil
	}}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = b.CreateMatcher(ctx, protocol.CreateMatcherRequest{Meta: meta(b, "cancel"), Rule: protocol.Rule{Type: "pid", Value: "42"}})
	}()
	<-started
	cancel()
	<-done
	close(release)
	// Synchronize with the mutation worker before checking its retained receipt.
	b.gate <- struct{}{}
	<-b.gate
	r, err := b.GetResult(context.Background(), protocol.OperationResultRequest{Meta: meta(b, ""), OperationID: "cancel"})
	if !errors.Is(err, context.Canceled) || r.Outcome != protocol.OutcomeCompensated || m.closes.Load() != 1 {
		t.Fatalf("%+v %v closes=%d", r, err, m.closes.Load())
	}
}

func FuzzMatcherRule(f *testing.F) {
	f.Add("pid", "42")
	f.Add("", "")
	f.Add("pid", "\x00")
	f.Add("\t forbidden\u00a0", "42")
	f.Fuzz(func(t *testing.T, kind, value string) {
		b, _ := testBroker(t)
		b.policy.NetworkRestrictions = &policy.NetworkRestrictions{DeniedRuleTypes: []string{"forbidden"}}
		err := b.validateMatcherRule(protocol.Rule{Type: kind, Value: value})
		valid := kind != "" && len(kind)+len(value) <= int(b.policy.Limits.MaxStringBytes) && utf8.ValidString(kind) && utf8.ValidString(value) && !strings.ContainsRune(kind+value, 0) && strings.TrimSpace(kind) != "forbidden"
		if (err == nil) != valid {
			t.Fatalf("rule validation mismatch: %q %q %v", kind, value, err)
		}
	})
}

func TestMatcherWireOverload(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	m := &fakeMatcher{match: func(sockowner.FlowTuple) (bool, error) { close(started); <-release; return true, nil }, close: func() error { close(release); return nil }}
	b := matcherBroker(t, m)
	b.matchSlots = make(chan struct{}, 1)
	c := startClient(t, b)
	adapter, err := c.BuildMatcher(sysnet.Rule{Type: "pid", Rule: "42"})
	if err != nil {
		t.Fatal(err)
	}
	flow := sockowner.FlowTuple{Proto: "tcp", LocalIP: net.IPv4(127, 0, 0, 1), RemoteIP: net.IPv4(127, 0, 0, 1)}
	done := make(chan error, 1)
	go func() { _, err := adapter.Match(flow); done <- err }()
	<-started
	if _, err := adapter.Match(flow); !errors.Is(err, protocol.ErrLimitExceeded) {
		t.Fatal(err)
	}
	if err := adapter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err == nil {
		t.Fatal("closed lookup returned a match")
	}
}

func TestMatcherPolicyCapabilities(t *testing.T) {
	for _, mode := range []string{"default", "rule", "padded-rule", "create", "match"} {
		t.Run(mode, func(t *testing.T) {
			b, f := testBroker(t)
			f.caps = &protocol.CapabilitySnapshot{Rules: []protocol.RuleCapability{{Type: "pid", Matchers: []protocol.MatcherCapability{{Capability: protocol.Capability{State: protocol.CapabilityUnknown, AttemptAllowed: true}}}}}}
			switch mode {
			case "rule":
				b.policy.NetworkRestrictions = &policy.NetworkRestrictions{DeniedRuleTypes: []string{"pid"}}
			case "padded-rule":
				b.policy.NetworkRestrictions = &policy.NetworkRestrictions{DeniedRuleTypes: []string{"\t pid\u00a0"}}
			case "create":
				b.policy.NetworkRestrictions = &policy.NetworkRestrictions{DeniedOperations: []protocol.Operation{protocol.OperationCreateMatcher}}
			case "match":
				b.policy.NetworkRestrictions = &policy.NetworkRestrictions{DeniedOperations: []protocol.Operation{protocol.OperationMatch}}
			}
			snapshot, err := b.Capabilities(context.Background(), protocol.ReadRequest{Meta: meta(b, "")})
			if err != nil {
				t.Fatal(err)
			}
			capability := snapshot.Rules[0].Matchers[0].Capability
			if capability.AttemptAllowed != (mode == "default") {
				t.Fatal("matcher policy not reflected in capability report")
			}
			if mode != "default" && capability.State != protocol.CapabilityUnavailable {
				t.Fatal(capability)
			}
		})
	}
}

func TestMatcherCloseWaitsForNativeLookup(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	m := &fakeMatcher{match: func(sockowner.FlowTuple) (bool, error) { close(started); <-release; return true, nil }}
	b := matcherBroker(t, m)
	tun := create(t, b, "tun", false)
	b.timeout = 30 * time.Millisecond
	ref := createMatcher(t, b, "one")
	done := make(chan error, 1)
	go func() { _, err := b.Match(context.Background(), matchRequest(b, ref)); done <- err }()
	<-started
	result, err := b.CloseResource(context.Background(), protocol.ResourceRequest{Meta: meta(b, "close"), Resource: ref})
	if result.Outcome != protocol.OutcomeUncertain || !errors.Is(err, protocol.ErrRecoveryRequired) {
		t.Fatalf("cleanup acknowledged an active native call: %+v %v", result, err)
	}
	if err := <-done; err == nil {
		t.Fatal("closed caller returned a match")
	}
	if _, err := b.Capabilities(context.Background(), protocol.ReadRequest{Meta: meta(b, "")}); err != nil {
		t.Fatalf("stuck matcher cleanup blocked native control: %v", err)
	}
	if _, err := b.CloseResource(context.Background(), protocol.ResourceRequest{Meta: meta(b, "close-tun"), Resource: tun.Resource}); err != nil {
		t.Fatalf("stuck matcher cleanup blocked device removal: %v", err)
	}
	close(release)
	b.gate <- struct{}{}
	<-b.gate
	status, err := b.Status(context.Background(), protocol.ReadRequest{Meta: meta(b, "")})
	if err != nil || !status.RecoveryRequired {
		t.Fatalf("lost uncertain cleanup: %+v %v", status, err)
	}
}
