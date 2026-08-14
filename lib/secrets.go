package almagest

import "github.com/asciimoth/almagest/lib/storage"

var (
	ErrSecretClosed       = storage.ErrSecretClosed
	ErrSecretAccessDenied = storage.ErrSecretAccessDenied
	ErrSecretConflict     = storage.ErrSecretConflict
	ErrSecretInvalidName  = storage.ErrSecretInvalidName
	ErrSecretInvalidValue = storage.ErrSecretInvalidValue
	ErrSecretReadOnly     = storage.ErrSecretReadOnly
)

type Secret = storage.Secret
type SecretsStorage = storage.SecretsStorage
type SecretValueChanged = storage.SecretValueChanged
