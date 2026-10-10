package main

import (
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/asciimoth/almagest/lib/storage"
	"github.com/asciimoth/pasta/pasta"
	"go.etcd.io/bbolt"
	bberrors "go.etcd.io/bbolt/errors"
)

type kvOwnershipKind byte

const (
	kvOwnershipBucket kvOwnershipKind = iota + 1
	kvOwnershipValue
)

const kvOwnershipAddressVersion byte = 1

// kvOwnershipAddress identifies an entry in the physical bbolt KV tree. The
// address is encoded as the metadata key, which gives each entry one owner and
// keeps metadata separate from user-controlled keys and bucket names.
type kvOwnershipAddress struct {
	Kind kvOwnershipKind
	Path []string
}

type kvOwnershipRecord struct {
	key     []byte
	address kvOwnershipAddress
	owner   string
}

func validKVOwner(owner string) bool {
	return pasta.ValidateNodeName(owner) == nil
}

func bboltKVOwnerAt(tx *bbolt.Tx, kind kvOwnershipKind, path []string) (string, error) {
	if kind == kvOwnershipBucket && isProtectedKVStoreRoot(path) {
		return "", nil
	}
	bucket := tx.Bucket([]byte(kvOwnersBucketName))
	if bucket == nil {
		return "", nil
	}
	key, err := encodeKVOwnershipAddress(kind, path)
	if err != nil {
		return "", err
	}
	raw := bucket.Get(key)
	if raw == nil {
		return "", nil
	}
	owner := string(raw)
	if !validKVOwner(owner) {
		return "", fmt.Errorf("decode kv ownership for %q: %w", path, storage.ErrKVInvalidOwner)
	}
	return owner, nil
}

func putBboltKVOwner(tx *bbolt.Tx, kind kvOwnershipKind, path []string, owner string) error {
	if !validKVOwner(owner) {
		return storage.ErrKVInvalidOwner
	}
	key, err := encodeKVOwnershipAddress(kind, path)
	if err != nil {
		return err
	}
	bucket, err := tx.CreateBucketIfNotExists([]byte(kvOwnersBucketName))
	if err != nil {
		return err
	}
	return bucket.Put(key, []byte(owner))
}

func deleteBboltKVOwner(tx *bbolt.Tx, kind kvOwnershipKind, path []string) error {
	bucket := tx.Bucket([]byte(kvOwnersBucketName))
	if bucket == nil {
		return nil
	}
	key, err := encodeKVOwnershipAddress(kind, path)
	if err != nil {
		return err
	}
	return bucket.Delete(key)
}

func deleteBboltKVOwnershipSubtree(tx *bbolt.Tx, path []string) error {
	records, err := bboltKVOwnershipRecords(tx)
	if err != nil {
		return err
	}
	var keys [][]byte
	for _, record := range records {
		if kvOwnershipPathHasPrefix(record.address.Path, path) {
			keys = append(keys, record.key)
		}
	}
	return deleteBboltKVOwnershipKeys(tx, keys)
}

func encodeKVOwnershipAddress(kind kvOwnershipKind, path []string) ([]byte, error) {
	address := kvOwnershipAddress{Kind: kind, Path: cloneKVPath(path)}
	if !validKVOwnershipAddress(address) {
		return nil, storage.ErrKVInvalidName
	}
	data := []byte{kvOwnershipAddressVersion, byte(kind)}
	data = binary.AppendUvarint(data, uint64(len(path)))
	for _, name := range path {
		data = binary.AppendUvarint(data, uint64(len(name)))
		data = append(data, name...)
	}
	return data, nil
}

func decodeKVOwnershipAddress(data []byte) (kvOwnershipAddress, error) {
	if len(data) < 2 || data[0] != kvOwnershipAddressVersion {
		return kvOwnershipAddress{}, errors.New("decode kv ownership address: unsupported or missing version")
	}
	address := kvOwnershipAddress{Kind: kvOwnershipKind(data[1])}
	data = data[2:]
	count, size := binary.Uvarint(data)
	if size <= 0 || count > uint64(len(data)) {
		return kvOwnershipAddress{}, errors.New("decode kv ownership address: invalid path length")
	}
	data = data[size:]
	address.Path = make([]string, 0, count)
	for range count {
		length, size := binary.Uvarint(data)
		if size <= 0 || length > uint64(len(data)-size) {
			return kvOwnershipAddress{}, errors.New("decode kv ownership address: invalid segment length")
		}
		data = data[size:]
		address.Path = append(address.Path, string(data[:length]))
		data = data[length:]
	}
	if len(data) != 0 {
		return kvOwnershipAddress{}, errors.New("decode kv ownership address: trailing data")
	}
	if !validKVOwnershipAddress(address) {
		return kvOwnershipAddress{}, fmt.Errorf("decode kv ownership address: %w", storage.ErrKVInvalidName)
	}
	return address, nil
}

