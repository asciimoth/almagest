package almagest

import "github.com/asciimoth/almagest/lib/storage"

var (
	ErrTlsConfigConflict     = storage.ErrTlsConfigConflict
	ErrTlsConfigInvalidName  = storage.ErrTlsConfigInvalidName
	ErrTlsConfigInvalidValue = storage.ErrTlsConfigInvalidValue
	ErrTlsConfigNotFound     = storage.ErrTlsConfigNotFound
	ErrTlsConfigReadOnly     = storage.ErrTlsConfigReadOnly
)

type TlsConfigStorage = storage.TlsConfigStorage
type TlsConfigChanged = storage.TlsConfigChanged
