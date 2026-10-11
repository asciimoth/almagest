package commandruntime

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	cmdrunner "github.com/asciimoth/almagest/bin/almagestd/command"
	"github.com/asciimoth/almagest/bin/internal/isolation/protocol"
	"github.com/asciimoth/almagest/bin/internal/isolation/transport"
)

type commandHandler struct {
	mu       sync.Mutex
	requests []protocol.NodeCommandRequest
	block    <-chan struct{}
	fail     error
}

func (h *commandHandler) handle(ctx context.Context, operation protocol.Operation, body json.RawMessage) (any, error) {
	if operation != protocol.OperationRunNodeCommand {
		return nil, protocol.ErrUnsupportedOperation
	}
	var request protocol.NodeCommandRequest
	if err := transport.Decode(body, &request); err != nil {
		return nil, err
	}
	h.mu.Lock()
	h.requests = append(h.requests, request)
	h.mu.Unlock()
	if h.block != nil {
		select {
		case <-h.block:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	result := protocol.CommandResult{MutationResult: protocol.MutationResult{OperationID: request.Meta.OperationID, Outcome: protocol.OutcomeCommitted}}
	if h.fail != nil {
		result.Outcome = protocol.OutcomeRejected
		result.Code = transport.ErrorCode(h.fail)
	}
	return result, nil
}

func commandSession(t *testing.T) protocol.Session {
	t.Helper()
	id, err := protocol.NewSessionID()
	if err != nil {
		t.Fatal(err)
	}
	return protocol.Session{ID: id, Epoch: 1, Version: protocol.Version}
}

func commandPeer(t *testing.T, handler *commandHandler) (*transport.Peer, protocol.Session, context.CancelFunc) {
	t.Helper()
	session := commandSession(t)
	left, right := net.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	server := make(chan *transport.Peer, 1)
	errs := make(chan error, 1)
	go func() {
		peer, err := transport.NewControl(ctx, left, session, handler.handle)
		server <- peer
		errs <- err
	}()
	peer, err := transport.NewControl(ctx, right, session, nil)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	if err := <-errs; err != nil {
		cancel()
		t.Fatal(err)
	}
	remote := <-server
	t.Cleanup(func() {
		cancel()
		_ = peer.Close()
		_ = remote.Close()
	})
	return peer, session, cancel
}

func TestDispatcherPreservesClosedCommandSurface(t *testing.T) {
	handler := &commandHandler{}
	peer, session, _ := commandPeer(t, handler)
	dispatcher, err := New(context.Background(), peer, session, 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer dispatcher.Close()
	slots := map[string]string{"name": "alpha"}
	if !dispatcher.SubmitNodeCommand(cmdrunner.Request{Name: "fixed", Label: "must-not-cross", Slots: slots}) {
		t.Fatal("request was not accepted")
	}
	slots["name"] = "changed"
	deadline := time.Now().Add(time.Second)
	for {
		handler.mu.Lock()
		if len(handler.requests) != 0 {
			request := handler.requests[0]
			handler.mu.Unlock()
			if request.CommandID != "fixed" || request.Slots["name"] != "alpha" {
				t.Fatalf("request=%+v", request)
			}
			return
		}
		handler.mu.Unlock()
		if time.Now().After(deadline) {
			t.Fatal("request did not reach helper")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestDispatcherCloseCancelsAndRejects(t *testing.T) {
	blocked := make(chan struct{})
	handler := &commandHandler{block: blocked}
	peer, session, _ := commandPeer(t, handler)
	dispatcher, err := New(context.Background(), peer, session, 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !dispatcher.SubmitNodeCommand(cmdrunner.Request{Name: "blocked"}) {
		t.Fatal("request was not accepted")
	}
	done := make(chan struct{})
	go func() { dispatcher.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("close did not cancel a blocked request")
	}
	if dispatcher.SubmitNodeCommand(cmdrunner.Request{Name: "late"}) {
		t.Fatal("closed dispatcher accepted a request")
	}
	close(blocked)
}

func TestDispatcherRejectsInvalidConstruction(t *testing.T) {
	if _, err := New(context.Background(), nil, protocol.Session{}, 0, nil); !errors.Is(err, protocol.ErrInvalidContract) {
		t.Fatalf("invalid construction=%v", err)
	}
}
