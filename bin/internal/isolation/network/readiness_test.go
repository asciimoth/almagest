package network

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/asciimoth/almagest/bin/internal/isolation/policy"
	"github.com/asciimoth/almagest/bin/internal/isolation/protocol"
)

func TestRuntimeApplicationReady(t *testing.T) {
	b, backend := testBroker(t)
	c := startClient(t, b)
	select {
	case <-b.ApplicationReady():
		t.Fatal("transport handshake reported application readiness")
	default:
	}
	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() {
			if err := c.MarkReady(context.Background()); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	select {
	case <-b.ApplicationReady():
	default:
		t.Fatal("application readiness was not published")
	}
	status, err := c.Status(context.Background())
	if err != nil || !status.RuntimeReady || status.LiveResources != 0 {
		t.Fatalf("status=%+v err=%v", status, err)
	}
	b.mu.Lock()
	receipts := len(b.receipts)
	b.mu.Unlock()
	if receipts != 0 || backend.count() != 0 {
		t.Fatal("readiness created a receipt or native resource")
	}
	if err := b.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	status, err = b.Status(context.Background(), protocol.ReadRequest{Meta: meta(b, "")})
	if err != nil || status.RuntimeReady {
		t.Fatalf("shutdown status=%+v err=%v", status, err)
	}
}

func TestRuntimeApplicationReadyRejectsInvalidState(t *testing.T) {
	for _, name := range []string{"nil_context", "canceled", "foreign_session", "stale_graph", "no_data", "recovery", "closed", "operation_id", "denied"} {
		t.Run(name, func(t *testing.T) {
			b, _ := testBroker(t)
			b.dataReady = true
			ctx := context.Background()
			req := protocol.ReadRequest{Meta: meta(b, "")}
			switch name {
			case "nil_context":
				ctx = nil
			case "canceled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			case "foreign_session":
				req.Meta.SessionID[0] ^= 1
			case "stale_graph":
				req.Meta.GraphEpoch++
			case "no_data":
				b.dataReady = false
			case "recovery":
				b.recovery = true
			case "closed":
				b.closed = true
			case "operation_id":
				req.Meta.OperationID = "not-a-mutation"
			case "denied":
				b.policy.NetworkRestrictions = &policy.NetworkRestrictions{DeniedOperations: []protocol.Operation{protocol.OperationRuntimeReady}}
			}
			if result, err := b.MarkRuntimeReady(ctx, req); err == nil || result.Ready {
				t.Fatalf("accepted invalid readiness: %+v, %v", result, err)
			}
			select {
			case <-b.ApplicationReady():
				t.Fatal("invalid request published readiness")
			default:
			}
		})
	}
}

func TestRuntimeApplicationReadyRejectsExtraFields(t *testing.T) {
	b, _ := testBroker(t)
	b.dataReady = true
	body, err := json.Marshal(protocol.ReadRequest{Meta: meta(b, "")})
	if err != nil {
		t.Fatal(err)
	}
	body = append(body[:len(body)-1], []byte(`,"Path":"/host","RestrictionsApplied":true}`)...)
	if _, err := b.Handle(context.Background(), protocol.OperationRuntimeReady, body); err == nil {
		t.Fatal("accepted extra authority claims")
	}
	select {
	case <-b.ApplicationReady():
		t.Fatal("invalid body published readiness")
	default:
	}
}
