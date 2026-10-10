package runtime

import (
	"context"
	"io"
	"os"
	"sync"

	"github.com/asciimoth/almagest/bin/internal/isolation/protocol"
	"github.com/asciimoth/almagest/bin/internal/isolation/transport"
	"github.com/asciimoth/gonnect/tun"
)

type Tun struct {
	*Conn
	stateMu  sync.Mutex
	state    protocol.ResourceState
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
	t := &Tun{Conn: &Conn{client: c, ref: ref, packet: true}, state: state, revision: state.Revision, events: make(chan tun.Event, 128)}
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
func (*Tun) File() *os.File { return nil }
func (*Tun) IsNative() bool { return false }
func (*Tun) MWO() int       { return 0 }
func (*Tun) MRO() int       { return 0 }
func (*Tun) BatchSize() int { return 1 }
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
	n, meta, err := t.readPacket(buffers[0][offset:])
	sizes[0] = n
	if meta != nil && meta.Truncated {
		return 0, io.ErrShortBuffer
	}
	if n > 0 {
		return 1, err
	}
	return 0, err
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
		return nil, protocol.ErrRecoveryRequired
	}
	return c.Tun(result.Resource, result.State)
}
