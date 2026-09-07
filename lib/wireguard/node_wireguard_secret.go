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
	storage := n.privateKeySecretsStorage()
	nodeName := n.currentSecretNodeName()
	key, secretName, err := resolveWireGuardPrivateKeySecret(
		storage,
		nodeName,
		config.privateKeySecret,
		config.privateKeyLegacyValue,
	)
	if err != nil {
		return config, err
	}
	config.privateKeySecret = secretName
	config.privateKey = key
	config.privateKeyLegacyValue = ""
	config.peers, err = resolveWireGuardPeerSecrets(storage, nodeName, config.peers)
	if err != nil {
		return config, err
	}
	config.normalize()
	return config, nil
}

// resolveObjectPeerSecrets resolves runtime-only preshared key material for an
// object input without changing the node's private-key configuration.
func (n *wireGuardNode) resolveObjectPeerSecrets(peers []wireGuardPeerConfig) ([]wireGuardPeerConfig, error) {
	return resolveWireGuardPeerSecrets(n.privateKeySecretsStorage(), n.currentSecretNodeName(), peers)
}

func resolveWireGuardPeerSecrets(storage SecretsStorage, nodeName string, peers []wireGuardPeerConfig) ([]wireGuardPeerConfig, error) {
	out := make([]wireGuardPeerConfig, len(peers))
	copy(out, peers)
	for i := range out {
		key, secretName, err := resolveWireGuardPresharedKeySecret(
			storage,
			nodeName,
			out[i].presharedKeySecret,
			out[i].presharedKey,
			peerLabel(out[i]),
		)
		if err != nil {
			return out, err
		}
		out[i].presharedKeySecret = secretName
		out[i].presharedKey = key
	}
	return out, nil
}

