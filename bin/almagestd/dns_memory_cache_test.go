package main

import (
	"testing"
	"time"

	gdns "github.com/asciimoth/gonnect/dns"
)

type memoryDNSCacheTestManager struct {
	stores map[string]*memoryDNSCacheTestStorage
}

func (m *memoryDNSCacheTestManager) Storage(name string) gdns.CacheStorage {
	if m.stores == nil {
		m.stores = make(map[string]*memoryDNSCacheTestStorage)
	}
	if m.stores[name] == nil {
		m.stores[name] = &memoryDNSCacheTestStorage{entries: make(map[string]memoryDNSCacheTestEntry)}
	}
	return m.stores[name]
}

type memoryDNSCacheTestEntry struct {
	msg     *gdns.Message
	expires time.Time
}

type memoryDNSCacheTestStorage struct {
	entries map[string]memoryDNSCacheTestEntry
	gets    int
	sets    int
	deletes int
}

func (s *memoryDNSCacheTestStorage) getWithExpiration(key string, now time.Time) (*gdns.Message, time.Time, bool) {
	s.gets++
	entry, found := s.entries[key]
	if !found || !entry.expires.IsZero() && !now.Before(entry.expires) {
		delete(s.entries, key)
		return nil, time.Time{}, false
	}
	return entry.msg.Copy(), entry.expires, true
}

func (s *memoryDNSCacheTestStorage) Get(key string, now time.Time) (*gdns.Message, bool) {
	msg, _, found := s.getWithExpiration(key, now)
	return msg, found
}

func (s *memoryDNSCacheTestStorage) Set(key string, msg *gdns.Message, now time.Time) {
	s.sets++
	if msg == nil || !msg.Response || msg.RCode != gdns.RCodeSuccess {
		return
	}
	if key == "" {
		key = dnsStorageCacheKey(msg)
	}
	ttl := dnsMessageMinTTL(msg)
	if key == "" || ttl == 0 {
		return
	}
	s.entries[key] = memoryDNSCacheTestEntry{msg: msg.Copy(), expires: now.Add(time.Duration(ttl) * time.Second)}
}

func (s *memoryDNSCacheTestStorage) Delete(key string) {
	s.deletes++
	delete(s.entries, key)
}

func TestMemoryDNSCacheManagerCachesCopiesAndPreservesExpiration(t *testing.T) {
	now := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	innerManager := &memoryDNSCacheTestManager{}
	inner := innerManager.Storage("resolver").(*memoryDNSCacheTestStorage)
	inner.entries["key"] = memoryDNSCacheTestEntry{
		msg:     testDNSCacheMessage("example.test.", gdns.TypeA, []byte{192, 0, 2, 1}, 300),
		expires: now.Add(time.Minute),
	}
	manager := newMemoryDNSCacheManager(innerManager, 1024, false)
	storage := manager.Storage("resolver")

	first, found := storage.Get("key", now)
	if !found || inner.gets != 1 {
		t.Fatalf("first Get = %#v, %v; durable gets = %d", first, found, inner.gets)
	}
	first.Answers[0].Data[3] = 99
	second, found := storage.Get("key", now.Add(30*time.Second))
	if !found || inner.gets != 1 || second.Answers[0].Data[3] != 1 {
		t.Fatalf("cached Get = %#v, %v; durable gets = %d", second, found, inner.gets)
	}
	if _, found := storage.Get("key", now.Add(time.Minute)); found || inner.gets != 2 {
		t.Fatalf("expired Get found = %v; durable gets = %d", found, inner.gets)
	}
}

func TestMemoryDNSCacheManagerWritesThroughAndInvalidates(t *testing.T) {
	now := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	innerManager := &memoryDNSCacheTestManager{}
	manager := newMemoryDNSCacheManager(innerManager, 1024, false)
	storage := manager.Storage("resolver")
	inner := innerManager.stores["resolver"]
	message := testDNSCacheMessage("example.test.", gdns.TypeA, []byte{192, 0, 2, 2}, 60)

	storage.Set("key", message, now)
	if inner.sets != 1 || inner.gets != 1 {
		t.Fatalf("write-through calls: sets=%d gets=%d", inner.sets, inner.gets)
	}
	if _, found := storage.Get("key", now); !found || inner.gets != 1 {
		t.Fatalf("Get after Set found = %v; durable gets = %d", found, inner.gets)
	}

	inner.entries["key"] = memoryDNSCacheTestEntry{
		msg:     testDNSCacheMessage("example.test.", gdns.TypeA, []byte{192, 0, 2, 3}, 60),
		expires: now.Add(time.Minute),
	}
	manager.invalidateChange(dnsCacheChange{kind: dnsCacheChangeKeyUpsert, cache: "resolver", key: "key"})
	updated, found := storage.Get("key", now)
	if !found || updated.Answers[0].Data[3] != 3 || inner.gets != 2 {
		t.Fatalf("Get after invalidation = %#v, %v; durable gets = %d", updated, found, inner.gets)
	}
	inner.entries["key"] = memoryDNSCacheTestEntry{
		msg:     testDNSCacheMessage("example.test.", gdns.TypeA, []byte{192, 0, 2, 4}, 60),
		expires: now.Add(time.Minute),
	}
	manager.invalidateChange(dnsCacheChange{kind: dnsCacheChangeCacheUpsert, cache: "resolver"})
	replaced, found := storage.Get("key", now)
	if !found || replaced.Answers[0].Data[3] != 4 || inner.gets != 3 {
		t.Fatalf("Get after cache reset = %#v, %v; durable gets = %d", replaced, found, inner.gets)
	}

	storage.Delete("key")
	if inner.deletes != 1 {
		t.Fatalf("durable deletes = %d", inner.deletes)
	}
	if _, found := storage.Get("key", now); found {
		t.Fatal("Get found deleted entry")
	}
}

