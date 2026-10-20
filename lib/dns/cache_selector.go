package dnsnodes

import (
	"errors"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/asciimoth/formular"
	"github.com/asciimoth/gonnect/dns"
)

const dnsCacheStorageDisabledPlaceholder = "disabled"

// DNSCacheStorageNames returns a sorted, deduplicated snapshot of storage names
// advertised by manager. Managers that cannot enumerate storages still satisfy
// DnsCacheManager; callers should treat an empty result as "no selector data".
func DNSCacheStorageNames(manager DnsCacheManager) []string {
	lister, ok := manager.(DnsCacheStorageLister)
	if !ok || lister == nil {
		return nil
	}
	names := append([]string(nil), lister.StorageNames()...)
	slices.Sort(names)
	out := names[:0]
	var last string
	for i, name := range names {
		if name == "" || (i > 0 && name == last) {
			continue
		}
		out = append(out, name)
		last = name
	}
	return append([]string(nil), out...)
}

// NormalizeOptionalCacheStorageName normalizes an optional cache storage name.
func NormalizeOptionalCacheStorageName(name string) string {
	return strings.TrimSpace(name)
}

// ValidateOptionalCacheStorageName validates an optional cache storage name.
func ValidateOptionalCacheStorageName(name string) error {
	if name == "" {
		return nil
	}
	if !utf8.ValidString(name) {
		return errors.New("DNS cache storage name must be valid UTF-8")
	}
	return nil
}

// CacheStorageForRouting returns a selected cache storage for routing rules.
func CacheStorageForRouting(manager DnsCacheManager, name string) dns.CacheStorage {
	name = NormalizeOptionalCacheStorageName(name)
	if manager == nil || name == "" {
		return nil
	}
	return manager.Storage(name)
}

// CacheStorageSelectorField builds a cache storage selector field.
func CacheStorageSelectorField(
	id, label, value string,
	manager DnsCacheManager,
	statusErr bool,
	statusText string,
) formular.Item {
	value = NormalizeOptionalCacheStorageName(value)
	opts := []formular.FieldOption{
		formular.Validation,
		formular.Placeholder(dnsCacheStorageDisabledPlaceholder),
	}
	if allowed := cacheStorageAllowedValues(manager, value); len(allowed) > 0 {
		opts = append(opts, formular.AllowedValues(allowed...))
	}
	if statusErr {
		opts = append(opts, formular.Status(formular.StatusError), formular.StatusText(statusText))
	}
	return formular.TextField(id, label, value, opts...)
}

func cacheStorageAllowedValues(manager DnsCacheManager, selected string) []any {
	if _, ok := manager.(DnsCacheStorageLister); !ok {
		return nil
	}
	selected = NormalizeOptionalCacheStorageName(selected)
	names := DNSCacheStorageNames(manager)
	values := make([]any, 0, len(names)+2)
	values = append(values, "")
	seen := map[string]struct{}{"": {}}
	for _, name := range names {
		values = append(values, name)
		seen[name] = struct{}{}
	}
	if selected != "" {
		if _, ok := seen[selected]; !ok {
			values = append(values, selected)
		}
	}
	return values
}
