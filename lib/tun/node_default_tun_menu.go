package tunnodes

import (
	"fmt"
	"strings"

	"github.com/asciimoth/formular"
	"github.com/asciimoth/gonnect/sysnet"
)

func (n *defaultTunNode) updateLabel() error {
	if n.formError != "" {
		return n.SetLabel("invalid config")
	}
	if n.dtun == nil {
		return n.SetLabel("idle")
	}
	return n.SetLabel("ready")
}

func (n *defaultTunNode) sendMenuSnapshot() {
	sendNodeMenuSnapshot(n.W, n.ID, false, n.menuBlocks()...)
}

func (n *defaultTunNode) sendForcedMenuSnapshot() {
	sendNodeMenuSnapshot(n.W, n.ID, true, n.menuBlocks()...)
}

func (n *defaultTunNode) menuBlocks() []formular.Block {
	return []formular.Block{n.effectiveBlock(), n.configBlock()}
}

func (n *defaultTunNode) sendEffectiveBlock() {
	sendNodeBlockSnapshot(n.W, n.ID, n.effectiveBlock())
}

func (n *defaultTunNode) effectiveBlock() formular.Block {
	opts := n.effectiveDefaultTunOpts()
	if n.hasEffective {
		opts = n.effective.Copy()
	}
	return formular.Block{ID: "effective", Order: 0, Generation: 1, Items: []formular.Item{
		readonlyStringListField("tunAddrs", "TUN addrs", opts.TunAddrs),
		readonlyStringListField("tunRoutes", "TUN routes", opts.TunRoutes),
		readonlyIntField("mtu", "MTU", opts.MTU),
		readonlyTextField("dnsIP", "DNS IP", opts.DnsIP),
		readonlyBoolField("strict", "Strict routing", opts.Strict),
		readonlyMultilineTextField("exclude", "Exclude", defaultTunRulesText(opts.Exclude)),
		readonlyMultilineTextField("include", "Include", defaultTunRulesText(opts.Include)),
	}}
}

func (n *defaultTunNode) configBlock() formular.Block {
	statusErr := n.formError != ""
	return formular.Block{ID: "config", Order: 10, Generation: 1, Form: true, Items: []formular.Item{
		defaultTunStringListField("tunAddrs", "TUN addrs", splitDefaultTunList(n.config.tunAddrs)),
		defaultTunStringListField("tunRoutes", "TUN routes", splitDefaultTunList(n.config.tunRoutes)),
		configIntField("mtu", "MTU", n.config.mtu, statusErr, n.formError),
		configTextField("dnsIP", "DNS IP", n.config.dnsIP, statusErr, n.formError, false),
		configCheckbox("strict", "Strict routing", n.config.strict),
		defaultTunRulesField("exclude", "Exclude", n.config.exclude),
		defaultTunRulesField("include", "Include", n.config.include),
	}}
}

func (n *defaultTunNode) sendConfigFieldStatus(msg formular.FieldValidateMessage) {
	sendNodeFieldStatus(n.W, n.ID, msg.Field, n.validateConfigField(msg))
}

func (n *defaultTunNode) validateConfigField(msg formular.FieldValidateMessage) error {
	if len(msg.Field.ElementPath) > 0 {
		return n.validateNestedConfigField(msg)
	}
	next := n.config
	switch msg.Field.FieldID {
	case "mtu":
		if value, ok := formular.IntValue(msg.Value); ok {
			next.mtu = value
		}
	case "dnsIP":
		if value, ok := formular.StringValue(msg.Value); ok {
			next.dnsIP = value
		}
	default:
		return nil
	}
	next.normalize()
	return n.validateConfig(next)
}

