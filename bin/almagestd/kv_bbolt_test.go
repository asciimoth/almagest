package main

import (
	"bytes"
	"context"
	"errors"
	"github.com/asciimoth/almagest/lib/storage"
	"path/filepath"
	"slices"
	"testing"

	"go.etcd.io/bbolt"
)

func TestBboltKVManagerPersistsDefaultAndPluginStores(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "kv.db")

	manager, err := openKVManager(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.DefaultStore().Update(ctx, func(tx storage.KVWriteTx) error {
		if err := tx.Set("root", storage.KValue{Data: []byte("value"), String: true}); err != nil {
			return err
		}
		return tx.Subtree("nested").Set("key", storage.KValue{Data: []byte{0xde, 0xad}})
	}); err != nil {
		t.Fatal(err)
	}
	if err := manager.PluginStore("plugin-a").Update(ctx, func(tx storage.KVWriteTx) error {
		return tx.Set("plugin-key", storage.KValue{Data: []byte("plugin-value"), String: true})
	}); err != nil {
		t.Fatal(err)
	}
	if err := manager.Close(); err != nil {
		t.Fatal(err)
	}

	manager, err = openKVManager(path)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close() //nolint:errcheck

	if err := manager.DefaultStore().View(ctx, func(tx storage.KVReadTx) error {
		value, found, err := tx.Get("root")
		if err != nil {
			return err
		}
		if !found || string(value.Data) != "value" || !value.String {
			t.Fatalf("root value = %#v, found %v; want string value", value, found)
		}
		value, found, err = tx.Subtree("nested").Get("key")
		if err != nil {
			return err
		}
		if !found || len(value.Data) != 2 || value.Data[0] != 0xde || value.String {
			t.Fatalf("nested value = %#v, found %v; want binary bytes", value, found)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	if err := manager.PluginStore("plugin-a").View(ctx, func(tx storage.KVReadTx) error {
		value, found, err := tx.Get("plugin-key")
		if err != nil {
			return err
		}
		if !found || string(value.Data) != "plugin-value" {
			t.Fatalf("plugin value = %#v, found %v; want persisted plugin value", value, found)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestBboltKVValueStoredAsFlagPrefixedBytes(t *testing.T) {
	ctx := context.Background()
	manager, err := openKVManager(filepath.Join(t.TempDir(), "kv.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close() //nolint:errcheck

	payload := []byte{'{', '"', 'v', '"', ':', 0xff, 0x00, '}'}
	if err := manager.DefaultStore().Update(ctx, func(tx storage.KVWriteTx) error {
		if err := tx.Set("bytes", storage.KValue{Data: payload, Hidden: true}); err != nil {
			return err
		}
		return tx.Set("text", storage.KValue{Data: []byte("value"), Hidden: true, String: true})
	}); err != nil {
		t.Fatal(err)
	}

	rawValues := map[string][]byte{}
	if err := manager.db.View(func(tx *bbolt.Tx) error {
		bucket := bucketAtPath(tx, []string{kvDataBucketName})
		for _, key := range []string{"bytes", "text"} {
			rawValues[key] = append([]byte(nil), bucket.Get([]byte(key))...)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	wantBytes := append([]byte{0x01}, payload...)
	if !bytes.Equal(rawValues["bytes"], wantBytes) {
		t.Fatalf("raw bytes value = %v, want %v", rawValues["bytes"], wantBytes)
	}
	wantText := append([]byte{0x03}, []byte("value")...)
	if !bytes.Equal(rawValues["text"], wantText) {
		t.Fatalf("raw text value = %v, want %v", rawValues["text"], wantText)
	}
}

func TestBboltKVStoreProtectsRootAndDropsSubtree(t *testing.T) {
	ctx := context.Background()
	manager, err := openKVManager(filepath.Join(t.TempDir(), "kv.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close() //nolint:errcheck

	err = manager.DefaultStore().Update(ctx, func(tx storage.KVWriteTx) error {
		return tx.Drop()
	})
	if !errors.Is(err, storage.ErrKVRoot) {
		t.Fatalf("drop root error = %v, want ErrKVRoot", err)
	}

	if err := manager.DefaultStore().Subtree("cache").Update(ctx, func(tx storage.KVWriteTx) error {
		return tx.Set("key", storage.KValue{Data: []byte("value"), String: true})
	}); err != nil {
		t.Fatal(err)
	}
	if err := manager.DefaultStore().Subtree("cache").Update(ctx, func(tx storage.KVWriteTx) error {
		return tx.Drop()
	}); err != nil {
		t.Fatal(err)
	}
	if err := manager.DefaultStore().View(ctx, func(tx storage.KVReadTx) error {
		if tx.Subtree("cache").Exists() {
			t.Fatal("dropped subtree still exists")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestBboltKVOwnedStorePersistsBucketAndValueOwners(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "kv.db")
	manager, err := openKVManager(path)
	if err != nil {
		t.Fatal(err)
	}
	owned := manager.DefaultStore().Subtree("nodes", "state").OwnedBy("alpha")
	if err := owned.Update(ctx, func(tx storage.KVWriteTx) error {
		if err := tx.Set("root-value", storage.KValue{Data: []byte("one"), String: true}); err != nil {
			return err
		}
		return tx.Subtree("cache").Set("nested-value", storage.KValue{Data: []byte("two"), String: true})
	}); err != nil {
		t.Fatal(err)
	}

	// A normal write can edit an owned value, but it does not silently remove
	// lifecycle metadata that belongs to the node.
	if err := manager.DefaultStore().Subtree("nodes", "state").Update(ctx, func(tx storage.KVWriteTx) error {
		return tx.Set("root-value", storage.KValue{Data: []byte("updated"), String: true})
	}); err != nil {
		t.Fatal(err)
	}
	if err := manager.Close(); err != nil {
		t.Fatal(err)
	}

	manager, err = openKVManager(path)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close() //nolint:errcheck
	if err := manager.DefaultStore().View(ctx, func(tx storage.KVReadTx) error {
		nodesOwner, err := tx.Subtree("nodes").Owner()
		if err != nil {
			return err
		}
		if nodesOwner != "" {
			t.Fatalf("shared nodes bucket owner = %q, want unowned", nodesOwner)
		}
		state := tx.Subtree("nodes", "state")
		stateOwner, err := state.Owner()
		if err != nil {
			return err
		}
		if stateOwner != "alpha" {
			t.Fatalf("state bucket owner = %q, want alpha", stateOwner)
		}
		cacheOwner, err := state.Subtree("cache").Owner()
		if err != nil {
			return err
		}
		if cacheOwner != "alpha" {
			t.Fatalf("cache bucket owner = %q, want alpha", cacheOwner)
		}
		for _, item := range []struct {
			tx  storage.KVReadTx
			key string
		}{
			{tx: state, key: "root-value"},
			{tx: state.Subtree("cache"), key: "nested-value"},
		} {
			value, found, err := item.tx.Get(item.key)
			if err != nil {
				return err
			}
			if !found || value.Owner != "alpha" {
				t.Fatalf("value %q = %#v, found=%t; want owner alpha", item.key, value, found)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestBboltKVOwnerRenameUpdatesLiveHandleAndDeleteRevokesIt(t *testing.T) {
	ctx := context.Background()
	manager, err := openKVManager(filepath.Join(t.TempDir(), "kv.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close() //nolint:errcheck

	owned := manager.DefaultStore().Subtree("owned").OwnedBy("before")
	if err := owned.Update(ctx, func(tx storage.KVWriteTx) error {
		return tx.Set("first", storage.KValue{Data: []byte("one")})
	}); err != nil {
		t.Fatal(err)
	}
	if err := manager.DefaultStore().Subtree("unowned").Update(ctx, func(tx storage.KVWriteTx) error {
		return tx.Set("keep", storage.KValue{Data: []byte("value")})
	}); err != nil {
		t.Fatal(err)
	}

	if err := manager.renameKVOwnerReferences("before", "after"); err != nil {
		t.Fatal(err)
	}
	if err := owned.Update(ctx, func(tx storage.KVWriteTx) error {
		return tx.Set("second", storage.KValue{Data: []byte("two")})
	}); err != nil {
		t.Fatalf("write through renamed handle: %v", err)
	}
	if err := owned.View(ctx, func(tx storage.KVReadTx) error {
		owner, err := tx.Owner()
		if err != nil || owner != "after" {
			t.Fatalf("renamed bucket owner = %q, err=%v; want after", owner, err)
		}
		for _, key := range []string{"first", "second"} {
			value, found, err := tx.Get(key)
			if err != nil || !found || value.Owner != "after" {
				t.Fatalf("renamed value %q = %#v, found=%t, err=%v", key, value, found, err)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	if err := manager.deleteKVOwnerEntries("after"); err != nil {
		t.Fatal(err)
	}
	if err := owned.Update(ctx, func(tx storage.KVWriteTx) error { return tx.Ensure() }); !errors.Is(err, storage.ErrKVOwnerDeleted) {
		t.Fatalf("write through deleted owner error = %v, want ErrKVOwnerDeleted", err)
	}
	if err := manager.DefaultStore().View(ctx, func(tx storage.KVReadTx) error {
		if tx.Subtree("owned").Exists() {
			t.Fatal("owned subtree exists after owner deletion")
		}
		if !tx.Subtree("unowned").Exists() {
			t.Fatal("unowned subtree was deleted with owned data")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestBboltKVOwnerReconcileDeletesOnlyStaleOwners(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "kv.db")
	manager, err := openKVManager(path)
	if err != nil {
		t.Fatal(err)
	}
	for owner, store := range map[string]storage.KVStore{
		"active": manager.DefaultStore().Subtree("active").OwnedBy("active"),
		"stale":  manager.PluginStore("plugin").Subtree("stale").OwnedBy("stale"),
	} {
		if err := store.Update(ctx, func(tx storage.KVWriteTx) error {
			return tx.Set("value", storage.KValue{Data: []byte(owner)})
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := manager.Close(); err != nil {
		t.Fatal(err)
	}

	manager, err = openKVManager(path)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close() //nolint:errcheck
	if err := manager.reconcileKVOwnerEntries(map[string]struct{}{"active": {}}); err != nil {
		t.Fatal(err)
	}
	if err := manager.DefaultStore().View(ctx, func(tx storage.KVReadTx) error {
		if !tx.Subtree("active").Exists() {
			t.Fatal("active owner's subtree was deleted")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := manager.PluginStore("plugin").View(ctx, func(tx storage.KVReadTx) error {
		if tx.Subtree("stale").Exists() {
			t.Fatal("stale owner's subtree still exists")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestBboltKVDropRemovesDescendantOwnershipMetadata(t *testing.T) {
	ctx := context.Background()
	manager, err := openKVManager(filepath.Join(t.TempDir(), "kv.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close() //nolint:errcheck
	store := manager.DefaultStore().Subtree("owned").OwnedBy("alpha")
	if err := store.Subtree("nested").Update(ctx, func(tx storage.KVWriteTx) error {
		return tx.Set("value", storage.KValue{Data: []byte("data")})
	}); err != nil {
		t.Fatal(err)
	}
	if err := manager.DefaultStore().Subtree("owned").Update(ctx, func(tx storage.KVWriteTx) error {
		return tx.Drop()
	}); err != nil {
		t.Fatal(err)
	}
	if err := manager.db.View(func(tx *bbolt.Tx) error {
		records, err := bboltKVOwnershipRecords(tx)
		if err != nil {
			return err
		}
		if len(records) != 0 {
			t.Fatalf("ownership records after drop = %#v, want none", records)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestBboltKVValueOwnershipDoesNotClaimOrDeleteStoreRoot(t *testing.T) {
	ctx := context.Background()
	manager, err := openKVManager(filepath.Join(t.TempDir(), "kv.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close() //nolint:errcheck

	if err := manager.DefaultStore().OwnedBy("alpha").Update(ctx, func(tx storage.KVWriteTx) error {
		return tx.Set("owned", storage.KValue{Data: []byte("remove")})
	}); err != nil {
		t.Fatal(err)
	}
	if err := manager.DefaultStore().Update(ctx, func(tx storage.KVWriteTx) error {
		// Owner is read-only metadata. An unowned writer cannot claim an entry
		// by copying it into a KValue.
		return tx.Set("keep", storage.KValue{Data: []byte("keep"), Owner: "alpha"})
	}); err != nil {
		t.Fatal(err)
	}
	if err := manager.deleteKVOwnerEntries("alpha"); err != nil {
		t.Fatal(err)
	}
	if err := manager.DefaultStore().View(ctx, func(tx storage.KVReadTx) error {
		owner, err := tx.Owner()
		if err != nil || owner != "" {
			t.Fatalf("default store root owner = %q, err=%v; want unowned", owner, err)
		}
		if _, found, err := tx.Get("owned"); err != nil || found {
			t.Fatalf("owned value found=%t err=%v, want deleted", found, err)
		}
		value, found, err := tx.Get("keep")
		if err != nil || !found || value.Owner != "" {
			t.Fatalf("unowned value = %#v, found=%t, err=%v", value, found, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	err = manager.DefaultStore().OwnedBy("bad[owner").Update(ctx, func(tx storage.KVWriteTx) error {
		return tx.Set("invalid", storage.KValue{})
	})
	if !errors.Is(err, storage.ErrKVInvalidOwner) {
		t.Fatalf("invalid owner error = %v, want ErrKVInvalidOwner", err)
	}
}

func TestBboltKVOwnershipAddressRoundTripsArbitraryKVNames(t *testing.T) {
	want := kvOwnershipAddress{
		Kind: kvOwnershipValue,
		Path: []string{kvDataBucketName, "nul\x00name", "invalid-utf8-\xff"},
	}
	data, err := encodeKVOwnershipAddress(want.Kind, want.Path)
	if err != nil {
		t.Fatal(err)
	}
	got, err := decodeKVOwnershipAddress(data)
	if err != nil {
		t.Fatal(err)
	}
	if got.Kind != want.Kind || !slices.Equal(got.Path, want.Path) {
		t.Fatalf("decoded ownership address = %#v, want %#v", got, want)
	}
}
