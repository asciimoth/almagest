package runtime

import (
	"context"
	"errors"
	"io"
	"os"
	"slices"
	"sync"
	"time"

	"github.com/asciimoth/almagest/bin/internal/isolation/protocol"
	"github.com/asciimoth/almagest/bin/internal/isolation/transport"
	"github.com/asciimoth/gonnect/tun"
)

type Tun struct {
	*Conn
	stateMu  sync.Mutex
	state    protocol.ResourceState
	batch    int
	revision uint64
	events   chan tun.Event
	done     bool
}

func (c *Client) Tun(ref protocol.ResourceRef, state protocol.ResourceState) (*Tun, error) {
	if ref.Type != protocol.ResourceTun && ref.Type != protocol.ResourceDefaultTun {
		return nil, protocol.ErrResourceType
	}
	if err := ref.Validate(c.session, ref.Type, c.epoch.Load(), ref.Generation); err != nil {
		return nil, err
	}
	if state.MTU == 0 || state.MTU > protocol.MaxPacketRecord {
		return nil, protocol.ErrCapabilityUnsupported
	}
	batch := int(state.TunBatchSize)
	if batch == 0 {
		batch = 1
	}
	if batch < 1 || batch > 256 {
		return nil, protocol.ErrCapabilityUnsupported
	}
	t := &Tun{Conn: &Conn{client: c, ref: ref, packet: true}, state: state, batch: batch, revision: state.Revision, events: make(chan tun.Event, 128)}
	c.mu.Lock()
	defer c.mu.Unlock()
	if previous := c.tuns[ref.ID]; previous != nil {
		if previous.ref.Generation == ref.Generation {
			return previous, nil
		}
		if previous.ref.Generation > ref.Generation {
			return nil, protocol.ErrOperationConflict
		}
		previous.finish()
	}
	c.tuns[ref.ID] = t
	if state.Up {
		t.events <- tun.EventUp
	}
	return t, nil
}
func (*Tun) File() *os.File   { return nil }
func (*Tun) IsNative() bool   { return false }
func (*Tun) MWO() int         { return 0 }
func (*Tun) MRO() int         { return 0 }
func (t *Tun) BatchSize() int { return t.batch }
func (t *Tun) sourceGeneration() uint64 {
	t.stateMu.Lock()
	defer t.stateMu.Unlock()
	return t.state.TunSourceGeneration
}

// Native TUN devices do not have a portable deadline API. Keep deadline state
// in R so detached-TUN cancellation does not turn a normal link replacement
// into an unsupported native operation. Closing the resource remains the
// broker-side mechanism that wakes native I/O.
func (t *Tun) SetDeadline(deadline time.Time) error {
	return errors.Join(t.SetReadDeadline(deadline), t.SetWriteDeadline(deadline))
}
func (t *Tun) SetReadDeadline(deadline time.Time) error {
	t.mu.Lock()
	t.readDeadline = deadline
	t.signalLocked()
	t.mu.Unlock()
	return nil
}
func (t *Tun) SetWriteDeadline(deadline time.Time) error {
	t.mu.Lock()
	t.writeDeadline = deadline
	t.signalLocked()
	t.mu.Unlock()
	return nil
}
func (t *Tun) MTU() (int, error) {
	t.stateMu.Lock()
	defer t.stateMu.Unlock()
	return int(t.state.MTU), nil
}
func (t *Tun) Name() (string, error) {
	t.stateMu.Lock()
	defer t.stateMu.Unlock()
	return t.state.Name, nil
}
func (t *Tun) Events() <-chan tun.Event { return t.events }
func (t *Tun) Read(buffers [][]byte, sizes []int, offset int) (int, error) {
	if len(buffers) < 1 || len(sizes) < len(buffers) || offset < 0 || offset > len(buffers[0]) {
		return 0, io.ErrShortBuffer
	}
	count := 0
	remaining := uint32(0)
	for count < len(buffers) {
		if offset > len(buffers[count]) {
			return count, io.ErrShortBuffer
		}
		n, meta, err := t.readPacket(buffers[count][offset:])
		sizes[count] = n
		if meta == nil || meta.Truncated {
			if err == nil {
				err = io.ErrShortBuffer
			}
			return count, err
		}
		if count > 0 && meta.BatchRemaining+1 != remaining {
			_ = t.client.Close()
			return count, protocol.ErrInvalidContract
		}
		remaining = meta.BatchRemaining
		if remaining >= uint32(t.batch) {
			_ = t.client.Close()
			return count, protocol.ErrInvalidContract
		}
		if n > 0 {
			count++
		}
		if err != nil || n == 0 || remaining == 0 {
			return count, err
		}
	}
	return count, nil
}
func (t *Tun) Write(buffers [][]byte, offset int) (int, error) {
	if offset < 0 {
		return 0, io.ErrShortBuffer
	}
	for _, p := range buffers {
		if offset >= len(p) {
			return 0, io.ErrShortBuffer
		}
	}
	for i, p := range buffers {
		n, err := t.Conn.Write(p[offset:])
		if err != nil {
			if n == len(p)-offset {
				return i + 1, err
			}
			return i, err
		}
		if n != len(p)-offset {
			return i, io.ErrShortWrite
		}
	}
	return len(buffers), nil
}
func (t *Tun) Close() error {
	err := t.Conn.Close()
	t.finish()
	t.client.mu.Lock()
	if t.client.tuns[t.ref.ID] == t {
		delete(t.client.tuns, t.ref.ID)
	}
	t.client.mu.Unlock()
	return err
}
func (t *Tun) finish() {
	t.stateMu.Lock()
	defer t.stateMu.Unlock()
	if !t.done {
		t.mu.Lock()
		t.closed = true
		t.signalLocked()
		t.mu.Unlock()
		t.done = true
		close(t.events)
	}
}
func (t *Tun) event(e transport.Event) {
	t.stateMu.Lock()
	defer t.stateMu.Unlock()
	if e.Resource.ID == t.ref.ID && e.Resource.Generation > t.ref.Generation {
		if !t.done {
			t.done = true
			t.mu.Lock()
			t.closed = true
			t.signalLocked()
			t.mu.Unlock()
			close(t.events)
		}
		return
	}
	if t.done || e.Resource != t.ref || e.Revision <= t.revision {
		return
	}
	t.revision = e.Revision
	t.state.Up = e.Up
	if e.Closed {
		t.mu.Lock()
		t.closed = true
		t.signalLocked()
		t.mu.Unlock()
		t.done = true
		close(t.events)
		return
	}
	event := tun.Event(tun.EventDown)
	if e.Up {
		event = tun.EventUp
	}
	if e.MTUChanged {
		event |= tun.EventMTUUpdate
	}
	select {
	case t.events <- event:
	default:
		_ = t.client.Close()
	}
}
func (c *Client) CreateTun(ctx context.Context, req protocol.CreateTunRequest) (*Tun, error) {
	req.Meta = c.meta(true)
	op := protocol.OperationCreateTun
	if req.Default {
		op = protocol.OperationCreateDefaultTun
	}
	result, err := c.openResource(ctx, op, req)
	if err != nil {
		return nil, err
	}
	if result.Outcome != protocol.OutcomeCommitted {
		if result.Code != "" {
			return nil, decodeError(result.Code)
		}
		return nil, protocol.ErrRecoveryRequired
	}
	return c.Tun(result.Resource, result.State)
}

