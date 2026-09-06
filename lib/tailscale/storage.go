package tailscalenodes

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sync"

	wgotailscale "github.com/asciimoth/wgo-tailscale"

	"github.com/asciimoth/almagest/lib/storage"
)

const tailscaleCacheKey = "client_cache"

// tailscaleStorageID gives a new node a stable private KV namespace. Pasta
// node names are unique in a workspace. The saved identifier keeps the
// namespace stable after a later node rename.
func tailscaleStorageID(nodeName string) string {
	digest := sha256.Sum256([]byte(nodeName))
	return hex.EncodeToString(digest[:])
}

func validTailscaleStorageID(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size
}

func tailscaleNodeStore(manager storage.KVManager, storageID string) storage.KVStore {
	if manager == nil || !validTailscaleStorageID(storageID) {
		return nil
	}
	root := manager.DefaultStore()
	if root == nil {
		return nil
	}
	return root.Subtree("nodes", "tailscale", storageID)
}

// tailscaleCache preserves one client identity across client rebuilds. KV is
// used when available. The memory copy keeps graph detach and reattach useful
// in engines that intentionally run without persistent storage.
type tailscaleCache struct {
	store storage.KVStore

	mu       sync.Mutex
	fallback []byte
}

func (c *tailscaleCache) callbacks() wgotailscale.CacheCallbacks {
	return wgotailscale.CacheCallbacks{Load: c.load, Store: c.storeValue}
}

func (c *tailscaleCache) load(ctx context.Context) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.store == nil {
		return append([]byte(nil), c.fallback...), nil
	}
	var (
		data  []byte
		found bool
	)
	err := c.store.View(ctx, func(tx storage.KVReadTx) error {
		value, ok, err := tx.Get(tailscaleCacheKey)
		if err != nil || !ok {
			return err
		}
		data = append([]byte(nil), value.Data...)
		found = true
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("load Tailscale client cache: %w", err)
	}
	if !found {
		data = append([]byte(nil), c.fallback...)
	}
	return data, nil
}

func (c *tailscaleCache) storeValue(ctx context.Context, data []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.fallback = append(c.fallback[:0], data...)
	if c.store == nil {
		return nil
	}
	copyData := append([]byte(nil), data...)
	err := c.store.Update(ctx, func(tx storage.KVWriteTx) error {
		return tx.Set(tailscaleCacheKey, storage.KValue{Data: copyData, Hidden: true})
	})
	if err != nil {
		return fmt.Errorf("store Tailscale client cache: %w", err)
	}
	return nil
}
