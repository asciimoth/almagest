package almagest

import "encoding/json"

const clipboardXTorDriverClass = "almagest/XTorDriver"

// sanitizeXTorDriverClipboard removes node-scoped secret selectors from copied
// XTorDriver nodes. A pasted node has a different name and cannot access the
// source node's secrets, so it must create its own private identities.
func sanitizeXTorDriverClipboard(data string) string {
	if data == "" {
		return data
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(data), &payload); err != nil {
		return data
	}
	nodes, ok := payload["nodes"].([]any)
	if !ok {
		return data
	}
	changed := false
	for _, rawNode := range nodes {
		node, ok := rawNode.(map[string]any)
		if !ok || node["class"] != clipboardXTorDriverClass {
			continue
		}
		config, ok := node["config"].(map[string]any)
		if !ok {
			continue
		}
		configuration, ok := config["configuration"].(map[string]any)
		if !ok {
			continue
		}
		changed = sanitizeXTorClipboardSelectors(configuration, "services", "keySecret") || changed
		changed = sanitizeXTorClipboardSelectors(configuration, "clientAuthorizations", "privateKeySecret") || changed
	}
	if !changed {
		return data
	}
	sanitized, err := json.Marshal(payload)
	if err != nil {
		return data
	}
	return string(sanitized)
}

func sanitizeXTorClipboardSelectors(configuration map[string]any, list, selector string) bool {
	values, ok := configuration[list].([]any)
	if !ok {
		return false
	}
	changed := false
	for _, rawValue := range values {
		value, ok := rawValue.(map[string]any)
		if !ok {
			continue
		}
		if _, exists := value[selector]; exists {
			delete(value, selector)
			changed = true
		}
	}
	return changed
}
