package almagest

import "github.com/asciimoth/gonnect/dns"

type DnsCacheManager interface {
	// Storage returns named CacheStorage instance creating it if it is not exists
	// yet.
	Storage(name string) dns.CacheStorage
}

type DnsCacheStorageLister interface {
	// StorageNames returns the currently known DNS cache storage names. It is an
	// optional extension used by node menus to expose cache-backed selectors.
	StorageNames() []string
}