// currentSecretNodeName returns the workspace node name used for SecretsStorage
// access. Secret allow lists are keyed by node name, and almagestd rewrites
// those allow lists when nodes are renamed. Looking up the current snapshot here
// avoids using the stale OnInit name after a rename while still keeping the
// OnInit name as a fallback for direct unit-test nodes.
func (n *wireGuardNode) currentSecretNodeName() string {
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

func resolveWireGuardPresharedKeySecret(storage SecretsStorage, nodeName, secretName, legacyValue, peer string) (presharedKey, resolvedSecretName string, err error) {
	nodeName = strings.TrimSpace(nodeName)
	secretName = strings.TrimSpace(secretName)
	legacyValue = normalizeWireGuardKey(legacyValue)
	peer = strings.TrimSpace(peer)
	if peer == "" {
		peer = "peer"
	}
	if secretName == "" && legacyValue == "" {
		return "", "", nil
	}
	if storage == nil {
		return "", secretName, errWireGuardSecretsUnavailable
	}
	if nodeName == "" {
		return "", secretName, fmt.Errorf("%w: missing WireGuard node name", ErrSecretInvalidName)
	}
	if secretName != "" {
		sec, err := storage.GetSecret(secretName, nodeName)
		if err != nil {
			return "", secretName, fmt.Errorf("peer %s preshared key secret %q: %w", peer, secretName, err)
		}
		key := normalizeWireGuardKey(sec.Value)
		if _, err := parseWireGuardPresharedKey(key); err != nil {
			return "", secretName, fmt.Errorf("peer %s preshared key secret %q: %w", peer, secretName, err)
		}
		return key, secretName, nil
	}
	if _, err := parseWireGuardPresharedKey(legacyValue); err != nil {
		return "", secretName, fmt.Errorf("peer %s legacy preshared key: %w", peer, err)
	}
	newName, err := storage.NewSecret(Secret{Value: legacyValue}, nodeName)
	if err != nil {
		return "", secretName, fmt.Errorf("create peer %s preshared key secret: %w", peer, err)
	}
	newName = strings.TrimSpace(newName)
	if newName == "" {
		return "", secretName, fmt.Errorf("%w: storage returned empty peer %s preshared key secret name", ErrSecretInvalidName, peer)
	}
	return legacyValue, newName, nil
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
	sec, err := storage.GetSecret(secretName, n.currentSecretNodeName())
	if err != nil {
		return nil
	}
	key := normalizeWireGuardKey(sec.Value)
	if _, err := parseWireGuardPrivateKey(key); err != nil {
		return fmt.Errorf("private key secret %q: %w", secretName, err)
	}
	return nil
}

func (n *wireGuardNode) validatePresharedKeySecretForForm(secretName, peer string) error {
	secretName = strings.TrimSpace(secretName)
	if secretName == "" {
		return nil
	}
	storage := n.privateKeySecretsStorage()
	if storage == nil {
		return errWireGuardSecretsUnavailable
	}
	sec, err := storage.GetSecret(secretName, n.currentSecretNodeName())
	if err != nil {
		return fmt.Errorf("peer %s preshared key secret %q: %w", peer, secretName, err)
	}
	key := normalizeWireGuardKey(sec.Value)
	if _, err := parseWireGuardPresharedKey(key); err != nil {
		return fmt.Errorf("peer %s preshared key secret %q: %w", peer, secretName, err)
	}
	return nil
}

func (n *wireGuardNode) handleSecretValueChanged(msg SecretValueChanged) {
	prev := strings.TrimSpace(msg.SecretNamePrev)
	nextName := strings.TrimSpace(msg.SecretNameNew)
	if !n.wireGuardSecretChangeRelevant(prev, nextName) {
		return
	}
	if n.config.privateKeySecret == prev && nextName != "" && nextName != prev {
		// Persist the rename before validation so a temporarily invalid renamed
		// secret can still be repaired by a later SecretValueChanged for nextName.
		n.config.privateKeySecret = nextName
	}
	for i := range n.config.peers {
		if n.config.peers[i].presharedKeySecret == prev && nextName != "" && nextName != prev {
			n.config.peers[i].presharedKeySecret = nextName
		}
	}
	for link, peers := range n.objectPeerInputs {
		for i := range peers {
			if peers[i].presharedKeySecret == prev && nextName != "" && nextName != prev {
				peers[i].presharedKeySecret = nextName
			}
		}
		n.objectPeerInputs[link] = peers
	}
	next := n.config.clone()
	next.privateKey = ""
	next.privateKeyLegacyValue = ""
	for i := range next.peers {
		next.peers[i].presharedKey = ""
	}
	_ = n.commitConfigForm(next, wireGuardConfigCommitOptions{emitTunConfig: true})

	objectsChanged := false
	for link, input := range n.objectPeerInputs {
		if !wireGuardPeerSecretChangeRelevant(input, prev, nextName) {
			continue
		}
		objectsChanged = true
		for i := range input {
			input[i].presharedKey = ""
		}
		resolved, err := n.resolveObjectPeerSecrets(input)
		if err == nil {
			candidate := n.config.clone()
			candidate.peers = resolved
			err = validateWireGuardConfig(candidate)
		}
		if err != nil {
			delete(n.objectPeers, link)
			n.objectErrors[link] = err.Error()
			continue
		}
		n.objectPeerInputs[link] = wireGuardPeerInputs(resolved)
		n.objectPeers[link] = resolved
		delete(n.objectErrors, link)
	}
	if objectsChanged {
		n.applyEffectivePeerConfig()
	}
}

func (n *wireGuardNode) wireGuardSecretChangeRelevant(prev, nextName string) bool {
	if wireGuardSecretChangeRelevant(n.config, prev, nextName) {
		return true
	}
	for _, peers := range n.objectPeerInputs {
		if wireGuardPeerSecretChangeRelevant(peers, prev, nextName) {
			return true
		}
	}
	return false
}

func wireGuardSecretChangeRelevant(config wireGuardConfig, prev, nextName string) bool {
	prev = strings.TrimSpace(prev)
	nextName = strings.TrimSpace(nextName)
	if prev == "" && nextName == "" {
		return false
	}
	if config.privateKeySecret != "" && (config.privateKeySecret == prev || config.privateKeySecret == nextName) {
		return true
	}
	return wireGuardPeerSecretChangeRelevant(config.peers, prev, nextName)
}

func wireGuardPeerSecretChangeRelevant(peers []wireGuardPeerConfig, prev, nextName string) bool {
	for _, peer := range peers {
		if peer.presharedKeySecret != "" && (peer.presharedKeySecret == prev || peer.presharedKeySecret == nextName) {
			return true
		}
	}
	return false
}
