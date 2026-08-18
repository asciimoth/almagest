package storage

import (
	"crypto/tls"
	"errors"
)

var (
	ErrTlsConfigConflict     = errors.New("tls config name conflict")
	ErrTlsConfigInvalidName  = errors.New("invalid tls config name")
	ErrTlsConfigInvalidValue = errors.New("invalid tls config value")
	ErrTlsConfigNotFound     = errors.New("no such tls config")
	ErrTlsConfigReadOnly     = errors.New("tls config is read-only")
)

// TlsConfigStorage provides named client TLS configs to nodes.
type TlsConfigStorage interface {
	// ListConfigs returns available TLS config names.
	// Currently each config is a client config that verifies a server.
	ListConfigs() []string

	// GetClientConfig returns the named client TLS config.
	// If node is not empty, the storage implementation can later notify that
	// node when the named config changes.
	GetClientConfig(name string, node string) (tls.Config, error)

	// GetLocalCA returns the engine-local certificate authority used by nodes
	// that create local TLS leaf certificates.
	// If node is not empty, the storage implementation can later notify that
	// node when the local CA changes.
	GetLocalCA(node string) (tls.Certificate, error)
}

// TlsConfigChanged is a pasta.InboxMessage payload sent to a node when a TLS
// config it previously fetched changes, is deleted, or is renamed.
//
// NamePrev and NameNew differ only on rename. In all other cases they are
// equal.
type TlsConfigChanged struct {
	NamePrev string
	NameNew  string
}

// TlsLocalCAChanged is a pasta.InboxMessage payload sent to a node when the
// local TLS CA it previously fetched changes.
type TlsLocalCAChanged struct{}
