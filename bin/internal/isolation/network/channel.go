package network

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"sync"
	"time"

	"github.com/asciimoth/almagest/bin/internal/isolation/protocol"
	"github.com/asciimoth/almagest/bin/internal/isolation/relay"
	"github.com/asciimoth/almagest/bin/internal/isolation/transport"
)

// DataIO belongs to N. Read always returns a complete packet for packet
// resources, and never exposes a native handle or control-message blob.
type DataIO interface {
	ReadData([]byte) (int, *protocol.PacketMetadata, error)
	WriteData([]byte, *protocol.PacketMetadata) (int, error)
	SetReadDeadline(time.Time) error
	SetWriteDeadline(time.Time) error
}

type channelState struct {
	readDeadline, writeDeadline     time.Time
	readDeadlineID, writeDeadlineID uint64
	mu                              sync.Mutex
	reading, writing                bool
	prepared                        uint64
	timer                           *time.Timer
}

// AttachData binds the independently authenticated data lane. It must be
// called once by bootstrap before any control request is dispatched.
func (b *Broker) AttachData(ctx context.Context, stream transport.Stream) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.data != nil || b.closed {
		return protocol.ErrInvalidContract
	}
	mux, err := relay.New(ctx, stream, b.session)
	if err != nil {
		return err
	}
	b.data = mux
	b.channels = make(map[protocol.ResourceID]*channelState)
	return nil
}

func remainingDeadline(n *int64) (time.Time, error) {
	if n == nil {
		return time.Time{}, nil
	}
	if *n < 0 {
		return time.Time{}, protocol.ErrInvalidContract
	}
	return time.Now().Add(time.Duration(*n)), nil
}

