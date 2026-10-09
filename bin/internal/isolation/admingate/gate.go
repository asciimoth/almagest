// Package admingate relays fixed privileged admin listeners to the restricted
// runtime. It transports byte streams only. HTTP, WebSocket, panel, Origin,
// and admin-protocol handling stay above this package in the runtime.
package admingate

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"github.com/asciimoth/almagest/bin/internal/isolation/protocol"
	"github.com/asciimoth/almagest/bin/internal/isolation/transport"
	"github.com/xtaci/smux"
)

const (
	MaxEndpoints   = 8
	MaxConnections = 128
	acceptDepth    = 16
	headerSize     = 8
)

var (
	helloMagic  = [8]byte{'A', 'L', 'M', 'A', 'D', 'M', 'N', 1}
	streamMagic = [4]byte{'A', 'D', 'M', 1}
)

// Endpoint is a listener that trusted bootstrap selected from protected
// configuration. IDs are fixed for the session and are not supplied by R.
type Endpoint struct {
	ID       uint16
	Listener net.Listener
}

// Broker owns the native public listeners and the broker side of the private
// relay. It never parses bytes from an accepted client.
type Broker struct {
	ctx       context.Context
	cancel    context.CancelFunc
	session   *smux.Session
	endpoints []Endpoint
	active    chan struct{}
	closeOnce sync.Once
	wg        sync.WaitGroup
}

// Runtime owns relay listeners that can be passed directly to HTTP or admin
// stream servers. It has no public listener or peer-inspection handle.
type Runtime struct {
	ctx       context.Context
	cancel    context.CancelFunc
	session   *smux.Session
	listeners map[uint16]*relayListener
	closeOnce sync.Once
	wg        sync.WaitGroup
}

// NewBroker binds one authenticated private lane to fixed public listeners.
// The stream is consumed even when setup fails.
func NewBroker(ctx context.Context, stream transport.Stream, session protocol.Session, endpoints []Endpoint) (*Broker, error) {
	if err := validateEndpoints(endpoints); err != nil {
		_ = stream.Close()
		return nil, err
	}
	if err := exchangeHello(stream, session); err != nil {
		_ = stream.Close()
		return nil, err
	}
	mux, err := smux.Client(stream, muxConfig())
	if err != nil {
		_ = stream.Close()
		return nil, err
	}
	bctx, cancel := context.WithCancel(ctx)
	b := &Broker{ctx: bctx, cancel: cancel, session: mux, endpoints: append([]Endpoint(nil), endpoints...), active: make(chan struct{}, MaxConnections)}
	b.wg.Go(b.rejectRuntimeStreams)
	for _, endpoint := range b.endpoints {
		b.wg.Go(func() { b.accept(endpoint) })
	}
	b.wg.Go(func() {
		select {
		case <-bctx.Done():
		case <-mux.CloseChan():
		}
		_ = b.Close()
	})
	return b, nil
}

// NewRuntime authenticates the broker lane and creates the fixed set of relay
// listeners. Call Listener to select the handler for each protected endpoint.
func NewRuntime(ctx context.Context, stream transport.Stream, session protocol.Session, endpointIDs []uint16) (*Runtime, error) {
	if err := validateEndpointIDs(endpointIDs); err != nil {
		_ = stream.Close()
		return nil, err
	}
	if err := exchangeHello(stream, session); err != nil {
		_ = stream.Close()
		return nil, err
	}
	mux, err := smux.Server(stream, muxConfig())
	if err != nil {
		_ = stream.Close()
		return nil, err
	}
	rctx, cancel := context.WithCancel(ctx)
	r := &Runtime{ctx: rctx, cancel: cancel, session: mux, listeners: make(map[uint16]*relayListener, len(endpointIDs))}
	for _, id := range endpointIDs {
		r.listeners[id] = &relayListener{id: id, runtime: r, accepted: make(chan net.Conn, acceptDepth), closed: make(chan struct{})}
	}
	r.wg.Go(r.accept)
	r.wg.Go(func() {
		select {
		case <-rctx.Done():
		case <-mux.CloseChan():
		}
		_ = r.Close()
	})
	return r, nil
}

