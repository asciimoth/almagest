package network

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/asciimoth/almagest/bin/internal/isolation/policy"
	"github.com/asciimoth/almagest/bin/internal/isolation/protocol"
	"github.com/asciimoth/gonnect/sysnet"
)

type ruleBackend struct {
	*fakeBackend
	calls    atomic.Int32
	check    func(context.Context, protocol.Rule, protocol.RuleContext) (protocol.ValidationResult, error)
	complete func(context.Context, protocol.Rule, protocol.RuleContext) ([]string, error)
}

func (f *ruleBackend) CheckRule(ctx context.Context, r protocol.Rule, c protocol.RuleContext) (protocol.ValidationResult, error) {
	f.calls.Add(1)
	if f.check != nil {
		return f.check(ctx, r, c)
	}
	return protocol.ValidationResult{CapabilityRevision: 42}, nil
}
func (f *ruleBackend) CompleteRule(ctx context.Context, r protocol.Rule, c protocol.RuleContext) ([]string, error) {
	f.calls.Add(1)
	if f.complete != nil {
		return f.complete(ctx, r, c)
	}
	return []string{"alice", "bob"}, nil
}
func ruleBroker(t *testing.T) (*Broker, *ruleBackend) {
	t.Helper()
	b, backend := testBroker(t)
	r := &ruleBackend{fakeBackend: backend}
	b.backend = r
	return b, r
}
func ruleUse() protocol.RuleContext {
	return protocol.RuleContext{Matcher: &protocol.MatcherContext{Family: "ipv4", Protocol: "tcp"}}
}
func checkRequest(b *Broker) protocol.CheckRuleRequest {
	return protocol.CheckRuleRequest{Meta: meta(b, ""), Rule: protocol.Rule{Type: "user", Value: "alice"}, Context: ruleUse()}
}

func TestRuleControlDispatchAndContext(t *testing.T) {
	b, backend := ruleBroker(t)
	for _, use := range []protocol.RuleContext{
		ruleUse(),
		{Matcher: &protocol.MatcherContext{Family: "ipv6", Protocol: "udp"}},
		{Routing: &protocol.RoutingContext{Family: "dual", Mode: "include", Strict: true}},
		{Routing: &protocol.RoutingContext{Family: "ipv4", Mode: "exclude"}},
	} {
		req := checkRequest(b)
		req.Context = use
		backend.check = func(_ context.Context, r protocol.Rule, c protocol.RuleContext) (protocol.ValidationResult, error) {
			if r != req.Rule || !reflect.DeepEqual(c, use) {
				t.Errorf("changed native request: %#v %#v", r, c)
			}
			return protocol.ValidationResult{CapabilityRevision: 42}, nil
		}
		body, err := json.Marshal(req)
		if err != nil {
			t.Fatal(err)
		}
		got, err := b.Handle(context.Background(), protocol.OperationCheckRule, body)
		if err != nil || got.(protocol.ValidationResult).CapabilityRevision != 42 {
			t.Fatalf("check = %#v, %v", got, err)
		}
		complete := protocol.CompleteRuleRequest{Meta: meta(b, ""), Rule: req.Rule, Context: use, Limit: 1}
		body, err = json.Marshal(complete)
		if err != nil {
			t.Fatal(err)
		}
		got, err = b.Handle(context.Background(), protocol.OperationCompleteRule, body)
		if err != nil || !reflect.DeepEqual(got, protocol.RuleCompletionResult{Rules: []protocol.Rule{{Type: "user", Value: "alice"}}}) {
			t.Fatalf("completion = %#v, %v", got, err)
		}
	}
	if backend.calls.Load() != 8 {
		t.Fatal("wrong backend call count")
	}
}

