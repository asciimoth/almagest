package main

import (
	"bytes"
	"context"
	"errors"
	"github.com/asciimoth/almagest/lib/storage"
	"path/filepath"
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
