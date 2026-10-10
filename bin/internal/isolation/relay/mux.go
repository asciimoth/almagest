package relay

import (
	"context"
	"errors"
	"io"
	"sync"
	"time"

	"github.com/asciimoth/almagest/bin/internal/isolation/protocol"
	"github.com/asciimoth/almagest/bin/internal/isolation/transport"
)

type delivery struct {
	frame Frame
	err   error
}
type slot struct {
	ref       protocol.ResourceRef
	request   uint64
	size      uint32
	packet    bool
	ch        chan delivery
	delivered bool
}
type sequence struct{ sent, received, request uint64 }
type outgoing struct {
	bytes []byte
	done  chan error
}

// Mux reserves memory before granting credit. A resource has at most one receive
// slot and one queued send. Native work never runs on its reader or writer.
// The FIFO ready queue visits each ready resource once before a second frame
// from that resource. A stalled writer has a fixed five-second limit.
type Mux struct {
	stream                 transport.Stream
	session                protocol.Session
	ctx                    context.Context
	cancel                 context.CancelFunc
	mu                     sync.Mutex
	err                    error
	slots                  map[protocol.ResourceID]*slot
	sequences              map[protocol.ResourceRef]*sequence
	sending                map[protocol.ResourceID]bool
	reserved, queued, held int
	out                    chan outgoing
}

func New(ctx context.Context, s transport.Stream, session protocol.Session) (*Mux, error) {
	if s == nil {
		return nil, protocol.ErrInvalidContract
	}
	if err := session.Validate(); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(ctx)
	m := &Mux{stream: s, session: session, ctx: ctx, cancel: cancel, slots: make(map[protocol.ResourceID]*slot), sequences: make(map[protocol.ResourceRef]*sequence), sending: make(map[protocol.ResourceID]bool), out: make(chan outgoing, protocol.MaxConcurrentRequests)}
	go m.read()
	go m.write()
	go func() { <-ctx.Done(); _ = m.Close() }()
	return m, nil
}
func (m *Mux) Done() <-chan struct{} { return m.ctx.Done() }
func (m *Mux) Err() error            { m.mu.Lock(); defer m.mu.Unlock(); return m.err }
func (m *Mux) Close() error          { m.fail(io.ErrClosedPipe); return nil }
func (m *Mux) fail(err error) {
	m.mu.Lock()
	if m.err != nil {
		m.mu.Unlock()
		return
	}
	m.err = err
	m.cancel()
	m.mu.Unlock()
	_ = m.stream.Close()
}
func (m *Mux) state(ref protocol.ResourceRef) (*sequence, error) {
	if ref.SessionID != m.session.ID {
		return nil, protocol.ErrForeignResource
	}
	if !ref.ID.Valid() || ref.Generation == 0 || ref.GraphEpoch == 0 {
		return nil, protocol.ErrInvalidContract
	}
	switch ref.Type {
	case protocol.ResourceSocket, protocol.ResourceTun, protocol.ResourceDefaultTun, protocol.ResourceDNS:
	default:
		return nil, protocol.ErrResourceType
	}
	s := m.sequences[ref]
	if s == nil {
		if len(m.sequences) >= protocol.MaxOperationReceipts {
			return nil, protocol.ErrLimitExceeded
		}
		s = &sequence{}
		m.sequences[ref] = s
	}
	return s, nil
}