func (b *Broker) Channel(ctx context.Context, q protocol.ChannelRequest) (protocol.ChannelResult, error) {
	var result protocol.ChannelResult
	if err := b.authorize(q.Meta, false, protocol.OperationChannel); err != nil {
		return result, err
	}
	b.mu.Lock()
	if err := b.current(q.Meta); err != nil {
		b.mu.Unlock()
		return result, err
	}
	if b.data == nil {
		b.mu.Unlock()
		return result, protocol.ErrCapabilityUnavailable
	}
	if q.Kind == protocol.ChannelNegotiate {
		if q.Size != protocol.DataVersion || q.Resource != (protocol.ResourceRef{}) || q.Request != 0 || q.Remaining != nil || q.Options != (protocol.SocketSetting{}) {
			b.mu.Unlock()
			return result, protocol.ErrInvalidContract
		}
		b.dataReady = true
		b.mu.Unlock()
		return protocol.ChannelResult{Count: protocol.DataVersion}, nil
	}
	if !b.dataReady {
		b.mu.Unlock()
		return result, protocol.ErrInvalidContract
	}
	r, err := b.lookup(q.Resource)
	if err != nil {
		b.mu.Unlock()
		return result, err
	}
	if r.closing || r.state.Closed || r.state.RecoveryRequired || r.native == nil {
		b.mu.Unlock()
		return result, net.ErrClosed
	}
	if b.offers[r.ref.ID] != nil && q.Kind != protocol.ChannelClaim && q.Kind != protocol.ChannelReject {
		b.mu.Unlock()
		return result, protocol.ErrPolicyDenied
	}
	native := r.native
	state := b.channels[r.ref.ID]
	if state == nil {
		state = &channelState{}
		b.channels[r.ref.ID] = state
	}
	data := b.data
	b.mu.Unlock()
	if q.Kind == protocol.ChannelAccept || q.Kind == protocol.ChannelClaim || q.Kind == protocol.ChannelReject {
		return b.acceptControl(ctx, q, r, native)
	}
	if q.Kind == protocol.ChannelSocketSetting {
		s, ok := native.(*nativeSocket)
		if !ok {
			return result, protocol.ErrResourceType
		}
		return result, s.setting(q.Options)
	}
	if q.Kind == protocol.ChannelCloseRead || q.Kind == protocol.ChannelCloseWrite {
		s, ok := native.(*nativeSocket)
		if !ok || s.tcp == nil {
			return result, protocol.ErrResourceType
		}
		state.mu.Lock()
		busy := state.writing || state.prepared != 0
		if q.Kind == protocol.ChannelCloseWrite && busy {
			state.mu.Unlock()
			return result, protocol.ErrOperationConflict
		}
		state.mu.Unlock()
		if q.Kind == protocol.ChannelCloseRead {
			return result, s.tcp.CloseRead()
		}
		return result, s.tcp.CloseWrite()
	}
	device, ok := native.(DataIO)
	if !ok {
		return result, protocol.ErrCapabilityUnsupported
	}
	deadline, err := remainingDeadline(q.Remaining)
	if err != nil {
		return result, err
	}
	switch q.Kind {
	case protocol.ChannelSetReadDeadline, protocol.ChannelSetWriteDeadline:
		return result, setDataDeadline(state, device, q, deadline)
	case protocol.ChannelPrepareWrite, protocol.ChannelRead, protocol.ChannelWrite:
	default:
		return result, protocol.ErrUnsupportedOperation
	}
	if q.Request == 0 || q.Options != (protocol.SocketSetting{}) {
		return result, protocol.ErrInvalidContract
	}
	packet := true
	if s, ok := native.(*nativeSocket); ok {
		packet = s.tcp == nil
	}
	if q.Size > protocol.MaxPacketRecord || !packet && q.Size > protocol.MaxStreamRecord {
		return result, protocol.ErrLimitExceeded
	}
	if packet && q.Kind == protocol.ChannelRead && q.Size != protocol.MaxPacketRecord {
		return result, protocol.ErrInvalidContract
	}
	state.mu.Lock()
	switch q.Kind {
	case protocol.ChannelPrepareWrite:
		if state.writing || state.prepared != 0 {
			state.mu.Unlock()
			return result, protocol.ErrLimitExceeded
		}
		err = data.Reserve(q.Resource, q.Request, q.Size, packet)
		if err == nil {
			state.prepared = q.Request
			state.timer = time.AfterFunc(transport.FrameTimeout, func() { _ = data.Close() })
		}
		state.mu.Unlock()
		return result, err
	case protocol.ChannelWrite:
		if state.writing || state.prepared != q.Request {
			state.mu.Unlock()
			return result, protocol.ErrInvalidContract
		}
		state.writing = true
		state.prepared = 0
		state.timer.Stop()
		state.timer = nil
	case protocol.ChannelRead:
		if state.reading {
			state.mu.Unlock()
			return result, protocol.ErrLimitExceeded
		}
		state.reading = true
	}
	state.mu.Unlock()
	defer func() {
		state.mu.Lock()
		if q.Kind == protocol.ChannelRead {
			state.reading = false
		} else {
			state.writing = false
		}
		state.mu.Unlock()
	}()
	if q.Kind == protocol.ChannelRead {
		release, reserveErr := data.HoldOutbound(int(q.Size))
		if reserveErr != nil {
			return result, reserveErr
		}
		defer release()
		if t, ok := native.(*systemTun); ok {
			t.reserveData = data.HoldOutbound
		}
		if err = setDataDeadline(state, device, q, deadline); err != nil {
			return result, err
		}
		stop := cancelIO(ctx, func() { _ = device.SetReadDeadline(time.Now()) })
		defer stop()
		payload := make([]byte, q.Size)
		n, meta, err := device.ReadData(payload)
		if n < 0 || n > len(payload) {
			return result, protocol.ErrInvalidContract
		}
		b.mu.Lock()
		current, staleErr := b.lookup(q.Resource)
		if staleErr == nil && (current.closing || current.state.Closed || b.closed) {
			staleErr = net.ErrClosed
		}
		b.mu.Unlock()
		if staleErr != nil {
			return result, staleErr
		}
		result.Count = uint32(n)
		result.Code = socketError(err)
		// Zero-byte UDP is a datagram. A zero-byte TCP EOF has no data frame.
		if n > 0 || packet && err == nil {
			if sendErr := data.Send(ctx, q.Resource, q.Request, payload[:n], meta); sendErr != nil {
				return result, sendErr
			}
		}
		return result, nil
	}
	defer data.Release(q.Resource, q.Request)
	transfer, cancel := context.WithTimeout(ctx, transport.FrameTimeout)
	defer cancel()
	frame, err := data.Receive(transfer, q.Resource, q.Request)
	if err != nil {
		_ = data.Close()
		return result, err
	}
	if err = setDataDeadline(state, device, q, deadline); err != nil {
		return result, err
	}
	record := frame.Metadata.Records[0]
	if record.Packet != nil && (record.Packet.Truncated || record.Packet.OriginalLength != record.Length) {
		_ = data.Close()
		return result, protocol.ErrInvalidContract
	}
	stop := cancelIO(ctx, func() { _ = device.SetWriteDeadline(time.Now()) })
	defer stop()
	n, err := device.WriteData(frame.Payload, record.Packet)
	if n < 0 || n > len(frame.Payload) {
		return result, protocol.ErrInvalidContract
	}
	result.Count = uint32(n)
	result.Code = socketError(err)
	return result, nil
}

