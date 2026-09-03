package amnesianodes

import "encoding/json"

// SanitizeClipboard removes Amnesia values that identify or authorize one
// client instance. Pasta uses the same node OnSave callback for config saves
// and clipboard copies, so this filtering must occur at the clipboard boundary.
//
// Other node configuration stays in the clipboard. A pasted Amnesia node keeps
// settings such as country and gateway URL, but starts without secret
// references and creates a new KV namespace and installation UUID.
func SanitizeClipboard(data string) string {
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
		if !ok || node["class"] != NodeTypeAmnesia {
			continue
		}
		config, ok := node["config"].(map[string]any)
		if !ok {
			continue
		}
		for _, field := range []string{
			fieldConnectionSecret,
			fieldGatewayPublicKeySecret,
			fieldStorageID,
		} {
			if _, present := config[field]; present {
				delete(config, field)
				changed = true
			}
		}
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
