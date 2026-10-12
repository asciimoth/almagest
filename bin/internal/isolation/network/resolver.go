package network

import (
	"context"
	"io"
	"net"
	"os"
	"sync"
	"time"

	"github.com/asciimoth/almagest/bin/internal/isolation/protocol"
	gdns "github.com/asciimoth/gonnect/dns"
	"github.com/asciimoth/gonnect/tun"
)

// resolverSocket owns no provider and no host socket. One request and one
// response can be retained per channel. Pending response capacity is charged
// to the same session budget as all other packet data, before native work.
type resolverSocket struct {
	provider                    gdns.Interface
	closeProvider               bool
	reserve                     func(int) (func(), error)
	mu                          sync.Mutex
	changed                     chan struct{}
	closed, writing             bool
	readDeadline, writeDeadline time.Time
	pending                     []byte
	release                     func()
	cancel                      context.CancelFunc
}

func newResolverSocket(provider gdns.Interface) *resolverSocket {
	return &resolverSocket{provider: provider, changed: make(chan struct{})}
}

func newOwnedResolverSocket(provider gdns.Interface) *resolverSocket {
	return &resolverSocket{provider: provider, closeProvider: true, changed: make(chan struct{})}
}

func (s *resolverSocket) wake()                            { close(s.changed); s.changed = make(chan struct{}) }
func (s *resolverSocket) Verify(ctx context.Context) error { return ctx.Err() }
func (*resolverSocket) Events() <-chan tun.Event           { return nil }
func (*resolverSocket) Capabilities(context.Context) (protocol.CapabilitySnapshot, error) {
	return protocol.CapabilitySnapshot{SchemaVersion: 1, Revision: 1}, nil
}
func (*resolverSocket) Update(context.Context, protocol.UpdateTunRequest) error {
	return protocol.ErrCapabilityUnsupported
}
func (s *resolverSocket) State(context.Context) (protocol.ResourceState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return protocol.ResourceState{Revision: 1, Up: !s.closed, Closed: s.closed}, nil
}
func (s *resolverSocket) Close(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	if s.cancel != nil {
		s.cancel()
	}
	if s.release != nil {
		s.release()
		s.release = nil
	}
	s.pending = nil
	s.wake()
	if s.closeProvider {
		return s.provider.Close()
	}
	return nil
}
func (s *resolverSocket) SetReadDeadline(deadline time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return net.ErrClosed
	}
	s.readDeadline = deadline
	s.wake()
	return nil
}
func (s *resolverSocket) SetWriteDeadline(deadline time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return net.ErrClosed
	}
	s.writeDeadline = deadline
	s.wake()
	return nil
}

func waitResolverChange(ctx context.Context, changed <-chan struct{}, deadline time.Time) error {
	if deadline.IsZero() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-changed:
			return nil
		}
	}
	timer := time.NewTimer(time.Until(deadline))
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-changed:
		return nil
	case <-timer.C:
		return os.ErrDeadlineExceeded
	}
}

func (s *resolverSocket) ReadData(p []byte) (int, *protocol.PacketMetadata, error) {
	for {
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			return 0, nil, net.ErrClosed
		}
		deadline, changed := s.readDeadline, s.changed
		if !deadline.IsZero() && !time.Now().Before(deadline) {
			s.mu.Unlock()
			return 0, nil, os.ErrDeadlineExceeded
		}
		if s.pending != nil {
			n := copy(p, s.pending)
			meta := &protocol.PacketMetadata{OriginalLength: uint32(len(s.pending)), Truncated: n < len(s.pending)}
			s.pending = nil
			s.release()
			s.release = nil
			s.mu.Unlock()
			if meta.Truncated {
				return n, meta, io.ErrShortBuffer
			}
			return n, meta, nil
		}
		s.mu.Unlock()
		// Recheck the current deadline after any wake, including an old timer.
		_ = waitResolverChange(context.Background(), changed, deadline)
	}
}

func decodeResolverRequest(p []byte, meta *protocol.PacketMetadata) (*gdns.Message, error) {
	if len(p) < 12 || len(p) > protocol.MaxPacketRecord || meta == nil || *meta != (protocol.PacketMetadata{OriginalLength: uint32(len(p))}) {
		return nil, protocol.ErrInvalidContract
	}
	query, err := gdns.Unpack(p)
	if err != nil || query.Response {
		return nil, protocol.ErrInvalidContract
	}
	return query, nil
}

func (s *resolverSocket) WriteData(p []byte, meta *protocol.PacketMetadata) (int, error) {
	query, err := decodeResolverRequest(p, meta)
	if err != nil {
		return 0, err
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return 0, net.ErrClosed
	}
	if s.writing || s.pending != nil {
		s.mu.Unlock()
		return 0, protocol.ErrOperationConflict
	}
	if s.reserve == nil {
		s.mu.Unlock()
		return 0, protocol.ErrCapabilityUnavailable
	}
	if !s.writeDeadline.IsZero() && !time.Now().Before(s.writeDeadline) {
		s.mu.Unlock()
		return 0, os.ErrDeadlineExceeded
	}
	release, err := s.reserve(protocol.MaxPacketRecord)
	if err != nil {
		s.mu.Unlock()
		return 0, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), operationTimeout)
	s.writing, s.cancel = true, cancel
	s.mu.Unlock()
	defer cancel()
	// Deadline updates and close must cancel a native query, not just its RPC
	// caller. This one watcher is bounded by the broker's data-worker ceiling.
	watchDone := make(chan struct{})
	go func() {
		defer close(watchDone)
		for {
			s.mu.Lock()
			deadline, changed := s.writeDeadline, s.changed
			expired := !deadline.IsZero() && !time.Now().Before(deadline)
			s.mu.Unlock()
			if expired {
				cancel()
				return
			}
			_ = waitResolverChange(ctx, changed, deadline)
			if ctx.Err() != nil {
				return
			}
		}
	}()
	response, err := gdns.Query(ctx, s.provider, query)
	var wire []byte
	if err == nil && response == nil {
		err = protocol.ErrInvalidContract
	}
	if err == nil {
		wire, err = gdns.Pack(response)
	}
	cancel()
	<-watchDone
	s.mu.Lock()
	defer s.mu.Unlock()
	s.writing, s.cancel = false, nil
	if s.closed {
		err = net.ErrClosed
	}
	if err != nil && !s.closed && !s.writeDeadline.IsZero() && !time.Now().Before(s.writeDeadline) {
		err = os.ErrDeadlineExceeded
	}
	if err == nil && (len(wire) < 12 || len(wire) > protocol.MaxPacketRecord) {
		err = protocol.ErrLimitExceeded
	}
	if err != nil {
		release()
		return 0, err
	}
	s.pending, s.release = wire, release
	s.wake()
	return len(p), nil
}
