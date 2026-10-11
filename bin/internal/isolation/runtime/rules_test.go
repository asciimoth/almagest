package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"reflect"
	"testing"
	"time"

	"github.com/asciimoth/almagest/bin/internal/isolation/protocol"
	"github.com/asciimoth/almagest/bin/internal/isolation/transport"
	"github.com/asciimoth/gonnect/sysnet"
)

func ruleTestClient(t *testing.T, handler transport.Handler) *Client {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	session := protocol.Session{ID: protocol.SessionID{1}, Epoch: 1, Version: protocol.Version}
	a, z := net.Pipe()
	t.Cleanup(func() { _ = a.Close(); _ = z.Close() })
	done := make(chan error, 1)
	go func() {
		peer, err := transport.NewControl(ctx, z, session, handler)
		if err == nil {
			<-peer.Done()
			_ = peer.Close()
		}
		done <- err
	}()
	peer, err := transport.NewControl(ctx, a, session, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = peer.Close()
		if err := <-done; err != nil {
			t.Error(err)
		}
	})
	c := &Client{peer: peer, session: session}
	c.epoch.Store(1)
	return c
}

func TestRuleClientRejectsMalformedReply(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value protocol.ValidationResult
	}{
		{"state", protocol.ValidationResult{Issues: []protocol.ValidationIssue{{State: "invented"}}}},
		{"code", protocol.ValidationResult{Issues: []protocol.ValidationIssue{{State: protocol.CapabilitySupported, Code: "exec"}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := ruleTestClient(t, func(context.Context, protocol.Operation, json.RawMessage) (any, error) { return tc.value, nil })
			if got := c.CheckRule(sysnet.Rule{}, sysnet.RuleContext{}); !errors.Is(got.Err(), sysnet.ErrInvalidOptions) {
				t.Fatalf("malformed report = %v", got.Err())
			}
		})
	}
	for _, tc := range []struct {
		name   string
		values []protocol.Rule
		want   error
	}{
		{"foreign_type", []protocol.Rule{{Type: "exec", Value: "value"}}, protocol.ErrInvalidContract},
		{"size", make([]protocol.Rule, protocol.MaxRuleCompletionResults+1), protocol.ErrLimitExceeded},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := ruleTestClient(t, func(context.Context, protocol.Operation, json.RawMessage) (any, error) {
				return protocol.RuleCompletionResult{Rules: tc.values}, nil
			})
			if _, err := c.CompleteRule(sysnet.Rule{Type: "pid"}, sysnet.RuleContext{}); !errors.Is(err, tc.want) {
				t.Fatalf("malformed completion = %v", err)
			}
		})
	}
}

func TestRuleClientPreservesContextOwnershipAndErrors(t *testing.T) {
	source := sysnet.RuleContext{Routing: &sysnet.RoutingProfileKey{Family: sysnet.FamilyDual, Mode: sysnet.RoutingInclude, Strict: true}, Matcher: &sysnet.MatcherProfileKey{Family: sysnet.FamilyIPv6, Transport: sysnet.TransportUDP}}
	use := ruleContext(source)
	source.Routing.Strict = false
	source.Matcher.Transport = sysnet.TransportTCP
	if !use.Routing.Strict || use.Matcher.Protocol != "udp" {
		t.Fatal("retained caller pointers")
	}
	for _, tc := range []struct{ err, want error }{
		{protocol.ErrInvalidContract, sysnet.ErrInvalidOptions},
		{protocol.ErrCapabilityUnknown, sysnet.ErrCapabilityUnknown},
		{protocol.ErrCapabilityUnsupported, sysnet.ErrNotSupported},
		{protocol.ErrCapabilityUnavailable, sysnet.ErrUnavailable},
		{protocol.ErrPolicyDenied, protocol.ErrPolicyDenied},
	} {
		c := ruleTestClient(t, func(context.Context, protocol.Operation, json.RawMessage) (any, error) { return nil, tc.err })
		if err := c.CheckRule(sysnet.Rule{}, sysnet.RuleContext{}).Err(); !errors.Is(err, tc.want) {
			t.Fatalf("check lost %v: %v", tc.want, err)
		}
		if _, err := c.CompleteRule(sysnet.Rule{}, sysnet.RuleContext{}); !errors.Is(err, tc.want) {
			t.Fatalf("completion lost %v: %v", tc.want, err)
		}
	}
}

func FuzzRuleValidationReply(f *testing.F) {
	f.Add(`{"CapabilityRevision":1,"Issues":[{"State":"unknown","Code":"unknown"}]}`)
	f.Add(`{"Issues":[{"State":"supported","Code":"invalid"}]}`)
	f.Add(`{"Issues":[{"State":"forged","Code":""}]}`)
	f.Fuzz(func(t *testing.T, data string) {
		var wire protocol.ValidationResult
		if transport.Decode([]byte(data), &wire) != nil {
			return
		}
		got := ruleValidationReport(wire)
		if len(wire.Issues) > 0 && got.Err() == nil {
			t.Fatal("issues became a successful report")
		}
		for _, issue := range wire.Issues {
			switch issue.State {
			case protocol.CapabilityUnknown, protocol.CapabilitySupported, protocol.CapabilityUnavailable, protocol.CapabilityUnsupported:
			default:
				if !errors.Is(got.Err(), sysnet.ErrInvalidOptions) {
					t.Fatal("accepted invalid state")
				}
				return
			}
			switch issue.Code {
			case "", "unknown", "invalid", "unsupported", "unavailable":
			default:
				if !errors.Is(got.Err(), sysnet.ErrInvalidOptions) {
					t.Fatal("accepted invalid code")
				}
				return
			}
		}
		if got.CapabilityRevision != wire.CapabilityRevision || len(got.Issues) != len(wire.Issues) {
			t.Fatal("lost validation metadata")
		}
		// Repeated decoding must not retain or mutate caller-owned memory.
		if again := ruleValidationReport(wire); !reflect.DeepEqual(got, again) {
			t.Fatal("unstable validation conversion")
		}
	})
}
