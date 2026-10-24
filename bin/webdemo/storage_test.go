package webdemo

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/asciimoth/almagest/lib/storage"
)

type memoryPersistence struct {
	values map[string][]byte
	err    error
}

func newMemoryPersistence() *memoryPersistence {
	return &memoryPersistence{values: map[string][]byte{}}
}

func (p *memoryPersistence) Load(key string) ([]byte, bool, error) {
	if p.err != nil {
		return nil, false, p.err
	}
	value, found := p.values[key]
	return append([]byte(nil), value...), found, nil
}

func (p *memoryPersistence) Store(key string, value []byte) error {
	if p.err != nil {
		return p.err
	}
	p.values[key] = append([]byte(nil), value...)
	return nil
}

func TestKVStoragePersistsTransactions(t *testing.T) {
	persistence := newMemoryPersistence()
	stores, err := NewStorage(persistence)
	if err != nil {
		t.Fatal(err)
	}
	store := stores.KV.PluginStore("plugin").Subtree("nested").OwnedBy("Node")
	err = store.Update(context.Background(), func(tx storage.KVWriteTx) error {
		return tx.Set("key", storage.KValue{Data: []byte("value"), Hidden: true, String: true})
	})
	if err != nil {
		t.Fatal(err)
	}

	reloaded, err := NewStorage(persistence)
	if err != nil {
		t.Fatal(err)
	}
	err = reloaded.KV.PluginStore("plugin").Subtree("nested").View(context.Background(), func(tx storage.KVReadTx) error {
		if owner, ownerErr := tx.Owner(); ownerErr != nil || owner != "Node" {
			t.Fatalf("owner = %q, %v", owner, ownerErr)
		}
		value, found, getErr := tx.Get("key")
		if getErr != nil || !found {
			t.Fatalf("Get() = %#v, %t, %v", value, found, getErr)
		}
		want := storage.KValue{Data: []byte("value"), Hidden: true, String: true, Owner: "Node"}
		if !reflect.DeepEqual(*value, want) {
			t.Fatalf("value = %#v, want %#v", *value, want)
		}
		value.Data[0] = 'X'
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestKVStorageRollsBackCallbackAndPersistenceErrors(t *testing.T) {
	persistence := newMemoryPersistence()
	stores, err := NewStorage(persistence)
	if err != nil {
		t.Fatal(err)
	}
	store := stores.KV.DefaultStore()
	wantErr := errors.New("stop")
	if err := store.Update(context.Background(), func(tx storage.KVWriteTx) error {
		if setErr := tx.Set("key", storage.KValue{Data: []byte("temporary")}); setErr != nil {
			return setErr
		}
		return wantErr
	}); !errors.Is(err, wantErr) {
		t.Fatalf("Update() error = %v", err)
	}
	assertKVAbsent(t, store, "key")

	persistence.err = wantErr
	if err := store.Update(context.Background(), func(tx storage.KVWriteTx) error {
		return tx.Set("key", storage.KValue{Data: []byte("temporary")})
	}); !errors.Is(err, wantErr) {
		t.Fatalf("Update() persistence error = %v", err)
	}
	persistence.err = nil
	assertKVAbsent(t, store, "key")
}

func TestSecretsPersistAndGeneratedLifecycle(t *testing.T) {
	persistence := newMemoryPersistence()
	stores, err := NewStorage(persistence)
	if err != nil {
		t.Fatal(err)
	}
	generated := stores.Secrets.(storage.GeneratedSecretsStorage)
	key, err := generated.NewGeneratedSecret(storage.Secret{Value: "secret value"}, "Node", "owner")
	if err != nil {
		t.Fatal(err)
	}
	if recovered, secret, err := generated.RecoverGeneratedSecret("owner", "Node"); err != nil || recovered != key || secret.Value != "secret value" {
		t.Fatalf("RecoverGeneratedSecret() = %q, %#v, %v", recovered, secret, err)
	}

	reloaded, err := NewStorage(persistence)
	if err != nil {
		t.Fatal(err)
	}
	secret, err := reloaded.Secrets.GetSecret(key, "Node")
	if err != nil || secret.Value != "secret value" {
		t.Fatalf("GetSecret() = %#v, %v", secret, err)
	}
	if _, err := reloaded.Secrets.GetSecret(key, "Other"); !errors.Is(err, storage.ErrSecretAccessDenied) {
		t.Fatalf("denied GetSecret() error = %v", err)
	}
	generated = reloaded.Secrets.(storage.GeneratedSecretsStorage)
	if err := generated.CommitGeneratedSecret(key, "Node"); err != nil {
		t.Fatal(err)
	}
	if recovered, _, err := generated.RecoverGeneratedSecret("owner", "Node"); err != nil || recovered != "" {
		t.Fatalf("recovery after commit = %q, %v", recovered, err)
	}
}

func TestDNSAndTLSStorageAreMemoryOnly(t *testing.T) {
	persistence := newMemoryPersistence()
	first, err := NewStorage(persistence)
	if err != nil {
		t.Fatal(err)
	}
	first.DNS.Storage("zeta")
	first.DNS.Storage("alpha")
	if got := first.DNS.(storage.DnsCacheStorageLister).StorageNames(); !reflect.DeepEqual(got, []string{"alpha", "zeta"}) {
		t.Fatalf("DNS names = %#v", got)
	}
	second, err := NewStorage(persistence)
	if err != nil {
		t.Fatal(err)
	}
	if got := second.DNS.(storage.DnsCacheStorageLister).StorageNames(); len(got) != 0 {
		t.Fatalf("reloaded DNS names = %#v", got)
	}
	if configs := second.TLS.ListConfigs(); len(configs) != 0 {
		t.Fatalf("TLS configs = %#v", configs)
	}
	if _, err := second.TLS.GetLocalCA(""); !errors.Is(err, storage.ErrTlsConfigNotFound) {
		t.Fatalf("GetLocalCA() error = %v", err)
	}
}

func TestStorageRejectsCorruptPersistentData(t *testing.T) {
	for _, key := range []string{kvStorageKey, secretsStorageKey} {
		persistence := newMemoryPersistence()
		persistence.values[key] = []byte("not-json")
		if _, err := NewStorage(persistence); err == nil {
			t.Fatalf("NewStorage() accepted corrupt %s", key)
		}
	}
}

func assertKVAbsent(t *testing.T, store storage.KVStore, key string) {
	t.Helper()
	if err := store.View(context.Background(), func(tx storage.KVReadTx) error {
		if _, found, err := tx.Get(key); err != nil || found {
			t.Fatalf("Get(%q) found = %t, error = %v", key, found, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
