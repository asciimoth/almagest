package network

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"math"
	"sync"
	"time"

	"github.com/asciimoth/almagest/bin/internal/isolation/policy"
	"github.com/asciimoth/almagest/bin/internal/isolation/protocol"
	"github.com/asciimoth/almagest/bin/internal/isolation/relay"
	"github.com/asciimoth/almagest/bin/internal/isolation/supervision"
	"github.com/asciimoth/almagest/bin/internal/isolation/transport"
)

const operationTimeout = 5 * time.Second

type resource struct {
	matcher *matcherResource
	closing bool
	ref     protocol.ResourceRef
	native  OwnedTun
	state   protocol.ResourceState
	options protocol.CreateTunRequest
	stop    context.CancelFunc
	dns     protocol.ResourceRef
	journal string
}

type receipt struct {
	hash   [32]byte
	result protocol.ResourceResult
	err    error
	done   chan struct{}
}

// Broker owns one authenticated session and one native mutation lane. The
// launcher must call Shutdown on peer loss, even if the peer cannot read a reply.
type Broker struct {
	matchSlots       chan struct{}
	offers           map[protocol.ResourceID]*acceptOffer
	data             *relay.Mux
	dataReady        bool
	channels         map[protocol.ResourceID]*channelState
	session          protocol.Session
	policy           policy.Policy
	backend          Backend
	journal          *supervision.Journal
	gate             chan struct{}
	mu               sync.Mutex
	epoch            uint64
	closed, recovery bool
	resources        map[protocol.ResourceID]*resource
	receipts         map[string]*receipt
	events           chan transport.Event
	stopped          chan struct{}
	stopOnce         sync.Once
	cancel           context.CancelFunc
	// timeout is fixed in production; tests shorten it for stalled backends.
	timeout time.Duration
}

func New(session protocol.Session, p policy.Policy, backend Backend) (*Broker, error) {
	return NewWithJournal(session, p, backend, nil)
}

// NewWithJournal constructs a broker which records durable native TUN
// ownership before it mutates the host. A production isolated composition must
// supply a broker-private journal. A nil journal is retained for component
// tests and cannot be used as complete-profile evidence.
func NewWithJournal(session protocol.Session, p policy.Policy, backend Backend, journal *supervision.Journal) (*Broker, error) {
	if err := session.Validate(); err != nil {
		return nil, err
	}
	digest, err := p.Digest()
	if err != nil {
		return nil, err
	}
	if digest != session.PolicyDigest || backend == nil {
		return nil, protocol.ErrInvalidContract
	}
	// Do not retain caller-owned policy slices or pointers.
	data, err := json.Marshal(p)
	if err != nil {
		return nil, err
	}
	var owned policy.Policy
	if err := json.Unmarshal(data, &owned); err != nil {
		return nil, err
	}
	return &Broker{session: session, policy: owned, backend: backend, journal: journal, gate: make(chan struct{}, 1), epoch: 1,
		matchSlots: make(chan struct{}, min(int(owned.Limits.MaxConcurrentRequests), 64)),
		resources:  make(map[protocol.ResourceID]*resource), receipts: make(map[string]*receipt),
		events: make(chan transport.Event, protocol.MaxConcurrentRequests), stopped: make(chan struct{}), timeout: operationTimeout}, nil
}

func (b *Broker) authorize(meta protocol.RequestMeta, mutation bool, op protocol.Operation) error {
	if err := meta.Validate(b.session, mutation); err != nil {
		return err
	}
	if !op.Valid() {
		return protocol.ErrUnsupportedOperation
	}
	if !b.policy.AllowsNetwork(op) {
		return protocol.ErrPolicyDenied
	}
	return nil
}

func (b *Broker) current(meta protocol.RequestMeta) error {
	if b.closed {
		return protocol.ErrCapabilityUnavailable
	}
	if meta.GraphEpoch != b.epoch {
		return protocol.ErrStaleGraphEpoch
	}
	return nil
}

