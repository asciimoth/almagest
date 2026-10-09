package network

import (
	"context"
	"net"
	"slices"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/asciimoth/almagest/bin/internal/isolation/protocol"
	"github.com/asciimoth/almagest/bin/internal/isolation/transport"
	"github.com/asciimoth/gonnect/sockowner"
	"github.com/asciimoth/gonnect/sysnet"
)

// MatcherBackend keeps native owner lookup and process identity checks in N.
// Rules are immutable: replacement uses CreateMatcher and CloseResource.
type MatcherBackend interface {
	BuildMatcher(context.Context, protocol.Rule) (sysnet.Matcher, error)
}

type matcherResource struct {
	native    sysnet.Matcher
	ctx       context.Context
	active    sync.WaitGroup // Add is protected by Broker.mu before closing is set.
	closeOnce sync.Once
	closed    chan struct{}
	closeErr  error // Published by closing closed.
}

func (s *SystemBackend) BuildMatcher(ctx context.Context, rule protocol.Rule) (sysnet.Matcher, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m, err := s.system.BuildMatcher(sysnet.Rule{Type: rule.Type, Rule: rule.Value})
	return m, nativeError(err)
}

func (b *Broker) validateMatcherRule(rule protocol.Rule) error {
	value := rule.Type + rule.Value
	if rule.Type == "" || len(value) > int(b.policy.Limits.MaxStringBytes) || !utf8.ValidString(rule.Type) || !utf8.ValidString(rule.Value) || strings.ContainsRune(value, 0) {
		return protocol.ErrInvalidContract
	}
	if b.matcherRuleDenied(rule.Type) {
		return protocol.ErrPolicyDenied
	}
	return nil
}

func (b *Broker) matcherRuleDenied(kind string) bool {
	// Windows trims rule type whitespace before native validation. Apply the
	// same spelling to policy checks so padding cannot bypass a denied type.
	p := b.policy.NetworkRestrictions
	return p != nil && slices.ContainsFunc(p.DeniedRuleTypes, func(denied string) bool {
		return strings.TrimSpace(denied) == strings.TrimSpace(kind)
	})
}

func (b *Broker) CreateMatcher(ctx context.Context, req protocol.CreateMatcherRequest) (protocol.ResourceResult, error) {
	input := req
	input.Meta.RequestID = 0
	return b.mutate(ctx, req.Meta, protocol.OperationCreateMatcher, input, func(ctx context.Context) (protocol.ResourceResult, error) {
		if err := b.validateMatcherRule(req.Rule); err != nil {
			return rejected(req.Meta.OperationID, err), err
		}
		backend, ok := b.backend.(MatcherBackend)
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
		m, err := backend.BuildMatcher(ctx, req.Rule)
		if m == nil {
			if err == nil {
				err = protocol.ErrCapabilityUnavailable
			}
			return rejected(req.Meta.OperationID, err), transport.ErrorFromCode(transport.ErrorCode(err))
		}
		parent, stop := context.WithCancel(context.Background())
		r := &resource{ref: protocol.ResourceRef{ID: id, SessionID: b.session.ID, Type: protocol.ResourceMatcher, Generation: 1, GraphEpoch: req.Meta.GraphEpoch}, matcher: &matcherResource{native: m, ctx: parent, closed: make(chan struct{})}, stop: stop}
		b.mu.Lock()
		b.resources[id] = r
		currentErr := b.current(req.Meta)
		if err == nil && ctx.Err() == nil && currentErr == nil {
			r.state.Up = true
			b.eventLocked(r, false)
			b.mu.Unlock()
			return b.result(r, protocol.OutcomeCommitted), nil
		}
		b.mu.Unlock()
		cleanup, cancel := context.WithTimeout(context.Background(), b.timeout)
		defer cancel()
		if closeErr := b.closeNative(cleanup, r); closeErr != nil {
			return b.failed(r, req.Meta.OperationID, closeErr)
		}
		if err == nil {
			err = ctx.Err()
		}
		if err == nil {
			err = currentErr
		}
		return b.result(r, protocol.OutcomeCompensated), transport.ErrorFromCode(transport.ErrorCode(err))
	})
}

