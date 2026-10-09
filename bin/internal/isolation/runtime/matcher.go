package runtime

import (
	"context"
	"net"
	"net/netip"
	"sync"

	"github.com/asciimoth/almagest/bin/internal/isolation/protocol"
	"github.com/asciimoth/gonnect/sockowner"
	"github.com/asciimoth/gonnect/sysnet"
)

// Matcher uses one read-only RPC per lookup. It adds no owner or result cache.
// Replace a rule by building a new matcher and closing the previous matcher.
type Matcher struct {
	client *Client
	ref    protocol.ResourceRef
	ctx    context.Context
	cancel context.CancelFunc
	once   sync.Once
	err    error
}

var _ sysnet.Matcher = (*Matcher)(nil)

func (c *Client) BuildMatcher(rule sysnet.Rule) (sysnet.Matcher, error) {
	result, err := c.openResource(context.Background(), protocol.OperationCreateMatcher, protocol.CreateMatcherRequest{Meta: c.meta(true), Rule: protocol.Rule{Type: rule.Type, Value: rule.Rule}})
	if err != nil {
		return nil, err
	}
	if result.Code != "" {
		return nil, decodeError(result.Code)
	}
	if result.Outcome != protocol.OutcomeCommitted {
		return nil, protocol.ErrRecoveryRequired
	}
	if err := result.Resource.Validate(c.session, protocol.ResourceMatcher, c.epoch.Load(), 1); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Matcher{client: c, ref: result.Resource, ctx: ctx, cancel: cancel}, nil
}

func (m *Matcher) Match(flow sockowner.FlowTuple) (bool, error) {
	return m.MatchContext(m.ctx, flow)
}

func (m *Matcher) MatchContext(ctx context.Context, flow sockowner.FlowTuple) (bool, error) {
	if m.ctx.Err() != nil {
		return false, net.ErrClosed
	}
	local, ok := netip.AddrFromSlice(flow.LocalIP)
	if !ok {
		return false, protocol.ErrInvalidContract
	}
	remote, ok := netip.AddrFromSlice(flow.RemoteIP)
	if !ok {
		return false, protocol.ErrInvalidContract
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(m.ctx, cancel)
	defer stop()
	meta := m.client.meta(false)
	meta.GraphEpoch = m.ref.GraphEpoch
	q := protocol.MatchRequest{Meta: meta, Matcher: m.ref, Flow: protocol.FiveTuple{Protocol: flow.Proto, Local: netip.AddrPortFrom(local, flow.LocalPort), Remote: netip.AddrPortFrom(remote, flow.RemotePort)}}
	var result protocol.MatchResult
	if err := m.client.peer.Call(ctx, protocol.OperationMatch, q, &result); err != nil {
		return false, err
	}
	if m.ctx.Err() != nil {
		return false, net.ErrClosed
	}
	return result.Matched, nil
}

func (m *Matcher) Close() error {
	m.once.Do(func() { m.cancel(); m.err = m.client.closeResource(m.ref) })
	return m.err
}