func validKVOwnershipAddress(address kvOwnershipAddress) bool {
	if address.Kind != kvOwnershipBucket && address.Kind != kvOwnershipValue || !validKVPath(address.Path) {
		return false
	}
	if len(address.Path) >= 2 && address.Path[0] == kvDataBucketName {
		return true
	}
	return len(address.Path) >= 3 && address.Path[0] == kvPluginBucketName
}

func isProtectedKVStoreRoot(path []string) bool {
	return len(path) == 1 && path[0] == kvDataBucketName ||
		len(path) == 2 && path[0] == kvPluginBucketName
}

func bboltKVOwnershipRecords(tx *bbolt.Tx) ([]kvOwnershipRecord, error) {
	bucket := tx.Bucket([]byte(kvOwnersBucketName))
	if bucket == nil {
		return nil, nil
	}
	var records []kvOwnershipRecord
	err := bucket.ForEach(func(k, v []byte) error {
		if v == nil {
			return errors.New("decode kv ownership: unexpected nested bucket")
		}
		address, err := decodeKVOwnershipAddress(k)
		if err != nil {
			return err
		}
		owner := string(v)
		if !validKVOwner(owner) {
			return fmt.Errorf("decode kv ownership for %q: %w", address.Path, storage.ErrKVInvalidOwner)
		}
		records = append(records, kvOwnershipRecord{
			key:     append([]byte(nil), k...),
			address: address,
			owner:   owner,
		})
		return nil
	})
	return records, err
}

func deleteBboltKVOwnershipKeys(tx *bbolt.Tx, keys [][]byte) error {
	bucket := tx.Bucket([]byte(kvOwnersBucketName))
	if bucket == nil {
		return nil
	}
	for _, key := range keys {
		if err := bucket.Delete(key); err != nil {
			return err
		}
	}
	return nil
}

func kvOwnershipPathHasPrefix(path, prefix []string) bool {
	if len(path) < len(prefix) {
		return false
	}
	for i := range prefix {
		if path[i] != prefix[i] {
			return false
		}
	}
	return true
}

// renameKVOwnerReferences updates persisted ownership and every live owned
// store handle. Holding kvOwnerMu across the database transaction makes the
// rename ordered with all writes through those handles.
func (m *bboltKVManager) renameKVOwnerReferences(oldName, newName string) error {
	if oldName == newName {
		return nil
	}
	if !validKVOwner(oldName) || !validKVOwner(newName) {
		return storage.ErrKVInvalidOwner
	}
	m.kvOwnerMu.Lock()
	changedRefs := make([]*bboltKVOwner, 0)
	for ref := range m.kvOwnerRefs {
		if ref.active && ref.name == oldName {
			ref.name = newName
			changedRefs = append(changedRefs, ref)
		}
	}
	changed := false
	err := mapBboltKVError(m.updateBackground(func(tx *bbolt.Tx) error {
		bucket := tx.Bucket([]byte(kvOwnersBucketName))
		if bucket == nil {
			return nil
		}
		records, err := bboltKVOwnershipRecords(tx)
		if err != nil {
			return err
		}
		for _, record := range records {
			if record.owner != oldName {
				continue
			}
			if err := bucket.Put(record.key, []byte(newName)); err != nil {
				return err
			}
			changed = true
		}
		return nil
	}))
	if err != nil {
		for _, ref := range changedRefs {
			ref.name = oldName
		}
	}
	m.kvOwnerMu.Unlock()
	if err == nil && changed {
		m.notifyChange()
	}
	return err
}

