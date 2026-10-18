// Package runtime provides relay adapters without host-network constructors.
// It is additive and is not linked into the direct daemon composition.
package runtime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/asciimoth/almagest/bin/internal/isolation/protocol"
	"github.com/asciimoth/almagest/bin/internal/isolation/relay"
	"github.com/asciimoth/almagest/bin/internal/isolation/transport"
	gdns "github.com/asciimoth/gonnect/dns"
	"github.com/asciimoth/gonnect/tun"
)

type Client struct {
	allocator *Allocator
	opens     chan struct{}
	emptyDNS  gdns.Interface
	peer      *transport.Peer
	data      *relay.Mux
	session   protocol.Session
	next      atomic.Uint64
	epoch     atomic.Uint64
	mu        sync.Mutex
	tuns      map[protocol.ResourceID]*Tun
}

func New(ctx context.Context, lanes transport.Lanes, session protocol.Session) (*Client, error) {
	data, err := relay.New(ctx, lanes.Data, session)
	if err != nil {
		lanes.Close()
		return nil, err
	}
	peer, err := transport.New(ctx, lanes, session, nil)
	if err != nil {
		_ = data.Close()
		return nil, err
	}
	c := &Client{peer: peer, data: data, session: session, tuns: make(map[protocol.ResourceID]*Tun), emptyDNS: gdns.NewClient(nil, nil, nil), opens: make(chan struct{}, protocol.MaxConcurrentRequests)}
	c.epoch.Store(1)
	q := protocol.ChannelRequest{Meta: c.meta(false), Kind: protocol.ChannelNegotiate, Size: protocol.DataVersion}
	var result protocol.ChannelResult
	if err = peer.Call(ctx, protocol.OperationChannel, q, &result); err != nil || result.Count != protocol.DataVersion {
		_ = c.Close()
		return nil, errors.Join(err, protocol.ErrUnsupportedVersion)
	}
	go func() {
		select {
		case <-data.Done():
		case <-peer.Done():
		}
		_ = c.Close()
	}()
	go c.events()
	return c, nil
}
func (c *Client) Close() error {
	if c.emptyDNS != nil {
		_ = c.emptyDNS.Close()
	}
	_ = c.data.Close()
	return c.peer.Close()
}

func (c *Client) Done() <-chan struct{} { return c.peer.Done() }
func (c *Client) Err() error            { return c.peer.Err() }
func (c *Client) meta(mutation bool) protocol.RequestMeta {
	m := protocol.RequestMeta{GraphEpoch: c.epoch.Load()}
	if mutation {
		m.OperationID = fmt.Sprintf("runtime-%d", c.next.Add(1))
	}
	return m
}
func (c *Client) channel(ctx context.Context, q protocol.ChannelRequest) (protocol.ChannelResult, error) {
	q.Meta = c.meta(false)
	q.Meta.GraphEpoch = q.Resource.GraphEpoch
	var result protocol.ChannelResult
	err := c.peer.CallSettled(ctx, protocol.OperationChannel, q, &result)
	if err == nil {
		err = decodeError(result.Code)
	}
	return result, err
}
func decodeError(code string) error {
	switch code {
	case "dns_no_upstream":
		return gdns.ErrNoUpstream
	case "":
		return nil
	case "eof":
		return io.EOF
	case "closed":
		return net.ErrClosed
	case "deadline":
		return os.ErrDeadlineExceeded
	case "canceled":
		return context.Canceled
	case "short_buffer":
		return io.ErrShortBuffer
	case "short_write":
		return io.ErrShortWrite
	case "unsupported":
		return protocol.ErrCapabilityUnsupported
	case "invalid":
		return protocol.ErrInvalidContract
	default:
		return transport.ErrorFromCode(code)
	}
}
func remaining(t time.Time) *int64 {
	if t.IsZero() {
		return nil
	}
	n := max(int64(time.Until(t)), 0)
	return &n
}
func (c *Client) closeResource(ref protocol.ResourceRef) error {
	ctx, cancel := context.WithTimeout(context.Background(), systemOperationTimeout)
	defer cancel()
	var result protocol.MutationResult
	m := c.meta(true)
	m.GraphEpoch = ref.GraphEpoch
	err := c.peer.Call(ctx, protocol.OperationCloseResource, protocol.ResourceRequest{Meta: m, Resource: ref}, &result)
	if err != nil {
		return err
	}
	if result.Outcome != protocol.OutcomeCommitted {
		return protocol.ErrRecoveryRequired
	}
	return nil
}
func (c *Client) events() {
	defer func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		for _, t := range c.tuns {
			t.finish()
		}
	}()
	for e := range c.peer.Events() {
		c.mu.Lock()
		t := c.tuns[e.Resource.ID]
		c.mu.Unlock()
		if t != nil {
			t.event(e)
		}
	}
}

