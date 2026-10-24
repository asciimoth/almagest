package socksnodes

import (
	"errors"
	"fmt"
	"net"
	"slices"
	"strings"

	"github.com/asciimoth/formular"
	"github.com/asciimoth/gonnect/sysnet"
	"github.com/asciimoth/pasta/pasta"
)

func localProxyConfigBlock(config localProxyConfig, formError string) formular.Block {
	block := socksServerConfigBlock(config.server, formError)
	block.Items = append(block.Items,
		localProxyRulesField("exclude", "Exclude clients", config.exclude, formError),
		localProxyRulesField("include", "Include clients", config.include, formError),
	)
	return block
}

func localProxyRulesField(id, label string, rules []sysnet.Rule, formError string) formular.Item {
	item := formular.Item{Type: formular.ItemField, ID: id, Label: label, Field: &formular.Field{
		Kind:      formular.FieldArray,
		Templates: []formular.ArrayTemplate{localProxyRuleTemplate()},
		Elements:  localProxyRuleElements(rules),
	}}
	if formError != "" {
		item.Status = formular.StatusError
		item.StatusText = formError
	}
	item.Help = "Client ownership is resolved on a best-effort basis. A lookup error rejects the client when filtering is configured."
	return item
}

func localProxyRuleTemplate() formular.ArrayTemplate {
	return formular.ArrayTemplate{Name: "rule", Label: "Rule", Items: []formular.Item{
		formular.TextField("type", "Type", "", formular.Validation, formular.AutocompleteConfig(formular.Autocomplete{Enabled: true})),
		formular.TextField("rule", "Rule", "", formular.Validation, formular.AutocompleteConfig(formular.Autocomplete{Enabled: true})),
	}}
}

func localProxyRuleElements(rules []sysnet.Rule) []formular.ArrayElement {
	template := localProxyRuleTemplate()
	out := make([]formular.ArrayElement, 0, len(rules))
	for i, rule := range rules {
		out = append(out, formular.InstantiateArrayTemplate(template, fmt.Sprintf("rule-%d", i), map[string]any{
			"type": rule.Type,
			"rule": rule.Rule,
		}))
	}
	return out
}

func (n *localProxyNode) validateLocalProxyConfig(config localProxyConfig) error {
	if err := config.validate(); err != nil {
		return err
	}
	for _, rules := range [][]sysnet.Rule{config.exclude, config.include} {
		for _, rule := range rules {
			if err := n.validateLocalProxyRule(config, rule); err != nil {
				return err
			}
		}
	}
	return nil
}

func (n *localProxyNode) validateLocalProxyRule(config localProxyConfig, rule sysnet.Rule) error {
	if rule.Type == "" || rule.Rule == "" {
		return errors.New("client rule type and value must not be empty")
	}
	if n.System() == nil {
		return nil
	}
	for _, context := range localProxyMatcherContexts(config.server) {
		if err := localProxyValidationError(n.System().CheckRule(rule, context)); err != nil {
			return fmt.Errorf("invalid %s client rule: %w", rule.Type, err)
		}
	}
	return nil
}

func localProxyMatcherContexts(config socksServerConfig) []sysnet.RuleContext {
	families := make([]sysnet.AddressFamily, 0, 2)
	if config.socksPort > 0 {
		families = append(families, localProxyHostFamilies(config.socksHost)...)
	}
	if config.wsPort > 0 {
		families = append(families, localProxyHostFamilies(config.wsHost)...)
	}
	if len(families) == 0 {
		families = append(families, sysnet.FamilyIPv4)
	}
	slices.Sort(families)
	families = slices.Compact(families)
	out := make([]sysnet.RuleContext, 0, len(families))
	for _, family := range families {
		key := sysnet.MatcherProfileKey{Family: family, Transport: sysnet.TransportTCP}
		out = append(out, sysnet.RuleContext{Matcher: &key})
	}
	return out
}

func localProxyHostFamilies(host string) []sysnet.AddressFamily {
	host = hostOrDefault(host)
	if ip := net.ParseIP(strings.Trim(host, "[]")); ip != nil {
		if ip.To4() != nil {
			return []sysnet.AddressFamily{sysnet.FamilyIPv4}
		}
		return []sysnet.AddressFamily{sysnet.FamilyIPv6}
	}
	return []sysnet.AddressFamily{sysnet.FamilyIPv4, sysnet.FamilyIPv6}
}