func dataError(err error) string {
	if err == nil {
		return ""
	}
	var ne net.Error
	switch {
	case errors.Is(err, io.EOF):
		return "eof"
	case errors.Is(err, os.ErrClosed), errors.Is(err, net.ErrClosed), errors.Is(err, io.ErrClosedPipe):
		return "closed"
	case errors.Is(err, io.ErrShortBuffer):
		return "short_buffer"
	case errors.Is(err, io.ErrShortWrite):
		return "short_write"
	case errors.As(err, &ne) && ne.Timeout():
		return "deadline"
	case errors.Is(err, protocol.ErrInvalidContract):
		return "invalid"
	default:
		return transport.ErrorCode(err)
	}
}

// Native Close must wake I/O. Do not acknowledge teardown until both workers
// have stopped; a backend that does not comply remains recovery-required.
func (b *Broker) waitData(ctx context.Context, id protocol.ResourceID) error {
	b.mu.Lock()
	state := b.channels[id]
	b.mu.Unlock()
	if state == nil {
		return nil
	}
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		state.mu.Lock()
		busy := state.reading || state.writing
		if !busy && state.timer != nil {
			state.timer.Stop()
			state.timer = nil
		}
		state.mu.Unlock()
		if !busy {
			return nil
		}
		select {
		case <-ctx.Done():
			return protocol.ErrRecoveryRequired
		case <-ticker.C:
		}
	}
}

func setDataDeadline(state *channelState, device DataIO, q protocol.ChannelRequest, deadline time.Time) error {
	state.mu.Lock()
	defer state.mu.Unlock()
	if q.Kind == protocol.ChannelSetReadDeadline {
		if q.Meta.RequestID <= state.readDeadlineID {
			return nil
		}
		state.readDeadlineID = q.Meta.RequestID
		state.readDeadline = deadline
		return device.SetReadDeadline(deadline)
	}
	if q.Kind == protocol.ChannelSetWriteDeadline {
		if q.Meta.RequestID <= state.writeDeadlineID {
			return nil
		}
		state.writeDeadlineID = q.Meta.RequestID
		state.writeDeadline = deadline
		return device.SetWriteDeadline(deadline)
	}
	if q.Kind == protocol.ChannelRead {
		if state.readDeadlineID != 0 {
			deadline = state.readDeadline
		}
		return device.SetReadDeadline(deadline)
	}
	if state.writeDeadlineID != 0 && (q.Remaining == nil || *q.Remaining != 0) {
		deadline = state.writeDeadline
	}
	return device.SetWriteDeadline(deadline)
}
func cancelIO(ctx context.Context, cancel func()) func() {
	done := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { defer close(done); cancel() })
	return func() {
		if !stop() {
			<-done
		}
	}
}
