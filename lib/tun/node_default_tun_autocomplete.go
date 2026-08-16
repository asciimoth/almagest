package tunnodes

import (
	"fmt"
	"strings"

	"github.com/asciimoth/formular"
	"github.com/asciimoth/gonnect/sysnet"
	"github.com/asciimoth/pasta/pasta"
)

func (n *defaultTunNode) sendConfigAutocompleteHints(msg formular.AutocompleteRequestMessage) {
	if n.W == nil || n.ID == 0 {
		return
	}
	n.W.SendNodeMenuMsgLocked(n.ID, formular.AutocompleteHints(pasta.NodeMenuID(n.ID), 1, msg.Field, msg.Prefix, n.configAutocompleteHints(msg)))
}

func (n *defaultTunNode) configAutocompleteHints(msg formular.AutocompleteRequestMessage) []string {
	if n.System() == nil {
		return nil
	}
	prefix := strings.TrimSpace(msg.Prefix)
	switch msg.Field.FieldID {
	case "type":
		info := n.System().ListRules()
		return defaultTunRuleTypeHints(info.TunRules, prefix)
	case "rule":
		arrayID, elementID, ok := defaultTunRuleFieldPath(msg.Field)
		if !ok {
			return nil
		}
		ruleType, ok := n.ruleTypeByElementID(arrayID, elementID)
		if !ok || ruleType == "" {
			return nil
		}
		return defaultTunPrefixHints(n.System().RuleCompl(sysnet.Rule{Type: ruleType, Rule: prefix}), prefix)
	default:
		return nil
	}
}

func defaultTunRuleTypeHints(infos []sysnet.RuleTypeInfo, prefix string) []string {
	hints := make([]string, 0, len(infos))
	for _, info := range infos {
		if strings.HasPrefix(info.Type, prefix) {
			hints = append(hints, info.Type)
		}
	}
	return hints
}

func defaultTunPrefixHints(values []string, prefix string) []string {
	hints := make([]string, 0, len(values))
	for _, value := range values {
		if strings.HasPrefix(value, prefix) {
			hints = append(hints, value)
		}
	}
	return hints
}

func defaultTunRuleFieldPath(ref formular.FieldRef) (arrayID, elementID string, ok bool) {
	if len(ref.ElementPath) == 0 {
		return "", "", false
	}
	last := ref.ElementPath[len(ref.ElementPath)-1]
	if last.ArrayFieldID != "exclude" && last.ArrayFieldID != "include" {
		return "", "", false
	}
	return last.ArrayFieldID, last.ElementID, true
}

func (n *defaultTunNode) ruleTypeByElementID(arrayID, elementID string) (string, bool) {
	if n.ruleDraftType != nil {
		if ruleType, ok := n.ruleDraftType[defaultTunRuleDraftKey(arrayID, elementID)]; ok {
			return ruleType, true
		}
	}
	var rules []sysnet.Rule
	switch arrayID {
	case "exclude":
		rules = n.config.exclude
	case "include":
		rules = n.config.include
	default:
		return "", false
	}
	for i, rule := range rules {
		if elementID == fmt.Sprintf("rule-%d", i) {
			return rule.Type, true
		}
	}
	return "", false
}

func (n *defaultTunNode) setRuleDraftType(arrayID, elementID, ruleType string) {
	if n.ruleDraftType == nil {
		n.ruleDraftType = map[string]string{}
	}
	n.ruleDraftType[defaultTunRuleDraftKey(arrayID, elementID)] = ruleType
}

func defaultTunRuleDraftKey(arrayID, elementID string) string {
	return arrayID + "/" + elementID
}

func (n *defaultTunNode) ruleTypeSupported(ruleType string) bool {
	if n.System() == nil {
		return true
	}
	info := n.System().ListRules()
	if len(info.TunRules) == 0 {
		return true
	}
	for _, rule := range info.TunRules {
		if rule.Type == ruleType {
			return true
		}
	}
	return false
}
