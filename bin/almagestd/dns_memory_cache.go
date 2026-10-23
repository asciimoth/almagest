package main

import (
	"container/list"
	"sync"
	"time"

	"github.com/asciimoth/almagest/lib/storage"
	gdns "github.com/asciimoth/gonnect/dns"
)

const (
	// isolationDNSMemoryCacheBytes bounds the estimated retained size before
	// bbolt, including DNS wire data, names, keys, and per-entry overhead.
	isolationDNSMemoryCacheBytes = 10 << 20
	dnsMemoryCacheEntryOverhead  = 128
)

var (
	_ storage.DnsCacheManager       = (*memoryDNSCacheManager)(nil)
	_ storage.DnsCacheStorageLister = (*memoryDNSCacheManager)(nil)
	_ gdns.CacheStorage             = (*memoryDNSCacheStorage)(nil)
)

// dnsCacheStorageWithExpiration is implemented by durable stores that can
// expose the original absolute expiration time. A read-through cache must not
// derive a new expiration time from a stored message's original TTL.
type dnsCacheStorageWithExpiration interface {
	getWithExpiration(string, time.Time) (*gdns.Message, time.Time, bool)
}

type memoryDNSCacheKey struct {
	cache string
	key   string
}

type memoryDNSCacheEntry struct {
	id      memoryDNSCacheKey
	wire    []byte
	expires time.Time
	size    int
}

// memoryDNSCacheManager is a transparent, bounded read-through layer shared by
// all named DNS cache stores in an isolated runtime. It keeps hot checks in the
// runtime process and writes every value through to the durable manager.
// A byte-bounded LRU gives O(1) hits and eviction without a maintenance worker.
type memoryDNSCacheManager struct {
	inner             storage.DnsCacheManager
	maxBytes          int
	disableExpiration bool

	mu       sync.Mutex
	stores   map[string]*memoryDNSCacheStorage
	entries  map[memoryDNSCacheKey]*list.Element
	lru      list.List
	bytes    int
	revision uint64

	bbolt          *bboltKVManager
	subscriptionID uint64
}

type memoryDNSCacheStorage struct {
	manager *memoryDNSCacheManager
	name    string
	inner   gdns.CacheStorage
}

func dnsCacheManagerForExecutionMode(
	mode executionMode,
	inner storage.DnsCacheManager,
	disableExpiration bool,
) (storage.DnsCacheManager, *memoryDNSCacheManager) {
	if mode == executionModeDirect {
		return inner, nil
	}
	cache := newMemoryDNSCacheManager(inner, isolationDNSMemoryCacheBytes, disableExpiration)
	return cache, cache
}

func newMemoryDNSCacheManager(inner storage.DnsCacheManager, maxBytes int, disableExpiration bool) *memoryDNSCacheManager {
	m := &memoryDNSCacheManager{
		inner: inner, maxBytes: maxBytes, disableExpiration: disableExpiration,
		stores: make(map[string]*memoryDNSCacheStorage), entries: make(map[memoryDNSCacheKey]*list.Element),
	}
	if bbolt, ok := inner.(*bboltKVManager); ok {
		m.bbolt = bbolt
		m.subscriptionID = bbolt.subscribeDNSChanges(m.invalidateChange)
	}
	return m
}

func (m *memoryDNSCacheManager) Close() error {
	if m == nil || m.bbolt == nil || m.subscriptionID == 0 {
		return nil
	}
	m.bbolt.unsubscribeDNSChanges(m.subscriptionID)
	m.subscriptionID = 0
	return nil
}

func (m *memoryDNSCacheManager) Storage(name string) gdns.CacheStorage {
	if m == nil || m.inner == nil {
		return nil
	}
	m.mu.Lock()
	if cached := m.stores[name]; cached != nil {
		m.mu.Unlock()
		return cached
	}
	m.mu.Unlock()

	// Opening a durable storage can publish a cache-upsert notification. Do not
	// hold the middleware lock while the synchronous notification runs.
	inner := m.inner.Storage(name)
	wrapped := &memoryDNSCacheStorage{manager: m, name: name, inner: inner}
	m.mu.Lock()
	defer m.mu.Unlock()
	if cached := m.stores[name]; cached != nil {
		return cached
	}
	m.stores[name] = wrapped
	return wrapped
}

func (m *memoryDNSCacheManager) StorageNames() []string {
	if m == nil {
		return nil
	}
	if lister, ok := m.inner.(storage.DnsCacheStorageLister); ok {
		return lister.StorageNames()
	}
	return nil
}

