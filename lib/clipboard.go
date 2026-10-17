package almagest

import "encoding/json"

const clipboardTorDriverClass = "almagest/TorDriver"

// sanitizeTorDriverClipboard removes node-scoped secret selectors from copied
// TorDriver nodes. A pasted node has a different name and cannot access the
// source node's secrets, so it must create its own private identities.
func sanitizeTorDriverClipboard(data string) string {
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
		if !ok || node["class"] != clipboardTorDriverClass {
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
		changed = sanitizeTorClipboardSelectors(configuration, "services", "keySecret") || changed
		changed = sanitizeTorClipboardSelectors(configuration, "clientAuthorizations", "privateKeySecret") || changed
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

func sanitizeTorClipboardSelectors(configuration map[string]any, list, selector string) bool {
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
