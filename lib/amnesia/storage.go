package amnesianodes

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"

	"github.com/asciimoth/almagest/lib/storage"
)

const installationUUIDKey = "installation_uuid"

func amnesiaNodeStore(manager storage.KVManager, storageID string) storage.KVStore {
	if manager == nil {
		return nil
	}
	store := manager.DefaultStore()
	if store == nil {
		return nil
	}
	return store.Subtree("nodes", "amnesia", storageID)
}

// amnesiaStorageID gives an old config a stable KV namespace before its first
// autosave writes the identifier into the node config. Node names are unique
// in one workspace. The persisted identifier keeps the namespace stable after
// a later node rename.
func amnesiaStorageID(nodeName string) string {
	digest := sha256.Sum256([]byte(nodeName))
	return fmt.Sprintf("%x", digest[:])
}

func validAmnesiaStorageID(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size
}

func loadOrCreateInstallationUUID(ctx context.Context, store storage.KVStore, fallback *string) (string, bool, error) {
	if store == nil {
		if *fallback == "" {
			uuid, err := newUUID(rand.Reader)
			if err != nil {
				return "", false, err
			}
			*fallback = uuid
		}
		return *fallback, false, nil
	}

	var uuid string
	err := store.Update(ctx, func(tx storage.KVWriteTx) error {
		value, found, err := tx.Get(installationUUIDKey)
		if err != nil {
			return err
		}
		if found {
			uuid = string(value.Data)
			if uuid != "" {
				return nil
			}
		}
		uuid, err = newUUID(rand.Reader)
		if err != nil {
			return err
		}
		return tx.Set(installationUUIDKey, storage.KValue{
			Data: []byte(uuid), Hidden: true, String: true,
		})
	})
	if err != nil {
		return "", false, fmt.Errorf("persist installation UUID: %w", err)
	}
	return uuid, true, nil
}

func newUUID(random io.Reader) (string, error) {
	var raw [16]byte
	if _, err := io.ReadFull(random, raw[:]); err != nil {
		return "", err
	}
	raw[6] = raw[6]&0x0f | 0x40
	raw[8] = raw[8]&0x3f | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		raw[0:4], raw[4:6], raw[6:8], raw[8:10], raw[10:16]), nil
}

type kvProxyCache struct {
	store storage.KVStore
}

func newKVProxyCache(store storage.KVStore) *kvProxyCache {
	if store == nil {
		return nil
	}
	return &kvProxyCache{store: store.Subtree("proxy_cache")}
}

func (c *kvProxyCache) Load(ctx context.Context, key string) ([]byte, bool, error) {
	if c == nil || c.store == nil {
		return nil, false, nil
	}
	var data []byte
	var found bool
	err := c.store.View(ctx, func(tx storage.KVReadTx) error {
		value, ok, err := tx.Get(proxyCacheStorageKey(key))
		if err != nil || !ok {
			return err
		}
		data = append([]byte(nil), value.Data...)
		found = true
		return nil
	})
	return data, found, err
}

func (c *kvProxyCache) Store(ctx context.Context, key string, encrypted []byte) error {
	if c == nil || c.store == nil {
		return nil
	}
	return c.store.Update(ctx, func(tx storage.KVWriteTx) error {
		return tx.Set(proxyCacheStorageKey(key), storage.KValue{
			Data: append([]byte(nil), encrypted...), Hidden: true,
		})
	})
}

func proxyCacheStorageKey(key string) string {
	digest := sha256.Sum256([]byte(key))
	return fmt.Sprintf("%x", digest[:])
}
