package network

import (
	"context"
	"net"
	"time"

	"github.com/asciimoth/almagest/bin/internal/isolation/protocol"
)

type acceptOffer struct {
	parent  protocol.ResourceID
	expires time.Time
	timer   *time.Timer
}

func (b *Broker) acceptControl(ctx context.Context, q protocol.ChannelRequest, r *resource, native OwnedTun) (protocol.ChannelResult, error) {
	var result protocol.ChannelResult
	if q.Kind != protocol.ChannelAccept {
		b.mu.Lock()
		offer := b.offers[r.ref.ID]
		if offer == nil || !time.Now().Before(offer.expires) {
			b.mu.Unlock()
			return result, protocol.ErrStaleGeneration
		}
		if q.Kind == protocol.ChannelClaim {
			offer.timer.Stop()
			delete(b.offers, r.ref.ID)
			b.mu.Unlock()
			return result, nil
		}
		b.mu.Unlock()
		return result, b.expireOffer(r.ref.ID)
	}
	s, ok := native.(*nativeSocket)
	if !ok || s.listener == nil {
		return result, protocol.ErrResourceType
	}
	b.mu.Lock()
	cs := b.channels[r.ref.ID]
	b.mu.Unlock()
	cs.mu.Lock()
	if cs.reading {
		cs.mu.Unlock()
		return result, protocol.ErrLimitExceeded
	}
	cs.reading = true
	cs.mu.Unlock()
	defer func() { cs.mu.Lock(); cs.reading = false; cs.mu.Unlock() }()
	deadline, err := remainingDeadline(q.Remaining)
	if err != nil {
		return result, err
	}
	deadlineRequest := q
	deadlineRequest.Kind = protocol.ChannelRead
	if err = setDataDeadline(cs, s, deadlineRequest, deadline); err != nil {
		return result, err
	}
	stop := cancelIO(ctx, func() { _ = s.listener.SetDeadline(time.Now()) })
	conn, err := s.listener.AcceptTCP()
	stop()
	if err != nil {
		// Gonnect may return a typed nil connection with an accept error.
		return result, err
	}
	if err = ctx.Err(); err != nil {
		_ = conn.Close()
		return result, err
	}
	owned := &nativeSocket{tcp: conn, network: s.network}
	state, err := owned.State(ctx)
	if err != nil {
		_ = conn.Close()
		return result, err
	}
	id, err := protocol.NewResourceID()
	if err != nil {
		_ = conn.Close()
		return result, err
	}
	b.mu.Lock()
	if err = ctx.Err(); err == nil {
		err = b.current(q.Meta)
	}
	if err == nil && (r.closing || r.state.Closed || b.resources[id] != nil) {
		err = net.ErrClosed
	}
	if err == nil {
		err = b.socketCapacity(protocol.ResourceSocket)
	}
	pending := 0
	for _, o := range b.offers {
		if o.parent == r.ref.ID {
			pending++
		}
	}
	if err == nil && (len(b.offers) >= 128 || pending >= 16) {
		err = protocol.ErrLimitExceeded
	}
	if err != nil {
		b.mu.Unlock()
		_ = conn.Close()
		return result, err
	}
	child := &resource{ref: protocol.ResourceRef{ID: id, SessionID: b.session.ID, Type: protocol.ResourceSocket, Generation: 1, GraphEpoch: q.Meta.GraphEpoch}, native: owned, state: state}
	if b.offers == nil {
		b.offers = make(map[protocol.ResourceID]*acceptOffer)
	}
	offer := &acceptOffer{parent: r.ref.ID, expires: time.Now().Add(5 * time.Second)}
	b.resources[id] = child
	b.offers[id] = offer
	offer.timer = time.AfterFunc(5*time.Second, func() { _ = b.expireOffer(id) })
	b.mu.Unlock()
	return protocol.ChannelResult{Resource: child.ref, State: state}, nil
}

func (b *Broker) expireOffer(id protocol.ResourceID) error {
	ctx, cancel := context.WithTimeout(context.Background(), b.timeout)
	defer cancel()
	select {
	case b.gate <- struct{}{}:
	case <-ctx.Done():
		b.mu.Lock()
		b.recovery = true
		if r := b.resources[id]; r != nil {
			r.state.RecoveryRequired = true
		}
		b.mu.Unlock()
		return protocol.ErrRecoveryRequired
	}
	defer func() { <-b.gate }()
	b.mu.Lock()
	offer := b.offers[id]
	r := b.resources[id]
	if offer != nil {
		offer.timer.Stop()
		delete(b.offers, id)
	}
	b.mu.Unlock()
	if offer == nil || r == nil {
		return nil
	}
	return b.closeNative(ctx, r)
}
