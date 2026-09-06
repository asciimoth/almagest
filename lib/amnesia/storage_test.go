package amnesianodes

import (
	"bytes"
	"context"
	"crypto/sha256"
	"strings"
	"sync"
	"testing"

	"github.com/asciimoth/almagest/lib/logging"
	"github.com/asciimoth/almagest/lib/storage"
	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/pasta/pasta"
)

func TestFreshAmnesiaNodesUseDistinctStorageIDs(t *testing.T) {
	for _, test := range []struct {
		name  string
		class interface {
			ClassName() string
			NewNode(configer.Config, ...*pasta.NodeClassState) (pasta.Node, error)
		}
		storageID func(pasta.Node) string
	}{
		{
			name:  "managed",
			class: AmnesiaClass{},
			storageID: func(raw pasta.Node) string {
				return raw.(*managedAmnesiaNode).config.storageID
			},
		},
		{
			name:  "controller",
			class: AmnesiaCtrlClass{},
			storageID: func(raw pasta.Node) string {
				return raw.(*amnesiaNode).config.storageID
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			w := pasta.NewWorkspace(logging.NewDebugLogFactory(128))
			t.Cleanup(func() { w.Close() })
			var storageIDs []string
			for _, name := range []string{"Fresh Amnesia A", "Fresh Amnesia B"} {
				raw, err := test.class.NewNode(nil)
				if err != nil {
					t.Fatalf("create %s: %v", name, err)
				}
				if _, err := w.AddNode(raw, test.class.ClassName(), name); err != nil {
					t.Fatalf("add %s: %v", name, err)
				}
				storageID := test.storageID(raw)
				if storageID != amnesiaStorageID(name) {
					t.Fatalf("%s storage ID = %q, want node-name namespace", name, storageID)
				}
				storageIDs = append(storageIDs, storageID)
			}
			if storageIDs[0] == storageIDs[1] {
				t.Fatalf("fresh %s Amnesia nodes share storage ID %q", test.name, storageIDs[0])
			}
		})
	}
}

func TestAmnesiaInstallationUUIDAndProxyCacheUseKVStorage(t *testing.T) {
	manager := amnesiaStorageTestManager{store: newAmnesiaMemoryKV()}
	store := amnesiaNodeStore(manager, amnesiaStorageID("Amnesia"), "Amnesia")
	fallback := ""
	uuid, persistent, err := loadOrCreateInstallationUUID(context.Background(), store, &fallback)
	if err != nil {
		t.Fatalf("create installation UUID: %v", err)
	}
	if !persistent || len(uuid) != 36 {
		t.Fatalf("UUID = %q persistent=%t", uuid, persistent)
	}
	again, persistent, err := loadOrCreateInstallationUUID(context.Background(), store, &fallback)
	if err != nil || !persistent || again != uuid {
		t.Fatalf("second UUID = %q persistent=%t err=%v, want %q", again, persistent, err, uuid)
	}
	if err := store.View(context.Background(), func(tx storage.KVReadTx) error {
		value, found, getErr := tx.Get(installationUUIDKey)
		if getErr != nil || !found || !value.Hidden || !value.String || value.Owner != "Amnesia" {
			t.Fatalf("stored UUID = %#v found=%t err=%v", value, found, getErr)
		}
		return nil
	}); err != nil {
		t.Fatalf("inspect UUID: %v", err)
	}

	cache := newKVProxyCache(store)
	want := []byte("encrypted proxy response")
	if err := cache.Store(context.Background(), "secret cache key", want); err != nil {
		t.Fatalf("store proxy cache: %v", err)
	}
	got, found, err := cache.Load(context.Background(), "secret cache key")
	if err != nil || !found || !bytes.Equal(got, want) {
		t.Fatalf("load proxy cache = %q found=%t err=%v", got, found, err)
	}
	if _, found, err := cache.Load(context.Background(), "other key"); err != nil || found {
		t.Fatalf("missing proxy cache found=%t err=%v", found, err)
	}
	if err := store.Subtree("proxy_cache").View(context.Background(), func(tx storage.KVReadTx) error {
		value, found, getErr := tx.Get(proxyCacheStorageKey("secret cache key"))
		if getErr != nil || !found || value.Owner != "Amnesia" {
			t.Fatalf("stored proxy cache = %#v found=%t err=%v", value, found, getErr)
		}
		return nil
	}); err != nil {
		t.Fatalf("inspect proxy cache: %v", err)
	}
}

type amnesiaStorageTestManager struct {
	store *amnesiaMemoryKV
}

func (m amnesiaStorageTestManager) DefaultStore() storage.KVStore { return m.store }
func (m amnesiaStorageTestManager) PluginStore(name string) storage.KVStore {
	return m.store.Subtree("plugins", name)
}

func TestAmnesiaInstallationUUIDHasStableEphemeralFallback(t *testing.T) {
	fallback := ""
	first, persistent, err := loadOrCreateInstallationUUID(context.Background(), nil, &fallback)
	if err != nil || persistent || first == "" {
		t.Fatalf("first fallback UUID = %q persistent=%t err=%v", first, persistent, err)
	}
	second, persistent, err := loadOrCreateInstallationUUID(context.Background(), nil, &fallback)
	if err != nil || persistent || second != first {
		t.Fatalf("second fallback UUID = %q persistent=%t err=%v, want %q", second, persistent, err, first)
	}
}