func TestRuleControlRejectsAuthorityAndMalformedInput(t *testing.T) {
	for name, change := range map[string]func(*protocol.CheckRuleRequest){
		"foreign_session": func(r *protocol.CheckRuleRequest) { r.Meta.SessionID[0]++ },
		"old_session":     func(r *protocol.CheckRuleRequest) { r.Meta.SessionEpoch++ },
		"old_graph":       func(r *protocol.CheckRuleRequest) { r.Meta.GraphEpoch++ },
		"mutation_id":     func(r *protocol.CheckRuleRequest) { r.Meta.OperationID = "forged" },
		"no_request_id":   func(r *protocol.CheckRuleRequest) { r.Meta.RequestID = 0 },
		"no_context":      func(r *protocol.CheckRuleRequest) { r.Context = protocol.RuleContext{} },
		"two_contexts": func(r *protocol.CheckRuleRequest) {
			r.Context.Routing = &protocol.RoutingContext{Family: "ipv4", Mode: "include"}
		},
		"family":   func(r *protocol.CheckRuleRequest) { r.Context.Matcher.Family = "host" },
		"protocol": func(r *protocol.CheckRuleRequest) { r.Context.Matcher.Protocol = "raw" },
		"routing_mode": func(r *protocol.CheckRuleRequest) {
			r.Context = protocol.RuleContext{Routing: &protocol.RoutingContext{Family: "ipv4", Mode: "exec"}}
		},
		"no_type":  func(r *protocol.CheckRuleRequest) { r.Rule.Type = "" },
		"nul":      func(r *protocol.CheckRuleRequest) { r.Rule.Value = "a\x00b" },
		"encoding": func(r *protocol.CheckRuleRequest) { r.Rule.Value = "\xff" },
		"size":     func(r *protocol.CheckRuleRequest) { r.Rule.Value = strings.Repeat("a", 65537) },
	} {
		t.Run(name, func(t *testing.T) {
			b, backend := ruleBroker(t)
			req := checkRequest(b)
			change(&req)
			if _, err := b.CheckRule(context.Background(), req); err == nil {
				t.Fatal("accepted invalid check")
			}
			if _, err := b.CompleteRule(context.Background(), protocol.CompleteRuleRequest{Meta: req.Meta, Rule: req.Rule, Context: req.Context, Limit: 1}); err == nil {
				t.Fatal("accepted invalid completion")
			}
			if backend.calls.Load() != 0 {
				t.Fatal("invalid request reached backend")
			}
		})
	}
	for _, op := range []protocol.Operation{protocol.OperationCheckRule, protocol.OperationCompleteRule} {
		b, backend := ruleBroker(t)
		for _, body := range []string{`{`, `{"Context":"matcher"}`, `{"Context":{},"Context":{}}`, `{"argv":["sh"]}`} {
			if _, err := b.Handle(context.Background(), op, json.RawMessage(body)); err == nil {
				t.Fatalf("accepted %s", body)
			}
		}
		if backend.calls.Load() != 0 {
			t.Fatal("malformed frame reached backend")
		}
	}
}

func TestRuleControlRestrictionsAndBounds(t *testing.T) {
	for _, kind := range []string{"type", "operation", "route", "strict"} {
		t.Run(kind, func(t *testing.T) {
			b, backend := ruleBroker(t)
			denied := false
			b.policy.NetworkRestrictions = &policy.NetworkRestrictions{}
			req := checkRequest(b)
			switch kind {
			case "type":
				b.policy.NetworkRestrictions.DeniedRuleTypes = []string{"user"}
				req.Rule.Type = " user "
			case "operation":
				b.policy.NetworkRestrictions.DeniedOperations = []protocol.Operation{protocol.OperationCheckRule, protocol.OperationCompleteRule}
			case "route":
				b.policy.NetworkRestrictions.AllowDefaultRoute = &denied
				req.Context = protocol.RuleContext{Routing: &protocol.RoutingContext{Family: "dual", Mode: "include"}}
			case "strict":
				b.policy.NetworkRestrictions.AllowStrictMode = &denied
				req.Context = protocol.RuleContext{Routing: &protocol.RoutingContext{Family: "ipv6", Mode: "exclude", Strict: true}}
			}
			if _, err := b.CheckRule(context.Background(), req); !errors.Is(err, protocol.ErrPolicyDenied) {
				t.Fatalf("check: %v", err)
			}
			if _, err := b.CompleteRule(context.Background(), protocol.CompleteRuleRequest{Meta: req.Meta, Rule: req.Rule, Context: req.Context, Limit: 1}); !errors.Is(err, protocol.ErrPolicyDenied) {
				t.Fatalf("complete: %v", err)
			}
			if backend.calls.Load() != 0 {
				t.Fatal("denied request reached backend")
			}
		})
	}
	b, backend := ruleBroker(t)
	req := checkRequest(b)
	complete := protocol.CompleteRuleRequest{Meta: req.Meta, Rule: req.Rule, Context: req.Context, Limit: 1}
	for _, limit := range []uint16{0, protocol.MaxRuleCompletionResults + 1} {
		complete.Limit = limit
		if _, err := b.CompleteRule(context.Background(), complete); !errors.Is(err, protocol.ErrInvalidContract) {
			t.Fatalf("limit: %v", err)
		}
	}
	if backend.calls.Load() != 0 {
		t.Fatal("invalid limit reached backend")
	}
	complete.Limit = protocol.MaxRuleCompletionResults
	for _, value := range []string{"\xff", "a\x00b", strings.Repeat("x", int(b.policy.Limits.MaxStringBytes)+1)} {
		backend.complete = func(context.Context, protocol.Rule, protocol.RuleContext) ([]string, error) {
			return []string{value}, nil
		}
		if _, err := b.CompleteRule(context.Background(), complete); !errors.Is(err, protocol.ErrLimitExceeded) {
			t.Fatalf("output bound: %v", err)
		}
	}
	backend.complete = func(context.Context, protocol.Rule, protocol.RuleContext) ([]string, error) {
		return make([]string, 1000), nil
	}
	got, err := b.CompleteRule(context.Background(), complete)
	if err != nil || len(got.Rules) != protocol.MaxRuleCompletionResults {
		t.Fatalf("completion bound: %d, %v", len(got.Rules), err)
	}
	backend.check = func(context.Context, protocol.Rule, protocol.RuleContext) (protocol.ValidationResult, error) {
		return protocol.ValidationResult{Warnings: []string{strings.Repeat("x", protocol.MaxControlFrameBytes)}}, nil
	}
	if _, err := b.CheckRule(context.Background(), req); !errors.Is(err, protocol.ErrLimitExceeded) {
		t.Fatalf("validation bound: %v", err)
	}
}

