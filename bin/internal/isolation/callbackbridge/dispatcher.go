package callbackbridge

import (
	"context"
	"fmt"
	"maps"
	"sync"
	"sync/atomic"

	"github.com/asciimoth/almagest/bin/internal/isolation/protocol"
	"github.com/asciimoth/almagest/bin/internal/isolation/transport"
)

const callbackQueueDepth = 64

type request struct {
	id, event string
	slots     map[string]string
}

// Dispatcher owns N's authenticated, callback-only channel to H.
type Dispatcher struct {
	peer    *transport.Peer
	session protocol.Session
	queue   chan request
	ctx     context.Context
	cancel  context.CancelFunc
	next    atomic.Uint64
	closed  atomic.Bool
	once    sync.Once
	wg      sync.WaitGroup
}

func New(parent context.Context, peer *transport.Peer, session protocol.Session) (*Dispatcher, error) {
	if parent == nil || peer == nil || session.Validate() != nil {
		return nil, protocol.ErrInvalidContract
	}
	ctx, cancel := context.WithCancel(parent)
	d := &Dispatcher{peer: peer, session: session, queue: make(chan request, callbackQueueDepth), ctx: ctx, cancel: cancel}
	d.wg.Go(d.run)
	return d, nil
}

func (d *Dispatcher) Submit(id, event string, slots map[string]string) bool {
	if d == nil || d.closed.Load() {
		return false
	}
	request := request{id: id, event: event, slots: maps.Clone(slots)}
	select {
	case d.queue <- request:
		return true
	case <-d.ctx.Done():
		return false
	default:
		return false
	}
}

func (d *Dispatcher) run() {
	defer d.wg.Done()
	for {
		select {
		case <-d.ctx.Done():
			return
		case request := <-d.queue:
			operation := fmt.Sprintf("backend-callback-%d", d.next.Add(1))
			var result protocol.CommandResult
			_ = d.peer.CallSettled(d.ctx, protocol.OperationRunCallback, protocol.CallbackRequest{
				Meta: protocol.RequestMeta{GraphEpoch: 1, OperationID: operation}, CallbackID: request.id,
				Event: request.event, Slots: request.slots,
			}, &result)
		}
	}
}

func (d *Dispatcher) Close() {
	if d == nil {
		return
	}
	d.once.Do(func() {
		d.closed.Store(true)
		d.cancel()
		_ = d.peer.Close()
	})
	d.wg.Wait()
}