func (b *Broker) lookup(ref protocol.ResourceRef) (*resource, error) {
	// Check session and closed type space before consulting the registry.
	if ref.SessionID != b.session.ID {
		return nil, protocol.ErrForeignResource
	}
	if ref.Type != protocol.ResourceTun && ref.Type != protocol.ResourceDefaultTun && ref.Type != protocol.ResourceSocket && ref.Type != protocol.ResourceListener && ref.Type != protocol.ResourceDNS && ref.Type != protocol.ResourceMatcher {
		return nil, protocol.ErrResourceType
	}
	if !ref.ID.Valid() {
		return nil, protocol.ErrInvalidContract
	}
	r := b.resources[ref.ID]
	if r == nil {
		return nil, protocol.ErrForeignResource
	}
	if err := ref.Validate(b.session, r.ref.Type, b.epoch, r.ref.Generation); err != nil {
		return nil, err
	}
	return r, nil
}

func rejected(id string, _ error) protocol.ResourceResult {
	return protocol.ResourceResult{MutationResult: protocol.MutationResult{OperationID: id, Outcome: protocol.OutcomeRejected}}
}

func uncertain(id string) protocol.ResourceResult {
	return protocol.ResourceResult{MutationResult: protocol.MutationResult{OperationID: id, Outcome: protocol.OutcomeUncertain, Code: "recovery"}}
}

func cloneResult(r protocol.ResourceResult) protocol.ResourceResult {
	r.State = cloneState(r.State)
	return r
}

func cloneState(s protocol.ResourceState) protocol.ResourceState {
	s.Capabilities = append(s.Capabilities[:0:0], s.Capabilities...)
	for i := range s.Capabilities {
		c := &s.Capabilities[i]
		c.Reasons = append(c.Reasons[:0:0], c.Reasons...)
		c.Limitations = append(c.Limitations[:0:0], c.Limitations...)
	}
	s.Addresses = append(s.Addresses[:0:0], s.Addresses...)
	s.Routes = append(s.Routes[:0:0], s.Routes...)
	s.Warnings = append(s.Warnings[:0:0], s.Warnings...)
	return s
}

func (b *Broker) stateForRuntime(s protocol.ResourceState) protocol.ResourceState {
	s = cloneState(s)
	for i := range s.Capabilities {
		entry := &s.Capabilities[i]
		if !b.policy.AllowsNetwork(entry.Operation) {
			entry.State = protocol.CapabilityUnavailable
			entry.AttemptAllowed = false
			entry.Reasons = append(entry.Reasons, "policy_denied")
		}
	}
	return s
}

// mutate retains a receipt before native work. Receipts last for this session;
// when the finite receipt budget is full, new mutations fail before effects.
// This avoids eviction followed by an accidental replay of an expired ID.
func (b *Broker) mutate(ctx context.Context, meta protocol.RequestMeta, op protocol.Operation, input any,
	work func(context.Context) (protocol.ResourceResult, error)) (protocol.ResourceResult, error) {
	if err := b.authorize(meta, true, op); err != nil {
		return rejected(meta.OperationID, err), err
	}
	data, err := json.Marshal(struct {
		Operation protocol.Operation
		Input     any
	}{op, input})
	if err != nil {
		return rejected(meta.OperationID, err), err
	}
	if len(data) > int(b.policy.Limits.MaxControlFrameBytes) {
		return rejected(meta.OperationID, protocol.ErrLimitExceeded), protocol.ErrLimitExceeded
	}
	hash := sha256.Sum256(data)
	b.mu.Lock()
	if old := b.receipts[meta.OperationID]; old != nil {
		if old.hash != hash {
			b.mu.Unlock()
			return rejected(meta.OperationID, protocol.ErrOperationConflict), protocol.ErrOperationConflict
		}
		b.mu.Unlock()
		return b.await(ctx, old)
	}
	if err := b.current(meta); err != nil {
		b.mu.Unlock()
		return rejected(meta.OperationID, err), err
	}
	if len(b.receipts) >= int(b.policy.Limits.MaxOperationReceipts) {
		b.mu.Unlock()
		return rejected(meta.OperationID, protocol.ErrLimitExceeded), protocol.ErrLimitExceeded
	}
	r := &receipt{hash: hash, result: uncertain(meta.OperationID), err: protocol.ErrRecoveryRequired, done: make(chan struct{})}
	b.receipts[meta.OperationID] = r
	b.mu.Unlock()
	bounded, cancel := context.WithTimeout(ctx, b.timeout)
	select {
	case b.gate <- struct{}{}:
	case <-bounded.Done():
		cancel()
		b.finish(r, rejected(meta.OperationID, bounded.Err()), bounded.Err())
		return b.await(ctx, r)
	}
	b.mu.Lock()
	if err := b.current(meta); err != nil {
		b.mu.Unlock()
		<-b.gate
		cancel()
		b.finish(r, rejected(meta.OperationID, err), err)
		return b.await(ctx, r)
	}
	b.cancel = cancel
	b.mu.Unlock()
	go func() {
		defer func() { b.mu.Lock(); b.cancel = nil; b.mu.Unlock(); <-b.gate; cancel() }()
		result, workErr := work(bounded)
		result.OperationID = meta.OperationID
		b.finish(r, result, workErr)
	}()
	select {
	case <-r.done:
		return b.await(ctx, r)
	case <-bounded.Done():
		b.mu.Lock()
		select {
		case <-r.done:
			result, err := cloneResult(r.result), r.err
			b.mu.Unlock()
			return result, err
		default:
		}
		b.recovery = true
		b.mu.Unlock()
		return uncertain(meta.OperationID), errors.Join(protocol.ErrRecoveryRequired, bounded.Err())
	}
}

