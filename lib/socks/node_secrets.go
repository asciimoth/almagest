package socksnodes

import "strings"

// currentSocksNodeName returns the current workspace name used by the
// node-scoped secret allow list. The workspace can rename a node after OnInit,
// so secret reads must not retain the initial name.
func currentSocksNodeName(base *NodeBase) string {
	if base == nil || base.W == nil || base.ID == 0 {
		return ""
	}
	snapshot, ok := base.W.NodeSnapshotLocked(base.ID)
	if !ok {
		return ""
	}
	return snapshot.Name
}

// socksSecretValue returns a selected secret only when it is currently
// available to this node. An empty secret value is available and is different
// from a failed read. Callers retain their inline password after a failed read.
func socksSecretValue(base *NodeBase, name string) (string, bool) {
	name = strings.TrimSpace(name)
	if base == nil || name == "" {
		return "", false
	}
	secrets := base.SecretsStorage()
	if secrets == nil {
		return "", false
	}
	secret, err := secrets.GetSecret(name, currentSocksNodeName(base))
	if err != nil {
		return "", false
	}
	return secret.Value, true
}

// socksSecretChangeMatches reports whether an inbox notification can change
// the effective value or persisted name of a selected secret.
func socksSecretChangeMatches(selected string, msg SecretValueChanged) bool {
	selected = strings.TrimSpace(selected)
	return selected != "" && (selected == strings.TrimSpace(msg.SecretNamePrev) || selected == strings.TrimSpace(msg.SecretNameNew))
}

// updateSocksServerPasswordSecretNames applies a selected-secret rename to a
// server configuration. It also reports value changes and deletions, which do
// not change the selector but require a runtime password reload.
func updateSocksServerPasswordSecretNames(config *socksServerConfig, msg SecretValueChanged) bool {
	if config == nil {
		return false
	}
	relevant := false
	prev := strings.TrimSpace(msg.SecretNamePrev)
	next := strings.TrimSpace(msg.SecretNameNew)
	for i := range config.users {
		if !socksSecretChangeMatches(config.users[i].passwordSecret, msg) {
			continue
		}
		relevant = true
		if config.users[i].passwordSecret == prev && next != "" && next != prev {
			config.users[i].passwordSecret = next
		}
	}
	return relevant
}