func TestMemoryDNSCacheManagerEvictsLeastRecentlyUsedWithinByteLimit(t *testing.T) {
	now := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	innerManager := &memoryDNSCacheTestManager{}
	inner := innerManager.Storage("resolver").(*memoryDNSCacheTestStorage)
	message := testDNSCacheMessage("example.test.", gdns.TypeA, []byte{192, 0, 2, 4}, 60)
	wire, err := gdns.Pack(message)
	if err != nil {
		t.Fatal(err)
	}
	entrySize := dnsMemoryCacheEntryOverhead + len("resolver") + len("first") + len(wire)
	manager := newMemoryDNSCacheManager(innerManager, entrySize*2, false)
	storage := manager.Storage("resolver")
	for _, key := range []string{"first", "other"} {
		inner.entries[key] = memoryDNSCacheTestEntry{msg: message, expires: now.Add(time.Minute)}
		if _, found := storage.Get(key, now); !found {
			t.Fatalf("Get(%q) missed", key)
		}
	}
	if _, found := storage.Get("first", now); !found {
		t.Fatal("recent Get(first) missed")
	}
	inner.entries["third"] = memoryDNSCacheTestEntry{msg: message, expires: now.Add(time.Minute)}
	if _, found := storage.Get("third", now); !found {
		t.Fatal("Get(third) missed")
	}
	manager.mu.Lock()
	_, firstRetained := manager.entries[memoryDNSCacheKey{cache: "resolver", key: "first"}]
	_, otherRetained := manager.entries[memoryDNSCacheKey{cache: "resolver", key: "other"}]
	_, thirdRetained := manager.entries[memoryDNSCacheKey{cache: "resolver", key: "third"}]
	bytes := manager.bytes
	manager.mu.Unlock()
	if !firstRetained || otherRetained || !thirdRetained || bytes > manager.maxBytes {
		t.Fatalf("LRU state: first=%v other=%v third=%v bytes=%d limit=%d", firstRetained, otherRetained, thirdRetained, bytes, manager.maxBytes)
	}
}

func TestMemoryDNSCacheManagerHonorsDisabledExpiration(t *testing.T) {
	now := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	innerManager := &memoryDNSCacheTestManager{}
	inner := innerManager.Storage("resolver").(*memoryDNSCacheTestStorage)
	inner.entries["key"] = memoryDNSCacheTestEntry{
		msg:     testDNSCacheMessage("example.test.", gdns.TypeA, []byte{192, 0, 2, 5}, 1),
		expires: now.Add(time.Second),
	}
	manager := newMemoryDNSCacheManager(innerManager, 1024, true)
	storage := manager.Storage("resolver")
	if _, found := storage.Get("key", now); !found {
		t.Fatal("initial Get missed")
	}
	if _, found := storage.Get("key", now.Add(time.Hour)); !found || inner.gets != 1 {
		t.Fatalf("disabled-expiration Get found = %v; durable gets = %d", found, inner.gets)
	}
}

func TestDNSCacheManagerForExecutionMode(t *testing.T) {
	inner := &memoryDNSCacheTestManager{}
	direct, directCache := dnsCacheManagerForExecutionMode(executionModeDirect, inner, false)
	if direct != inner || directCache != nil {
		t.Fatalf("direct manager = %#v, cache = %#v; want unchanged manager", direct, directCache)
	}
	for _, mode := range []executionMode{executionModeIsolation, executionModeIsolationDevelopment} {
		got, cache := dnsCacheManagerForExecutionMode(mode, inner, false)
		if got != cache || cache == nil || cache.maxBytes != isolationDNSMemoryCacheBytes {
			t.Fatalf("%s manager = %#v, cache = %#v", mode, got, cache)
		}
	}
}
