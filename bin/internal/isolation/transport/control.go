package transport

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"sync"
	"syscall"
	"time"

	"github.com/asciimoth/almagest/bin/internal/isolation/protocol"
	"github.com/asciimoth/gonnect/sockowner"
)

// WireError sends only a closed code, never native error strings or paths.
type WireError struct {
	Code string `json:"code"`
}

var errorCodes = map[string]error{
	"no_owner": sockowner.ErrNoOwner, "ambiguous_owner": sockowner.ErrAUW, "ambiguous_exact_owner": sockowner.ErrAEx,
	"invalid": protocol.ErrInvalidContract, "version": protocol.ErrUnsupportedVersion,
	"operation": protocol.ErrUnsupportedOperation, "unsupported": protocol.ErrCapabilityUnsupported,
	"capability_unknown": protocol.ErrCapabilityUnknown,
	"unavailable":        protocol.ErrCapabilityUnavailable, "denied": protocol.ErrPolicyDenied,
	"exhausted": protocol.ErrAllocationExhausted,
	"limit":     protocol.ErrLimitExceeded, "foreign": protocol.ErrForeignResource,
	"type": protocol.ErrResourceType, "generation": protocol.ErrStaleGeneration,
	"epoch": protocol.ErrStaleGraphEpoch, "conflict": protocol.ErrOperationConflict,
	"unknown": protocol.ErrOperationUnknown, "recovery": protocol.ErrRecoveryRequired,
	"canceled": context.Canceled, "deadline": context.DeadlineExceeded,
	"refused": syscall.ECONNREFUSED, "address_in_use": syscall.EADDRINUSE,
	"unreachable": syscall.ENETUNREACH, "host_unreachable": syscall.EHOSTUNREACH,
	"permission": os.ErrPermission, "short_buffer": io.ErrShortBuffer,
	"short_write": io.ErrShortWrite, "reset": syscall.ECONNRESET,
	"eof": io.EOF, "closed": net.ErrClosed, "timeout": os.ErrDeadlineExceeded,
	"internal": errors.New("isolation owner failed"),
}

func (e *WireError) Error() string { return "isolation control: " + e.Code }
func (e *WireError) Unwrap() error { return errorCodes[e.Code] }
func wireError(err error) *WireError {
	if err == nil {
		return nil
	}
	for code, target := range errorCodes {
		if errors.Is(err, target) {
			return &WireError{code}
		}
	}
	return &WireError{"internal"}
}

type hello struct {
	Version    uint32           `json:"version"`
	Session    protocol.Session `json:"session"`
	FrameBytes uint32           `json:"frameBytes"`
	Depth      uint16           `json:"depth"`
	InFlight   uint16           `json:"inFlight"`
}

type message struct {
	Kind      string             `json:"kind"`
	ID        uint64             `json:"id"`
	Operation protocol.Operation `json:"operation,omitempty"`
	Body      json.RawMessage    `json:"body,omitempty"`
	Error     *WireError         `json:"error,omitempty"`
}

// Handler must decode into the operation's concrete protocol type with Decode,
// validate authority before side effects, and honor context cancellation. It
// must not use an error as proof that an in-progress mutation had no effect.
// It is called by at most 128 workers. A canceled worker retains its slot until
// it returns, so an owner that ignores cancellation cannot grow worker count.
type Handler func(context.Context, protocol.Operation, json.RawMessage) (any, error)

// Event is a coalescible resource-state notification, not an RPC completion.
type Event struct {
	Resource   protocol.ResourceRef `json:"resource"`
	Revision   uint64               `json:"revision"`
	Up         bool                 `json:"up,omitempty"`
	Closed     bool                 `json:"closed,omitempty"`
	MTUChanged bool                 `json:"mtuChanged,omitempty"`
}

func (e Event) valid(session protocol.Session) bool {
	if e.Revision == 0 || !e.Resource.ID.Valid() || e.Resource.SessionID != session.ID || e.Resource.Generation == 0 || e.Resource.GraphEpoch == 0 {
		return false
	}
	switch e.Resource.Type {
	case protocol.ResourceAllocation, protocol.ResourceTun, protocol.ResourceDefaultTun, protocol.ResourceSocket, protocol.ResourceListener, protocol.ResourceMatcher, protocol.ResourceDNS:
		return true
	default:
		return false
	}
}

