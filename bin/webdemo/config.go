package webdemo

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"

	"github.com/asciimoth/configer/hujson"
	tailscalehujson "github.com/tailscale/hujson"
)

const configStorageKey = "almagest.webdemo.config.v1"

const defaultConfig = `{
	"nodes": {
		"Mock Network": {
			"Class": "almagest/MockNetwork",
			"Pos":   "{\"x\":1103,\"y\":211}",
		},
		"Http Client": {
			"Class": "almagest/HttpClient",
			"Pos":   "{\"x\":864,\"y\":211}",
			"Links": ["Network -> [Mock Network] Network"],
			"url":   "http://127.0.0.1:8081/",
		},
		"Http Server": {
			"Class":    "almagest/HttpServer",
			"Pos":      "{\"x\":862,\"y\":358}",
			"Links":    ["Network -> [Mock Network] Network"],
			"port":     8081,
			"response": "HTTP server response",
		},
	},
}`

// ConfigStore keeps the editable web-demo configuration in browser storage.
// An absent value starts with the default demo. An empty value is the empty
// object.
type ConfigStore struct {
	mu          sync.Mutex
	persistence Persistence
	text        []byte
}

func NewConfigStore(persistence Persistence) (*ConfigStore, error) {
	if persistence == nil {
		return nil, fmt.Errorf("web demo config: nil persistence")
	}
	text, found, err := persistence.Load(configStorageKey)
	if err != nil {
		return nil, fmt.Errorf("load web demo config: %w", err)
	}
	if !found {
		text = []byte(defaultConfig)
	} else if strings.TrimSpace(string(text)) == "" {
		text = []byte("{}")
	}
	if _, err := hujson.Parse(text); err != nil {
		return nil, fmt.Errorf("decode web demo config: %w", err)
	}
	text, err = tailscalehujson.Format(text)
	if err != nil {
		return nil, fmt.Errorf("format web demo config: %w", err)
	}
	return &ConfigStore{persistence: persistence, text: append([]byte(nil), text...)}, nil
}

func (s *ConfigStore) Parse() (*hujson.Config, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return hujson.Parse(s.text)
}

func (s *ConfigStore) Snapshot() (text, revision string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return string(s.text), configRevision(s.text)
}

func (s *ConfigStore) Store(text []byte, expectedRevision string) (string, error) {
	if strings.TrimSpace(string(text)) == "" {
		text = []byte("{}")
	}
	parsed, err := hujson.Parse(text)
	if err != nil {
		return "", err
	}
	formatted, err := tailscalehujson.Format(parsed.Pack())
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if expectedRevision != "" && expectedRevision != configRevision(s.text) {
		return "", ErrConfigConflict
	}
	if err := s.persistence.Store(configStorageKey, formatted); err != nil {
		return "", err
	}
	s.text = append(s.text[:0], formatted...)
	return configRevision(s.text), nil
}

func configRevision(text []byte) string {
	sum := sha256.Sum256(text)
	return hex.EncodeToString(sum[:])
}