func localProxyValidationError(report sysnet.ValidationReport) error {
	var errs []error
	for _, issue := range report.Issues {
		err := (sysnet.ValidationReport{Issues: []sysnet.ValidationIssue{issue}}).Err()
		if errors.Is(err, sysnet.ErrCapabilityUnknown) && !errors.Is(err, sysnet.ErrInvalidOptions) {
			continue
		}
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

func (n *localProxyNode) validateLocalProxyRuleField(msg formular.FieldValidateMessage) error {
	arrayID, elementID, ok := localProxyRuleFieldPath(msg.Field)
	if !ok {
		return nil
	}
	switch msg.Field.FieldID {
	case "type":
		ruleType, _ := formular.StringValue(msg.Value)
		ruleType = strings.TrimSpace(ruleType)
		if ruleType == "" {
			return nil
		}
		if !n.localProxyRuleTypeSupported(ruleType) {
			return fmt.Errorf("unsupported client rule type %q", ruleType)
		}
		n.setLocalProxyRuleDraftType(arrayID, elementID, ruleType)
	case "rule":
		ruleValue, _ := formular.StringValue(msg.Value)
		ruleValue = strings.TrimSpace(ruleValue)
		if ruleValue == "" {
			return nil
		}
		ruleType, ok := n.localProxyRuleTypeByElementID(arrayID, elementID)
		if !ok || ruleType == "" {
			return nil
		}
		return n.validateLocalProxyRule(n.config, sysnet.Rule{Type: ruleType, Rule: ruleValue})
	}
	return nil
}

func localProxyRuleFieldPath(ref formular.FieldRef) (arrayID, elementID string, ok bool) {
	if len(ref.ElementPath) == 0 {
		return "", "", false
	}
	last := ref.ElementPath[len(ref.ElementPath)-1]
	if last.ArrayFieldID != "exclude" && last.ArrayFieldID != "include" {
		return "", "", false
	}
	return last.ArrayFieldID, last.ElementID, true
}

func (n *localProxyNode) localProxyRuleTypeByElementID(arrayID, elementID string) (string, bool) {
	if ruleType, ok := n.ruleDraftType[arrayID+"/"+elementID]; ok {
		return ruleType, true
	}
	rules := n.config.exclude
	if arrayID == "include" {
		rules = n.config.include
	}
	for i, rule := range rules {
		if elementID == fmt.Sprintf("rule-%d", i) {
			return rule.Type, true
		}
	}
	return "", false
}

func (n *localProxyNode) setLocalProxyRuleDraftType(arrayID, elementID, ruleType string) {
	if n.ruleDraftType == nil {
		n.ruleDraftType = make(map[string]string)
	}
	n.ruleDraftType[arrayID+"/"+elementID] = ruleType
}

func (n *localProxyNode) localProxyRuleCapabilities() []sysnet.RuleCapability {
	if n.System() == nil {
		return nil
	}
	report := n.System().Capabilities()
	contexts := localProxyMatcherContexts(n.config.server)
	out := make([]sysnet.RuleCapability, 0, len(report.Rules))
	for _, rule := range report.Rules {
		if !localProxyCapabilityAllowsAttempt(rule.Validation) {
			continue
		}
		usable := true
		for _, context := range contexts {
			capability := sysnet.Capability{State: sysnet.CapabilityUnknown}
			for _, matcher := range rule.Matchers {
				if matcher.Key == *context.Matcher {
					capability = matcher.Capability
					break
				}
			}
			if !localProxyCapabilityAllowsAttempt(capability) {
				usable = false
				break
			}
		}
		if usable {
			out = append(out, rule)
		}
	}
	slices.SortFunc(out, func(a, b sysnet.RuleCapability) int { return strings.Compare(a.Type, b.Type) })
	return out
}

func localProxyCapabilityAllowsAttempt(capability sysnet.Capability) bool {
	return capability.State == sysnet.CapabilityAvailable || capability.State == sysnet.CapabilityUnknown
}

func (n *localProxyNode) localProxyRuleTypeSupported(ruleType string) bool {
	if n.System() == nil {
		return true
	}
	return slices.ContainsFunc(n.localProxyRuleCapabilities(), func(rule sysnet.RuleCapability) bool {
		return rule.Type == ruleType
	})
}

func (n *localProxyNode) sendConfigAutocompleteHints(msg formular.AutocompleteRequestMessage) {
	if n.W == nil || n.ID == 0 {
		return
	}
	n.W.SendNodeMenuMsgLocked(n.ID, formular.AutocompleteHints(
		pasta.NodeMenuID(n.ID), 1, msg.Field, msg.Prefix, n.localProxyAutocompleteHints(msg),
	))
}

func (n *localProxyNode) localProxyAutocompleteHints(msg formular.AutocompleteRequestMessage) []string {
	prefix := strings.TrimSpace(msg.Prefix)
	arrayID, elementID, ok := localProxyRuleFieldPath(msg.Field)
	if !ok || n.System() == nil {
		return nil
	}
	switch msg.Field.FieldID {
	case "type":
		var hints []string
		for _, rule := range n.localProxyRuleCapabilities() {
			if strings.HasPrefix(rule.Type, prefix) {
				hints = append(hints, rule.Type)
			}
		}
		return hints
	case "rule":
		ruleType, ok := n.localProxyRuleTypeByElementID(arrayID, elementID)
		if !ok || ruleType == "" {
			return nil
		}
		var common []string
		for i, context := range localProxyMatcherContexts(n.config.server) {
			values, err := n.System().CompleteRule(sysnet.Rule{Type: ruleType, Rule: prefix}, context)
			if err != nil {
				return nil
			}
			values = slices.DeleteFunc(values, func(value string) bool { return !strings.HasPrefix(value, prefix) })
			if i == 0 {
				common = values
			} else {
				common = slices.DeleteFunc(common, func(value string) bool { return !slices.Contains(values, value) })
			}
		}
		return common
	}
	return nil
}
