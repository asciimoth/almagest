package diagnostics

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"github.com/asciimoth/almagest/bin/internal/isolation/protocol"
	"github.com/asciimoth/almagest/bin/internal/isolation/transport"
)

const MaxFrameBytes = 8 * 1024

// StreamSink writes bounded strict frames on the dedicated diagnostic lane.
type StreamSink struct {
	stream transport.Stream
	mu     sync.Mutex
}

func NewStreamSink(stream transport.Stream) (*StreamSink, error) {
	if stream == nil {
		return nil, protocol.ErrInvalidContract
	}
	return &StreamSink{stream: stream}, nil
}

func (s *StreamSink) WritePayload(ctx context.Context, p Payload) error {
	frame, err := Encode(p)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	deadline := time.Now().Add(transport.FrameTimeout)
	if caller, ok := ctx.Deadline(); ok && caller.Before(deadline) {
		deadline = caller
	}
	if err := s.stream.SetWriteDeadline(deadline); err != nil {
		return err
	}
	for len(frame) != 0 {
		n, err := s.stream.Write(frame)
		if err != nil {
			return err
		}
		if n <= 0 || n > len(frame) {
			return io.ErrShortWrite
		}
		frame = frame[n:]
	}
	return nil
}

func Encode(p Payload) ([]byte, error) {
	if err := p.validate(); err != nil {
		return nil, err
	}
	body, err := json.Marshal(p)
	if err != nil {
		return nil, err
	}
	if len(body)+4 > MaxFrameBytes {
		return nil, protocol.ErrLimitExceeded
	}
	frame := make([]byte, len(body)+4)
	binary.BigEndian.PutUint32(frame, uint32(len(body)))
	copy(frame[4:], body)
	return frame, nil
}

func ReadPayload(stream transport.Stream) (Payload, error) {
	var p Payload
	if stream == nil {
		return p, protocol.ErrInvalidContract
	}
	if err := stream.SetReadDeadline(time.Time{}); err != nil {
		return p, err
	}
	var prefix [4]byte
	if _, err := io.ReadFull(stream, prefix[:1]); err != nil {
		return p, err
	}
	if err := stream.SetReadDeadline(time.Now().Add(transport.FrameTimeout)); err != nil {
		return p, err
	}
	if _, err := io.ReadFull(stream, prefix[1:]); err != nil {
		return p, err
	}
	size := binary.BigEndian.Uint32(prefix[:])
	if size == 0 || size > MaxFrameBytes-4 {
		return p, protocol.ErrLimitExceeded
	}
	body := make([]byte, size)
	if _, err := io.ReadFull(stream, body); err != nil {
		return p, err
	}
	return decodeBody(body)
}

// DecodeFrame decodes exactly one frame. It is used by bootstrap tests and
// fuzzers; stream readers use the same strict body decoder.
func DecodeFrame(frame []byte) (Payload, error) {
	if len(frame) < 4 || len(frame) > MaxFrameBytes {
		return Payload{}, protocol.ErrLimitExceeded
	}
	size := binary.BigEndian.Uint32(frame)
	if size == 0 || uint64(size) != uint64(len(frame)-4) {
		return Payload{}, protocol.ErrInvalidContract
	}
	return decodeBody(bytes.Clone(frame[4:]))
}

func decodeBody(body []byte) (Payload, error) {
	var p Payload
	if err := transport.Decode(body, &p); err != nil {
		return p, err
	}
	return p, p.validateWire()
}

type CollectorStats struct {
	Received      uint64
	Malformed     uint64
	SequenceGaps  uint64
	ReportedDrops uint64
}

// Collector binds untrusted payloads to one trusted process origin. A record
// is delivered directly to its sink and is never sent back to an emitter.
type Collector struct {
	origin    Origin
	sink      RecordSink
	redact    *Redactor
	mu        sync.Mutex
	last      uint64
	received  atomic.Uint64
	malformed atomic.Uint64
	gaps      atomic.Uint64
	reported  atomic.Uint64
}

func NewCollector(origin Origin, sink RecordSink, secrets ...string) (*Collector, error) {
	if err := origin.validate(); err != nil || sink == nil {
		return nil, protocol.ErrInvalidContract
	}
	return &Collector{origin: origin, sink: sink, redact: NewRedactor(secrets...)}, nil
}

func (c *Collector) Collect(ctx context.Context, p Payload) error {
	if err := p.validateWire(); err != nil {
		c.malformed.Add(1)
		return err
	}
	c.mu.Lock()
	if p.Sequence <= c.last {
		c.mu.Unlock()
		c.malformed.Add(1)
		return protocol.ErrInvalidContract
	}
	gap := uint64(0)
	if c.last != 0 && p.Sequence > c.last+1 {
		gap = p.Sequence - c.last - 1
	}
	c.last = p.Sequence
	c.mu.Unlock()
	c.gaps.Add(gap)
	c.reported.Add(p.DroppedBefore)
	if gap > p.DroppedBefore {
		p.DroppedBefore = gap
	}
	// Repeat the safety boundary at the trusted receiver. A compromised child
	// can bypass all local logger checks.
	p.Source = Sanitize(p.Source, protocol.MaxDiagnosticSourceBytes)
	p.Message = Sanitize(c.redact.String(Sanitize(p.Message, protocol.MaxDiagnosticMessageBytes)), protocol.MaxDiagnosticMessageBytes)
	p.OperationID = Sanitize(p.OperationID, protocol.MaxOperationIDBytes)
	p.Decision = Sanitize(p.Decision, protocol.MaxDiagnosticSourceBytes)
	if err := p.validate(); err != nil {
		c.malformed.Add(1)
		return err
	}
	record := p.record(c.origin)
	if err := record.Validate(); err != nil {
		c.malformed.Add(1)
		return err
	}
	if err := c.sink.WriteRecord(ctx, record); err != nil {
		return err
	}
	c.received.Add(1)
	return nil
}

func (c *Collector) Run(ctx context.Context, stream transport.Stream) error {
	if stream == nil {
		return protocol.ErrInvalidContract
	}
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			_ = stream.Close()
		case <-done:
		}
	}()
	for {
		p, err := ReadPayload(stream)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			c.malformed.Add(1)
			return err
		}
		if err := c.Collect(ctx, p); err != nil {
			return err
		}
	}
}

func (c *Collector) Stats() CollectorStats {
	return CollectorStats{Received: c.received.Load(), Malformed: c.malformed.Load(), SequenceGaps: c.gaps.Load(), ReportedDrops: c.reported.Load()}
}
