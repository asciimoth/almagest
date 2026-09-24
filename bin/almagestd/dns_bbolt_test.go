package main

import (
	"bytes"
	"path/filepath"
	"testing"
	"time"

	gdns "github.com/asciimoth/gonnect/dns"
	"go.etcd.io/bbolt"
)

func TestBboltDNSCacheStoragePersistsJSONAndCachesMessages(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kv.db")
	manager, err := openKVManager(path)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close() //nolint:errcheck

	storage := manager.Storage("resolver").(*bboltDNSCacheStorage)
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	msg := testDNSCacheMessage("example.test.", gdns.TypeA, []byte{192, 0, 2, 10}, 60)
	storage.Set("", msg, now)

	key := "example.test.|1|1"
	got, ok := storage.Get(key, now.Add(time.Second))
	if !ok {
		t.Fatal("Get returned miss, want cached response")
	}
	if got == msg || got.Answers[0].Data[0] != 192 {
		t.Fatalf("Get returned %#v, want copied DNS message", got)
	}
	got.Answers[0].Data[0] = 203
	got, ok = storage.Get(key, now.Add(2*time.Second))
	if !ok || got.Answers[0].Data[0] != 192 {
		t.Fatalf("second Get returned %#v, %v; want independent cached copy", got, ok)
	}

	var raw []byte
	if err := manager.db.View(func(tx *bbolt.Tx) error {
		bucket := dnsCacheBucket(tx, "resolver")
		raw = append([]byte(nil), bucket.Get([]byte(key))...)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(raw, []byte(`"expires_at"`)) || !bytes.Contains(raw, []byte(`"answers"`)) {
		t.Fatalf("raw dns cache value = %s, want json entry with expiration and answers", raw)
	}
	if bytes.Contains(raw, []byte(`"opcode"`)) || bytes.Contains(raw, []byte(`"authorities"`)) {
		t.Fatalf("raw dns cache value = %s, want omitted zero-value DNS fields", raw)
	}

	if err := manager.db.Update(func(tx *bbolt.Tx) error {
		return dnsCacheBucket(tx, "resolver").Put([]byte(key), []byte(`{`))
	}); err != nil {
		t.Fatal(err)
	}
	got, ok = storage.Get(key, now.Add(3*time.Second))
	if !ok || got.Answers[0].Data[0] != 192 {
		t.Fatalf("memory-backed Get returned %#v, %v; want cached DNS message without reparsing bbolt json", got, ok)
	}
}

func TestBboltDNSCacheStorageDeletesExpiredOnGet(t *testing.T) {
	manager, err := openKVManager(filepath.Join(t.TempDir(), "kv.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close() //nolint:errcheck

	storage := manager.Storage("resolver").(*bboltDNSCacheStorage)
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	storage.Set("expired", testDNSCacheMessage("expired.test.", gdns.TypeA, []byte{192, 0, 2, 20}, 1), now)
	if _, ok := storage.Get("expired", now.Add(2*time.Second)); ok {
		t.Fatal("Get returned hit after expiration")
	}

	if err := manager.db.View(func(tx *bbolt.Tx) error {
		if raw := dnsCacheBucket(tx, "resolver").Get([]byte("expired")); raw != nil {
			t.Fatalf("expired raw value still present: %s", raw)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestBboltDNSCacheStorageCanDisableExpiration(t *testing.T) {
	manager, err := openKVManagerWithOptions(filepath.Join(t.TempDir(), "kv.db"), bboltKVManagerOptions{
		DisableDNSCacheExpiration: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close() //nolint:errcheck

	storage := manager.Storage("resolver").(*bboltDNSCacheStorage)
	expiredAtSetTime := time.Now().Add(-time.Hour)
	storage.Set("expired", testDNSCacheMessage("expired.test.", gdns.TypeA, []byte{192, 0, 2, 21}, 1), expiredAtSetTime)

	got, ok := storage.Get("expired", time.Now())
	if !ok {
		t.Fatal("Get returned miss with DNS cache expiration disabled")
	}
	if got.Answers[0].Data[3] != 21 {
		t.Fatalf("Get returned %#v, want expired cached DNS message", got)
	}

	keys, err := manager.dnsKeysSnapshot("resolver")
	if err != nil {
		t.Fatal(err)
	}
	if !keys.Exists || len(keys.Keys) != 1 || keys.Keys[0].Key != "expired" {
		t.Fatalf("dnsKeysSnapshot = %#v, want expired key retained", keys)
	}

	value, err := manager.dnsValueSnapshot("resolver", "expired")
	if err != nil {
		t.Fatal(err)
	}
	if !value.Found || value.Value == nil || value.Value.ExpiresAt == "" {
		t.Fatalf("dnsValueSnapshot = %#v, want expired value retained with expiration metadata", value)
	}
}

func TestBboltDNSCacheStorageCleansExpiredEntriesOnStartup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kv.db")
	manager, err := openKVManager(path)
	if err != nil {
		t.Fatal(err)
	}
	storage := manager.Storage("resolver").(*bboltDNSCacheStorage)
	now := time.Now()
	storage.Set("expired", testDNSCacheMessage("expired.test.", gdns.TypeA, []byte{192, 0, 2, 30}, 1), now.Add(-time.Hour))
	storage.Set("live", testDNSCacheMessage("live.test.", gdns.TypeA, []byte{192, 0, 2, 31}, 3600), now)
	if err := manager.Close(); err != nil {
		t.Fatal(err)
	}

	manager, err = openKVManager(path)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close() //nolint:errcheck

	if err := manager.db.View(func(tx *bbolt.Tx) error {
		bucket := dnsCacheBucket(tx, "resolver")
		if raw := bucket.Get([]byte("expired")); raw != nil {
			t.Fatalf("expired startup value still present: %s", raw)
		}
		if raw := bucket.Get([]byte("live")); raw == nil {
			t.Fatal("live startup value missing")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestBboltDNSCacheStorageDisabledExpirationSkipsStartupCleanup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kv.db")
	manager, err := openKVManager(path)
	if err != nil {
		t.Fatal(err)
	}
	storage := manager.Storage("resolver").(*bboltDNSCacheStorage)
	storage.Set("expired", testDNSCacheMessage("expired.test.", gdns.TypeA, []byte{192, 0, 2, 32}, 1), time.Now().Add(-time.Hour))
	if err := manager.Close(); err != nil {
		t.Fatal(err)
	}

	manager, err = openKVManagerWithOptions(path, bboltKVManagerOptions{
		DisableDNSCacheExpiration: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close() //nolint:errcheck

	storage = manager.Storage("resolver").(*bboltDNSCacheStorage)
	if _, ok := storage.Get("expired", time.Now()); !ok {
		t.Fatal("Get returned miss for expired startup entry with DNS cache expiration disabled")
	}
	if err := manager.db.View(func(tx *bbolt.Tx) error {
		if raw := dnsCacheBucket(tx, "resolver").Get([]byte("expired")); raw == nil {
			t.Fatal("expired startup value missing with DNS cache expiration disabled")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestBboltDNSCacheStorageNamesAreSorted(t *testing.T) {
	manager, err := openKVManager(filepath.Join(t.TempDir(), "kv.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close() //nolint:errcheck

	manager.Storage("z-cache")
	manager.Storage("a-cache")

	got := manager.StorageNames()
	want := []string{"a-cache", "z-cache"}
	if len(got) != len(want) {
		t.Fatalf("StorageNames() = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("StorageNames() = %#v, want %#v", got, want)
		}
	}
}

func TestBboltDNSMemoryEvictionAndGuardBranches(t *testing.T) {
	storage := newBboltDNSCacheStorage(nil, "memory", false)
	now := time.Now()
	message := testDNSCacheMessage("example.test.", gdns.TypeA, []byte{192, 0, 2, 30}, 60)
	for i := 0; i <= dnsMemoryCacheMaxEntries; i++ {
		storage.memoryPut(string(rune(i+1)), message, now.Add(time.Hour), []byte{byte(i)})
	}
	if len(storage.entries) != dnsMemoryCacheMaxEntries {
		t.Fatalf("memory entries = %d", len(storage.entries))
	}
	if _, ok := storage.entries[string(rune(1))]; ok {
		t.Fatal("oldest memory entry was not evicted")
	}
	storage.memoryClear()
	if len(storage.entries) != 0 {
		t.Fatalf("cleared entries = %#v", storage.entries)
	}
	storage.memoryPut("ignored", nil, now, nil)
	if len(storage.entries) != 0 {
		t.Fatal("nil DNS message was cached")
	}

	var nilStorage *bboltDNSCacheStorage
	if _, ok := nilStorage.Get("key", now); ok {
		t.Fatal("nil storage returned a hit")
	}
	nilStorage.Set("key", message, now)
	nilStorage.Delete("key")
	invalid := newBboltDNSCacheStorage(nil, "invalid", true)
	invalid.Set("key", message, now)
	invalid.Delete("key")
}

func testDNSCacheMessage(name string, typ uint16, data []byte, ttl uint32) *gdns.Message {
	return &gdns.Message{
		ID:                 42,
		Response:           true,
		RCode:              gdns.RCodeSuccess,
		RecursionAvailable: true,
		Questions: []gdns.Question{{
			Name:  name,
			Type:  typ,
			Class: gdns.ClassIN,
		}},
		Answers: []gdns.Resource{{
			Name:  name,
			Type:  typ,
			Class: gdns.ClassIN,
			TTL:   ttl,
			Data:  data,
		}},
	}
}
