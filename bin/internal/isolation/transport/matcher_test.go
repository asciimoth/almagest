package transport

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/asciimoth/almagest/bin/internal/isolation/protocol"
)

func TestMatcherReservesControlCapacity(t *testing.T) {
	p := statePeer(t)
	for id := uint64(1); id <= protocol.MaxBulkConcurrentRequests; id++ {
		p.active[id] = func() {}
		p.pending[id] = make(chan message, 1)
	}
	q := protocol.MatchRequest{Meta: protocol.RequestMeta{RequestID: 1, SessionID: p.session.ID, SessionEpoch: p.session.Epoch, GraphEpoch: 1}}
	body, err := json.Marshal(q)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.receive(message{Kind: "request", ID: 1, Operation: protocol.OperationMatch, Body: body}); err != nil {
		t.Fatal(err)
	}
	if len(p.active) != protocol.MaxBulkConcurrentRequests || p.received != 1 || len(p.terminal) != 1 {
		t.Fatal("matcher used reserved control capacity or lost its rejection reply")
	}
	var result protocol.MatchResult
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := p.Call(ctx, protocol.OperationMatch, q, &result); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	if p.err != nil {
		t.Fatal("lookup overload closed the control session")
	}
}

func TestBulkCallWaitsForCapacity(t *testing.T) {
	p := statePeer(t)
	for id := uint64(1); id <= protocol.MaxBulkConcurrentRequests; id++ {
		p.pending[id] = make(chan message, 1)
	}
	p.next = protocol.MaxBulkConcurrentRequests
	q := protocol.MatchRequest{Meta: protocol.RequestMeta{GraphEpoch: 1}}
	done := make(chan error, 1)
	go func() {
		done <- p.Call(context.Background(), protocol.OperationMatch, q, new(protocol.MatchResult))
	}()
	select {
	case <-p.out:
		t.Fatal("bulk call did not wait for capacity")
	case <-time.After(20 * time.Millisecond):
	}
	if err := p.receive(message{Kind: "reply", ID: 1, Body: json.RawMessage(`{}`)}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-p.out:
	case <-time.After(time.Second):
		t.Fatal("bulk call did not resume after capacity was released")
	}
	id := uint64(protocol.MaxBulkConcurrentRequests + 1)
	if err := p.receive(message{Kind: "reply", ID: id, Body: json.RawMessage(`{}`)}); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("resumed bulk call did not complete")
	}
}