// Conn serializes each direction independently. It reports native completion,
// not successful delivery to the IPC writer. It never exposes RawConn or File.
type Conn struct {
	wake                        chan struct{}
	writeBusy                   bool
	client                      *Client
	ref                         protocol.ResourceRef
	local, remote               net.Addr
	packet                      bool
	readMu, writeMu             sync.Mutex
	mu                          sync.Mutex
	readDeadline, writeDeadline time.Time
	closed                      bool
}

func (c *Conn) LocalAddr() net.Addr        { return c.local }
func (c *Conn) RemoteAddr() net.Addr       { return c.remote }
func (c *Conn) Read(p []byte) (int, error) { n, _, err := c.readPacket(p); return n, err }
func (c *Conn) readPacketSync(ctx context.Context, size int) ([]byte, *protocol.PacketMetadata, error) {
	if !c.packet && size == 0 {
		return nil, nil, nil
	}
	c.mu.Lock()
	deadline, closed := c.readDeadline, c.closed
	c.mu.Unlock()
	if closed {
		return nil, nil, net.ErrClosed
	}
	if !deadline.IsZero() && !time.Now().Before(deadline) {
		return nil, nil, os.ErrDeadlineExceeded
	}
	id := c.client.next.Add(1)
	if err := c.client.data.Reserve(c.ref, id, uint32(size), c.packet); err != nil {
		return nil, nil, err
	}
	defer c.client.data.Release(c.ref, id)
	result, err := c.client.channel(ctx, protocol.ChannelRequest{Resource: c.ref, Kind: protocol.ChannelRead, Request: id, Size: uint32(size), Remaining: remaining(deadline)})
	if result.Count == 0 && (!c.packet || err != nil) {
		return nil, nil, err
	}
	frame, receiveErr := c.client.data.Receive(context.Background(), c.ref, id)
	if receiveErr != nil {
		return nil, nil, receiveErr
	}
	if result.Count != uint32(len(frame.Payload)) {
		_ = c.client.Close()
		return nil, nil, protocol.ErrInvalidContract
	}
	c.mu.Lock()
	closed = c.closed
	c.mu.Unlock()
	if closed {
		return nil, nil, net.ErrClosed
	}
	meta := frame.Metadata.Records[0].Packet
	return frame.Payload, meta, err
}
func (c *Conn) Write(p []byte) (int, error) {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if c.packet {
		return c.writeRecord(p, &protocol.PacketMetadata{OriginalLength: uint32(len(p))})
	}
	total := 0
	for len(p) > 0 {
		size := min(len(p), protocol.MaxStreamRecord)
		n, err := c.writeRecord(p[:size], nil)
		total += n
		if err != nil {
			return total, err
		}
		if n != size {
			return total, io.ErrShortWrite
		}
		p = p[size:]
	}
	return total, nil
}
func (c *Conn) writeRecordSync(ctx context.Context, p []byte, meta *protocol.PacketMetadata) (int, error) {
	c.mu.Lock()
	deadline, closed := c.writeDeadline, c.closed
	c.mu.Unlock()
	if closed {
		return 0, net.ErrClosed
	}
	if !deadline.IsZero() && !time.Now().Before(deadline) {
		return 0, os.ErrDeadlineExceeded
	}
	if len(p) > protocol.MaxPacketRecord {
		return 0, protocol.ErrLimitExceeded
	}
	id := c.client.next.Add(1)
	q := protocol.ChannelRequest{Resource: c.ref, Kind: protocol.ChannelPrepareWrite, Request: id, Size: uint32(len(p))}
	if _, err := c.client.channel(ctx, q); err != nil {
		return 0, err
	}
	if err := c.client.data.Send(context.Background(), c.ref, id, p, meta); err != nil {
		return 0, err
	}
	q.Kind = protocol.ChannelWrite
	q.Remaining = remaining(deadline)
	if ctx.Err() != nil {
		zero := int64(0)
		q.Remaining = &zero
	}
	// A granted write is always completed or explicitly expired. Caller
	// cancellation must not strand its reservation or replay its payload.
	result, err := c.client.channel(context.Background(), q)
	if result.Count > uint32(len(p)) {
		_ = c.client.Close()
		return 0, protocol.ErrInvalidContract
	}
	return int(result.Count), err
}
func (c *Conn) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	c.signalLocked()
	c.mu.Unlock()
	return c.client.closeResource(c.ref)
}
func (c *Conn) SetDeadline(t time.Time) error {
	return errors.Join(c.SetReadDeadline(t), c.SetWriteDeadline(t))
}
func (c *Conn) SetReadDeadline(t time.Time) error {
	c.mu.Lock()
	c.readDeadline = t
	c.signalLocked()
	c.mu.Unlock()
	_, err := c.client.channel(context.Background(), protocol.ChannelRequest{Resource: c.ref, Kind: protocol.ChannelSetReadDeadline, Remaining: remaining(t)})
	return err
}
func (c *Conn) SetWriteDeadline(t time.Time) error {
	c.mu.Lock()
	c.writeDeadline = t
	c.signalLocked()
	c.mu.Unlock()
	_, err := c.client.channel(context.Background(), protocol.ChannelRequest{Resource: c.ref, Kind: protocol.ChannelSetWriteDeadline, Remaining: remaining(t)})
	return err
}
func (c *Conn) CloseRead() error {
	_, err := c.client.channel(context.Background(), protocol.ChannelRequest{Resource: c.ref, Kind: protocol.ChannelCloseRead})
	return err
}
func (c *Conn) CloseWrite() error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	_, err := c.client.channel(context.Background(), protocol.ChannelRequest{Resource: c.ref, Kind: protocol.ChannelCloseWrite})
	return err
}
func (c *Conn) ReadFrom(r io.Reader) (int64, error) { return io.Copy(struct{ io.Writer }{c}, r) }
func (c *Conn) WriteTo(w io.Writer) (int64, error)  { return io.Copy(w, struct{ io.Reader }{c}) }
func (c *Conn) setting(o protocol.SocketSetting) error {
	_, err := c.client.channel(context.Background(), protocol.ChannelRequest{Resource: c.ref, Kind: protocol.ChannelSocketSetting, Options: o})
	return err
}
func (c *Conn) SetKeepAlive(v bool) error {
	return c.setting(protocol.SocketSetting{Kind: "keepalive", Enabled: v})
}
func (c *Conn) SetKeepAlivePeriod(v time.Duration) error {
	return c.setting(protocol.SocketSetting{Kind: "keepalive_period", Idle: int64(v)})
}
func (c *Conn) SetKeepAliveConfig(v net.KeepAliveConfig) error {
	return c.setting(protocol.SocketSetting{Kind: "keepalive_config", Enabled: v.Enable, Idle: int64(v.Idle), Interval: int64(v.Interval), Count: int32(v.Count)})
}
func (c *Conn) SetLinger(v int) error {
	return c.setting(protocol.SocketSetting{Kind: "linger", Value: int32(v)})
}
func (c *Conn) SetNoDelay(v bool) error {
	return c.setting(protocol.SocketSetting{Kind: "no_delay", Enabled: v})
}
func (c *Conn) SetReadBuffer(v int) error {
	return c.setting(protocol.SocketSetting{Kind: "read_buffer", Value: int32(v)})
}
func (c *Conn) SetWriteBuffer(v int) error {
	return c.setting(protocol.SocketSetting{Kind: "write_buffer", Value: int32(v)})
}

