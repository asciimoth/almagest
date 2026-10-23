package transport

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/asciimoth/almagest/bin/internal/isolation/protocol"
)

func testSession() protocol.Session {
	return protocol.Session{ID: protocol.SessionID{1}, Epoch: 1, Version: protocol.Version}
}

func pair(t *testing.T, handler Handler) (*Peer, *Peer) {
	t.Helper()
	a, b := net.Pipe()
	c, d := net.Pipe()
	e, f := net.Pipe()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	t.Cleanup(cancel)
	type result struct {
		p   *Peer
		err error
	}
	ready := make(chan result, 1)
	go func() { p, err := New(ctx, Lanes{a, c, e}, testSession(), nil); ready <- result{p, err} }()
	p, err := New(ctx, Lanes{b, d, f}, testSession(), handler)
	if err != nil {
		t.Fatal(err)
	}
	r := <-ready
	if r.err != nil {
		t.Fatal(r.err)
	}
	t.Cleanup(func() { _ = p.Close(); _ = r.p.Close() })
	return r.p, p
}

func readRequest() protocol.ReadRequest {
	return protocol.ReadRequest{Meta: protocol.RequestMeta{GraphEpoch: 1}}
}

func TestStrictJSON(t *testing.T) {
	for _, data := range []string{
		`{"Meta":{},"Meta":{}}`, `{"Meta":{},"\u004deta":{}}`, `{"meta":{}}`,
		`{"Meta":{"RequestID":1,"RequestID":2}}`, `{"Meta":{"requestID":1}}`,
		`{"Meta":{},"unknown":0}`, `{"Meta":{}} {}`, `{"Meta":{"RequestID":1.1}}`,
		`{"Meta":{"RequestID":-1}}`, `{"Meta":{"RequestID":18446744073709551616}}`,
		`{"Meta":{"OperationID":"\ud800"}}`, `{"Meta":{"OperationID":"\udc00"}}`,
		`{"Meta":{"SessionID":[1]}}`, `{"Meta":{"SessionID":null}}`,
		"{\"Meta\":\"\xff\"}", strings.Repeat("[", 33) + strings.Repeat("]", 33),
	} {
		t.Run(data, func(t *testing.T) {
			if Decode([]byte(data), new(protocol.ReadRequest)) == nil {
				t.Fatal("accepted malformed JSON")
			}
		})
	}
	if err := Decode([]byte(`{"Meta":{"RequestID":1,"SessionEpoch":1,"GraphEpoch":1}}`), new(protocol.ReadRequest)); err != nil {
		t.Fatal(err)
	}
}

