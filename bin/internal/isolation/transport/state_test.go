package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/asciimoth/almagest/bin/internal/isolation/protocol"
)

// No I/O workers: state transitions and queue limits can be checked without races.
func statePeer(t *testing.T) *Peer {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	return &Peer{session: testSession(), ctx: ctx, cancel: cancel,
		pending: make(map[uint64]chan message), active: make(map[uint64]context.CancelFunc),
		out: make(chan queued, 2*protocol.MaxConcurrentRequests), ordinary: make(chan struct{}, protocol.MaxConcurrentRequests), terminal: make(chan struct{}, protocol.MaxConcurrentRequests),
		events: make(chan Event, protocol.MaxConcurrentRequests), configs: make(chan ConfigTransfer, 1), configSend: make(chan struct{}, 1)}
}

func testEvent() Event {
	return Event{Resource: protocol.ResourceRef{ID: protocol.ResourceID{1}, SessionID: testSession().ID, Type: protocol.ResourceTun, Generation: 1, GraphEpoch: 1}, Revision: 1, Up: true, MTUChanged: true}
}

func TestControlReceiveValidation(t *testing.T) {
	for _, m := range []message{
		{Kind: "cancel"}, {Kind: "unknown", ID: 1},
		{Kind: "cancel", ID: 1}, // Cannot cancel a request not yet received.
		{Kind: "cancel", ID: 1, Operation: protocol.OperationStatus},
		{Kind: "cancel", ID: 1, Body: json.RawMessage(`null`)},
		{Kind: "cancel", ID: 1, Error: &WireError{"internal"}},
		{Kind: "reply", ID: 1},
		{Kind: "reply", ID: 1, Body: json.RawMessage(`{}`)}, // No pending request.
		{Kind: "reply", ID: 1, Operation: protocol.OperationStatus, Body: json.RawMessage(`{}`)},
		{Kind: "reply", ID: 1, Error: &WireError{"future"}},
		{Kind: "reply", ID: 1, Error: &WireError{"denied"}, Body: json.RawMessage(`{}`)},
		{Kind: "request", ID: 1, Operation: protocol.OperationHandshake, Body: json.RawMessage(`{}`)},
		{Kind: "request", ID: 1, Operation: protocol.OperationStatus},
		{Kind: "request", ID: 1, Operation: protocol.OperationStatus, Body: json.RawMessage(`{}`), Error: &WireError{"denied"}},
		{Kind: "request", ID: 1, Operation: "ioctl", Body: json.RawMessage(`{}`)},
		{Kind: "event", ID: 1, Operation: protocol.OperationStatus},
		{Kind: "event", ID: 1, Error: &WireError{"denied"}},
		{Kind: "event", ID: 2, Body: json.RawMessage(`{}`)},
		{Kind: "event", ID: 1, Body: json.RawMessage(`{}`)},
		{Kind: "event", ID: 1, Body: json.RawMessage(`{"unexpected":1}`)},
		{Kind: "config", ID: 1, Operation: protocol.OperationStatus},
		{Kind: "config", ID: 1, Error: &WireError{"denied"}},
		{Kind: "config", ID: 1, Body: json.RawMessage(`{"unexpected":1}`)},
	} {
		t.Run(fmt.Sprintf("%s-%d", m.Kind, m.ID), func(t *testing.T) {
			p := statePeer(t)
			if err := p.receive(m); !errors.Is(err, protocol.ErrInvalidContract) {
				t.Fatalf("%+v: %v", m, err)
			}
			if len(p.pending) != 0 || len(p.active) != 0 || p.received != 0 || p.eventReceived != 0 {
				t.Fatal("rejected message changed state")
			}
		})
	}
	t.Run("reply consumed once", func(t *testing.T) {
		p := statePeer(t)
		ch := make(chan message, 1)
		p.pending[1] = ch
		m := message{Kind: "reply", ID: 1, Error: &WireError{"denied"}}
		if err := p.receive(m); err != nil {
			t.Fatal(err)
		}
		if len(p.pending) != 0 || len(ch) != 1 {
			t.Fatal("reply did not release pending slot")
		}
		if err := p.receive(m); !errors.Is(err, protocol.ErrInvalidContract) {
			t.Fatal("duplicate reply accepted")
		}
	})
	t.Run("cancel retains active slot", func(t *testing.T) {
		p := statePeer(t)
		ctx, cancel := context.WithCancel(p.ctx)
		defer cancel()
		p.active[1], p.received = cancel, 1
		for range 2 {
			if err := p.receive(message{Kind: "cancel", ID: 1}); err != nil {
				t.Fatal(err)
			}
		}
		if ctx.Err() == nil || len(p.active) != 1 {
			t.Fatal("cancellation released worker early")
		}
		delete(p.active, 1)
		if err := p.receive(message{Kind: "cancel", ID: 1}); err != nil {
			t.Fatal("late cancellation rejected")
		}
	})
	t.Run("request order and worker budget", func(t *testing.T) {
		for _, mode := range []string{"gap", "replay", "overflow", "workers"} {
			p := statePeer(t)
			id := uint64(1)
			switch mode {
			case "gap":
				id = 2
			case "replay":
				p.received = 1
			case "overflow":
				p.received = ^uint64(0)
			case "workers":
				for i := uint64(1); i <= protocol.MaxConcurrentRequests; i++ {
					p.active[i] = func() {}
				}
			}
			body, err := validateRequest(protocol.OperationStatus, []byte(`{"Meta":{"GraphEpoch":1}}`), id, p.session, true)
			if err != nil {
				t.Fatal(err)
			}
			if err := p.receive(message{Kind: "request", ID: id, Operation: protocol.OperationStatus, Body: body}); !errors.Is(err, protocol.ErrLimitExceeded) {
				t.Fatalf("%s: %v", mode, err)
			}
		}
	})
}