type Peer struct {
	lanes                                    Lanes
	session                                  protocol.Session
	handler                                  Handler
	ctx                                      context.Context
	cancel                                   context.CancelFunc
	once                                     sync.Once
	mu                                       sync.Mutex
	err                                      error
	next, received, eventSent, eventReceived uint64
	pending                                  map[uint64]chan message
	active                                   map[uint64]context.CancelFunc
	out                                      chan queued
	ordinary, terminal                       chan struct{}
	events                                   chan Event
	configs                                  chan ConfigTransfer
	assembly                                 ConfigAssembler
	configTimer                              *time.Timer
	configSend                               chan struct{}
}

type queued struct {
	data     []byte
	terminal bool
}

// New negotiates version 1 on a private authenticated control endpoint. Session
// and policy digest come from trusted bootstrap, not from peer-selected values.
// The lanes must already be bound to the expected final process. Each endpoint
// is owned by Peer even if negotiation fails. Context bounds the session life.
func New(ctx context.Context, lanes Lanes, session protocol.Session, handler Handler) (*Peer, error) {
	if lanes.Data == nil || lanes.Diagnostic == nil {
		lanes.Close()
		return nil, protocol.ErrInvalidContract
	}
	return newPeer(ctx, lanes, session, handler)
}

// NewControl constructs a peer for C/H channels which carry typed control and
// bounded config chunks only. It does not invent unused data or diagnostic
// streams, and it owns the supplied control endpoint.
func NewControl(ctx context.Context, control Stream, session protocol.Session, handler Handler) (*Peer, error) {
	return newPeer(ctx, Lanes{Control: control}, session, handler)
}

func newPeer(ctx context.Context, lanes Lanes, session protocol.Session, handler Handler) (*Peer, error) {
	if err := session.Validate(); err != nil {
		lanes.Close()
		return nil, err
	}
	if lanes.Control == nil {
		lanes.Close()
		return nil, protocol.ErrInvalidContract
	}
	negotiation, cancel := context.WithTimeout(ctx, FrameTimeout)
	stop := context.AfterFunc(negotiation, lanes.Close)
	h := hello{protocol.Version, session, protocol.MaxControlFrameBytes, protocol.MaxControlDepth, protocol.MaxConcurrentRequests}
	data, err := encode(h)
	if err != nil {
		stop()
		cancel()
		lanes.Close()
		return nil, err
	}
	written := make(chan error, 1)
	go func() { written <- writeFrame(lanes.Control, data) }()
	var remote hello
	err = ReadFrame(lanes.Control, &remote)
	if err != nil {
		lanes.Close()
	}
	writeErr := <-written
	if err == nil {
		err = writeErr
	}
	if err == nil && remote.Version != protocol.Version {
		err = protocol.ErrUnsupportedVersion
	}
	if err == nil && remote != h {
		err = protocol.ErrInvalidContract
	}
	if !stop() && err == nil {
		err = negotiation.Err()
	}
	cancel()
	if err != nil {
		lanes.Close()
		return nil, err
	}
	pctx, pcancel := context.WithCancel(ctx)
	p := &Peer{lanes: lanes, session: session, handler: handler, ctx: pctx, cancel: pcancel,
		pending: make(map[uint64]chan message), active: make(map[uint64]context.CancelFunc),
		out: make(chan queued, 2*protocol.MaxConcurrentRequests), ordinary: make(chan struct{}, protocol.MaxConcurrentRequests), terminal: make(chan struct{}, protocol.MaxConcurrentRequests), events: make(chan Event, protocol.MaxConcurrentRequests), configs: make(chan ConfigTransfer, 1), configSend: make(chan struct{}, 1)}
	go func() { <-pctx.Done(); p.fail(pctx.Err()) }()
	go p.read()
	go p.write()
	return p, nil
}

func (p *Peer) Close() error          { p.fail(io.ErrClosedPipe); return nil }
func (p *Peer) Done() <-chan struct{} { return p.ctx.Done() }
func (p *Peer) Events() <-chan Event  { return p.events }
func (p *Peer) Err() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.err != nil {
		return p.err
	}
	return p.ctx.Err()
}

func (p *Peer) fail(err error) {
	p.once.Do(func() {
		p.mu.Lock()
		p.err = err
		if p.configTimer != nil {
			p.configTimer.Stop()
		}
		p.assembly.Reset()
		for _, cancel := range p.active {
			cancel()
		}
		p.mu.Unlock()
		p.cancel()
		p.lanes.Close()
	})
}

// queue never waits on a slow peer. Terminal capacity is reserved separately;
// exhaustion fails the session and wakes all calls, even if R cannot read.
func (p *Peer) queue(m message, terminal bool) error {
	data, err := encode(m)
	if err != nil {
		return err
	}
	return p.enqueue(data, terminal)
}

