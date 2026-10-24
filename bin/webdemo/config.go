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
			"Pos":   "{\"x\":800,\"y\":220}",
		},
		"Http Client": {
			"Class": "almagest/HttpClient",
			"Pos":   "{\"x\":40,\"y\":100}",
			"Links": ["Network -> [Client IP Stack] Network"],
			"url":   "http://10.220.0.10:8081/",
		},
		"Client IP Stack": {
			"Class":      "almagest/VTun",
			"Pos":        "{\"x\":290,\"y\":100}",
			"Links":      ["Tun -> [Spoofer] Tun"],
			"localAddrs": "10.220.0.1/24",
			"name":       "spoofer-client",
		},
		"Spoofer": {
			"Class": "almagest/Spoofer",
			"Pos":   "{\"x\":545,\"y\":100}",
			"Links": ["Network -> [Mock Network] Network"],
		},
		"Http Server": {
			"Class": "almagest/HttpServer",
			"Pos":   "{\"x\":545,\"y\":285}",
			"Links": ["Network -> [Mock Network] Network"],
			"host":  "10.220.0.10",
			"port":  8081,
		},
		"Spoofer Response": {
			"Class": "pasta/StringConstant",
			"Pos":   "{\"x\":211,\"y\":281}",
			"Links": ["output -> [Http Server] Response"],
			"value": "Response after TUN flow conversion",
		},
		"TUN HTTP Client": {
			"Class": "almagest/HttpClient",
			"Pos":   "{\"x\":40,\"y\":500}",
			"Links": ["Network -> [TUN Client Stack] Network"],
			"url":   "http://service.webdemo.test:8082/",
		},
		"TUN Client Stack": {
			"Class":      "almagest/VTun",
			"Pos":        "{\"x\":290,\"y\":500}",
			"Links":      ["Tun -> [Direct TUN Link] TunA"],
			"dnsServers": "10.219.0.2",
			"localAddrs": "10.219.0.1/24",
			"name":       "tun-client",
		},
		"Direct TUN Link": {
			"Class": "almagest/TunCopy",
			"Pos":   "{\"x\":545,\"y\":590}",
		},
		"TUN Server Stack": {
			"Class":      "almagest/VTun",
			"Pos":        "{\"x\":290,\"y\":690}",
			"Links":      ["Tun -> [Direct TUN Link] TunB"],
			"localAddrs": "10.219.0.2/24",
			"name":       "tun-server",
		},
		"TUN HTTP Server": {
			"Class": "almagest/HttpServer",
			"Pos":   "{\"x\":44,\"y\":680}",
			"Links": ["Network -> [TUN Server Stack] Network"],
			"host":  "10.219.0.2",
			"port":  8082,
		},
		"Direct TUN Response": {
			"Class": "pasta/StringConstant",
			"Pos":   "{\"x\":-274,\"y\":680}",
			"Links": ["output -> [TUN HTTP Server] Response"],
			"value": "Response across a direct TUN link",
		},
		"TUN DNS Server": {
			"Class":   "almagest/DnsServer",
			"Pos":     "{\"x\":44,\"y\":847}",
			"Links":   ["Network -> [TUN Server Stack] Network", "DNS -> [DNS Cache] DNS"],
			"host":    "10.219.0.2",
			"network": "udp4",
			"port":    53,
		},
		"DNS Cache": {
			"Class":       "almagest/DnsCache",
			"Pos":         "{\"x\":290,\"y\":865}",
			"Links":       ["DNS -> [Static DNS] DNS"],
			"storageName": "webdemo",
		},
		"Static DNS": {
			"Class":   "almagest/StaticDns",
			"Pos":     "{\"x\":498,\"y\":864}",
			"records": "service.webdemo.test A 10.219.0.2",
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
