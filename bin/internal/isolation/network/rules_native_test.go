package network

import (
	"errors"
	"reflect"
	"testing"

	"github.com/asciimoth/almagest/bin/internal/isolation/protocol"
	"github.com/asciimoth/gonnect/sysnet"
)

// The native callers are guarded by Docker/VM checks. Each comparison uses
// the same backend object and launch credentials; no optional policy is added.
func nativeRuleParity(t *testing.T, system sysnet.System) {
	t.Helper()
	b, _ := testBroker(t)
	b.backend = &SystemBackend{system: system}
	c := startClient(t, b)
	report := system.Capabilities()
	var uses []sysnet.RuleContext
	for _, family := range []sysnet.AddressFamily{sysnet.FamilyIPv4, sysnet.FamilyIPv6} {
		for _, transport := range []sysnet.Transport{sysnet.TransportTCP, sysnet.TransportUDP} {
			uses = append(uses, sysnet.RuleContext{Matcher: &sysnet.MatcherProfileKey{Family: family, Transport: transport}})
		}
	}
	for _, family := range []sysnet.AddressFamily{sysnet.FamilyIPv4, sysnet.FamilyIPv6, sysnet.FamilyDual} {
		for _, mode := range []sysnet.RoutingMode{sysnet.RoutingFull, sysnet.RoutingInclude, sysnet.RoutingExclude} {
			for _, strict := range []bool{false, true} {
				uses = append(uses, sysnet.RuleContext{Routing: &sysnet.RoutingProfileKey{Family: family, Mode: mode, Strict: strict}})
			}
		}
	}
	if len(report.Rules) == 0 {
		t.Fatal("native backend returned no rule inventory")
	}
	for _, definition := range report.Rules {
		t.Run(definition.Type, func(t *testing.T) {
			for _, use := range uses {
				for _, value := range []string{"0", "not-a-valid-numeric-id", "["} {
					rule := sysnet.Rule{Type: definition.Type, Rule: value}
					want := system.CheckRule(rule, use)
					got := c.CheckRule(rule, use)
					if got.CapabilityRevision != want.CapabilityRevision || len(got.Issues) != len(want.Issues) {
						t.Fatalf("%#v: direct=%#v remote=%#v", use, want, got)
					}
					for i, issue := range want.Issues {
						other := got.Issues[i]
						if issue.Path != other.Path || issue.Reason != other.Reason || issue.State != other.State {
							t.Fatalf("validation issue differs: direct=%#v remote=%#v", issue, other)
						}
					}
					for _, category := range []error{sysnet.ErrInvalidOptions, sysnet.ErrNotSupported, sysnet.ErrUnavailable, sysnet.ErrCapabilityUnknown} {
						if errors.Is(want.Err(), category) != errors.Is(got.Err(), category) {
							t.Fatalf("lost %v: direct=%v remote=%v", category, want.Err(), got.Err())
						}
					}
				}
				// A numeric prefix avoids comparisons against changing process listings.
				rule := sysnet.Rule{Type: definition.Type, Rule: "2147483647"}
				want, wantErr := system.CompleteRule(rule, use)
				got, gotErr := c.CompleteRule(rule, use)
				if (wantErr == nil) != (gotErr == nil) {
					t.Fatalf("completion errors: direct=%v remote=%v", wantErr, gotErr)
				}
				for _, category := range []error{sysnet.ErrInvalidOptions, sysnet.ErrNotSupported, sysnet.ErrUnavailable, sysnet.ErrCapabilityUnknown} {
					if errors.Is(wantErr, category) != errors.Is(gotErr, category) {
						t.Fatalf("completion lost %v: direct=%v remote=%v", category, wantErr, gotErr)
					}
				}
				if wantErr == nil && len(want) > 0 && !reflect.DeepEqual(got, want[:min(len(want), protocol.MaxRuleCompletionResults)]) {
					t.Fatalf("completion differs: direct=%v remote=%v", want, got)
				}
				if wantErr == nil && len(want) == 0 && len(got) != 0 {
					t.Fatal("unexpected completion values")
				}
			}
		})
	}
}