var _ tun.Tun = (*Tun)(nil)

func (c *Client) Status(ctx context.Context) (protocol.StatusResult, error) {
	var result protocol.StatusResult
	err := c.peer.Call(ctx, protocol.OperationStatus, protocol.ReadRequest{Meta: c.meta(false)}, &result)
	return result, err
}

// MarkReady must be called after application initialization, not after the
// restriction or data-channel handshake. Loss of the acknowledgement fails
// startup; retry is safe and never adds native authority.
func (c *Client) MarkReady(ctx context.Context) error {
	bounded, cancel := context.WithTimeout(ctx, systemOperationTimeout)
	defer cancel()
	var result protocol.RuntimeReadyResult
	if err := c.peer.Call(bounded, protocol.OperationRuntimeReady, protocol.ReadRequest{Meta: c.meta(false)}, &result); err != nil {
		return err
	}
	if !result.Ready || result.Session != c.session {
		return protocol.ErrInvalidContract
	}
	return nil
}

type ioResult struct {
	accepted protocol.ChannelResult
	payload  []byte
	count    int
	metadata *protocol.PacketMetadata
	err      error
}

func (c *Conn) signalLocked() {
	if c.wake != nil {
		close(c.wake)
	}
	c.wake = make(chan struct{})
}

func (c *Conn) waitReadIdle() {
	for !c.readMu.TryLock() {
		time.Sleep(time.Millisecond)
	}
	c.readMu.Unlock()
}