func muxConfig() *smux.Config {
	return &smux.Config{
		Version:           2,
		KeepAliveDisabled: true,
		MaxFrameSize:      32 * 1024,
		MaxReceiveBuffer:  8 * 1024 * 1024,
		MaxStreamBuffer:   256 * 1024,
	}
}

func validateEndpoints(endpoints []Endpoint) error {
	if len(endpoints) == 0 || len(endpoints) > MaxEndpoints {
		return protocol.ErrLimitExceeded
	}
	ids := make([]uint16, len(endpoints))
	for i, endpoint := range endpoints {
		if endpoint.Listener == nil {
			return protocol.ErrInvalidContract
		}
		ids[i] = endpoint.ID
	}
	return validateEndpointIDs(ids)
}

func validateEndpointIDs(ids []uint16) error {
	if len(ids) == 0 || len(ids) > MaxEndpoints {
		return protocol.ErrLimitExceeded
	}
	seen := make(map[uint16]struct{}, len(ids))
	for _, id := range ids {
		if id == 0 {
			return protocol.ErrInvalidContract
		}
		if _, exists := seen[id]; exists {
			return protocol.ErrInvalidContract
		}
		seen[id] = struct{}{}
	}
	return nil
}

func exchangeHello(stream transport.Stream, session protocol.Session) error {
	if stream == nil {
		return protocol.ErrInvalidContract
	}
	if err := session.Validate(); err != nil {
		return err
	}
	local := encodeHello(session)
	deadline := time.Now().Add(transport.FrameTimeout)
	if err := stream.SetReadDeadline(deadline); err != nil {
		return err
	}
	if err := stream.SetWriteDeadline(deadline); err != nil {
		return err
	}
	writeDone := make(chan error, 1)
	go func() { writeDone <- writeFull(stream, local[:]) }()
	var remote [68]byte
	_, readErr := io.ReadFull(stream, remote[:])
	writeErr := <-writeDone
	_ = stream.SetReadDeadline(time.Time{})
	_ = stream.SetWriteDeadline(time.Time{})
	if writeErr != nil {
		return writeErr
	}
	if readErr != nil {
		return readErr
	}
	return validateHello(remote[:], session)
}

func encodeHello(session protocol.Session) [68]byte {
	var hello [68]byte
	copy(hello[:8], helloMagic[:])
	copy(hello[8:24], session.ID[:])
	binary.BigEndian.PutUint64(hello[24:32], session.Epoch)
	binary.BigEndian.PutUint32(hello[32:36], session.Version)
	copy(hello[36:], session.PolicyDigest[:])
	return hello
}

func validateHello(data []byte, session protocol.Session) error {
	if len(data) != 68 {
		return protocol.ErrInvalidContract
	}
	want := encodeHello(session)
	if !bytes.Equal(data, want[:]) {
		return protocol.ErrForeignResource
	}
	return nil
}

func writeFull(writer io.Writer, data []byte) error {
	for len(data) != 0 {
		n, err := writer.Write(data)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrNoProgress
		}
		data = data[n:]
	}
	return nil
}

func encodeStreamHeader(endpoint uint16) [headerSize]byte {
	var header [headerSize]byte
	copy(header[:4], streamMagic[:])
	binary.BigEndian.PutUint16(header[4:6], endpoint)
	return header
}

func decodeStreamHeader(header []byte) (uint16, error) {
	if len(header) != headerSize || !bytes.Equal(header[:4], streamMagic[:]) || header[6] != 0 || header[7] != 0 {
		return 0, protocol.ErrInvalidContract
	}
	id := binary.BigEndian.Uint16(header[4:6])
	if id == 0 {
		return 0, protocol.ErrInvalidContract
	}
	return id, nil
}

