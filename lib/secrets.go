package almagest

import "errors"

var (
	ErrSecretClosed       = errors.New("secret storage is closed")
	ErrSecretAccessDenied = errors.New("secret access denied")
	ErrSecretConflict     = errors.New("secret name conflict")
	ErrSecretInvalidName  = errors.New("invalid secret key or node name")
	ErrSecretInvalidValue = errors.New("invalid secret value")
	ErrSecretReadOnly     = errors.New("secret is read-only")
)

type Secret struct {
	Value string // empty string means no value

	// ReadOnly reports if secret is provided by read-only storage.
	// ReadOnly flag cannot be set dynamically.
	ReadOnly bool
}

type SecretsStorage interface {
	// GetSecret returns secret if it exists and this node allowed to access it
	// or ErrSecretAccessDenied in both no secret and no access cases.
	GetSecret(key string, node string) (sec Secret, err error)

	// NewSecret creates new secret with random unused key, configures it to
	// allow access from this node and returns a generated key.
	// NewSecret ignores Secret.ReadOnly flag.
	NewSecret(sec Secret, node string) (key string, err error)
}

// SecretValueChanged is intended to use as payload in pasta.InboxMessage to
// notify node that secret it previously fetched via SecretsStorage.GetSecret
// is changed/deleted/renamed.
// SecretNamePrev and SecretNameNew diffs only on secret rename. In all other
// cases they are equals.
type SecretValueChanged struct {
	SecretNamePrev string
	SecretNameNew  string
}
