package dnsnodes

import (
	"reflect"
	"testing"

	"github.com/asciimoth/formular"
	"github.com/asciimoth/gonnect/dns"
)

type cacheSelectorManager struct {
	names    []string
	requests []string
}

func (m *cacheSelectorManager) Storage(name string) dns.CacheStorage {
	m.requests = append(m.requests, name)
	return nil
}

func (m *cacheSelectorManager) StorageNames() []string { return m.names }

type cacheManagerWithoutLister struct{}

func (cacheManagerWithoutLister) Storage(string) dns.CacheStorage { return nil }

func TestDNSCacheStorageNames(t *testing.T) {
	manager := &cacheSelectorManager{names: []string{"beta", "", "alpha", "beta"}}
	if got, want := DNSCacheStorageNames(manager), []string{"alpha", "beta"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("DNSCacheStorageNames() = %#v, want %#v", got, want)
	}
	if got := DNSCacheStorageNames(cacheManagerWithoutLister{}); got != nil {
		t.Fatalf("names from manager without lister = %#v, want nil", got)
	}
}

func TestOptionalCacheStorageSelection(t *testing.T) {
	if got := NormalizeOptionalCacheStorageName(" cache "); got != "cache" {
		t.Fatalf("normalized name = %q, want cache", got)
	}
	if err := ValidateOptionalCacheStorageName(""); err != nil {
		t.Fatalf("empty name error = %v", err)
	}
	if err := ValidateOptionalCacheStorageName(string([]byte{0xff})); err == nil {
		t.Fatal("invalid UTF-8 name was accepted")
	}

	manager := &cacheSelectorManager{}
	if got := CacheStorageForRouting(nil, "cache"); got != nil {
		t.Fatalf("nil manager storage = %#v", got)
	}
	if got := CacheStorageForRouting(manager, " "); got != nil || len(manager.requests) != 0 {
		t.Fatalf("empty selection requested storage: %#v, %v", got, manager.requests)
	}
	_ = CacheStorageForRouting(manager, " cache ")
	if !reflect.DeepEqual(manager.requests, []string{"cache"}) {
		t.Fatalf("storage requests = %#v, want [cache]", manager.requests)
	}
}

func TestCacheStorageSelectorField(t *testing.T) {
	manager := &cacheSelectorManager{names: []string{"beta", "alpha"}}
	item := CacheStorageSelectorField("cache", "Cache", " missing ", manager, true, "not found")
	if item.Field == nil || item.Value != "missing" || !item.Validation {
		t.Fatalf("selector field = %#v", item)
	}
	wantAllowed := []any{"", "alpha", "beta", "missing"}
	if !reflect.DeepEqual(item.AllowedValues, wantAllowed) {
		t.Fatalf("allowed values = %#v, want %#v", item.AllowedValues, wantAllowed)
	}
	if item.Status != formular.StatusError || item.StatusText != "not found" || item.Help == "" {
		t.Fatalf("selector status/help = %#v", item)
	}

	withoutLister := CacheStorageSelectorField("cache", "Cache", "selected", cacheManagerWithoutLister{}, false, "")
	if withoutLister.AllowedValues != nil || withoutLister.Status != "" {
		t.Fatalf("selector without lister = %#v", withoutLister)
	}
}
