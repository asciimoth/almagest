package almagest

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

func normalizeOptionalDNSCacheStorageName(name string) string {
	return strings.TrimSpace(name)
}

func validateOptionalDNSCacheStorageName(name string) error {
	if name == "" {
		return nil
	}
	if !utf8.ValidString(name) {
		return errors.New("DNS cache storage name must be valid UTF-8")
	}
	return nil
}

func dnsCacheStorageForRouting(manager DnsCacheManager, name string) dns.CacheStorage {
	name = normalizeOptionalDNSCacheStorageName(name)
	if manager == nil || name == "" {
		return nil
	}
	return manager.Storage(name)
}

func dnsCacheStorageSelectorField(
	id, label, value string,
	manager DnsCacheManager,
	statusErr bool,
	statusText string,
) formular.Item {
	value = normalizeOptionalDNSCacheStorageName(value)
	opts := []formular.FieldOption{
		formular.Validation,
		formular.Placeholder(dnsCacheStorageDisabledPlaceholder),
	}
	if allowed := dnsCacheStorageAllowedValues(manager, value); len(allowed) > 0 {
		opts = append(opts, formular.AllowedValues(allowed...))
	}
	if statusErr {
		opts = append(opts, formular.Status(formular.StatusError), formular.StatusText(statusText))
	}
	item := formular.TextField(id, label, value, opts...)
	item.Help = "Optional CacheStorage name used by bytecode address string and regexp rules for reverse DNS lookups. Leave empty to match only literal address strings. Populate the storage with an almagest/DnsCache node that has reverse lookups enabled."
	return item
}

func dnsCacheStorageAllowedValues(manager DnsCacheManager, selected string) []any {
	if _, ok := manager.(DnsCacheStorageLister); !ok {
		return nil
	}
	selected = normalizeOptionalDNSCacheStorageName(selected)
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
