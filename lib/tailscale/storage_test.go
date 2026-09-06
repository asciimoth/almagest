package tailscalenodes

import (
	"bytes"
	"context"
	"sync"
	"testing"

	"github.com/asciimoth/almagest/lib/storage"
)

func TestTailscaleCachePersistsHiddenCopies(t *testing.T) {
	store := &tailscaleCacheTestStore{}
	cache := &tailscaleCache{store: store}
	want := []byte("private client identity")
	input := bytes.Clone(want)
	if err := cache.storeValue(context.Background(), input); err != nil {
		t.Fatalf("store cache: %v", err)
	}
	input[0] = 'X'

	got, err := cache.load(context.Background())
	if err != nil {
		t.Fatalf("load cache: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("loaded cache = %q, want %q", got, want)
	}
	got[0] = 'Y'
	again, err := cache.load(context.Background())
	if err != nil || !bytes.Equal(again, want) {
		t.Fatalf("second cache load = %q, %v; want %q", again, err, want)
	}

	if err := store.View(context.Background(), func(tx storage.KVReadTx) error {
		value, found, getErr := tx.Get(tailscaleCacheKey)
		if getErr != nil || !found || !value.Hidden || value.String {
			t.Fatalf("stored cache = %#v, found=%t, err=%v", value, found, getErr)
		}
		return nil
	}); err != nil {
		t.Fatalf("inspect cache: %v", err)
	}
}

func TestTailscaleCacheTreatsStoredEmptyValueAsAuthoritative(t *testing.T) {
	store := &tailscaleCacheTestStore{}
	if err := store.Update(context.Background(), func(tx storage.KVWriteTx) error {
		return tx.Set(tailscaleCacheKey, storage.KValue{Hidden: true})
	}); err != nil {
		t.Fatalf("store empty cache: %v", err)
	}
	cache := &tailscaleCache{store: store, fallback: []byte("stale in-memory identity")}
	got, err := cache.load(context.Background())
	if err != nil {
		t.Fatalf("load empty cache: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("empty persistent cache loaded fallback %q", got)
	}
}

type tailscaleCacheTestStore struct {
	mu     sync.Mutex
	values map[string]storage.KValue
}

func (s *tailscaleCacheTestStore) Subtree(...string) storage.KVStore { return s }

func (s *tailscaleCacheTestStore) View(ctx context.Context, fn func(storage.KVReadTx) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return fn(tailscaleCacheTestReadTx{store: s})
}

func (s *tailscaleCacheTestStore) Update(ctx context.Context, fn func(storage.KVWriteTx) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return fn(tailscaleCacheTestWriteTx{store: s})
}

type tailscaleCacheTestReadTx struct {
	store *tailscaleCacheTestStore
}

func (tx tailscaleCacheTestReadTx) Exists() bool { return true }

func (tx tailscaleCacheTestReadTx) Get(key string) (*storage.KValue, bool, error) {
	value, found := tx.store.values[key]
	if !found {
		return nil, false, nil
	}
	copy := value.Clone()
	return &copy, true, nil
}

func (tailscaleCacheTestReadTx) ForEachValue(func(string, storage.KValue) error) error {
	return nil
}

func (tailscaleCacheTestReadTx) ForEachSubtree(func(string) error) error { return nil }

func (tx tailscaleCacheTestReadTx) Subtree(...string) storage.KVReadTx { return tx }

type tailscaleCacheTestWriteTx struct {
	store *tailscaleCacheTestStore
}

func (tx tailscaleCacheTestWriteTx) Exists() bool { return true }

func (tx tailscaleCacheTestWriteTx) Get(key string) (*storage.KValue, bool, error) {
	return tailscaleCacheTestReadTx(tx).Get(key)
}

func (tailscaleCacheTestWriteTx) ForEachValue(func(string, storage.KValue) error) error {
	return nil
}

func (tailscaleCacheTestWriteTx) ForEachSubtree(func(string) error) error { return nil }

func (tx tailscaleCacheTestWriteTx) Subtree(...string) storage.KVWriteTx { return tx }

func (tailscaleCacheTestWriteTx) Ensure() error { return nil }

func (tx tailscaleCacheTestWriteTx) Set(key string, value storage.KValue) error {
	if tx.store.values == nil {
		tx.store.values = make(map[string]storage.KValue)
	}
	tx.store.values[key] = value.Clone()
	return nil
}

func (tx tailscaleCacheTestWriteTx) Delete(key string) error {
	delete(tx.store.values, key)
	return nil
}

func (tx tailscaleCacheTestWriteTx) Drop() error {
	clear(tx.store.values)
	return nil
}