// deleteKVOwnerEntries revokes live handles and recursively deletes each
// bucket or value owned by name. Deleting an owned bucket also removes all
// descendant ownership metadata, including claims by other nodes.
func (m *bboltKVManager) deleteKVOwnerEntries(name string) error {
	if !validKVOwner(name) {
		return storage.ErrKVInvalidOwner
	}
	m.kvOwnerMu.Lock()
	var revoked []*bboltKVOwner
	for ref := range m.kvOwnerRefs {
		if ref.active && ref.name == name {
			ref.active = false
			revoked = append(revoked, ref)
		}
	}
	changed := false
	err := mapBboltKVError(m.updateBackground(func(tx *bbolt.Tx) error {
		var err error
		changed, err = deleteInactiveKVOwnership(tx, func(owner string) bool { return owner == name })
		return err
	}))
	if err != nil {
		for _, ref := range revoked {
			ref.active = true
		}
	}
	m.kvOwnerMu.Unlock()
	if err == nil && changed {
		m.notifyChange()
	}
	return err
}

// reconcileKVOwnerEntries deletes entries whose owner is absent from active.
// Almagestd calls it once against loaded config before it starts nodes and from
// workspace snapshots to repair missed incremental notifications.
func (m *bboltKVManager) reconcileKVOwnerEntries(active map[string]struct{}) error {
	return m.reconcileKVOwnerEntriesWithUpdate(active, m.updateBackground)
}

func (m *bboltKVManager) reconcileKVOwnerEntriesDuringSnapshot(active map[string]struct{}) error {
	return m.reconcileKVOwnerEntriesWithUpdate(active, m.updateDuringSnapshot)
}

func (m *bboltKVManager) reconcileKVOwnerEntriesWithUpdate(
	active map[string]struct{},
	updateDB func(func(*bbolt.Tx) error) error,
) error {
	for owner := range active {
		if !validKVOwner(owner) {
			return storage.ErrKVInvalidOwner
		}
	}
	m.kvOwnerMu.Lock()
	var revoked []*bboltKVOwner
	for ref := range m.kvOwnerRefs {
		if _, ok := active[ref.name]; ref.active && !ok {
			ref.active = false
			revoked = append(revoked, ref)
		}
	}
	changed := false
	err := mapBboltKVError(updateDB(func(tx *bbolt.Tx) error {
		var err error
		changed, err = deleteInactiveKVOwnership(tx, func(owner string) bool {
			_, ok := active[owner]
			return !ok
		})
		return err
	}))
	if err != nil {
		for _, ref := range revoked {
			ref.active = true
		}
	}
	m.kvOwnerMu.Unlock()
	if err == nil && changed {
		m.notifyChange()
	}
	return err
}

func deleteInactiveKVOwnership(tx *bbolt.Tx, inactive func(string) bool) (bool, error) {
	records, err := bboltKVOwnershipRecords(tx)
	if err != nil {
		return false, err
	}
	var removedBuckets [][]string
	for _, record := range records {
		if inactive(record.owner) && record.address.Kind == kvOwnershipBucket {
			removedBuckets = append(removedBuckets, record.address.Path)
		}
	}
	removedByParent := func(path []string) bool {
		for _, prefix := range removedBuckets {
			if kvOwnershipPathHasPrefix(path, prefix) {
				return true
			}
		}
		return false
	}
	changed := false
	for _, record := range records {
		if !inactive(record.owner) || removedByParent(record.address.Path) {
			continue
		}
		if err := deleteBboltKVEntry(tx, record.address); err != nil {
			return false, err
		}
		changed = true
	}
	for _, path := range removedBuckets {
		if err := deleteBboltKVEntry(tx, kvOwnershipAddress{Kind: kvOwnershipBucket, Path: path}); err != nil {
			return false, err
		}
		changed = true
	}
	var keys [][]byte
	for _, record := range records {
		if inactive(record.owner) || removedByParent(record.address.Path) {
			keys = append(keys, record.key)
		}
	}
	if err := deleteBboltKVOwnershipKeys(tx, keys); err != nil {
		return false, err
	}
	return changed, nil
}

func deleteBboltKVEntry(tx *bbolt.Tx, address kvOwnershipAddress) error {
	if !validKVOwnershipAddress(address) {
		return storage.ErrKVInvalidName
	}
	parent := bucketAtPath(tx, address.Path[:len(address.Path)-1])
	if parent == nil {
		return nil
	}
	name := []byte(address.Path[len(address.Path)-1])
	switch address.Kind {
	case kvOwnershipBucket:
		err := parent.DeleteBucket(name)
		if errors.Is(err, bberrors.ErrBucketNotFound) {
			return nil
		}
		return err
	case kvOwnershipValue:
		if parent.Bucket(name) != nil {
			return nil
		}
		return parent.Delete(name)
	default:
		return storage.ErrKVInvalidName
	}
}