func validateTuple(flow protocol.FiveTuple) error {
	if flow.Protocol != "tcp" && flow.Protocol != "udp" {
		return protocol.ErrInvalidContract
	}
	if !flow.Local.IsValid() || !flow.Remote.IsValid() || flow.Local.Addr().Zone() != "" || flow.Remote.Addr().Zone() != "" || flow.Local.Addr().Unmap().BitLen() != flow.Remote.Addr().Unmap().BitLen() {
		return protocol.ErrInvalidContract
	}
	return nil
}

// Match does not use the mutation lane or retain a result cache. A native call
// that ignores cancellation retains its slot until it ends. Thus timeouts
// cannot create an unbounded number of native workers.
func (b *Broker) Match(ctx context.Context, req protocol.MatchRequest) (protocol.MatchResult, error) {
	var zero protocol.MatchResult
	if err := b.authorize(req.Meta, false, protocol.OperationMatch); err != nil {
		return zero, err
	}
	if req.Matcher.Type != protocol.ResourceMatcher {
		return zero, protocol.ErrResourceType
	}
	if err := validateTuple(req.Flow); err != nil {
		return zero, err
	}
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	b.mu.Lock()
	err := b.current(req.Meta)
	var r *resource
	if err == nil {
		r, err = b.lookup(req.Matcher)
	}
	if err == nil && (r.closing || r.state.Closed || r.matcher == nil) {
		err = net.ErrClosed
	}
	if err != nil {
		b.mu.Unlock()
		return zero, err
	}
	select {
	case b.matchSlots <- struct{}{}:
	default:
		b.mu.Unlock()
		return zero, protocol.ErrLimitExceeded
	}
	m := r.matcher
	m.active.Add(1)
	b.mu.Unlock()
	bounded, cancel := context.WithTimeout(ctx, b.timeout)
	defer cancel()
	stop := context.AfterFunc(m.ctx, cancel)
	defer stop()
	done := make(chan readResult[bool], 1)
	go func() {
		defer func() { m.active.Done(); <-b.matchSlots }()
		if err := bounded.Err(); err != nil {
			done <- readResult[bool]{err: err}
			return
		}
		flow := req.Flow
		matched, err := m.native.Match(sockowner.FlowTuple{Proto: flow.Protocol, LocalIP: net.IP(flow.Local.Addr().AsSlice()), LocalPort: flow.Local.Port(), RemoteIP: net.IP(flow.Remote.Addr().AsSlice()), RemotePort: flow.Remote.Port()})
		done <- readResult[bool]{matched, err}
	}()
	select {
	case <-bounded.Done():
		return zero, bounded.Err()
	case result := <-done:
		b.mu.Lock()
		err := b.current(req.Meta)
		if err == nil {
			_, err = b.lookup(req.Matcher)
		}
		if err == nil && (r.closing || r.state.Closed) {
			err = net.ErrClosed
		}
		b.mu.Unlock()
		if err != nil {
			return zero, err
		}
		if err = bounded.Err(); err != nil {
			return zero, err
		}
		if result.err != nil {
			return zero, transport.ErrorFromCode(transport.ErrorCode(nativeError(result.err)))
		}
		// The native Matcher interface returns no per-lookup quality.
		return protocol.MatchResult{Matched: result.value, Quality: string(sysnet.MatchQualityUnknown)}, nil
	}
}

func (b *Broker) closeMatcher(ctx context.Context, r *resource) error {
	m := r.matcher
	if m == nil {
		return protocol.ErrRecoveryRequired
	}
	// One cleanup worker per owned matcher. A stuck native Close or lookup
	// cannot retain the shared mutation lane after this caller's deadline.
	// Retries wait on the same worker; they never repeat a native close.
	m.closeOnce.Do(func() {
		go func() {
			m.closeErr = m.native.Close()
			if m.closeErr == nil {
				m.active.Wait()
			}
			close(m.closed)
		}()
	})
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-m.closed:
		if m.closeErr != nil {
			return m.closeErr
		}
	}
	b.mu.Lock()
	r.state.Closed = true
	r.state.RecoveryRequired = false
	r.matcher = nil
	r.stop = nil
	b.eventLocked(r, false)
	b.mu.Unlock()
	return nil
}
