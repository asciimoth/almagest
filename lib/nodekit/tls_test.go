package nodekit

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/asciimoth/bufpool"
	"github.com/asciimoth/formular"
	"github.com/asciimoth/gonnect/sysnet"
	"github.com/asciimoth/pasta/pasta"
	"github.com/asciimoth/pasta/pasta/std"

	"github.com/asciimoth/almagest/lib/storage"
	"github.com/asciimoth/almagest/lib/testkit"
)

func TestNodeTLSConfigResolverFallsBackToDefaultWhenSelectionMissing(t *testing.T) {
	tlsStorage := testkit.NewTLSConfigStorage(map[string]string{
		DefaultTLSConfigName: "default.test",
	})

	result, err := ResolveTLSClientConfig(&Base{Runtime: tlsTestRuntime{tlsConfigs: tlsStorage}}, "missing")
	if err != nil {
		t.Fatalf("ResolveTLSClientConfig error = %v, want nil", err)
	}
	if result.Config == nil || result.Config.ServerName != "default.test" {
		t.Fatalf("resolved TLS config = %#v, want default.test", result.Config)
	}
	if result.EffectiveName != DefaultTLSConfigName || !result.MissingSelected {
		t.Fatalf("resolution metadata = %#v, want default fallback", result)
	}
	assertTLSGets(t, tlsStorage, []testkit.TLSConfigGet{
		{Name: "missing"},
		{Name: DefaultTLSConfigName},
	})
}

func TestNodeTLSConfigSelectorWarnsForMissingSelection(t *testing.T) {
	tlsStorage := testkit.NewTLSConfigStorage(map[string]string{
		DefaultTLSConfigName: "",
		"custom":             "",
	})

	item := TLSConfigSelectorItem(&Base{Runtime: tlsTestRuntime{tlsConfigs: tlsStorage}}, "missing", "")
	if item.Field == nil {
		t.Fatal("selector item has no field")
	}
	if item.Value != DefaultTLSConfigName {
		t.Fatalf("selector value = %#v, want default", item.Value)
	}
	if !reflect.DeepEqual(item.AllowedValues, []any{DefaultTLSConfigName, "custom"}) {
		t.Fatalf("selector allowed values = %#v, want default and custom", item.AllowedValues)
	}
	if item.Status != formular.StatusWarn || !strings.Contains(item.StatusText, "missing") {
		t.Fatalf("selector status = %q %q, want missing warning", item.Status, item.StatusText)
	}
}

func TestNodeTLSConfigChangedRenamesSelectedPolicy(t *testing.T) {
	next, changed := HandleTLSConfigChanged("custom", "custom", storage.TlsConfigChanged{
		NamePrev: "custom",
		NameNew:  "renamed",
	})
	if !changed || next != "renamed" {
		t.Fatalf("rename handling = %q, %v; want renamed, true", next, changed)
	}
}

func TestNodeTLSConfigChangedMatchesEffectiveFallback(t *testing.T) {
	next, changed := HandleTLSConfigChanged("missing", DefaultTLSConfigName, storage.TlsConfigChanged{
		NamePrev: DefaultTLSConfigName,
		NameNew:  DefaultTLSConfigName,
	})
	if !changed || next != "missing" {
		t.Fatalf("fallback change handling = %q, %v; want missing, true", next, changed)
	}
}

func TestNodeTLSConfigValueFromAnyAcceptsMenuStringShapes(t *testing.T) {
	for _, raw := range []any{" custom ", std.String(" custom ")} {
		got, ok := TLSConfigValueFromAny(raw)
		if !ok || got != "custom" {
			t.Fatalf("TLS config value from %#v = %q, %v; want custom, true", raw, got, ok)
		}
	}
	if got, ok := TLSConfigValueFromAny(42); ok || got != "" {
		t.Fatalf("TLS config value from int = %q, %v; want empty, false", got, ok)
	}
}

type tlsTestRuntime struct {
	tlsConfigs storage.TlsConfigStorage
}

func (r tlsTestRuntime) Context() context.Context { return context.Background() }

func (r tlsTestRuntime) Pool() bufpool.Pool { return nil }

func (r tlsTestRuntime) System() sysnet.System { return nil }

func (r tlsTestRuntime) KVManager() storage.KVManager { return nil }

func (r tlsTestRuntime) DnsCacheManager() storage.DnsCacheManager { return nil }

func (r tlsTestRuntime) SecretsStorage() storage.SecretsStorage { return nil }

func (r tlsTestRuntime) TlsConfigStorage() storage.TlsConfigStorage { return r.tlsConfigs }

func (r tlsTestRuntime) WorkerSpawner(uint64) WorkerSpawner { return nil }

func (r tlsTestRuntime) ObserveNodeSnapshot(uint64, pasta.NodeSnapshot) {}

func assertTLSGets(t *testing.T, store *testkit.TLSConfigStorage, want []testkit.TLSConfigGet) {
	t.Helper()
	if got := store.Gets(); !reflect.DeepEqual(got, want) {
		t.Fatalf("TLS config gets = %#v, want %#v", got, want)
	}
}
