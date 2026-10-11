package relay

import (
	"bytes"
	"context"
	"io"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/asciimoth/almagest/bin/internal/isolation/protocol"
)

type gatedWriteStream struct {
	writes  chan string
	permits chan struct{}
	closed  chan struct{}
	once    sync.Once
}

func newGatedWriteStream() *gatedWriteStream {
	return &gatedWriteStream{writes: make(chan string, 4), permits: make(chan struct{}, 4), closed: make(chan struct{})}
}

func (s *gatedWriteStream) Read([]byte) (int, error) {
	<-s.closed
	return 0, io.ErrClosedPipe
}

func (s *gatedWriteStream) Write(data []byte) (int, error) {
	frame, err := ReadFrame(&memoryStream{bytes.NewReader(data)})
	if err != nil {
		return 0, err
	}
	select {
	case s.writes <- frame.Metadata.Resource:
	case <-s.closed:
		return 0, io.ErrClosedPipe
	}
	select {
	case <-s.permits:
		return len(data), nil
	case <-s.closed:
		return 0, io.ErrClosedPipe
	}
}

func (s *gatedWriteStream) Close() error {
	s.once.Do(func() { close(s.closed) })
	return nil
}

func (*gatedWriteStream) SetReadDeadline(time.Time) error  { return nil }
func (*gatedWriteStream) SetWriteDeadline(time.Time) error { return nil }

// A hot resource cannot put a second frame ahead of a frame that is already
// queued for another resource. This is the per-resource fairness property used
// by saturated socket and TUN relays.
func TestMuxSaturatedResourceDoesNotOvertakeQueuedPeer(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	session := protocol.Session{ID: protocol.SessionID{1}, Epoch: 1, Version: protocol.Version}
	stream := newGatedWriteStream()
	mux, err := New(ctx, stream, session)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = mux.Close() })
	ref := func(id byte) protocol.ResourceRef {
		return protocol.ResourceRef{ID: protocol.ResourceID{id}, SessionID: session.ID, Type: protocol.ResourceSocket, Generation: 1, GraphEpoch: 1}
	}
	a, b := ref(2), ref(3)
	type result struct {
		name string
		err  error
	}
	done := make(chan result, 3)
	send := func(name string, resource protocol.ResourceRef, request uint64) {
		go func() { done <- result{name, mux.Send(ctx, resource, request, []byte(name), nil)} }()
	}
	nextWrite := func() string {
		t.Helper()
		select {
		case value := <-stream.writes:
			return value
		case <-ctx.Done():
			t.Fatal("timed out waiting for relay write")
			return ""
		}
	}
	nextDone := func() result {
		t.Helper()
		select {
		case value := <-done:
			return value
		case <-ctx.Done():
			t.Fatal("timed out waiting for relay completion")
			return result{}
		}
	}
	waitQueued := func(id protocol.ResourceID) {
		t.Helper()
		for {
			mux.mu.Lock()
			queued := mux.sending[id]
			mux.mu.Unlock()
			if queued {
				return
			}
			select {
			case <-ctx.Done():
				t.Fatal("timed out waiting for relay queue")
			default:
				runtime.Gosched()
			}
		}
	}

	send("a1", a, 1)
	if got := nextWrite(); got != a.ID.String() {
		t.Fatalf("first write = %s", got)
	}
	send("b1", b, 1)
	waitQueued(b.ID)
	stream.permits <- struct{}{}
	if got := nextDone(); got.name != "a1" || got.err != nil {
		t.Fatalf("first completion = %+v", got)
	}
	send("a2", a, 2)
	if got := nextWrite(); got != b.ID.String() {
		t.Fatalf("queued peer was overtaken: %s", got)
	}
	stream.permits <- struct{}{}
	if got := nextDone(); got.name != "b1" || got.err != nil {
		t.Fatalf("peer completion = %+v", got)
	}
	if got := nextWrite(); got != a.ID.String() {
		t.Fatalf("third write = %s", got)
	}
	stream.permits <- struct{}{}
	if got := nextDone(); got.name != "a2" || got.err != nil {
		t.Fatalf("third completion = %+v", got)
	}
}
