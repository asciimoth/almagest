package nodekit

import "github.com/asciimoth/almagest/lib/storage"

// KVManager is the engine key-value storage manager.
type KVManager = storage.KVManager

// Secret is one secret value visible to a node.
type Secret = storage.Secret

// SecretsStorage provides node-scoped secret access.
type SecretsStorage = storage.SecretsStorage

// SecretValueChanged notifies a node that a secret changed.
type SecretValueChanged = storage.SecretValueChanged

// TlsConfigStorage provides named client TLS configs to nodes.
type TlsConfigStorage = storage.TlsConfigStorage

// TlsConfigChanged notifies a node that a TLS config changed.
type TlsConfigChanged = storage.TlsConfigChanged
