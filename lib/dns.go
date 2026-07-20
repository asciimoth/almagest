package almagest

import "github.com/asciimoth/gonnect/dns"

type DnsCacheManager interface {
	// Storage returns named CacheStorage instance creating it if it is not exists
	// yet.
	Storage(name string) dns.CacheStorage
}