func TestControlCallsAndCancellation(t *testing.T) {
	started := make(chan struct{}, 1)
	stopped := make(chan struct{}, 1)
	a, b := pair(t, func(ctx context.Context, op protocol.Operation, body json.RawMessage) (any, error) {
		var req protocol.ReadRequest
		if err := Decode(body, &req); err != nil {
			return nil, err
		}
		if err := req.Meta.Validate(testSession(), false); err != nil {
			return nil, err
		}
		if op == protocol.OperationStatus {
			started <- struct{}{}
			<-ctx.Done()
			stopped <- struct{}{}
			return nil, ctx.Err()
		}
		return protocol.StatusResult{LiveResources: 7}, nil
	})
	var result protocol.StatusResult
	if err := a.Call(context.Background(), protocol.OperationCapabilities, readRequest(), &result); err != nil || result.LiveResources != 7 {
		t.Fatalf("result %v: %v", result, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	complete := make(chan error, 1)
	go func() { complete <- a.Call(ctx, protocol.OperationStatus, readRequest(), &result) }()
	<-started
	cancel()
	if err := <-complete; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("handler was not canceled")
	}
	if err := a.Call(context.Background(), protocol.OperationCapabilities, readRequest(), &result); err != nil {
		t.Fatal(err)
	}
	go func() { complete <- a.Call(context.Background(), protocol.OperationStatus, readRequest(), &result) }()
	<-started
	_ = b.Close()
	select {
	case err := <-complete:
		if err == nil {
			t.Fatal("peer death reported success")
		}
	case <-time.After(time.Second):
		t.Fatal("peer death left a pending call")
	}
	select {
	case <-a.Done():
	case <-time.After(time.Second):
		t.Fatal("peer death not detected")
	}
}

func TestControlConcurrentAndLimits(t *testing.T) {
	started := make(chan struct{}, protocol.MaxConcurrentRequests)
	a, _ := pair(t, func(ctx context.Context, _ protocol.Operation, _ json.RawMessage) (any, error) {
		started <- struct{}{}
		<-ctx.Done()
		return nil, ctx.Err()
	})
	var wg sync.WaitGroup
	for range protocol.MaxConcurrentRequests {
		wg.Go(func() {
			var out protocol.StatusResult
			_ = a.Call(context.Background(), protocol.OperationStatus, readRequest(), &out)
		})
	}
	for range protocol.MaxConcurrentRequests {
		<-started
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := a.Call(ctx, protocol.OperationStatus, readRequest(), new(protocol.StatusResult)); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	_ = a.Close()
	wg.Wait()
}

func TestControlIndependentLanes(t *testing.T) {
	a, _ := pair(t, func(context.Context, protocol.Operation, json.RawMessage) (any, error) {
		return protocol.StatusResult{}, nil
	})
	var wg sync.WaitGroup
	for _, lane := range []Stream{a.lanes.Data, a.lanes.Diagnostic} {
		wg.Go(func() { _, _ = lane.Write(make([]byte, 65536)) })
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	for range 20 {
		if err := a.Call(ctx, protocol.OperationStatus, readRequest(), new(protocol.StatusResult)); err != nil {
			t.Fatal(err)
		}
	}
	_ = a.Close()
	wg.Wait()
}

func TestControlMalformedAndEvents(t *testing.T) {
	t.Run("unknown-reply", func(t *testing.T) {
		a, b := pair(t, nil)
		if err := b.queue(message{Kind: "reply", ID: 77, Body: json.RawMessage(`{}`)}, true); err != nil {
			t.Fatal(err)
		}
		select {
		case <-a.Done():
		case <-time.After(time.Second):
			t.Fatal("unknown reply accepted")
		}
	})
	t.Run("events", func(t *testing.T) {
		a, b := pair(t, nil)
		e := Event{Resource: protocol.ResourceRef{ID: protocol.ResourceID{1}, SessionID: testSession().ID, Type: protocol.ResourceTun, Generation: 1, GraphEpoch: 1}, Revision: 1}
		if err := b.Notify(e); err != nil {
			t.Fatal(err)
		}
		select {
		case got := <-a.Events():
			if got != e {
				t.Fatal(got)
			}
		case <-time.After(time.Second):
			t.Fatal("event lost")
		}
		for i := 0; i < protocol.MaxConcurrentRequests; i++ {
			if err := b.Notify(e); err != nil {
				t.Fatal(err)
			}
			deadline := time.Now().Add(time.Second)
			for len(a.events) != i+1 {
				if time.Now().After(deadline) {
					t.Fatal("event did not reach bounded queue")
				}
				time.Sleep(time.Millisecond)
			}
		}
		if err := b.Notify(e); err != nil {
			t.Fatal(err)
		}
		select {
		case <-a.Done():
		case <-time.After(time.Second):
			t.Fatal("event queue was not bounded")
		}
	})
}

func TestConfigChunks(t *testing.T) {
	var a ConfigAssembler
	now := time.Now()
	data := bytes.Repeat([]byte{42}, protocol.MaxConfigBytes)
	for offset := 0; offset < len(data); offset += ConfigChunkPayload {
		end := offset + ConfigChunkPayload
		chunk := ConfigChunk{1, uint32(offset), uint32(len(data)), end == len(data), data[offset:end]}
		frame, err := encode(chunk)
		if err != nil || len(frame) > protocol.MaxControlFrameBytes {
			t.Fatal(err)
		}
		got, err := a.Add(chunk, now)
		if err != nil {
			t.Fatal(err)
		}
		if chunk.Final && !bytes.Equal(got, data) {
			t.Fatal("assembly mismatch")
		}
	}
	if _, err := a.Add(ConfigChunk{Transfer: 1, Final: true}, now); err == nil {
		t.Fatal("replay accepted")
	}
	for _, c := range []ConfigChunk{
		{Transfer: 2, Offset: 1, Total: 2, Data: []byte{1}}, {Transfer: 2, Total: protocol.MaxConfigBytes + 1},
		{Transfer: 2, Total: 1, Data: []byte{1}}, {Transfer: 2, Total: 2, Final: true, Data: []byte{1}},
	} {
		a.Reset()
		if _, err := a.Add(c, now); err == nil {
			t.Fatal("invalid chunk accepted")
		}
	}
	a = ConfigAssembler{}
	if _, err := a.Add(ConfigChunk{Transfer: 1, Total: 2, Data: []byte{1}}, now); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Add(ConfigChunk{Transfer: 1, Offset: 1, Total: 2, Final: true, Data: []byte{2}}, now.Add(time.Minute)); err == nil {
		t.Fatal("expired transfer accepted")
	}
}

func TestControlConfigTransfer(t *testing.T) {
	a, b := pair(t, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	data := bytes.Repeat([]byte{42}, protocol.MaxConfigBytes)
	if err := a.SendConfig(ctx, 1, data); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-b.Configs():
		if got.Transfer != 1 || !bytes.Equal(got.Data, data) {
			t.Fatal("config mismatch")
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if err := a.SendConfig(ctx, 2, nil); err != nil {
		t.Fatal(err)
	}
	if got := <-b.Configs(); got.Transfer != 2 || len(got.Data) != 0 {
		t.Fatal("empty config mismatch")
	}
	if err := a.SendConfig(ctx, 2, nil); err == nil {
		t.Fatal("replayed config accepted")
	}
}

func TestControlRequestIdentity(t *testing.T) {
	for _, mutate := range []func(*message){
		func(m *message) { m.ID = 2 },
		func(m *message) { m.Body = json.RawMessage(`{"Meta":{"RequestID":1,"SessionEpoch":1,"GraphEpoch":1}}`) },
		func(m *message) { m.Kind = "unknown" },
		func(m *message) { m.Operation = "native_ioctl" },
	} {
		a, b := pair(t, nil)
		body, err := json.Marshal(readRequest())
		if err != nil {
			t.Fatal(err)
		}
		body, err = validateRequest(protocol.OperationStatus, body, 1, testSession(), true)
		if err != nil {
			t.Fatal(err)
		}
		m := message{Kind: "request", ID: 1, Operation: protocol.OperationStatus, Body: body}
		mutate(&m)
		if err := a.queue(m, false); err != nil {
			t.Fatal(err)
		}
		select {
		case <-b.Done():
		case <-time.After(time.Second):
			t.Fatal("forged request accepted")
		}
	}
}

func FuzzControlIdentity(f *testing.F) {
	f.Add(uint64(1), uint32(1), []byte(`{"Meta":{"GraphEpoch":1}}`))
	f.Fuzz(func(t *testing.T, id uint64, version uint32, body []byte) {
		session := testSession()
		session.Version = version
		_, _ = validateRequest(protocol.OperationStatus, body, id, session, false)
	})
}

type memoryStream struct{ *bytes.Reader }

func (s memoryStream) Write(b []byte) (int, error)      { return len(b), nil }
func (s memoryStream) Close() error                     { return nil }
func (s memoryStream) SetReadDeadline(time.Time) error  { return nil }
func (s memoryStream) SetWriteDeadline(time.Time) error { return nil }

func TestFrameBounds(t *testing.T) {
	for _, data := range [][]byte{{0, 1, 0, 1}, {0, 0, 0, 0}, {0, 0, 0, 2, '{'}, {0, 0, 0}} {
		if ReadFrame(memoryStream{bytes.NewReader(data)}, new(message)) == nil {
			t.Fatal("malformed frame accepted")
		}
	}
	frame, err := encode(message{Kind: "cancel", ID: 1})
	if err != nil {
		t.Fatal(err)
	}
	var got message
	if err := ReadFrame(memoryStream{bytes.NewReader(frame)}, &got); err != nil || got.ID != 1 {
		t.Fatal(err)
	}
}

func TestNegotiationRejectsForeignSession(t *testing.T) {
	a, b := net.Pipe()
	c, d := net.Pipe()
	e, f := net.Pipe()
	defer func() { _ = d.Close() }()
	defer func() { _ = f.Close() }()
	defer func() { _ = b.Close() }()
	go func() {
		var h hello
		if ReadFrame(b, &h) != nil {
			return
		}
		h.Version++
		data, _ := encode(h)
		_ = writeFrame(b, data)
	}()
	_, err := New(context.Background(), Lanes{a, c, e}, testSession(), nil)
	if !errors.Is(err, protocol.ErrUnsupportedVersion) {
		t.Fatal(err)
	}
}

func FuzzControlDecode(f *testing.F) {
	f.Add([]byte(`{"kind":"cancel","id":1}`))
	f.Add([]byte(`{"kind":"reply","id":0,"error":{"code":"invalid"}}`))
	f.Fuzz(func(t *testing.T, data []byte) { _ = Decode(data, new(message)) })
}

func FuzzControlFrame(f *testing.F) {
	f.Add([]byte{0, 0, 0, 2, '{', '}'})
	f.Fuzz(func(t *testing.T, data []byte) { _ = ReadFrame(memoryStream{bytes.NewReader(data)}, new(message)) })
}

func FuzzConfigAssembly(f *testing.F) {
	f.Add(uint64(1), uint32(0), uint32(1), true, []byte{1})
	f.Fuzz(func(t *testing.T, id uint64, offset, total uint32, final bool, data []byte) {
		var a ConfigAssembler
		_, _ = a.Add(ConfigChunk{id, offset, total, final, data}, time.Unix(1, 0))
		_, _ = a.Add(ConfigChunk{id, offset, total, final, data}, time.Unix(2, 0))
		if len(a.data) > protocol.MaxConfigBytes {
			t.Fatal("unbounded assembly")
		}
	})
}

func TestTypedErrorAndInvalidRequests(t *testing.T) {
	a, _ := pair(t, func(context.Context, protocol.Operation, json.RawMessage) (any, error) {
		return nil, protocol.ErrPolicyDenied
	})
	if err := a.Call(context.Background(), protocol.OperationStatus, readRequest(), new(protocol.StatusResult)); !errors.Is(err, protocol.ErrPolicyDenied) {
		t.Fatal(err)
	}
	if err := a.Call(context.Background(), protocol.Operation("syscall"), readRequest(), new(protocol.StatusResult)); !errors.Is(err, protocol.ErrUnsupportedOperation) {
		t.Fatal(err)
	}
	if err := a.Call(context.Background(), protocol.OperationStatus, map[string]any{"Meta": readRequest().Meta, "path": "/etc"}, new(protocol.StatusResult)); err == nil {
		t.Fatal("unknown request field accepted")
	}
}

func TestStartedFrameTimeout(t *testing.T) {
	a, b := net.Pipe()
	defer func() { _ = a.Close() }()
	defer func() { _ = b.Close() }()
	go func() { _, _ = b.Write([]byte{0}) }()
	start := time.Now()
	err := ReadFrame(a, new(message))
	if err == nil || time.Since(start) > FrameTimeout+time.Second {
		t.Fatalf("unbounded frame: %v", err)
	}
}

func TestTruncatedFrame(t *testing.T) {
	a, b := net.Pipe()
	defer func() { _ = a.Close() }()
	go func() {
		var prefix [4]byte
		binary.BigEndian.PutUint32(prefix[:], 10)
		_, _ = b.Write(prefix[:])
		_ = b.Close()
	}()
	if err := ReadFrame(a, new(message)); !errors.Is(err, io.EOF) {
		t.Fatal(err)
	}
}