// Reserve is called only after resource authorization. Release must follow the
// terminal control result, so a late delivery cannot consume a newer slot.
func (m *Mux) Reserve(ref protocol.ResourceRef, request uint64, size uint32, packet bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return m.err
	}
	s, err := m.state(ref)
	if err != nil {
		return err
	}
	if request == 0 || request <= s.request {
		return protocol.ErrInvalidContract
	}
	if size > protocol.MaxPacketRecord || !packet && size > protocol.MaxStreamRecord {
		return protocol.ErrLimitExceeded
	}
	if m.slots[ref.ID] != nil || len(m.slots) >= protocol.MaxConcurrentRequests || m.reserved+protocol.MaxDataFrame > protocol.MaxSessionData {
		return protocol.ErrLimitExceeded
	}
	m.slots[ref.ID] = &slot{ref: ref, request: request, size: size, packet: packet, ch: make(chan delivery, 1)}
	s.request = request
	m.reserved += protocol.MaxDataFrame
	return nil
}
func (m *Mux) Release(ref protocol.ResourceRef, request uint64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s := m.slots[ref.ID]; s != nil && s.ref == ref && s.request == request {
		delete(m.slots, ref.ID)
		m.reserved -= protocol.MaxDataFrame
	}
}
func (m *Mux) Receive(ctx context.Context, ref protocol.ResourceRef, request uint64) (Frame, error) {
	m.mu.Lock()
	s := m.slots[ref.ID]
	m.mu.Unlock()
	if s == nil || s.ref != ref || s.request != request {
		return Frame{}, protocol.ErrInvalidContract
	}
	select {
	case d := <-s.ch:
		return d.frame, d.err
	case <-ctx.Done():
		return Frame{}, ctx.Err()
	case <-m.ctx.Done():
		return Frame{}, m.Err()
	}
}
func (m *Mux) Send(ctx context.Context, ref protocol.ResourceRef, request uint64, payload []byte, packet *protocol.PacketMetadata) error {
	m.mu.Lock()
	if m.err != nil {
		err := m.err
		m.mu.Unlock()
		return err
	}
	s, err := m.state(ref)
	if err != nil {
		m.mu.Unlock()
		return err
	}
	if s.sent == ^uint64(0) || m.sending[ref.ID] || m.queued+m.held+protocol.MaxDataFrame > protocol.MaxSessionData {
		m.mu.Unlock()
		return protocol.ErrLimitExceeded
	}
	f := Frame{Metadata: protocol.DataMetadata{Version: protocol.DataVersion, Session: m.session.ID.String(), SessionEpoch: m.session.Epoch, Resource: ref.ID.String(), Generation: ref.Generation, GraphEpoch: ref.GraphEpoch, Sequence: s.sent + 1, Records: []protocol.DataRecord{{Request: request, Length: uint32(len(payload)), Packet: packet}}}, Payload: payload}
	data, err := Encode(f)
	if err != nil {
		m.mu.Unlock()
		return err
	}
	q := outgoing{data, make(chan error, 1)}
	select {
	case m.out <- q:
	default:
		m.mu.Unlock()
		return protocol.ErrLimitExceeded
	}
	s.sent++
	m.sending[ref.ID] = true
	m.queued += protocol.MaxDataFrame
	m.mu.Unlock()
	// Keep queue accounting until the writer consumes this frame, including on
	// caller cancellation. The writer always completes or fails the session.
	select {
	case err = <-q.done:
	case <-m.ctx.Done():
		err = m.Err()
	}
	m.mu.Lock()
	delete(m.sending, ref.ID)
	m.queued -= protocol.MaxDataFrame
	m.mu.Unlock()
	return err
}
func (m *Mux) read() {
	for {
		f, err := ReadFrame(m.stream)
		if err != nil {
			m.fail(err)
			return
		}
		if err = m.deliver(f); err != nil {
			if errors.Is(err, protocol.ErrStaleGeneration) {
				// Lifecycle errors belong to the matching control operation.
				// A late old-generation record cannot consume the new credit.
				continue
			}
			m.fail(err)
			return
		}
	}
}
func (m *Mux) deliver(f Frame) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	meta := f.Metadata
	if meta.Session != m.session.ID.String() || meta.SessionEpoch != m.session.Epoch {
		return protocol.ErrForeignResource
	}
	id, err := protocol.ParseResourceID(meta.Resource)
	if err != nil {
		return err
	}
	slot := m.slots[id]
	if slot == nil || slot.delivered || len(meta.Records) != 1 {
		return protocol.ErrInvalidContract
	}
	if slot.ref.Generation != meta.Generation || slot.ref.GraphEpoch != meta.GraphEpoch {
		if meta.GraphEpoch < slot.ref.GraphEpoch || meta.GraphEpoch == slot.ref.GraphEpoch && meta.Generation < slot.ref.Generation {
			return protocol.ErrStaleGeneration
		}
		return protocol.ErrInvalidContract
	}
	seq := m.sequences[slot.ref]
	if seq.received == ^uint64(0) || meta.Sequence != seq.received+1 {
		return protocol.ErrInvalidContract
	}
	rec := meta.Records[0]
	if rec.Request != slot.request || rec.Length > slot.size || (rec.Packet != nil) != slot.packet {
		return protocol.ErrInvalidContract
	}
	if slot.ref.Type == protocol.ResourceTun || slot.ref.Type == protocol.ResourceDefaultTun {
		p := rec.Packet
		if p == nil || rec.Length == 0 || p.Source != nil || p.Destination != nil || p.InterfaceIndex != nil || p.HopLimit != nil || p.TrafficClass != nil || p.DNS != nil {
			return protocol.ErrInvalidContract
		}
	}
	seq.received++
	slot.delivered = true
	slot.ch <- delivery{frame: f}
	return nil
}
func (m *Mux) write() {
	for {
		select {
		case <-m.ctx.Done():
			return
		case q := <-m.out:
			err := m.stream.SetWriteDeadline(time.Now().Add(transport.FrameTimeout))
			data := q.bytes
			for err == nil && len(data) > 0 {
				var n int
				n, err = m.stream.Write(data)
				if n == 0 && err == nil {
					err = io.ErrNoProgress
				}
				data = data[n:]
			}
			q.done <- err
			if err != nil {
				m.fail(err)
				return
			}
		}
	}
}

// HoldOutbound accounts native read scratch and retained batch buffers against
// the same outbound ceiling as encoded frames. Release must follow the last
// native use, including cancellation and partial batches.
func (m *Mux) HoldOutbound(bytes int) (func(), error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return nil, m.err
	}
	if bytes < 0 || bytes > 256*1024 || m.queued+m.held+bytes > protocol.MaxSessionData {
		return nil, protocol.ErrLimitExceeded
	}
	m.held += bytes
	var once sync.Once
	return func() { once.Do(func() { m.mu.Lock(); m.held -= bytes; m.mu.Unlock() }) }, nil
}