func TestAmnesiaStorageIDIsStableForNodeName(t *testing.T) {
	first := amnesiaStorageID("Amnesia Service")
	if len(first) != sha256.Size*2 {
		t.Fatalf("storage ID length = %d, want %d", len(first), sha256.Size*2)
	}
	if again := amnesiaStorageID("Amnesia Service"); again != first {
		t.Fatalf("second storage ID = %q, want %q", again, first)
	}
	if other := amnesiaStorageID("Other Amnesia"); other == first {
		t.Fatal("different node names produced the same storage ID")
	}
	if !validAmnesiaStorageID(first) || validAmnesiaStorageID("not-a-storage-id") {
		t.Fatal("storage ID validation did not accept only a valid digest")
	}
}

type amnesiaMemoryKV struct {
	shared *amnesiaMemoryKVData
	path   []string
	owner  string
}

type amnesiaMemoryKVData struct {
	mu     sync.RWMutex
	values map[string]storage.KValue
}

func newAmnesiaMemoryKV() *amnesiaMemoryKV {
	return &amnesiaMemoryKV{shared: &amnesiaMemoryKVData{values: make(map[string]storage.KValue)}}
}

func (s *amnesiaMemoryKV) Subtree(path ...string) storage.KVStore {
	return &amnesiaMemoryKV{shared: s.shared, path: append(append([]string(nil), s.path...), path...), owner: s.owner}
}

func (s *amnesiaMemoryKV) OwnedBy(owner string) storage.KVStore {
	return &amnesiaMemoryKV{shared: s.shared, path: append([]string(nil), s.path...), owner: owner}
}

func (s *amnesiaMemoryKV) View(ctx context.Context, fn func(storage.KVReadTx) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.shared.mu.RLock()
	defer s.shared.mu.RUnlock()
	return fn(amnesiaMemoryKVReadTx{store: s})
}

func (s *amnesiaMemoryKV) Update(ctx context.Context, fn func(storage.KVWriteTx) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.shared.mu.Lock()
	defer s.shared.mu.Unlock()
	return fn(amnesiaMemoryKVTx{store: s})
}

type amnesiaMemoryKVTx struct {
	store *amnesiaMemoryKV
}

type amnesiaMemoryKVReadTx struct {
	store *amnesiaMemoryKV
}

func (tx amnesiaMemoryKVReadTx) Exists() bool           { return true }
func (tx amnesiaMemoryKVReadTx) Owner() (string, error) { return tx.store.owner, nil }

func (tx amnesiaMemoryKVReadTx) Get(key string) (*storage.KValue, bool, error) {
	return amnesiaMemoryGet(tx.store, key)
}

func (amnesiaMemoryKVReadTx) ForEachValue(func(string, storage.KValue) error) error { return nil }
func (amnesiaMemoryKVReadTx) ForEachSubtree(func(string) error) error               { return nil }

func (tx amnesiaMemoryKVReadTx) Subtree(path ...string) storage.KVReadTx {
	return amnesiaMemoryKVReadTx{store: tx.store.Subtree(path...).(*amnesiaMemoryKV)}
}

func (tx amnesiaMemoryKVTx) Exists() bool           { return true }
func (tx amnesiaMemoryKVTx) Owner() (string, error) { return tx.store.owner, nil }

func (tx amnesiaMemoryKVTx) Get(key string) (*storage.KValue, bool, error) {
	return amnesiaMemoryGet(tx.store, key)
}

func amnesiaMemoryGet(store *amnesiaMemoryKV, key string) (*storage.KValue, bool, error) {
	value, found := store.shared.values[amnesiaMemoryKey(store, key)]
	if !found {
		return nil, false, nil
	}
	copy := value.Clone()
	return &copy, true, nil
}

func (amnesiaMemoryKVTx) ForEachValue(func(string, storage.KValue) error) error { return nil }
func (amnesiaMemoryKVTx) ForEachSubtree(func(string) error) error               { return nil }

func (tx amnesiaMemoryKVTx) Subtree(path ...string) storage.KVWriteTx {
	return amnesiaMemoryKVTx{store: tx.store.Subtree(path...).(*amnesiaMemoryKV)}
}

func (amnesiaMemoryKVTx) Ensure() error { return nil }

func (tx amnesiaMemoryKVTx) Set(key string, value storage.KValue) error {
	value.Owner = tx.store.owner
	tx.store.shared.values[tx.key(key)] = value.Clone()
	return nil
}

func (tx amnesiaMemoryKVTx) Delete(key string) error {
	delete(tx.store.shared.values, tx.key(key))
	return nil
}

func (tx amnesiaMemoryKVTx) Drop() error {
	prefix := strings.Join(tx.store.path, "\x00")
	for key := range tx.store.shared.values {
		if key == prefix || strings.HasPrefix(key, prefix+"\x00") {
			delete(tx.store.shared.values, key)
		}
	}
	return nil
}

func (tx amnesiaMemoryKVTx) key(key string) string {
	return amnesiaMemoryKey(tx.store, key)
}

func amnesiaMemoryKey(store *amnesiaMemoryKV, key string) string {
	return strings.Join(append(append([]string(nil), store.path...), key), "\x00")
}