func (c *Conn) readPacket(p []byte) (int, *protocol.PacketMetadata, error) {
	for !c.readMu.TryLock() {
		c.mu.Lock()
		closed, deadline := c.closed, c.readDeadline
		c.mu.Unlock()
		if closed {
			return 0, nil, net.ErrClosed
		}
		if !deadline.IsZero() && !time.Now().Before(deadline) {
			return 0, nil, os.ErrDeadlineExceeded
		}
		select {
		case <-c.client.peer.Done():
			return 0, nil, c.client.peer.Err()
		case <-time.After(time.Millisecond):
		}
	}
	size := min(len(p), protocol.MaxStreamRecord)
	if c.packet {
		size = protocol.MaxPacketRecord
	}
	// A private buffer remains owned by the worker after a local timeout.
	done := make(chan ioResult, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		defer c.readMu.Unlock()
		data, m, err := c.readPacketSync(ctx, size)
		done <- ioResult{payload: data, metadata: m, err: err}
	}()
	result := c.awaitIO(done, false, cancel)
	n := copy(p, result.payload)
	if result.metadata != nil && n < len(result.payload) {
		result.metadata.Truncated = true
		result.metadata.OriginalLength = uint32(len(result.payload))
	}
	return n, result.metadata, result.err
}
func (c *Conn) writeRecord(p []byte, meta *protocol.PacketMetadata) (int, error) {
	c.mu.Lock()
	if c.writeBusy {
		c.mu.Unlock()
		return 0, protocol.ErrLimitExceeded
	}
	c.writeBusy = true
	c.mu.Unlock()
	if len(p) > protocol.MaxPacketRecord {
		c.mu.Lock()
		c.writeBusy = false
		c.mu.Unlock()
		return 0, protocol.ErrLimitExceeded
	}
	release, err := c.client.data.HoldOutbound(len(p))
	if err != nil {
		c.mu.Lock()
		c.writeBusy = false
		c.mu.Unlock()
		return 0, err
	}
	payload := append([]byte(nil), p...)
	done := make(chan ioResult, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		n, err := c.writeRecordSync(ctx, payload, meta)
		release()
		c.mu.Lock()
		c.writeBusy = false
		c.signalLocked()
		c.mu.Unlock()
		done <- ioResult{count: n, err: err}
	}()
	result := c.awaitIO(done, true, nil)
	return result.count, result.err
}
func (c *Conn) awaitIO(done <-chan ioResult, write bool, cancel context.CancelFunc) ioResult {
	for {
		c.mu.Lock()
		closed, deadline := c.closed, c.readDeadline
		if write {
			deadline = c.writeDeadline
		}
		if c.wake == nil {
			c.wake = make(chan struct{})
		}
		wake := c.wake
		c.mu.Unlock()
		if closed {
			return ioResult{err: net.ErrClosed}
		}
		var timer *time.Timer
		var expiry <-chan time.Time
		if !deadline.IsZero() {
			timer = time.NewTimer(max(time.Until(deadline), 0))
			expiry = timer.C
		}
		select {
		case result := <-done:
			if timer != nil {
				timer.Stop()
			}
			return result
		case <-wake:
			if timer != nil {
				timer.Stop()
			}
		case <-c.client.peer.Done():
			if timer != nil {
				timer.Stop()
			}
			return ioResult{err: c.client.peer.Err()}
		case <-expiry:
			if !write {
				// A stream read can consume bytes after its local deadline while
				// the broker is still stopping the native read. Wait for the
				// settled terminal result so a later Read cannot lose those bytes.
				cancel()
				select {
				case result := <-done:
					return result
				case <-c.client.peer.Done():
					return ioResult{err: c.client.peer.Err()}
				}
			}
			err := error(os.ErrDeadlineExceeded)
			err = errors.Join(err, protocol.ErrRecoveryRequired)
			return ioResult{err: err}
		}
	}
}