func TestControlQueueAndSequenceBounds(t *testing.T) {
	p := statePeer(t)
	for range protocol.MaxConcurrentRequests {
		if err := p.queue(message{Kind: "event", ID: 1}, false); err != nil {
			t.Fatal(err)
		}
	}
	if err := p.queue(message{Kind: "event", ID: 1}, false); !errors.Is(err, protocol.ErrLimitExceeded) {
		t.Fatal(err)
	}
	for range protocol.MaxConcurrentRequests {
		if err := p.queue(message{Kind: "cancel", ID: 1}, true); err != nil {
			t.Fatal(err)
		}
	}
	if err := p.queue(message{Kind: "cancel", ID: 1}, true); !errors.Is(err, protocol.ErrLimitExceeded) {
		t.Fatal(err)
	}
	if len(p.out) != 2*protocol.MaxConcurrentRequests {
		t.Fatal("terminal capacity not reserved")
	}
	p.cancel()
	if err := p.enqueue(nil, false); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatal(err)
	}
	q := statePeer(t)
	q.next = ^uint64(0)
	if err := q.Call(context.Background(), protocol.OperationStatus, readRequest(), new(protocol.StatusResult)); !errors.Is(err, protocol.ErrLimitExceeded) {
		t.Fatal(err)
	}
	q.eventSent = ^uint64(0)
	if err := q.Notify(testEvent()); !errors.Is(err, protocol.ErrLimitExceeded) {
		t.Fatal(err)
	}
	if q.next != ^uint64(0) || q.eventSent != ^uint64(0) {
		t.Fatal("sequence wrapped")
	}
}

func TestControlCallLifecycle(t *testing.T) {
	t.Run("canceled call retains slot until reply", func(t *testing.T) {
		p := statePeer(t)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		done := make(chan error, 1)
		go func() { done <- p.Call(ctx, protocol.OperationStatus, readRequest(), new(protocol.StatusResult)) }()
		select {
		case <-p.out: // The request is queued before cancellation.
		case <-time.After(3 * time.Second):
			t.Fatal("request was not queued")
		}
		cancel()
		select {
		case err := <-done:
			if !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("cancellation blocked")
		}
		if len(p.pending) != 1 || len(p.terminal) != 1 {
			t.Fatal("pending slot released before terminal reply")
		}
		if err := p.receive(message{Kind: "reply", ID: 1, Body: json.RawMessage(`{}`)}); err != nil {
			t.Fatal(err)
		}
		if len(p.pending) != 0 {
			t.Fatal("late reply did not release slot")
		}
	})
	t.Run("rejection does not consume ID", func(t *testing.T) {
		p := statePeer(t)
		for _, request := range []any{make(chan int), protocol.ReadRequest{}} {
			if err := p.Call(context.Background(), protocol.OperationStatus, request, new(protocol.StatusResult)); !errors.Is(err, protocol.ErrInvalidContract) {
				t.Fatal(err)
			}
		}
		for range protocol.MaxConcurrentRequests {
			p.ordinary <- struct{}{}
		}
		if err := p.Call(context.Background(), protocol.OperationStatus, readRequest(), new(protocol.StatusResult)); !errors.Is(err, protocol.ErrLimitExceeded) {
			t.Fatal(err)
		}
		if p.next != 0 || len(p.pending) != 0 {
			t.Fatal("rejected call consumed an ID or slot")
		}
		p.fail(errTestStream)
		if err := p.Call(context.Background(), protocol.OperationStatus, readRequest(), new(protocol.StatusResult)); !errors.Is(err, errTestStream) {
			t.Fatal(err)
		}
		p.fail(io.EOF)
		if !errors.Is(p.Err(), errTestStream) {
			t.Fatal("first failure replaced")
		}
	})
	t.Run("oversized reply fails session", func(t *testing.T) {
		a, b := pair(t, func(context.Context, protocol.Operation, json.RawMessage) (any, error) {
			return strings.Repeat("x", protocol.MaxControlFrameBytes), nil
		})
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := a.Call(ctx, protocol.OperationStatus, readRequest(), new(string)); err == nil || errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("oversized reply: %v", err)
		}
		if !errors.Is(b.Err(), protocol.ErrLimitExceeded) {
			t.Fatal(b.Err())
		}
	})
}