func (b *Broker) finish(r *receipt, result protocol.ResourceResult, err error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	r.result, r.err = cloneResult(result), err
	if result.Outcome == protocol.OutcomeUncertain {
		b.recovery = true
	}
	close(r.done)
}

func (b *Broker) await(ctx context.Context, r *receipt) (protocol.ResourceResult, error) {
	ctx, cancel := context.WithTimeout(ctx, b.timeout)
	defer cancel()
	select {
	case <-r.done:
	case <-ctx.Done():
		b.mu.Lock()
		select {
		case <-r.done:
			result, err := cloneResult(r.result), r.err
			b.mu.Unlock()
			return result, err
		default:
		}
		id := r.result.OperationID
		b.mu.Unlock()
		return uncertain(id), errors.Join(protocol.ErrRecoveryRequired, ctx.Err())
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return cloneResult(r.result), r.err
}

func (b *Broker) GetResult(ctx context.Context, req protocol.OperationResultRequest) (protocol.ResourceResult, error) {
	if err := b.authorize(req.Meta, false, protocol.OperationGetOperationResult); err != nil {
		return protocol.ResourceResult{}, err
	}
	if req.OperationID == "" || len(req.OperationID) > protocol.MaxOperationIDBytes {
		return protocol.ResourceResult{}, protocol.ErrInvalidContract
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	r := b.receipts[req.OperationID]
	if r == nil {
		return protocol.ResourceResult{}, protocol.ErrOperationUnknown
	}
	return cloneResult(r.result), r.err
}

func (b *Broker) Status(ctx context.Context, req protocol.ReadRequest) (protocol.StatusResult, error) {
	if err := b.authorize(req.Meta, false, protocol.OperationStatus); err != nil {
		return protocol.StatusResult{}, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	var live uint32
	for _, r := range b.resources {
		if !r.state.Closed {
			live++
		}
	}
	return protocol.StatusResult{RecoveryRequired: b.recovery, LiveResources: live}, nil
}

// Events has bounded capacity. A slow consumer causes recovery-required state
// and revokes this session, rather than losing a terminal event silently.
func (b *Broker) Events() <-chan transport.Event { return b.events }

func (b *Broker) eventLocked(r *resource, mtu bool) {
	if r.state.Revision == math.MaxUint64 {
		b.recovery = true
		return
	}
	r.state.Revision++
	select {
	case b.events <- transport.Event{Resource: r.ref, Revision: r.state.Revision, Up: r.state.Up, Closed: r.state.Closed, MTUChanged: mtu}:
	default:
		if !b.closed {
			b.closed = true
			b.stopOnce.Do(func() { close(b.stopped) })
			b.recovery = true
			if b.cancel != nil {
				b.cancel()
			}
			go func() { _ = b.Shutdown(context.Background()) }()
		}
	}
}
