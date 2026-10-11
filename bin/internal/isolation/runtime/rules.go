package runtime

import (
	"context"
	"errors"

	"github.com/asciimoth/almagest/bin/internal/isolation/protocol"
	"github.com/asciimoth/gonnect/sysnet"
)

func ruleContext(c sysnet.RuleContext) protocol.RuleContext {
	result := protocol.RuleContext{}
	if c.Routing != nil {
		result.Routing = &protocol.RoutingContext{Family: string(c.Routing.Family), Mode: string(c.Routing.Mode), Strict: c.Routing.Strict}
	}
	if c.Matcher != nil {
		result.Matcher = &protocol.MatcherContext{Family: string(c.Matcher.Family), Protocol: string(c.Matcher.Transport)}
	}
	return result
}

// CheckRule preserves native validation categories without evaluating a host
// rule in R. A transport failure is a failed validation, never an empty report.
func (c *Client) CheckRule(rule sysnet.Rule, use sysnet.RuleContext) sysnet.ValidationReport {
	var result protocol.ValidationResult
	err := c.peer.Call(context.Background(), protocol.OperationCheckRule, protocol.CheckRuleRequest{Meta: c.meta(false), Rule: protocol.Rule{Type: rule.Type, Value: rule.Rule}, Context: ruleContext(use)}, &result)
	if err != nil {
		return ruleValidationFailure(err)
	}
	return ruleValidationReport(result)
}

func ruleValidationReport(result protocol.ValidationResult) sysnet.ValidationReport {
	report := sysnet.ValidationReport{CapabilityRevision: result.CapabilityRevision}
	for _, issue := range result.Issues {
		var state sysnet.CapabilityState
		switch issue.State {
		case protocol.CapabilityUnknown:
			state = sysnet.CapabilityUnknown
		case protocol.CapabilitySupported:
			state = sysnet.CapabilityAvailable
		case protocol.CapabilityUnavailable:
			state = sysnet.CapabilityUnavailable
		case protocol.CapabilityUnsupported:
			state = sysnet.CapabilityUnsupported
		default:
			return ruleValidationFailure(protocol.ErrInvalidContract)
		}
		var cause error
		switch issue.Code {
		case "":
		case "unknown":
			cause = sysnet.ErrCapabilityUnknown
		case "invalid":
			cause = sysnet.ErrInvalidOptions
		case "unsupported":
			cause = sysnet.ErrNotSupported
		case "unavailable":
			cause = sysnet.ErrUnavailable
		default:
			return ruleValidationFailure(protocol.ErrInvalidContract)
		}
		report.Issues = append(report.Issues, sysnet.ValidationIssue{Path: issue.Path, Reason: sysnet.CapabilityReason(issue.Reason), State: state, Err: cause})
	}
	return report
}

func ruleValidationFailure(err error) sysnet.ValidationReport {
	state, cause := sysnet.CapabilityUnavailable, err
	switch {
	case errors.Is(err, protocol.ErrCapabilityUnknown):
		state, cause = sysnet.CapabilityUnknown, errors.Join(sysnet.ErrCapabilityUnknown, err)
	case errors.Is(err, protocol.ErrInvalidContract):
		cause = errors.Join(sysnet.ErrInvalidOptions, err)
	case errors.Is(err, protocol.ErrCapabilityUnsupported):
		state, cause = sysnet.CapabilityUnsupported, errors.Join(sysnet.ErrNotSupported, err)
	}
	return sysnet.ValidationReport{Issues: []sysnet.ValidationIssue{{Path: "Rule", State: state, Err: cause}}}
}

// CompleteRule returns only the bounded suggestions produced by N's backend.
func (c *Client) CompleteRule(rule sysnet.Rule, use sysnet.RuleContext) ([]string, error) {
	var result protocol.RuleCompletionResult
	err := c.peer.Call(context.Background(), protocol.OperationCompleteRule, protocol.CompleteRuleRequest{Meta: c.meta(false), Rule: protocol.Rule{Type: rule.Type, Value: rule.Rule}, Context: ruleContext(use), Limit: protocol.MaxRuleCompletionResults}, &result)
	if err != nil {
		switch {
		case errors.Is(err, protocol.ErrCapabilityUnknown):
			return nil, errors.Join(sysnet.ErrCapabilityUnknown, err)
		case errors.Is(err, protocol.ErrCapabilityUnsupported):
			return nil, errors.Join(sysnet.ErrNotSupported, err)
		case errors.Is(err, protocol.ErrCapabilityUnavailable):
			return nil, errors.Join(sysnet.ErrUnavailable, err)
		case errors.Is(err, protocol.ErrInvalidContract):
			return nil, errors.Join(sysnet.ErrInvalidOptions, err)
		}
		return nil, err
	}
	if len(result.Rules) > protocol.MaxRuleCompletionResults {
		return nil, protocol.ErrLimitExceeded
	}
	values := make([]string, len(result.Rules))
	for i, value := range result.Rules {
		if value.Type != rule.Type {
			return nil, protocol.ErrInvalidContract
		}
		values[i] = value.Value
	}
	return values, nil
}
