package storage

import "errors"

var (
	ErrSecretClosed       = errors.New("secret storage is closed")
	ErrSecretAccessDenied = errors.New("secret access denied")
	ErrSecretConflict     = errors.New("secret name conflict")
	ErrSecretInvalidName  = errors.New("invalid secret key or node name")
	ErrSecretInvalidValue = errors.New("invalid secret value")
	ErrSecretReadOnly     = errors.New("secret is read-only")
)

// Secret is one secret value visible to a node.
type Secret struct {
	// Value is empty when no value is present.
	Value string

	// ReadOnly reports that the secret is provided by read-only storage.
	// Dynamic updates cannot set this flag.
	ReadOnly bool
}

// SecretsStorage provides node-scoped secret access.
type SecretsStorage interface {
	// GetSecret returns sec if it exists and node is allowed to access it.
	// It returns ErrSecretAccessDenied for both missing and denied secrets.
	GetSecret(key string, node string) (sec Secret, err error)

	// NewSecret creates a secret with a random unused key, allows this node to
	// access it, and returns the generated key. It ignores Secret.ReadOnly.
	NewSecret(sec Secret, node string) (key string, err error)
}

// GeneratedSecretsStorage manages generated secrets until their selectors are
// durable. Implementations must keep recovery metadata durable. They must
// delete a key only when it is mutable and its allow list contains exactly
// node. Deletion must be idempotent when the key does not exist.
type GeneratedSecretsStorage interface {
	SecretsStorage
	// NewGeneratedSecret creates or returns the uncommitted secret identified by
	// owner for this node. Owner is an opaque, stable identifier within a node.
	NewGeneratedSecret(sec Secret, node, owner string) (key string, err error)
	// RecoverGeneratedSecret returns an uncommitted generated secret. An empty
	// key and nil error mean that no matching secret exists.
	RecoverGeneratedSecret(owner, node string) (key string, sec Secret, err error)
	// CommitGeneratedSecret removes crash-recovery metadata after the node
	// configuration that contains key is durable.
	CommitGeneratedSecret(key, node string) error
	DeleteGeneratedSecret(key, node string) error
}

// SecretValueChanged is a pasta.InboxMessage payload sent to a node when a
// secret it previously fetched changes, is deleted, or is renamed.
//
// SecretNamePrev and SecretNameNew differ only on rename. In all other cases
// they are equal.
type SecretValueChanged struct {
	SecretNamePrev string
	SecretNameNew  string
}