func TestRuleControlFailureCancellationAndRevocation(t *testing.T) {
	t.Run("unsupported", func(t *testing.T) {
		b, _ := testBroker(t)
		req := checkRequest(b)
		if _, err := b.CheckRule(context.Background(), req); !errors.Is(err, protocol.ErrCapabilityUnsupported) {
			t.Fatal(err)
		}
		if _, err := b.CompleteRule(context.Background(), protocol.CompleteRuleRequest{Meta: req.Meta, Rule: req.Rule, Context: req.Context, Limit: 1}); !errors.Is(err, protocol.ErrCapabilityUnsupported) {
			t.Fatal(err)
		}
	})
	for _, failure := range []error{protocol.ErrCapabilityUnavailable, context.Canceled, protocol.ErrCapabilityUnsupported} {
		b, backend := ruleBroker(t)
		req := checkRequest(b)
		backend.check = func(context.Context, protocol.Rule, protocol.RuleContext) (protocol.ValidationResult, error) {
			return protocol.ValidationResult{}, failure
		}
		backend.complete = func(context.Context, protocol.Rule, protocol.RuleContext) ([]string, error) { return nil, failure }
		if _, err := b.CheckRule(context.Background(), req); !errors.Is(err, failure) {
			t.Fatal(err)
		}
		if _, err := b.CompleteRule(context.Background(), protocol.CompleteRuleRequest{Meta: req.Meta, Rule: req.Rule, Context: req.Context, Limit: 1}); !errors.Is(err, failure) {
			t.Fatal(err)
		}
	}
	for _, operation := range []string{"check", "complete"} {
		for _, mode := range []string{"cancel", "epoch", "closed", "timeout"} {
			t.Run(operation+"/"+mode, func(t *testing.T) {
				b, backend := ruleBroker(t)
				b.timeout = 40 * time.Millisecond
				started, release := make(chan struct{}), make(chan struct{})
				backend.check = func(context.Context, protocol.Rule, protocol.RuleContext) (protocol.ValidationResult, error) {
					close(started)
					<-release
					return protocol.ValidationResult{}, nil
				}
				backend.complete = func(ctx context.Context, rule protocol.Rule, use protocol.RuleContext) ([]string, error) {
					_, err := backend.check(ctx, rule, use)
					return []string{"late"}, err
				}
				call := func(ctx context.Context) error {
					req := checkRequest(b)
					if operation == "complete" {
						_, err := b.CompleteRule(ctx, protocol.CompleteRuleRequest{Meta: req.Meta, Rule: req.Rule, Context: req.Context, Limit: 1})
						return err
					}
					_, err := b.CheckRule(ctx, req)
					return err
				}
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				done := make(chan error, 1)
				go func() { done <- call(ctx) }()
				<-started
				want := error(context.Canceled)
				switch mode {
				case "cancel":
					cancel()
				case "epoch":
					b.mu.Lock()
					b.epoch++
					b.mu.Unlock()
					want = protocol.ErrStaleGraphEpoch
				case "closed":
					b.mu.Lock()
					b.closed = true
					b.mu.Unlock()
					want = protocol.ErrCapabilityUnavailable
				case "timeout":
					want = context.DeadlineExceeded
				}
				if mode != "timeout" {
					close(release)
				}
				select {
				case err := <-done:
					if !errors.Is(err, want) {
						t.Fatalf("late read = %v, want %v", err, want)
					}
				case <-time.After(time.Second):
					t.Fatal("read did not stop")
				}
				if mode == "timeout" {
					// A stuck backend retains the single worker; retries cannot start more.
					if err := call(context.Background()); !errors.Is(err, context.DeadlineExceeded) {
						t.Fatal(err)
					}
					if backend.calls.Load() != 1 {
						t.Fatal("unbounded native workers")
					}
					close(release)
				}
				// Synchronize with the native worker before test cleanup.
				b.gate <- struct{}{}
				<-b.gate
			})
		}
	}
}

