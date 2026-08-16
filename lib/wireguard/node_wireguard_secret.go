package wireguardnodes

import (
	"errors"
	"fmt"
	"strings"
)

var errWireGuardSecretsUnavailable = errors.New("secrets storage is unavailable")

// resolvePrivateKeyConfig returns a config whose runtime-only privateKey has
// been fetched from SecretsStorage. If the persisted secret name is empty or no
// longer readable by this node, a fresh private key is generated and saved as a
// new node-scoped secret; the returned config carries the generated secret name.
//
// privateKeyLegacyValue is consumed only when loading an old pre-secrets config
// or receiving an old form payload. It is intentionally ignored when a secret
// name was configured but cannot be read, because that path must rotate to a
// new random key instead of silently continuing with stale runtime material.
func (n *wireGuardNode) resolvePrivateKeyConfig(config wireGuardConfig) (wireGuardConfig, error) {
	config.normalize()
	key, secretName, err := resolveWireGuardPrivateKeySecret(
		n.privateKeySecretsStorage(),
		n.currentPrivateKeySecretNodeName(),
		config.privateKeySecret,
		config.privateKeyLegacyValue,
	)
	if err != nil {
		return config, err
	}
	config.privateKeySecret = secretName
	config.privateKey = key
	config.privateKeyLegacyValue = ""
	config.normalize()
	return config, nil
}

// currentPrivateKeySecretNodeName returns the workspace node name used for
// SecretsStorage access. Secret allow lists are keyed by node name, and
// almagestd rewrites those allow lists when nodes are renamed. Looking up the
// current snapshot here avoids using the stale OnInit name after a rename while
// still keeping the OnInit name as a fallback for direct unit-test nodes.
func (n *wireGuardNode) currentPrivateKeySecretNodeName() string {
	if n.W != nil && n.ID != 0 {
		if snapshot, ok := n.W.NodeSnapshotLocked(n.ID); ok {
			return snapshot.Name
		}
	}
	return n.nodeName
}

func (n *wireGuardNode) privateKeySecretsStorage() SecretsStorage {
	if n.secrets != nil {
		return n.secrets
	}
	if n.Runtime == nil {
		return nil
	}
	return n.Runtime.SecretsStorage()
}

func resolveWireGuardPrivateKeySecret(storage SecretsStorage, nodeName, secretName, legacyValue string) (privateKey, resolvedSecretName string, err error) {
	nodeName = strings.TrimSpace(nodeName)
	secretName = strings.TrimSpace(secretName)
	legacyValue = normalizeWireGuardKey(legacyValue)
	if storage == nil {
		return "", secretName, errWireGuardSecretsUnavailable
	}
	if nodeName == "" {
		return "", secretName, fmt.Errorf("%w: missing WireGuard node name", ErrSecretInvalidName)
	}
	if secretName != "" {
		sec, err := storage.GetSecret(secretName, nodeName)
		if err == nil {
			key := normalizeWireGuardKey(sec.Value)
			if _, err := parseWireGuardPrivateKey(key); err != nil {
				return "", secretName, fmt.Errorf("private key secret %q: %w", secretName, err)
			}
			return key, secretName, nil
		}
	}

	key := ""
	if secretName == "" {
		key = legacyValue
	}
	if key == "" {
		var err error
		key, err = generateWireGuardPrivateKeyBase64()
		if err != nil {
			return "", secretName, fmt.Errorf("generate private key: %w", err)
		}
	} else if _, err := parseWireGuardPrivateKey(key); err != nil {
		return "", secretName, fmt.Errorf("legacy private key: %w", err)
	}
	newName, err := storage.NewSecret(Secret{Value: key}, nodeName)
	if err != nil {
		return "", secretName, fmt.Errorf("create private key secret: %w", err)
	}
	newName = strings.TrimSpace(newName)
	if newName == "" {
		return "", secretName, fmt.Errorf("%w: storage returned empty private key secret name", ErrSecretInvalidName)
	}
	return key, newName, nil
}

// validatePrivateKeySecretForForm checks only accessible secret values. Empty
// and inaccessible names are valid form input because submit will create a new
// generated private-key secret for those cases.
func (n *wireGuardNode) validatePrivateKeySecretForForm(secretName string) error {
	secretName = strings.TrimSpace(secretName)
	if secretName == "" {
		return nil
	}
	storage := n.privateKeySecretsStorage()
	if storage == nil {
		return errWireGuardSecretsUnavailable
	}
	sec, err := storage.GetSecret(secretName, n.currentPrivateKeySecretNodeName())
	if err != nil {
		return nil
	}
	key := normalizeWireGuardKey(sec.Value)
	if _, err := parseWireGuardPrivateKey(key); err != nil {
		return fmt.Errorf("private key secret %q: %w", secretName, err)
	}
	return nil
}

func (n *wireGuardNode) handleSecretValueChanged(msg SecretValueChanged) {
	current := n.config.privateKeySecret
	if current == "" {
		return
	}
	prev := strings.TrimSpace(msg.SecretNamePrev)
	nextName := strings.TrimSpace(msg.SecretNameNew)
	if current != prev && current != nextName {
		return
	}
	if current == prev && nextName != "" && nextName != prev {
		// Persist the rename before validation so a temporarily invalid renamed
		// secret can still be repaired by a later SecretValueChanged for nextName.
		n.config.privateKeySecret = nextName
	}
	next := n.config.clone()
	next.privateKey = ""
	next.privateKeyLegacyValue = ""
	_ = n.commitConfigForm(next, wireGuardConfigCommitOptions{rebuildPeers: true, emitTunConfig: true})
}
