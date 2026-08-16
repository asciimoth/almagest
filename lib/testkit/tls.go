package testkit

import (
	"crypto/tls"
	"sync"

	"github.com/asciimoth/almagest/lib/storage"
)

// TLSConfigGet records one TLS config lookup.
type TLSConfigGet struct {
	Name string
	Node string
}

// TLSConfigStorage is a small in-memory TLS config store for tests.
type TLSConfigStorage struct {
	mu      sync.Mutex
	configs map[string]string
	errs    map[string]error
	gets    []TLSConfigGet
}

// NewTLSConfigStorage returns a test TLS config store keyed by config name.
func NewTLSConfigStorage(configs map[string]string) *TLSConfigStorage {
	copied := make(map[string]string, len(configs))
	for name, serverName := range configs {
		copied[name] = serverName
	}
	return &TLSConfigStorage{configs: copied}
}

// ListConfigs returns all configured names.
func (s *TLSConfigStorage) ListConfigs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	names := make([]string, 0, len(s.configs))
	for name := range s.configs {
		names = append(names, name)
	}
	return names
}

// GetClientConfig returns a TLS config with ServerName set from the store.
func (s *TLSConfigStorage) GetClientConfig(name string, node string) (tls.Config, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.gets = append(s.gets, TLSConfigGet{Name: name, Node: node})
	if err := s.errs[name]; err != nil {
		return tls.Config{}, err
	}
	serverName, ok := s.configs[name]
	if !ok {
		return tls.Config{}, storage.ErrTlsConfigNotFound
	}
	return tls.Config{ServerName: serverName}, nil
}

// Set adds or replaces one config name.
func (s *TLSConfigStorage) Set(name, serverName string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.configs[name] = serverName
}

// SetError adds or replaces the error returned for name.
func (s *TLSConfigStorage) SetError(name string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.errs == nil {
		s.errs = map[string]error{}
	}
	s.errs[name] = err
}

// Gets returns recorded TLS config lookups.
func (s *TLSConfigStorage) Gets() []TLSConfigGet {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]TLSConfigGet(nil), s.gets...)
}