func TestRuleValidationPreservesCategoriesWithoutNativeDetails(t *testing.T) {
	for _, tc := range []struct {
		state sysnet.CapabilityState
		cause error
		code  string
	}{
		{sysnet.CapabilityUnknown, nil, "unknown"},
		{sysnet.CapabilityUnsupported, nil, "unsupported"},
		{sysnet.CapabilityUnavailable, nil, "unavailable"},
		{sysnet.CapabilityAvailable, sysnet.ErrInvalidOptions, "invalid"},
		{sysnet.CapabilityUnknown, sysnet.ErrInvalidOptions, "invalid"},
	} {
		result := validationResult(sysnet.ValidationReport{CapabilityRevision: 7, Issues: []sysnet.ValidationIssue{{Path: "Rule", Reason: sysnet.ReasonProbeFailed, State: tc.state, Err: tc.cause, Detail: "secret host path"}}})
		encoded, err := json.Marshal(result)
		if err != nil || strings.Contains(string(encoded), "secret") || result.CapabilityRevision != 7 || len(result.Issues) != 1 || result.Issues[0].Code != tc.code {
			t.Fatalf("validation = %s, %v", encoded, err)
		}
	}
}

func FuzzRuleContext(f *testing.F) {
	f.Add("ipv4", "tcp", "include", false, true, false)
	f.Add("dual", "udp", "exclude", true, false, true)
	f.Fuzz(func(t *testing.T, family, transport, mode string, strict, matcher, routing bool) {
		use := protocol.RuleContext{}
		if matcher {
			use.Matcher = &protocol.MatcherContext{Family: family, Protocol: transport}
		}
		if routing {
			use.Routing = &protocol.RoutingContext{Family: family, Mode: mode, Strict: strict}
		}
		result, err := nativeRuleContext(use)
		valid := matcher != routing
		if matcher {
			valid = valid && (family == "ipv4" || family == "ipv6") && (transport == "tcp" || transport == "udp")
		}
		if routing {
			valid = valid && (family == "ipv4" || family == "ipv6" || family == "dual") && (mode == "full" || mode == "include" || mode == "exclude")
		}
		if (err == nil) != valid {
			t.Fatalf("context accepted=%v want=%v: %#v", err == nil, valid, use)
		}
		if err == nil && matcher && (string(result.Matcher.Family) != family || string(result.Matcher.Transport) != transport) {
			t.Fatal("matcher context changed")
		}
		if err == nil && routing && (string(result.Routing.Family) != family || string(result.Routing.Mode) != mode || result.Routing.Strict != strict) {
			t.Fatal("routing context changed")
		}
	})
}

// ruleSystem uses the production SystemBackend and wire client without native
// network effects. The only overridden methods are the native rule contract.
type ruleSystem struct {
	sysnet.System
	report sysnet.ValidationReport
	values []string
	err    error
	use    sysnet.RuleContext
}

func (s *ruleSystem) CheckRule(_ sysnet.Rule, c sysnet.RuleContext) sysnet.ValidationReport {
	s.use = c
	return s.report.Clone()
}
func (s *ruleSystem) CompleteRule(_ sysnet.Rule, c sysnet.RuleContext) ([]string, error) {
	s.use = c
	return s.values, s.err
}