func (b *Broker) accept(endpoint Endpoint) {
	for {
		conn, err := endpoint.Listener.Accept()
		if err != nil {
			if b.ctx.Err() == nil && !errors.Is(err, net.ErrClosed) {
				b.cancel()
			}
			return
		}
		select {
		case b.active <- struct{}{}:
			b.wg.Go(func() {
				defer func() { <-b.active }()
				b.forward(endpoint.ID, conn)
			})
		default:
			_ = conn.Close()
		}
	}
}

func (b *Broker) forward(endpoint uint16, conn net.Conn) {
	defer func() { _ = conn.Close() }()
	stream, err := b.session.OpenStream()
	if err != nil {
		return
	}
	defer func() { _ = stream.Close() }()
	_ = stream.SetWriteDeadline(time.Now().Add(transport.FrameTimeout))
	header := encodeStreamHeader(endpoint)
	if err = writeFull(stream, header[:]); err != nil {
		return
	}
	_ = stream.SetWriteDeadline(time.Time{})

	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(stream, conn); done <- struct{}{} }()
	go func() { _, _ = io.Copy(conn, stream); done <- struct{}{} }()
	select {
	case <-b.ctx.Done():
	case <-done:
	}
}

func (b *Broker) rejectRuntimeStreams() {
	for {
		stream, err := b.session.AcceptStream()
		if err != nil {
			b.cancel()
			return
		}
		_ = stream.Close()
	}
}

func (b *Broker) Close() error {
	b.closeOnce.Do(func() {
		b.cancel()
		for _, endpoint := range b.endpoints {
			_ = endpoint.Listener.Close()
		}
		_ = b.session.Close()
	})
	return nil
}

func (b *Broker) Wait() { b.wg.Wait() }

func (r *Runtime) accept() {
	for {
		stream, err := r.session.AcceptStream()
		if err != nil {
			r.cancel()
			return
		}
		_ = stream.SetReadDeadline(time.Now().Add(transport.FrameTimeout))
		var header [headerSize]byte
		if _, err = io.ReadFull(stream, header[:]); err != nil {
			_ = stream.Close()
			continue
		}
		_ = stream.SetReadDeadline(time.Time{})
		id, err := decodeStreamHeader(header[:])
		listener := r.listeners[id]
		if err != nil || listener == nil {
			_ = stream.Close()
			continue
		}
		listener.mu.Lock()
		if listener.isClosed {
			listener.mu.Unlock()
			_ = stream.Close()
			continue
		}
		select {
		case listener.accepted <- stream:
		default:
			_ = stream.Close()
		}
		listener.mu.Unlock()
	}
}

func (r *Runtime) Listener(id uint16) (net.Listener, error) {
	listener := r.listeners[id]
	if listener == nil {
		return nil, fmt.Errorf("%w: admin endpoint %d", protocol.ErrInvalidContract, id)
	}
	return listener, nil
}

func (r *Runtime) Close() error {
	r.closeOnce.Do(func() {
		r.cancel()
		for _, listener := range r.listeners {
			_ = listener.Close()
		}
		_ = r.session.Close()
	})
	return nil
}

func (r *Runtime) Wait() { r.wg.Wait() }

type relayListener struct {
	id        uint16
	runtime   *Runtime
	accepted  chan net.Conn
	closed    chan struct{}
	closeOnce sync.Once
	mu        sync.Mutex
	isClosed  bool
}

func (l *relayListener) Accept() (net.Conn, error) {
	select {
	case conn := <-l.accepted:
		return conn, nil
	case <-l.closed:
		return nil, net.ErrClosed
	case <-l.runtime.ctx.Done():
		return nil, net.ErrClosed
	}
}

func (l *relayListener) Close() error {
	l.closeOnce.Do(func() {
		l.mu.Lock()
		defer l.mu.Unlock()
		l.isClosed = true
		close(l.closed)
		for {
			select {
			case conn := <-l.accepted:
				_ = conn.Close()
			default:
				return
			}
		}
	})
	return nil
}

func (l *relayListener) Addr() net.Addr { return relayAddress(l.id) }

type relayAddress uint16

func (relayAddress) Network() string  { return "almagest-admin-relay" }
func (a relayAddress) String() string { return fmt.Sprintf("endpoint-%d", uint16(a)) }