func (p *Peer) enqueue(data []byte, terminal bool) error {
	q := p.ordinary
	if terminal {
		q = p.terminal
	}
	select {
	case <-p.ctx.Done():
		return io.ErrClosedPipe
	case q <- struct{}{}:
		p.out <- queued{data, terminal}
		return nil
	default:
		return protocol.ErrLimitExceeded
	}
}

// Call does not replay requests. Local cancellation returns promptly; the
// pending slot remains occupied until the terminal reply or session failure.
// This bounds abandoned requests without an unbounded late-reply cache.
func (p *Peer) Call(ctx context.Context, op protocol.Operation, request, result any) error {
	if !op.Valid() || op == protocol.OperationHandshake {
		return protocol.ErrUnsupportedOperation
	}
	return p.call(ctx, "request", op, request, result)
}

// CallSettled sends cancellation but retains the operation until its terminal
// result or peer loss. Data adapters use it to release late-frame capacity.
func (p *Peer) CallSettled(ctx context.Context, op protocol.Operation, request, result any) error {
	if !op.Valid() || op == protocol.OperationHandshake {
		return protocol.ErrUnsupportedOperation
	}
	return p.callMode(ctx, "request", op, request, result, true)
}
func (p *Peer) call(ctx context.Context, kind string, op protocol.Operation, request, result any) error {
	return p.callMode(ctx, kind, op, request, result, false)
}
func (p *Peer) callMode(ctx context.Context, kind string, op protocol.Operation, request, result any, settled bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	var (
		body     []byte
		prepared any
		mutation bool
		err      error
	)
	if kind == "request" {
		prepared, mutation, err = prepareRequest(op, request)
	} else {
		body, err = json.Marshal(request)
	}
	if err != nil {
		return protocol.ErrInvalidContract
	}
	p.mu.Lock()
	if p.err != nil {
		err = p.err
		p.mu.Unlock()
		return err
	}
	bulk := op == protocol.OperationMatch
	if op == protocol.OperationChannel {
		if q, ok := prepared.(*protocol.ChannelRequest); ok {
			switch q.Kind {
			case protocol.ChannelRead, protocol.ChannelPrepareWrite, protocol.ChannelWrite, protocol.ChannelAccept:
				bulk = true
			}
		} else {
			bulk = dataCall(body)
		}
	}
	if len(p.pending) >= protocol.MaxConcurrentRequests || (bulk && len(p.pending) >= 96) || p.next == ^uint64(0) {
		p.mu.Unlock()
		return protocol.ErrLimitExceeded
	}
	id := p.next + 1
	if kind == "request" {
		body, err = marshalPreparedRequest(prepared, mutation, id, p.session)
		if err != nil {
			p.mu.Unlock()
			return err
		}
	}
	data, err := encode(message{Kind: kind, ID: id, Operation: op, Body: body})
	if err != nil {
		p.mu.Unlock()
		return err
	}
	ch := make(chan message, 1)
	p.pending[id] = ch
	// Serialize assignment and enqueue so IDs arrive strictly in order.
	err = p.enqueue(data, false)
	if err != nil {
		delete(p.pending, id)
		p.mu.Unlock()
		return err
	}
	p.next = id
	p.mu.Unlock()
	select {
	case m := <-ch:
		if m.Error != nil {
			return m.Error
		}
		return Decode(m.Body, result)
	case <-ctx.Done():
		if err := p.queue(message{Kind: "cancel", ID: id}, true); err != nil {
			p.fail(err)
		}
		if settled {
			select {
			case m := <-ch:
				if m.Error != nil {
					return m.Error
				}
				return Decode(m.Body, result)
			case <-p.ctx.Done():
				return p.Err()
			}
		}
		return ctx.Err()
	case <-p.ctx.Done():
		return p.Err()
	}
}

func (p *Peer) Notify(e Event) error {
	if !e.valid(p.session) {
		return protocol.ErrInvalidContract
	}
	body, err := json.Marshal(e)
	if err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.eventSent == ^uint64(0) {
		return protocol.ErrLimitExceeded
	}
	err = p.queue(message{Kind: "event", ID: p.eventSent + 1, Body: body}, false)
	if err == nil {
		p.eventSent++
	}
	return err
}

func (p *Peer) write() {
	for {
		var item queued
		select {
		case <-p.ctx.Done():
			return
		case item = <-p.out:
		}
		if err := writeFrame(p.lanes.Control, item.data); err != nil {
			p.fail(err)
			return
		}
		if item.terminal {
			<-p.terminal
		} else {
			<-p.ordinary
		}
	}
}

