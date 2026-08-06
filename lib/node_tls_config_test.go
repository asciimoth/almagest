package almagest

import (
	"crypto/tls"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/asciimoth/formular"
	"github.com/asciimoth/pasta/pasta/std"
)

func TestNodeTLSConfigResolverFallsBackToDefaultWhenSelectionMissing(t *testing.T) {
	storage := newNodeTestTLSConfigStorage(map[string]string{
		DefaultTlsConfigName: "default.test",
	})

	result, err := resolveNodeTLSClientConfig(&NodeBase{engine: &engine{tlsConfigs: storage}}, "missing")
	if err != nil {
		t.Fatalf("resolveNodeTLSClientConfig error = %v, want nil", err)
	}
	if result.config == nil || result.config.ServerName != "default.test" {
		t.Fatalf("resolved TLS config = %#v, want default.test", result.config)
	}
	if result.effectiveName != DefaultTlsConfigName || !result.missingSelected {
		t.Fatalf("resolution metadata = %#v, want default fallback", result)
	}
	storage.assertGets(t, []nodeTestTLSGet{
		{name: "missing"},
		{name: DefaultTlsConfigName},
	})
}

func TestNodeTLSConfigSelectorWarnsForMissingSelection(t *testing.T) {
	storage := newNodeTestTLSConfigStorage(map[string]string{
		DefaultTlsConfigName: "",
		"custom":             "",
	})

	item := nodeTLSConfigSelectorItem(&NodeBase{engine: &engine{tlsConfigs: storage}}, "missing", "")
	if item.Field == nil {
		t.Fatal("selector item has no field")
	}
	if item.Value != DefaultTlsConfigName {
		t.Fatalf("selector value = %#v, want default", item.Value)
	}
	if !reflect.DeepEqual(item.AllowedValues, []any{DefaultTlsConfigName, "custom"}) {
		t.Fatalf("selector allowed values = %#v, want default and custom", item.AllowedValues)
	}
	if item.Status != formular.StatusWarn || !strings.Contains(item.StatusText, "missing") {
		t.Fatalf("selector status = %q %q, want missing warning", item.Status, item.StatusText)
	}
}

func TestNodeTLSConfigChangedRenamesSelectedPolicy(t *testing.T) {
	next, changed := handleNodeTLSConfigChanged("custom", "custom", TlsConfigChanged{
		NamePrev: "custom",
		NameNew:  "renamed",
	})
	if !changed || next != "renamed" {
		t.Fatalf("rename handling = %q, %v; want renamed, true", next, changed)
	}
}

func TestNodeTLSConfigChangedMatchesEffectiveFallback(t *testing.T) {
	next, changed := handleNodeTLSConfigChanged("missing", DefaultTlsConfigName, TlsConfigChanged{
		NamePrev: DefaultTlsConfigName,
		NameNew:  DefaultTlsConfigName,
	})
	if !changed || next != "missing" {
		t.Fatalf("fallback change handling = %q, %v; want missing, true", next, changed)
	}
}

func TestNodeTLSConfigValueFromAnyAcceptsMenuStringShapes(t *testing.T) {
	for _, raw := range []any{" custom ", std.String(" custom ")} {
		got, ok := nodeTLSConfigValueFromAny(raw)
		if !ok || got != "custom" {
			t.Fatalf("TLS config value from %#v = %q, %v; want custom, true", raw, got, ok)
		}
	}
	if got, ok := nodeTLSConfigValueFromAny(42); ok || got != "" {
		t.Fatalf("TLS config value from int = %q, %v; want empty, false", got, ok)
	}
}

type nodeTestTLSGet struct {
	name string
	node string
}

type nodeTestTLSConfigStorage struct {
	mu      sync.Mutex
	configs map[string]string
	errs    map[string]error
	gets    []nodeTestTLSGet
}

func newNodeTestTLSConfigStorage(configs map[string]string) *nodeTestTLSConfigStorage {
	return &nodeTestTLSConfigStorage{configs: configs}
}

func (s *nodeTestTLSConfigStorage) ListConfigs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	names := make([]string, 0, len(s.configs))
	for name := range s.configs {
		names = append(names, name)
	}
	return names
}

func (s *nodeTestTLSConfigStorage) GetClientConfig(name string, node string) (tls.Config, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.gets = append(s.gets, nodeTestTLSGet{name: name, node: node})
	if err := s.errs[name]; err != nil {
		return tls.Config{}, err
	}
	serverName, ok := s.configs[name]
	if !ok {
		return tls.Config{}, ErrTlsConfigNotFound
	}
	return tls.Config{ServerName: serverName}, nil
}

func (s *nodeTestTLSConfigStorage) set(name, serverName string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.configs[name] = serverName
}

func (s *nodeTestTLSConfigStorage) assertGets(t *testing.T, want []nodeTestTLSGet) {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if !reflect.DeepEqual(s.gets, want) {
		t.Fatalf("TLS config gets = %#v, want %#v", s.gets, want)
	}
}
