package main

import (
	"fmt"
	"os"

	"go.etcd.io/bbolt"
)

// copyBboltDatabase writes a consistent bbolt image while the live database
// remains open. bbolt holds a read transaction for the duration of the copy.
func (m *bboltKVManager) copyBboltDatabase(path string) error {
	if m == nil || m.db == nil {
		return fmt.Errorf("bbolt database is unavailable")
	}
	if err := m.db.View(func(tx *bbolt.Tx) error {
		return tx.CopyFile(path, 0o600)
	}); err != nil {
		return fmt.Errorf("copy bbolt database: %w", err)
	}
	return nil
}

// replaceBboltDatabase replaces all live buckets in one write transaction.
// The DB object is kept open so existing daemon storage handles remain valid.
func (m *bboltKVManager) replaceBboltDatabase(path string) error {
	if m == nil || m.db == nil {
		return fmt.Errorf("bbolt database is unavailable")
	}
	source, err := bbolt.Open(path, 0o600, &bbolt.Options{ReadOnly: true})
	if err != nil {
		return fmt.Errorf("open snapshot bbolt database: %w", err)
	}
	defer source.Close() //nolint:errcheck

	err = source.View(func(sourceTx *bbolt.Tx) error {
		return m.db.Update(func(destinationTx *bbolt.Tx) error {
			var names [][]byte
			if err := destinationTx.ForEach(func(name []byte, _ *bbolt.Bucket) error {
				names = append(names, append([]byte(nil), name...))
				return nil
			}); err != nil {
				return err
			}
			for _, name := range names {
				if err := destinationTx.DeleteBucket(name); err != nil {
					return err
				}
			}
			return sourceTx.ForEach(func(name []byte, sourceBucket *bbolt.Bucket) error {
				destinationBucket, err := destinationTx.CreateBucket(name)
				if err != nil {
					return err
				}
				return copyBboltBucket(destinationBucket, sourceBucket)
			})
		})
	})
	if err != nil {
		return fmt.Errorf("replace live bbolt database: %w", err)
	}
	m.resetBboltRuntimeCaches()
	return nil
}

// validateBboltDatabase opens and traverses a staged database before restore
// stops the current graph or changes file storage. Traversal makes bbolt read
// every stored bucket instead of validating only the database header.
func validateBboltDatabase(path string) error {
	source, err := bbolt.Open(path, 0o600, &bbolt.Options{ReadOnly: true})
	if err != nil {
		return fmt.Errorf("open snapshot bbolt database: %w", err)
	}
	err = source.View(func(tx *bbolt.Tx) error {
		return tx.ForEach(func(_ []byte, bucket *bbolt.Bucket) error {
			return validateBboltBucket(bucket)
		})
	})
	closeErr := source.Close()
	if err != nil {
		return fmt.Errorf("validate snapshot bbolt database: %w", err)
	}
	if closeErr != nil {
		return fmt.Errorf("close snapshot bbolt database: %w", closeErr)
	}
	return nil
}

func validateBboltBucket(bucket *bbolt.Bucket) error {
	return bucket.ForEach(func(key, value []byte) error {
		if value != nil {
			return nil
		}
		child := bucket.Bucket(key)
		if child == nil {
			return fmt.Errorf("snapshot bucket %q is unavailable", key)
		}
		return validateBboltBucket(child)
	})
}

func copyBboltBucket(destination, source *bbolt.Bucket) error {
	if err := destination.SetSequence(source.Sequence()); err != nil {
		return err
	}
	return source.ForEach(func(key, value []byte) error {
		if value != nil {
			return destination.Put(key, value)
		}
		sourceChild := source.Bucket(key)
		if sourceChild == nil {
			return fmt.Errorf("snapshot bucket %q is unavailable", key)
		}
		destinationChild, err := destination.CreateBucket(key)
		if err != nil {
			return err
		}
		return copyBboltBucket(destinationChild, sourceChild)
	})
}

// resetBboltRuntimeCaches drops data derived from the database image. It does
// not drop admin subscriptions or immutable protected overlays.
func (m *bboltKVManager) resetBboltRuntimeCaches() {
	m.kvOwnerMu.Lock()
	for owner := range m.kvOwnerRefs {
		owner.active = false
	}
	m.kvOwnerRefs = map[*bboltKVOwner]struct{}{}
	m.kvOwnerMu.Unlock()

	m.dnsMu.Lock()
	stores := make([]*bboltDNSCacheStorage, 0, len(m.dnsStores))
	for _, store := range m.dnsStores {
		stores = append(stores, store)
	}
	m.dnsMu.Unlock()
	for _, store := range stores {
		store.mu.Lock()
		store.entries = map[string]dnsMemoryCacheEntry{}
		store.seq = 0
		store.mu.Unlock()
	}
}

// replaceImmutableStorageState restores the in-memory values that a fresh
// daemon engine would derive from protected config and the host OS. These
// values are intentionally not read from a snapshot.
func (m *bboltKVManager) replaceImmutableStorageState(
	secrets map[string]immutableSecret,
	tlsData immutableTLSData,
	builtinCA *tlsBuiltinCA,
) {
	m.secretsMu.Lock()
	m.immutableSecrets = cloneImmutableSecrets(secrets)
	m.secretsMu.Unlock()

	tlsData = cloneImmutableTLSData(tlsData)
	builtinCA = cloneTLSBuiltinCA(builtinCA)
	if builtinCA != nil {
		tlsData = withTLSBuiltinCA(tlsData, builtinCA)
	}
	m.tlsMu.Lock()
	m.immutableTLS = tlsData
	m.builtinCA = builtinCA
	m.tlsWatchers = map[string]map[string]struct{}{}
	m.tlsCAWatchers = map[string]struct{}{}
	m.tlsMu.Unlock()
}

func removeFileIfPresent(path string) {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		// Temporary snapshot files are best-effort cleanup. Their names do not
		// use the public .zip suffix, so they never appear in snapshot listings.
		return
	}
}