func (p *Peer) read() {
	defer close(p.events)
	defer close(p.configs)
	for {
		var m message
		if err := ReadFrame(p.lanes.Control, &m); err != nil {
			p.fail(err)
			return
		}
		if err := p.receive(m); err != nil {
			p.fail(err)
			return
		}
	}
}

func (p *Peer) receive(m message) error {
	if m.ID == 0 {
		return protocol.ErrInvalidContract
	}
	switch m.Kind {
	case "config":
		return p.receiveConfig(m)
	case "request":
		if !m.Operation.Valid() || m.Operation == protocol.OperationHandshake || m.Error != nil || len(m.Body) == 0 {
			return protocol.ErrInvalidContract
		}
		if _, err := validateRequest(m.Operation, m.Body, m.ID, p.session, false); err != nil {
			return err
		}
		p.mu.Lock()
		if m.ID != p.received+1 || p.received == ^uint64(0) || len(p.active) >= protocol.MaxConcurrentRequests {
			p.mu.Unlock()
			return protocol.ErrLimitExceeded
		}
		p.received = m.ID
		// Data and matcher operations leave capacity for status and close.
		// Reject excess work before starting a native worker.
		if bulkCall(m.Operation, m.Body) && len(p.active) >= 96 {
			p.mu.Unlock()
			return p.queue(message{Kind: "reply", ID: m.ID, Error: wireError(protocol.ErrLimitExceeded)}, true)
		}
		ctx, cancel := context.WithCancel(p.ctx)
		p.active[m.ID] = cancel
		p.mu.Unlock()
		go p.handle(ctx, m)
	case "cancel":
		if m.Operation != "" || m.Error != nil || len(m.Body) != 0 {
			return protocol.ErrInvalidContract
		}
		p.mu.Lock()
		if m.ID > p.received {
			p.mu.Unlock()
			return protocol.ErrInvalidContract
		}
		if cancel := p.active[m.ID]; cancel != nil {
			cancel()
		}
		p.mu.Unlock()
	case "reply":
		if m.Operation != "" || (m.Error == nil && len(m.Body) == 0) || (m.Error != nil && (errorCodes[m.Error.Code] == nil || len(m.Body) != 0)) {
			return protocol.ErrInvalidContract
		}
		p.mu.Lock()
		ch := p.pending[m.ID]
		delete(p.pending, m.ID)
		p.mu.Unlock()
		if ch == nil {
			return protocol.ErrInvalidContract
		}
		ch <- m
	case "event":
		if m.Operation != "" || m.Error != nil || m.ID != p.eventReceived+1 || p.eventReceived == ^uint64(0) {
			return protocol.ErrInvalidContract
		}
		var e Event
		if err := Decode(m.Body, &e); err != nil {
			return err
		}
		if !e.valid(p.session) {
			return protocol.ErrInvalidContract
		}
		p.eventReceived = m.ID
		select {
		case p.events <- e:
		default:
			return protocol.ErrLimitExceeded
		}
	default:
		return protocol.ErrInvalidContract
	}
	return nil
}

func (p *Peer) handle(ctx context.Context, m message) {
	var value any
	err := error(protocol.ErrUnsupportedOperation)
	if p.handler != nil {
		value, err = p.handler(ctx, m.Operation, m.Body)
	}
	reply := message{Kind: "reply", ID: m.ID, Error: wireError(err)}
	if err == nil {
		reply.Body, err = json.Marshal(value)
		if err != nil {
			reply.Body = nil
			reply.Error = wireError(err)
		}
	}
	p.mu.Lock()
	p.active[m.ID]()
	delete(p.active, m.ID)
	p.mu.Unlock()
	if err := p.queue(reply, true); err != nil {
		p.fail(err)
	}
}

func bulkCall(op protocol.Operation, body []byte) bool {
	return op == protocol.OperationMatch || (op == protocol.OperationChannel && dataCall(body))
}

func dataCall(body []byte) bool {
	var q protocol.ChannelRequest
	if err := Decode(body, &q); err != nil {
		return true
	}
	switch q.Kind {
	case protocol.ChannelRead, protocol.ChannelPrepareWrite, protocol.ChannelWrite, protocol.ChannelAccept:
		return true
	default:
		return false
	}
}

// ErrorCode and ErrorFromCode share the closed error set with data results.
// Native strings, paths, and endpoint details never cross the boundary.
func ErrorCode(err error) string {
	if err == nil {
		return ""
	}
	return wireError(err).Code
}
func ErrorFromCode(code string) error {
	if code == "" {
		return nil
	}
	if errorCodes[code] == nil {
		return protocol.ErrInvalidContract
	}
	return &WireError{Code: code}
}
