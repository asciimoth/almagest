package transport

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/asciimoth/almagest/bin/internal/isolation/protocol"
)

func TestMatcherReservesControlCapacity(t *testing.T) {
	p := statePeer(t)
	for id := uint64(1); id <= 96; id++ {
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
	if len(p.active) != 96 || p.received != 1 || len(p.terminal) != 1 {
		t.Fatal("matcher used reserved control capacity or lost its rejection reply")
	}
	var result protocol.MatchResult
	if err := p.Call(context.Background(), protocol.OperationMatch, q, &result); !errors.Is(err, protocol.ErrLimitExceeded) {
		t.Fatal(err)
	}
	if p.err != nil {
		t.Fatal("lookup overload closed the control session")
	}
}