func TestControlEventValidation(t *testing.T) {
	for _, kind := range []protocol.ResourceType{protocol.ResourceAllocation, protocol.ResourceTun, protocol.ResourceDefaultTun, protocol.ResourceSocket, protocol.ResourceListener, protocol.ResourceMatcher, protocol.ResourceDNS} {
		e := testEvent()
		e.Resource.Type = kind
		if !e.valid(testSession()) {
			t.Fatalf("valid resource type rejected: %s", kind)
		}
	}
	for name, edit := range map[string]func(*Event){
		"revision":   func(e *Event) { e.Revision = 0 },
		"ID":         func(e *Event) { e.Resource.ID = protocol.ResourceID{} },
		"session":    func(e *Event) { e.Resource.SessionID = protocol.SessionID{} },
		"generation": func(e *Event) { e.Resource.Generation = 0 },
		"epoch":      func(e *Event) { e.Resource.GraphEpoch = 0 },
		"type":       func(e *Event) { e.Resource.Type = "future" },
	} {
		t.Run(name, func(t *testing.T) {
			p := statePeer(t)
			e := testEvent()
			edit(&e)
			if err := p.Notify(e); !errors.Is(err, protocol.ErrInvalidContract) {
				t.Fatal(err)
			}
			body, err := json.Marshal(e)
			if err != nil {
				t.Fatal(err)
			}
			if err := p.receive(message{Kind: "event", ID: 1, Body: body}); !errors.Is(err, protocol.ErrInvalidContract) {
				t.Fatal(err)
			}
			if p.eventSent != 0 || p.eventReceived != 0 {
				t.Fatal("invalid event advanced sequence")
			}
		})
	}
	p := statePeer(t)
	body, err := json.Marshal(testEvent())
	if err != nil {
		t.Fatal(err)
	}
	for i := uint64(1); i <= protocol.MaxConcurrentRequests; i++ {
		if err := p.receive(message{Kind: "event", ID: i, Body: body}); err != nil {
			t.Fatal(err)
		}
	}
	if err := p.receive(message{Kind: "event", ID: 1, Body: body}); !errors.Is(err, protocol.ErrInvalidContract) {
		t.Fatal("replayed event accepted")
	}
	if err := p.receive(message{Kind: "event", ID: protocol.MaxConcurrentRequests + 1, Body: body}); !errors.Is(err, protocol.ErrLimitExceeded) {
		t.Fatal("event queue grew")
	}
	p.eventReceived = ^uint64(0)
	if err := p.receive(message{Kind: "event", ID: 1, Body: body}); !errors.Is(err, protocol.ErrInvalidContract) {
		t.Fatal("event sequence wrapped")
	}
}

func TestControlErrorMapping(t *testing.T) {
	if wireError(nil) != nil {
		t.Fatal("nil error encoded")
	}
	for code, target := range errorCodes {
		err := wireError(fmt.Errorf("private native path: %w", target))
		if err.Code != code || !errors.Is(err, target) || strings.Contains(err.Error(), "private") {
			t.Fatalf("%s: %v", code, err)
		}
	}
	if got := wireError(errors.New("secret native details")); got.Code != "internal" || strings.Contains(got.Error(), "secret") {
		t.Fatal(got)
	}
	if (&WireError{"future"}).Unwrap() != nil {
		t.Fatal("unknown error has authority")
	}
	for _, tc := range []struct {
		name    string
		handler Handler
		want    error
	}{
		{"missing handler", nil, protocol.ErrUnsupportedOperation},
		{"private error", func(context.Context, protocol.Operation, json.RawMessage) (any, error) {
			return nil, errors.New("secret")
		}, errorCodes["internal"]},
		{"unencodable reply", func(context.Context, protocol.Operation, json.RawMessage) (any, error) { return make(chan int), nil }, errorCodes["internal"]},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, _ := pair(t, tc.handler)
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			if err := a.Call(ctx, protocol.OperationStatus, readRequest(), new(protocol.StatusResult)); !errors.Is(err, tc.want) {
				t.Fatal(err)
			}
		})
	}
}