func (n *defaultTunNode) validateNestedConfigField(msg formular.FieldValidateMessage) error {
	switch msg.Field.FieldID {
	case "value":
		return nil
	case "type":
		ruleType, _ := formular.StringValue(msg.Value)
		ruleType = strings.TrimSpace(ruleType)
		if ruleType == "" {
			return nil
		}
		if !n.ruleTypeSupported(ruleType) {
			return fmt.Errorf("unsupported rule type %q", ruleType)
		}
		if arrayID, elementID, ok := defaultTunRuleFieldPath(msg.Field); ok {
			n.setRuleDraftType(arrayID, elementID, ruleType)
		}
		return nil
	case "rule":
		ruleValue, _ := formular.StringValue(msg.Value)
		ruleValue = strings.TrimSpace(ruleValue)
		if ruleValue == "" {
			return nil
		}
		arrayID, elementID, ok := defaultTunRuleFieldPath(msg.Field)
		if !ok {
			return nil
		}
		ruleType, ok := n.ruleTypeByElementID(arrayID, elementID)
		if !ok || ruleType == "" || n.System() == nil {
			return nil
		}
		if n.System().RuleVerify(sysnet.Rule{Type: ruleType, Rule: ruleValue}) {
			return nil
		}
		return fmt.Errorf("invalid %s rule", ruleType)
	default:
		return nil
	}
}

func defaultTunRulesText(rules []sysnet.Rule) string {
	lines := make([]string, 0, len(rules))
	for _, rule := range rules {
		ruleType := strings.TrimSpace(rule.Type)
		ruleValue := strings.TrimSpace(rule.Rule)
		switch {
		case ruleType == "":
			if ruleValue != "" {
				lines = append(lines, ruleValue)
			}
		case ruleValue == "":
			lines = append(lines, ruleType)
		default:
			lines = append(lines, fmt.Sprintf("%s %s", ruleType, ruleValue))
		}
	}
	return strings.Join(lines, "\n")
}

func defaultTunRulesField(id, label string, rules []sysnet.Rule) formular.Item {
	return formular.Item{Type: formular.ItemField, ID: id, Label: label, Field: &formular.Field{
		Kind:      formular.FieldArray,
		Templates: []formular.ArrayTemplate{defaultTunRuleTemplate()},
		Elements:  defaultTunRuleElements(rules),
	}}
}

func defaultTunStringListField(id, label string, values []string) formular.Item {
	return formular.Item{Type: formular.ItemField, ID: id, Label: label, Field: &formular.Field{
		Kind:      formular.FieldArray,
		Templates: []formular.ArrayTemplate{defaultTunStringListTemplate()},
		Elements:  defaultTunStringListElements(values),
	}}
}

func defaultTunStringListTemplate() formular.ArrayTemplate {
	return formular.ArrayTemplate{
		Name:  "value",
		Label: "Value",
		Items: []formular.Item{
			formular.TextField("value", "Value", "", formular.Validation),
		},
	}
}

func defaultTunStringListElements(values []string) []formular.ArrayElement {
	template := defaultTunStringListTemplate()
	out := make([]formular.ArrayElement, 0, len(values))
	for i, value := range values {
		out = append(out, formular.InstantiateArrayTemplate(template, fmt.Sprintf("value-%d", i), map[string]any{"value": value}))
	}
	return out
}

func defaultTunRuleTemplate() formular.ArrayTemplate {
	return formular.ArrayTemplate{
		Name:  "rule",
		Label: "Rule",
		Items: []formular.Item{
			defaultTunRuleTypeField(""),
			defaultTunRuleValueField(""),
		},
	}
}

func defaultTunRuleElements(rules []sysnet.Rule) []formular.ArrayElement {
	template := defaultTunRuleTemplate()
	out := make([]formular.ArrayElement, 0, len(rules))
	for i, rule := range rules {
		out = append(out, formular.InstantiateArrayTemplate(template, fmt.Sprintf("rule-%d", i), map[string]any{"type": rule.Type, "rule": rule.Rule}))
	}
	return out
}

func defaultTunRuleTypeField(value string) formular.Item {
	return formular.TextField("type", "Type", value, formular.Validation, formular.AutocompleteConfig(formular.Autocomplete{Enabled: true}))
}

func defaultTunRuleValueField(value string) formular.Item {
	return formular.TextField("rule", "Rule", value, formular.Validation, formular.AutocompleteConfig(formular.Autocomplete{Enabled: true}))
}