func (m *memoryDNSCacheManager) invalidateChange(change dnsCacheChange) {
	if change.kind == dnsCacheChangeKeyUpsert || change.kind == dnsCacheChangeKeyDelete {
		m.invalidate(memoryDNSCacheKey{cache: change.cache, key: change.key})
		return
	}
	m.mu.Lock()
	m.revision++
	for id, element := range m.entries {
		if id.cache == change.cache {
			m.removeLocked(element)
		}
	}
	m.mu.Unlock()
}

func (m *memoryDNSCacheManager) invalidate(id memoryDNSCacheKey) {
	m.mu.Lock()
	m.revision++
	if element := m.entries[id]; element != nil {
		m.removeLocked(element)
	}
	m.mu.Unlock()
}

func (m *memoryDNSCacheManager) get(id memoryDNSCacheKey, now time.Time) (*gdns.Message, bool) {
	m.mu.Lock()
	element := m.entries[id]
	if element == nil {
		m.mu.Unlock()
		return nil, false
	}
	entry := element.Value.(*memoryDNSCacheEntry)
	if !entry.expires.IsZero() && !now.Before(entry.expires) {
		m.revision++
		m.removeLocked(element)
		m.mu.Unlock()
		return nil, false
	}
	m.lru.MoveToFront(element)
	wire := append([]byte(nil), entry.wire...)
	m.mu.Unlock()
	msg, err := gdns.Unpack(wire)
	if err != nil {
		m.invalidate(id)
		return nil, false
	}
	return msg, true
}

func (m *memoryDNSCacheManager) put(id memoryDNSCacheKey, msg *gdns.Message, expires time.Time, revision uint64) {
	if msg == nil {
		return
	}
	wire, err := gdns.Pack(msg)
	size := dnsMemoryCacheEntryOverhead + len(id.cache) + len(id.key) + len(wire)
	if err != nil || size > m.maxBytes || m.maxBytes <= 0 {
		return
	}
	if m.disableExpiration {
		expires = time.Time{}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.revision != revision {
		return
	}
	if old := m.entries[id]; old != nil {
		m.removeLocked(old)
	}
	entry := &memoryDNSCacheEntry{id: id, wire: wire, expires: expires, size: size}
	m.entries[id] = m.lru.PushFront(entry)
	m.bytes += size
	for m.bytes > m.maxBytes {
		m.removeLocked(m.lru.Back())
	}
}

func (m *memoryDNSCacheManager) removeLocked(element *list.Element) {
	if element == nil {
		return
	}
	entry := element.Value.(*memoryDNSCacheEntry)
	delete(m.entries, entry.id)
	m.bytes -= entry.size
	m.lru.Remove(element)
}

func (s *memoryDNSCacheStorage) Get(key string, now time.Time) (*gdns.Message, bool) {
	if s == nil || s.manager == nil || s.inner == nil {
		return nil, false
	}
	id := memoryDNSCacheKey{cache: s.name, key: key}
	if msg, found := s.manager.get(id, now); found {
		return msg, true
	}
	s.manager.mu.Lock()
	revision := s.manager.revision
	s.manager.mu.Unlock()
	storage, ok := s.inner.(dnsCacheStorageWithExpiration)
	if !ok {
		// Without an absolute expiration time, a value loaded from persistence
		// cannot be cached without changing its TTL. Keep the fallback exact.
		return s.inner.Get(key, now)
	}
	msg, expires, found := storage.getWithExpiration(key, now)
	if found {
		s.manager.put(id, msg, expires, revision)
	}
	return msg, found
}

func (s *memoryDNSCacheStorage) Set(key string, msg *gdns.Message, now time.Time) {
	if s == nil || s.manager == nil || s.inner == nil {
		return
	}
	s.inner.Set(key, msg, now)
	if key == "" {
		key = dnsStorageCacheKey(msg)
	}
	if key != "" {
		// Read back through the durable store. This caches only a successful
		// write and obtains the exact expiration timestamp.
		_, _ = s.Get(key, now)
	}
}

func (s *memoryDNSCacheStorage) Delete(key string) {
	if s == nil || s.manager == nil || s.inner == nil {
		return
	}
	s.inner.Delete(key)
	// The durable store does not publish a change when the key did not exist.
	s.manager.invalidate(memoryDNSCacheKey{cache: s.name, key: key})
}