func TestRuleRuntimeWireParityAndPeerLoss(t *testing.T) {
	for _, state := range []sysnet.CapabilityState{sysnet.CapabilityAvailable, sysnet.CapabilityUnknown, sysnet.CapabilityUnavailable, sysnet.CapabilityUnsupported} {
		t.Run(string(rune('0'+state)), func(t *testing.T) {
			b, _ := testBroker(t)
			system := &ruleSystem{report: sysnet.ValidationReport{CapabilityRevision: 27}, values: []string{"alice", "bob"}}
			if state != sysnet.CapabilityAvailable {
				system.report.Issues = []sysnet.ValidationIssue{{Path: "Context", State: state, Reason: sysnet.ReasonProbeNotRun}}
			}
			b.backend = &SystemBackend{system: system}
			c := startClient(t, b)
			rule := sysnet.Rule{Type: "user", Rule: "a"}
			use := sysnet.RuleContext{Routing: &sysnet.RoutingProfileKey{Family: sysnet.FamilyDual, Mode: sysnet.RoutingInclude, Strict: true}}
			got := c.CheckRule(rule, use)
			if got.CapabilityRevision != 27 || len(got.Issues) != len(system.report.Issues) || !reflect.DeepEqual(system.use, use) {
				t.Fatalf("report=%#v context=%#v", got, system.use)
			}
			for _, category := range []error{sysnet.ErrCapabilityUnknown, sysnet.ErrUnavailable, sysnet.ErrNotSupported, sysnet.ErrInvalidOptions} {
				if errors.Is(got.Err(), category) != errors.Is(system.report.Err(), category) {
					t.Fatalf("lost category %v: %v", category, got.Err())
				}
			}
			values, err := c.CompleteRule(rule, use)
			if err != nil || !reflect.DeepEqual(values, system.values) {
				t.Fatalf("completion=%v %v", values, err)
			}
			for _, category := range []error{sysnet.ErrCapabilityUnknown, sysnet.ErrUnavailable, sysnet.ErrNotSupported, sysnet.ErrInvalidOptions} {
				system.err = category
				if _, err := c.CompleteRule(rule, use); !errors.Is(err, category) {
					t.Fatalf("completion lost %v: %v", category, err)
				}
			}
			if err := c.Close(); err != nil {
				t.Fatal(err)
			}
			if report := c.CheckRule(rule, use); report.Err() == nil {
				t.Fatal("dead broker returned successful validation")
			}
			if _, err := c.CompleteRule(rule, use); err == nil {
				t.Fatal("dead broker returned successful completion")
			}
		})
	}
}

func TestRuleControlCanceledBeforeDispatch(t *testing.T) {
	b, backend := ruleBroker(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for range 20 {
		if _, err := b.CheckRule(ctx, checkRequest(b)); !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	}
	if backend.calls.Load() != 0 {
		t.Fatal("pre-canceled reads reached the native backend")
	}
}

func TestRuleControlCapabilityRestrictions(t *testing.T) {
	for _, kind := range []string{"type", "validation", "completion"} {
		t.Run(kind, func(t *testing.T) {
			b, backend := ruleBroker(t)
			available := protocol.Capability{State: protocol.CapabilitySupported, AttemptAllowed: true}
			backend.caps = &protocol.CapabilitySnapshot{SchemaVersion: 1, Rules: []protocol.RuleCapability{{Type: "uid", Validation: available, Completion: available}}}
			b.policy.NetworkRestrictions = &policy.NetworkRestrictions{}
			switch kind {
			case "type":
				b.policy.NetworkRestrictions.DeniedRuleTypes = []string{" uid "}
			case "validation":
				b.policy.NetworkRestrictions.DeniedOperations = []protocol.Operation{protocol.OperationCheckRule}
			case "completion":
				b.policy.NetworkRestrictions.DeniedOperations = []protocol.Operation{protocol.OperationCompleteRule}
			}
			report, err := b.Capabilities(context.Background(), protocol.ReadRequest{Meta: meta(b, "")})
			if err != nil || len(report.Rules) != 1 {
				t.Fatalf("report: %#v %v", report, err)
			}
			rule := report.Rules[0]
			if rule.Validation.CanAttempt() != (kind == "completion") || rule.Completion.CanAttempt() != (kind == "validation") {
				t.Fatalf("incorrect effective capabilities: %#v", rule)
			}
		})
	}
}
