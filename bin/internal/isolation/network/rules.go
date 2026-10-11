package network

import (
	"context"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/asciimoth/almagest/bin/internal/isolation/protocol"
	"github.com/asciimoth/gonnect/sysnet"
)

// RuleBackend validates and completes rules in the same exact context as the
// direct backend. It grants no general process inspection interface to R.
type RuleBackend interface {
	CheckRule(context.Context, protocol.Rule, protocol.RuleContext) (protocol.ValidationResult, error)
	CompleteRule(context.Context, protocol.Rule, protocol.RuleContext) ([]string, error)
}

func nativeRuleContext(c protocol.RuleContext) (sysnet.RuleContext, error) {
	if (c.Routing == nil) == (c.Matcher == nil) {
		return sysnet.RuleContext{}, protocol.ErrInvalidContract
	}
	if c.Routing != nil {
		r := c.Routing
		if (!ruleFamily(r.Family) && r.Family != "dual") || (r.Mode != "full" && r.Mode != "include" && r.Mode != "exclude") {
			return sysnet.RuleContext{}, protocol.ErrInvalidContract
		}
		return sysnet.RuleContext{Routing: &sysnet.RoutingProfileKey{Family: sysnet.AddressFamily(r.Family), Mode: sysnet.RoutingMode(r.Mode), Strict: r.Strict}}, nil
	}
	m := c.Matcher
	if !ruleFamily(m.Family) || (m.Protocol != "tcp" && m.Protocol != "udp") {
		return sysnet.RuleContext{}, protocol.ErrInvalidContract
	}
	return sysnet.RuleContext{Matcher: &sysnet.MatcherProfileKey{Family: sysnet.AddressFamily(m.Family), Transport: sysnet.Transport(m.Protocol)}}, nil
}

func ruleFamily(f string) bool { return f == "ipv4" || f == "ipv6" }

func (b *Broker) validateRuleContext(rule protocol.Rule, c protocol.RuleContext) error {
	if err := b.validateMatcherRule(rule); err != nil {
		return err
	}
	if _, err := nativeRuleContext(c); err != nil {
		return err
	}
	if p := b.policy.NetworkRestrictions; p != nil && c.Routing != nil {
		if p.AllowDefaultRoute != nil && !*p.AllowDefaultRoute || c.Routing.Strict && p.AllowStrictMode != nil && !*p.AllowStrictMode {
			return protocol.ErrPolicyDenied
		}
	}
	return nil
}

func (b *Broker) CheckRule(ctx context.Context, req protocol.CheckRuleRequest) (protocol.ValidationResult, error) {
	return read(ctx, b, req.Meta, protocol.OperationCheckRule, func(ctx context.Context) (protocol.ValidationResult, error) {
		if err := b.validateRuleContext(req.Rule, req.Context); err != nil {
			return protocol.ValidationResult{}, err
		}
		backend, ok := b.backend.(RuleBackend)
		if !ok {
			return protocol.ValidationResult{}, protocol.ErrCapabilityUnsupported
		}
		result, err := backend.CheckRule(ctx, req.Rule, req.Context)
		if err != nil {
			return protocol.ValidationResult{}, err
		}
		if err := b.boundSnapshot(result); err != nil {
			return protocol.ValidationResult{}, err
		}
		return result, nil
	})
}

func (b *Broker) CompleteRule(ctx context.Context, req protocol.CompleteRuleRequest) (protocol.RuleCompletionResult, error) {
	return read(ctx, b, req.Meta, protocol.OperationCompleteRule, func(ctx context.Context) (protocol.RuleCompletionResult, error) {
		var result protocol.RuleCompletionResult
		if req.Limit == 0 || req.Limit > protocol.MaxRuleCompletionResults {
			return result, protocol.ErrInvalidContract
		}
		if err := b.validateRuleContext(req.Rule, req.Context); err != nil {
			return result, err
		}
		backend, ok := b.backend.(RuleBackend)
		if !ok {
			return result, protocol.ErrCapabilityUnsupported
		}
		values, err := backend.CompleteRule(ctx, req.Rule, req.Context)
		if err != nil {
			return result, err
		}
		for _, value := range values[:min(len(values), int(req.Limit))] {
			// Completion values need not be valid complete rules (for example a path
			// prefix), but they must satisfy the same string and encoding bounds.
			if len(req.Rule.Type)+len(value) > int(b.policy.Limits.MaxStringBytes) || !utf8.ValidString(value) || strings.ContainsRune(value, 0) {
				return protocol.RuleCompletionResult{}, protocol.ErrLimitExceeded
			}
			result.Rules = append(result.Rules, protocol.Rule{Type: req.Rule.Type, Value: value})
		}
		if err := b.boundSnapshot(result); err != nil {
			return protocol.RuleCompletionResult{}, err
		}
		return result, nil
	})
}

func (s *SystemBackend) CheckRule(ctx context.Context, rule protocol.Rule, input protocol.RuleContext) (protocol.ValidationResult, error) {
	if err := ctx.Err(); err != nil {
		return protocol.ValidationResult{}, err
	}
	use, err := nativeRuleContext(input)
	if err != nil {
		return protocol.ValidationResult{}, err
	}
	return validationResult(s.system.CheckRule(sysnet.Rule{Type: rule.Type, Rule: rule.Value}, use)), nil
}

func (s *SystemBackend) CompleteRule(ctx context.Context, rule protocol.Rule, input protocol.RuleContext) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	use, err := nativeRuleContext(input)
	if err != nil {
		return nil, err
	}
	values, err := s.system.CompleteRule(sysnet.Rule{Type: rule.Type, Rule: rule.Value}, use)
	return values, nativeError(err)
}

func validationResult(r sysnet.ValidationReport) protocol.ValidationResult {
	result := protocol.ValidationResult{CapabilityRevision: r.CapabilityRevision}
	for _, issue := range r.Issues {
		// Keep structured diagnostics and their error categories. Native Detail and
		// Err strings can contain host paths or process details and are not sent.
		code := ""
		switch err := (sysnet.ValidationReport{Issues: []sysnet.ValidationIssue{issue}}).Err(); {
		case errors.Is(err, sysnet.ErrInvalidOptions):
			code = "invalid"
		case errors.Is(err, sysnet.ErrNotSupported):
			code = "unsupported"
		case errors.Is(err, sysnet.ErrUnavailable):
			code = "unavailable"
		case errors.Is(err, sysnet.ErrCapabilityUnknown):
			code = "unknown"
		}
		result.Issues = append(result.Issues, protocol.ValidationIssue{Path: issue.Path, Reason: string(issue.Reason), State: capability(sysnet.Capability{State: issue.State}).State, Code: code})
	}
	return result
}