func TestNegotiationContract(t *testing.T) {
	for name, edit := range map[string]func(*hello){
		"session":         func(h *hello) { h.Session.ID[0]++ },
		"epoch":           func(h *hello) { h.Session.Epoch++ },
		"policy":          func(h *hello) { h.Session.PolicyDigest[0]++ },
		"session version": func(h *hello) { h.Session.Version++ },
		"frame limit":     func(h *hello) { h.FrameBytes-- },
		"depth limit":     func(h *hello) { h.Depth-- },
		"request limit":   func(h *hello) { h.InFlight-- },
	} {
		t.Run(name, func(t *testing.T) {
			a, b := net.Pipe()
			defer func() { _ = b.Close() }()
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			finished := make(chan error, 1)
			go func() {
				var h hello
				if err := ReadFrame(b, &h); err != nil {
					finished <- err
					return
				}
				edit(&h)
				data, err := encode(h)
				if err == nil {
					err = writeFrame(b, data)
				}
				finished <- err
			}()
			d, e := &faultStream{}, &faultStream{}
			p, err := New(ctx, Lanes{a, d, e}, testSession(), nil)
			if !errors.Is(err, protocol.ErrInvalidContract) || p != nil || !d.closed || !e.closed {
				t.Fatalf("negotiation: %v, %v", p, err)
			}
			if err := <-finished; err != nil {
				t.Fatal(err)
			}
		})
	}
	for _, invalidSession := range []bool{false, true} {
		s := testSession()
		a, b, c := &faultStream{}, &faultStream{}, &faultStream{}
		lanes := Lanes{a, b, c}
		if invalidSession {
			s.Epoch = 0
		} else {
			lanes.Control = nil
		}
		if _, err := New(context.Background(), lanes, s, nil); !errors.Is(err, protocol.ErrInvalidContract) {
			t.Fatal(err)
		}
		if !b.closed || !c.closed || (invalidSession && !a.closed) {
			t.Fatal("negotiation leaked endpoints")
		}
	}
}

func TestConfigAssemblyTransitions(t *testing.T) {
	now := time.Unix(100, 0)
	for name, second := range map[string]ConfigChunk{
		"different transfer": {Transfer: 2, Offset: 1, Total: 2, Final: true, Data: []byte{2}},
		"different total":    {Transfer: 1, Offset: 1, Total: 3, Data: []byte{2}},
		"repeated offset":    {Transfer: 1, Total: 2, Data: []byte{2}},
		"empty intermediate": {Transfer: 1, Offset: 1, Total: 2},
		"missing final":      {Transfer: 1, Offset: 1, Total: 2, Data: []byte{2}},
		"early final":        {Transfer: 1, Offset: 1, Total: 2, Final: true},
	} {
		t.Run(name, func(t *testing.T) {
			var a ConfigAssembler
			if _, err := a.Add(ConfigChunk{Transfer: 1, Total: 2, Data: []byte{1}}, now); err != nil {
				t.Fatal(err)
			}
			if _, err := a.Add(second, now); !errors.Is(err, protocol.ErrInvalidContract) {
				t.Fatal(err)
			}
			if a.id != 0 || a.data != nil || a.total != 0 || !a.until.IsZero() {
				t.Fatal("failed transfer retained state")
			}
			if _, err := a.Add(ConfigChunk{Transfer: 1, Final: true}, now); !errors.Is(err, protocol.ErrInvalidContract) {
				t.Fatal("failed transfer ID reused")
			}
			if _, err := a.Add(ConfigChunk{Transfer: 3, Final: true}, now); err != nil {
				t.Fatal(err)
			}
		})
	}
	for _, c := range []ConfigChunk{
		{Final: true}, {Transfer: 1, Total: protocol.MaxConfigBytes + 1},
		{Transfer: 1, Total: ConfigChunkPayload + 1, Data: make([]byte, ConfigChunkPayload+1)},
		{Transfer: 1, Offset: 2, Total: 1}, {Transfer: 1, Total: 1, Data: []byte{1, 2}},
	} {
		var a ConfigAssembler
		if _, err := a.Add(c, now); !errors.Is(err, protocol.ErrLimitExceeded) {
			t.Fatalf("chunk: %+v: %v", c, err)
		}
	}
	for _, elapsed := range []time.Duration{30*time.Second - time.Nanosecond, 30 * time.Second, 31 * time.Second} {
		var a ConfigAssembler
		if _, err := a.Add(ConfigChunk{Transfer: 1, Total: 3, Data: []byte{1}}, now); err != nil {
			t.Fatal(err)
		}
		if _, err := a.Add(ConfigChunk{Transfer: 1, Offset: 1, Total: 3, Data: []byte{2}}, now.Add(29*time.Second)); err != nil {
			t.Fatal(err)
		}
		got, err := a.Add(ConfigChunk{Transfer: 1, Offset: 2, Total: 3, Final: true, Data: []byte{3}}, now.Add(elapsed))
		if elapsed < 30*time.Second {
			if err != nil || !bytes.Equal(got, []byte{1, 2, 3}) {
				t.Fatalf("before deadline: %v", err)
			}
		} else if !errors.Is(err, protocol.ErrInvalidContract) {
			t.Fatal("deadline slid with intermediate chunk")
		}
	}
}

