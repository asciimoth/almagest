package dnsnodes

import "github.com/asciimoth/gonnect/dns"

// DnsCacheManager opens named DNS cache stores.
type DnsCacheManager interface {
	// Storage returns a named CacheStorage instance, creating it if needed.
	Storage(name string) dns.CacheStorage
}

// DnsCacheStorageLister is an optional extension for managers that can list
// known DNS cache storage names.
type DnsCacheStorageLister interface {
	// StorageNames returns the currently known DNS cache storage names.
	StorageNames() []string
}