// openResource returns cancellation promptly but retains a bounded completion
// worker. A late committed resource is closed, never forgotten or replayed.
func (c *Client) openResource(ctx context.Context, op protocol.Operation, request any) (protocol.ResourceResult, error) {
	select {
	case c.opens <- struct{}{}:
	case <-ctx.Done():
		return protocol.ResourceResult{}, ctx.Err()
	default:
		return protocol.ResourceResult{}, protocol.ErrLimitExceeded
	}
	type completion struct {
		result protocol.ResourceResult
		err    error
	}
	done := make(chan completion, 1)
	// The caller decides whether it accepted the terminal result. Do not race a
	// closed accepted channel against ctx.Done: the caller cancels many bounded
	// contexts immediately after a successful return, and a random select could
	// then close a resource which it had already returned as owned.
	type completionDecision uint8
	const (
		completionAccepted completionDecision = iota + 1
		completionAbandoned
		completionPeerLost
	)
	decision := make(chan completionDecision, 1)
	go func() {
		defer func() { <-c.opens }()
		var result protocol.ResourceResult
		err := c.peer.CallSettled(ctx, op, request, &result)
		done <- completion{result, err}
		switch <-decision {
		case completionAccepted, completionPeerLost:
			return
		case completionAbandoned:
			if result.Outcome == protocol.OutcomeCommitted && result.Resource.ID.Valid() {
				if closeErr := c.closeResource(result.Resource); closeErr != nil {
					_ = c.Close()
				}
			}
		}
	}()
	select {
	case value := <-done:
		decision <- completionAccepted
		return value.result, value.err
	case <-ctx.Done():
		decision <- completionAbandoned
		return protocol.ResourceResult{}, ctx.Err()
	case <-c.peer.Done():
		decision <- completionPeerLost
		return protocol.ResourceResult{}, c.peer.Err()
	}
}

func (c *Client) ReleaseGraphEpoch(ctx context.Context) error {
	epoch := c.epoch.Load()
	var result protocol.MutationResult
	err := c.peer.CallSettled(ctx, protocol.OperationReleaseGraphEpoch, protocol.ReleaseGraphEpochRequest{Meta: c.meta(true), GraphEpoch: epoch}, &result)
	if err != nil {
		return err
	}
	if result.Outcome != protocol.OutcomeCommitted {
		return protocol.ErrRecoveryRequired
	}
	c.epoch.CompareAndSwap(epoch, epoch+1)
	c.mu.Lock()
	defer c.mu.Unlock()
	for id, t := range c.tuns {
		t.finish()
		delete(c.tuns, id)
	}
	return nil
}