func TestControlConfigBounds(t *testing.T) {
	p := statePeer(t)
	for _, tc := range []struct {
		id   uint64
		data []byte
	}{{0, nil}, {1, make([]byte, protocol.MaxConfigBytes+1)}} {
		if err := p.SendConfig(context.Background(), tc.id, tc.data); !errors.Is(err, protocol.ErrLimitExceeded) {
			t.Fatal(err)
		}
	}
	p.configSend <- struct{}{}
	if err := p.SendConfig(context.Background(), 1, nil); !errors.Is(err, protocol.ErrLimitExceeded) {
		t.Fatal("concurrent transfer accepted")
	}
	<-p.configSend
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := p.SendConfig(ctx, 1, nil); !errors.Is(err, context.Canceled) || len(p.configSend) != 0 {
		t.Fatalf("canceled send leaked slot: %v", err)
	}
	body, err := json.Marshal(ConfigChunk{Transfer: 1, Final: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := p.receive(message{Kind: "config", ID: 2, Body: body}); !errors.Is(err, protocol.ErrInvalidContract) {
		t.Fatal("config request gap accepted")
	}
	if err := p.receive(message{Kind: "config", ID: 1, Body: body}); err != nil {
		t.Fatal(err)
	}
	if len(p.configs) != 1 || len(p.terminal) != 1 {
		t.Fatal("config was not delivered and acknowledged")
	}
	if err := p.receive(message{Kind: "config", ID: 2, Body: body}); !errors.Is(err, protocol.ErrLimitExceeded) {
		t.Fatal("unconsumed config replaced")
	}
	p.fail(io.ErrClosedPipe)
	if err := p.receive(message{Kind: "config", ID: 2, Body: body}); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatal(err)
	}
}

func FuzzConfigRoundTrip(f *testing.F) {
	f.Add([]byte{}, uint16(1))
	f.Add([]byte("configuration"), uint16(3))
	f.Add(bytes.Repeat([]byte{42}, ConfigChunkPayload+1), uint16(ConfigChunkPayload))
	f.Fuzz(func(t *testing.T, data []byte, step uint16) {
		if len(data) > protocol.MaxConfigBytes {
			return
		}
		width := 1 + int(step)%ConfigChunkPayload
		var a ConfigAssembler
		now := time.Unix(1, 0)
		for offset := 0; ; {
			end := min(offset+width, len(data))
			c := ConfigChunk{Transfer: 1, Offset: uint32(offset), Total: uint32(len(data)), Final: end == len(data), Data: data[offset:end]}
			got, err := a.Add(c, now)
			if err != nil {
				t.Fatal(err)
			}
			if c.Final {
				if !bytes.Equal(got, data) || a.data != nil || a.id != 0 {
					t.Fatal("assembly changed data or retained buffer")
				}
				break
			}
			if got != nil || len(a.data) != end {
				t.Fatal("partial document published")
			}
			offset = end
		}
		if _, err := a.Add(ConfigChunk{Transfer: 1, Final: true}, now); !errors.Is(err, protocol.ErrInvalidContract) {
			t.Fatal("completed ID replayed")
		}
		if _, err := a.Add(ConfigChunk{Transfer: 2, Final: true}, now); err != nil {
			t.Fatal(err)
		}
	})
}