func (c *Client) tunState(ctx context.Context, device *Tun) (protocol.ResourceState, error) {
	if device == nil {
		return protocol.ResourceState{}, protocol.ErrInvalidContract
	}
	meta := c.meta(false)
	meta.GraphEpoch = device.ref.GraphEpoch
	var state protocol.ResourceState
	err := c.peer.Call(ctx, protocol.OperationGetTunState, protocol.ResourceRequest{Meta: meta, Resource: device.ref}, &state)
	if err != nil {
		return protocol.ResourceState{}, err
	}
	state.Capabilities = slices.Clone(state.Capabilities)
	state.Addresses = slices.Clone(state.Addresses)
	state.Routes = slices.Clone(state.Routes)
	state.Warnings = slices.Clone(state.Warnings)
	return state, nil
}

func (c *Client) updateTun(ctx context.Context, device *Tun, req protocol.UpdateTunRequest) (*Tun, error) {
	if device == nil {
		return nil, protocol.ErrInvalidContract
	}
	req.Meta = c.meta(true)
	req.Meta.GraphEpoch = device.ref.GraphEpoch
	req.Resource = device.ref
	var result protocol.MutationResult
	if err := c.peer.CallSettled(ctx, updateOperation(req.Kind), req, &result); err != nil {
		return nil, err
	}
	if result.Outcome != protocol.OutcomeCommitted {
		if result.Code != "" {
			return nil, decodeError(result.Code)
		}
		return nil, protocol.ErrRecoveryRequired
	}
	if result.CurrentResource.ID != device.ref.ID || result.CurrentResource.SessionID != device.ref.SessionID ||
		result.CurrentResource.Type != device.ref.Type || result.CurrentResource.GraphEpoch != device.ref.GraphEpoch ||
		result.CurrentResource.Generation < device.ref.Generation || result.CurrentResource.Generation > device.ref.Generation+1 {
		_ = c.Close()
		return nil, protocol.ErrInvalidContract
	}
	if result.CurrentResource == device.ref {
		return device, nil
	}
	state, err := c.tunState(ctx, &Tun{Conn: &Conn{client: c, ref: result.CurrentResource}})
	if err != nil {
		return nil, err
	}
	return c.Tun(result.CurrentResource, state)
}

func updateOperation(kind protocol.TunUpdateKind) protocol.Operation {
	switch kind {
	case protocol.TunUpdateMTU:
		return protocol.OperationSetTunMTU
	case protocol.TunUpdateAddresses:
		return protocol.OperationSetTunAddresses
	case protocol.TunUpdateRoutes:
		return protocol.OperationSetTunRoutes
	case protocol.TunUpdateName:
		return protocol.OperationSetTunName
	default:
		return ""
	}
}
