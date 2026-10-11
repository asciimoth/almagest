package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/asciimoth/almagest/bin/internal/isolation/protocol"
)

func TestApplicationReadyReply(t *testing.T) {
	for _, name := range []string{"valid", "not_ready", "foreign_session", "epoch", "version", "policy", "denied"} {
		t.Run(name, func(t *testing.T) {
			c := ruleTestClient(t, func(_ context.Context, op protocol.Operation, body json.RawMessage) (any, error) {
				if op != protocol.OperationRuntimeReady {
					t.Errorf("unexpected operation %s", op)
				}
				var req protocol.ReadRequest
				if err := json.Unmarshal(body, &req); err != nil {
					return nil, err
				}
				r := protocol.RuntimeReadyResult{Session: protocol.Session{ID: req.Meta.SessionID, Epoch: req.Meta.SessionEpoch, Version: protocol.Version}, Ready: true}
				switch name {
				case "not_ready":
					r.Ready = false
				case "foreign_session":
					r.Session.ID[0]++
				case "epoch":
					r.Session.Epoch++
				case "version":
					r.Session.Version++
				case "policy":
					r.Session.PolicyDigest[0]++
				case "denied":
					return nil, protocol.ErrPolicyDenied
				}
				return r, nil
			})
			err := c.MarkReady(context.Background())
			if name == "valid" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil {
				t.Fatal("invalid readiness reply accepted")
			}
		})
	}
}

func TestApplicationReadyCanceled(t *testing.T) {
	c := ruleTestClient(t, func(context.Context, protocol.Operation, json.RawMessage) (any, error) {
		return nil, protocol.ErrPolicyDenied
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := c.MarkReady(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled startup: %v", err)
	}
}
