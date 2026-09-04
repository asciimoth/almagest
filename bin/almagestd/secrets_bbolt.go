package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/asciimoth/almagest/lib/storage"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/asciimoth/almagest/bin/api"
	"github.com/asciimoth/pasta/pasta"
	"github.com/kataras/golog"
	"go.etcd.io/bbolt"
	bberrors "go.etcd.io/bbolt/errors"
)

const secretGeneratedKeyBytes = 16

type secretChangeKind string

const (
	secretChangeUpsert secretChangeKind = "upsert"
	secretChangeDelete secretChangeKind = "delete"
)

type secretChange struct {
	kind secretChangeKind
	key  string
}

// secretRecord is the stable JSON shape stored in the mutable bbolt
// secrets/<key> value. Values are UTF-8 strings because the public admin API and
// lib.Secret both expose secrets as strings.
type secretRecord struct {
	Value string   `json:"value,omitempty"`
	Nodes []string `json:"nodes,omitempty"`
}

// immutableSecret stores one protected-config or environment-backed entry.
// The value and key are immutable; Nodes may be adjusted in memory when the
// workspace renames or deletes nodes so access does not break mid-run.
type immutableSecret struct {
	secretRecord
	source api.SecretSource
}

// secretEffectiveEntry is the merged view exposed through GetSecret and the
// admin API after applying immutable-over-mutable priority.
type secretEffectiveEntry struct {
	Key      string
	Value    string
	Nodes    []string
	ReadOnly bool
	Source   api.SecretSource
}

// secretMutationResult records which nodes should be told to refetch or drop a
// previously fetched secret after a value, metadata, rename, or delete change.
type secretMutationResult struct {
	prevKey     string
	newKey      string
	notifyNodes []string
}

func (m *bboltKVManager) GetSecret(key string, node string) (storage.Secret, error) {
	if !validSecretName(key) || !validSecretNodeName(node) {
		return storage.Secret{}, storage.ErrSecretAccessDenied
	}
	golog.Debugf("secrets storage: secret access key=%q node=%q", key, node)
	entry, found, err := m.secretEffectiveEntry(key)
	if err != nil {
		return storage.Secret{}, err
	}
	if !found || !secretNodeAllowed(entry.Nodes, node) {
		return storage.Secret{}, storage.ErrSecretAccessDenied
	}
	return storage.Secret{
		Value:    entry.Value,
		ReadOnly: entry.ReadOnly,
	}, nil
}

func (m *bboltKVManager) NewSecret(sec storage.Secret, node string) (string, error) {
	if !validSecretNodeName(node) {
		return "", storage.ErrSecretInvalidName
	}
	if !utf8.ValidString(sec.Value) {
		return "", storage.ErrSecretInvalidValue
	}
	golog.Debugf("secrets storage: creating mutable secret for node=%q", node)
	record := secretRecord{
		Value: sec.Value,
		Nodes: []string{node},
	}
	for range 64 {
		key, err := randomSecretKey()
		if err != nil {
			return "", err
		}
		created, err := m.createMutableSecret(key, record)
		if err != nil {
			return "", err
		}
		if created {
			m.notifySecretChange(secretChange{kind: secretChangeUpsert, key: key})
			return key, nil
		}
	}
	return "", fmt.Errorf("%w: could not generate an unused secret key", storage.ErrSecretConflict)
}

func (m *bboltKVManager) secretsSnapshot() (api.SecretsSnapshot, error) {
	golog.Debug("secrets storage: snapshot access")
	immutable := m.cloneImmutableSecrets()
	entries := make(map[string]secretEffectiveEntry, len(immutable))
	err := mapBboltSecretError(m.db.View(func(tx *bbolt.Tx) error {
		bucket := tx.Bucket([]byte(secretsBucketName))
		if bucket == nil {
			return nil
		}
		return bucket.ForEach(func(k, v []byte) error {
			key := string(k)
			if _, shadowed := immutable[key]; shadowed {
				return nil
			}
			record, err := decodeSecretRecord(v)
			if err != nil {
				return err
			}
			entries[key] = secretEffectiveEntry{
				Key:      key,
				Value:    record.Value,
				Nodes:    normalizeSecretNodes(record.Nodes),
				ReadOnly: false,
				Source:   api.SecretSourceMutable,
			}
			return nil
		})
	}))
	if err != nil {
		return api.SecretsSnapshot{}, err
	}
	for key, record := range immutable {
		entries[key] = secretEffectiveEntry{
			Key:      key,
			Value:    record.Value,
			Nodes:    normalizeSecretNodes(record.Nodes),
			ReadOnly: true,
			Source:   record.source,
		}
	}
	out := make([]api.SecretEntry, 0, len(entries))
	for _, entry := range entries {
		out = append(out, secretEntryToAPI(entry))
	}
	slices.SortFunc(out, func(a, b api.SecretEntry) int {
		return strings.Compare(a.Key, b.Key)
	})
	return api.SecretsSnapshot{Secrets: out}, nil
}

// secretNodeNames returns every node name in mutable and immutable secret
// allow lists. It includes mutable entries hidden by immutable entries.
func (m *bboltKVManager) secretNodeNames() ([]string, error) {
	names := map[string]struct{}{}
	for _, record := range m.cloneImmutableSecrets() {
		for _, node := range record.Nodes {
			names[node] = struct{}{}
		}
	}
	err := mapBboltSecretError(m.db.View(func(tx *bbolt.Tx) error {
		bucket := tx.Bucket([]byte(secretsBucketName))
		if bucket == nil {
			return nil
		}
		return bucket.ForEach(func(_, value []byte) error {
			record, err := decodeSecretRecord(value)
			if err != nil {
				return err
			}
			for _, node := range record.Nodes {
				names[node] = struct{}{}
			}
			return nil
		})
	}))
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(names))
	for name := range names {
		out = append(out, name)
	}
	slices.Sort(out)
	return out, nil
}

func (m *bboltKVManager) secretAPIEntry(key string) (api.SecretEntry, bool, error) {
	golog.Debugf("secrets storage: entry access key=%q", key)
	entry, found, err := m.secretEffectiveEntry(key)
	if err != nil || !found {
		return api.SecretEntry{}, false, err
	}
	return secretEntryToAPI(entry), true, nil
}

func (m *bboltKVManager) setSecretValue(key, value string) (secretMutationResult, error) {
	if !validSecretName(key) {
		return secretMutationResult{}, storage.ErrSecretInvalidName
	}
	if !utf8.ValidString(value) {
		return secretMutationResult{}, storage.ErrSecretInvalidValue
	}
	if _, ok := m.immutableSecret(key); ok {
		return secretMutationResult{}, storage.ErrSecretReadOnly
	}
	golog.Debugf("secrets storage: writing mutable secret value key=%q", key)
	var notifyNodes []string
	err := mapBboltSecretError(m.db.Update(func(tx *bbolt.Tx) error {
		bucket, err := tx.CreateBucketIfNotExists([]byte(secretsBucketName))
		if err != nil {
			return err
		}
		record := secretRecord{}
		if raw := bucket.Get([]byte(key)); raw != nil {
			record, err = decodeSecretRecord(raw)
			if err != nil {
				return err
			}
			notifyNodes = normalizeSecretNodes(record.Nodes)
		}
		record.Value = value
		record.Nodes = normalizeSecretNodes(record.Nodes)
		raw, err := encodeSecretRecord(record)
		if err != nil {
			return err
		}
		return bucket.Put([]byte(key), raw)
	}))
	if err != nil {
		return secretMutationResult{}, err
	}
	m.notifySecretChange(secretChange{kind: secretChangeUpsert, key: key})
	return secretMutationResult{prevKey: key, newKey: key, notifyNodes: notifyNodes}, nil
}

func (m *bboltKVManager) setSecretNodes(key string, nodes []string) (secretMutationResult, error) {
	if !validSecretName(key) || !validSecretNodeNames(nodes) {
		return secretMutationResult{}, storage.ErrSecretInvalidName
	}
	if _, ok := m.immutableSecret(key); ok {
		return secretMutationResult{}, storage.ErrSecretReadOnly
	}
	nodes = normalizeSecretNodes(nodes)
	golog.Debugf("secrets storage: writing mutable secret nodes key=%q nodes=%q", key, nodes)
	var oldNodes []string
	err := mapBboltSecretError(m.db.Update(func(tx *bbolt.Tx) error {
		bucket, err := tx.CreateBucketIfNotExists([]byte(secretsBucketName))
		if err != nil {
			return err
		}
		record := secretRecord{}
		if raw := bucket.Get([]byte(key)); raw != nil {
			record, err = decodeSecretRecord(raw)
			if err != nil {
				return err
			}
			oldNodes = normalizeSecretNodes(record.Nodes)
		}
		record.Nodes = nodes
		raw, err := encodeSecretRecord(record)
		if err != nil {
			return err
		}
		return bucket.Put([]byte(key), raw)
	}))
	if err != nil {
		return secretMutationResult{}, err
	}
	m.notifySecretChange(secretChange{kind: secretChangeUpsert, key: key})
	return secretMutationResult{
		prevKey:     key,
		newKey:      key,
		notifyNodes: secretNodeUnion(oldNodes, nodes),
	}, nil
}

func (m *bboltKVManager) renameSecret(key, newKey string) (secretMutationResult, error) {
	if !validSecretName(key) || !validSecretName(newKey) {
		return secretMutationResult{}, storage.ErrSecretInvalidName
	}
	golog.Debugf("secrets storage: renaming mutable secret key=%q new_key=%q", key, newKey)
	if key == newKey {
		entry, found, err := m.secretEffectiveEntry(key)
		if err != nil {
			return secretMutationResult{}, err
		}
		if !found {
			return secretMutationResult{}, errSecretNotFound
		}
		if entry.ReadOnly {
			return secretMutationResult{}, storage.ErrSecretReadOnly
		}
		return secretMutationResult{prevKey: key, newKey: key, notifyNodes: normalizeSecretNodes(entry.Nodes)}, nil
	}
	if _, ok := m.immutableSecret(key); ok {
		return secretMutationResult{}, storage.ErrSecretReadOnly
	}
	if _, ok := m.immutableSecret(newKey); ok {
		return secretMutationResult{}, storage.ErrSecretConflict
	}
	var notifyNodes []string
	err := mapBboltSecretError(m.db.Update(func(tx *bbolt.Tx) error {
		bucket := tx.Bucket([]byte(secretsBucketName))
		if bucket == nil {
			return errSecretNotFound
		}
		if bucket.Get([]byte(newKey)) != nil {
			return storage.ErrSecretConflict
		}
		raw := bucket.Get([]byte(key))
		if raw == nil {
			return errSecretNotFound
		}
		record, err := decodeSecretRecord(raw)
		if err != nil {
			return err
		}
		notifyNodes = normalizeSecretNodes(record.Nodes)
		if err := bucket.Put([]byte(newKey), append([]byte(nil), raw...)); err != nil {
			return err
		}
		return bucket.Delete([]byte(key))
	}))
	if err != nil {
		return secretMutationResult{}, err
	}
	m.notifySecretChange(
		secretChange{kind: secretChangeDelete, key: key},
		secretChange{kind: secretChangeUpsert, key: newKey},
	)
	return secretMutationResult{prevKey: key, newKey: newKey, notifyNodes: notifyNodes}, nil
}

func (m *bboltKVManager) deleteSecret(key string) (secretMutationResult, error) {
	if !validSecretName(key) {
		return secretMutationResult{}, storage.ErrSecretInvalidName
	}
	if _, ok := m.immutableSecret(key); ok {
		return secretMutationResult{}, storage.ErrSecretReadOnly
	}
	golog.Debugf("secrets storage: deleting mutable secret key=%q", key)
	var notifyNodes []string
	deleted := false
	err := mapBboltSecretError(m.db.Update(func(tx *bbolt.Tx) error {
		bucket := tx.Bucket([]byte(secretsBucketName))
		if bucket == nil {
			return nil
		}
		raw := bucket.Get([]byte(key))
		if raw == nil {
			return nil
		}
		record, err := decodeSecretRecord(raw)
		if err != nil {
			return err
		}
		notifyNodes = normalizeSecretNodes(record.Nodes)
		if err := bucket.Delete([]byte(key)); err != nil {
			return err
		}
		deleted = true
		return nil
	}))
	if err != nil {
		return secretMutationResult{}, err
	}
	if deleted {
		m.notifySecretChange(secretChange{kind: secretChangeDelete, key: key})
	}
	return secretMutationResult{prevKey: key, newKey: key, notifyNodes: notifyNodes}, nil
}

// renameSecretNodeReferences updates every mutable and runtime immutable allow
// list that mentions oldName. Protected config files are not rewritten.
func (m *bboltKVManager) renameSecretNodeReferences(oldName, newName string) error {
	if oldName == newName {
		return nil
	}
	if !validSecretNodeName(oldName) || !validSecretNodeName(newName) {
		return storage.ErrSecretInvalidName
	}
	changed := map[string]struct{}{}
	if err := m.updateMutableSecretNodes(func(nodes []string) ([]string, bool) {
		return renameSecretNode(nodes, oldName, newName)
	}, changed); err != nil {
		return err
	}
	m.updateImmutableSecretNodes(func(nodes []string) ([]string, bool) {
		return renameSecretNode(nodes, oldName, newName)
	}, changed)
	m.notifySecretKeysChanged(changed)
	return nil
}

// deleteSecretNodeReferences removes a deleted node name from every mutable and
// runtime immutable allow list. Secrets themselves are left in place.
func (m *bboltKVManager) deleteSecretNodeReferences(name string) error {
	if !validSecretNodeName(name) {
		return storage.ErrSecretInvalidName
	}
	changed := map[string]struct{}{}
	if err := m.updateMutableSecretNodes(func(nodes []string) ([]string, bool) {
		return deleteSecretNode(nodes, name)
	}, changed); err != nil {
		return err
	}
	m.updateImmutableSecretNodes(func(nodes []string) ([]string, bool) {
		return deleteSecretNode(nodes, name)
	}, changed)
	m.notifySecretKeysChanged(changed)
	return nil
}

func (m *bboltKVManager) createMutableSecret(key string, record secretRecord) (bool, error) {
	if !validSecretName(key) || !validSecretNodeNames(record.Nodes) {
		return false, storage.ErrSecretInvalidName
	}
	if !utf8.ValidString(record.Value) {
		return false, storage.ErrSecretInvalidValue
	}
	if _, ok := m.immutableSecret(key); ok {
		return false, nil
	}
	created := false
	err := mapBboltSecretError(m.db.Update(func(tx *bbolt.Tx) error {
		bucket, err := tx.CreateBucketIfNotExists([]byte(secretsBucketName))
		if err != nil {
			return err
		}
		if bucket.Get([]byte(key)) != nil {
			return nil
		}
		raw, err := encodeSecretRecord(secretRecord{
			Value: record.Value,
			Nodes: normalizeSecretNodes(record.Nodes),
		})
		if err != nil {
			return err
		}
		if err := bucket.Put([]byte(key), raw); err != nil {
			return err
		}
		created = true
		return nil
	}))
	return created, err
}

func (m *bboltKVManager) secretEffectiveEntry(key string) (secretEffectiveEntry, bool, error) {
	if !validSecretName(key) {
		return secretEffectiveEntry{}, false, storage.ErrSecretInvalidName
	}
	if record, ok := m.immutableSecret(key); ok {
		return secretEffectiveEntry{
			Key:      key,
			Value:    record.Value,
			Nodes:    normalizeSecretNodes(record.Nodes),
			ReadOnly: true,
			Source:   record.source,
		}, true, nil
	}
	var entry secretEffectiveEntry
	err := mapBboltSecretError(m.db.View(func(tx *bbolt.Tx) error {
		bucket := tx.Bucket([]byte(secretsBucketName))
		if bucket == nil {
			return nil
		}
		raw := bucket.Get([]byte(key))
		if raw == nil {
			return nil
		}
		record, err := decodeSecretRecord(raw)
		if err != nil {
			return err
		}
		entry = secretEffectiveEntry{
			Key:      key,
			Value:    record.Value,
			Nodes:    normalizeSecretNodes(record.Nodes),
			ReadOnly: false,
			Source:   api.SecretSourceMutable,
		}
		return nil
	}))
	if err != nil {
		return secretEffectiveEntry{}, false, err
	}
	return entry, entry.Key != "", nil
}

func (m *bboltKVManager) updateMutableSecretNodes(
	update func([]string) ([]string, bool),
	changed map[string]struct{},
) error {
	return mapBboltSecretError(m.db.Update(func(tx *bbolt.Tx) error {
		bucket := tx.Bucket([]byte(secretsBucketName))
		if bucket == nil {
			return nil
		}
		rewrites := map[string][]byte{}
		if err := bucket.ForEach(func(k, v []byte) error {
			record, err := decodeSecretRecord(v)
			if err != nil {
				return err
			}
			nodes, ok := update(record.Nodes)
			if !ok {
				return nil
			}
			record.Nodes = nodes
			raw, err := encodeSecretRecord(record)
			if err != nil {
				return err
			}
			key := string(k)
			rewrites[key] = raw
			changed[key] = struct{}{}
			return nil
		}); err != nil {
			return err
		}
		for key, raw := range rewrites {
			if err := bucket.Put([]byte(key), raw); err != nil {
				return err
			}
		}
		return nil
	}))
}

func (m *bboltKVManager) updateImmutableSecretNodes(
	update func([]string) ([]string, bool),
	changed map[string]struct{},
) {
	m.secretsMu.Lock()
	defer m.secretsMu.Unlock()
	for key, record := range m.immutableSecrets {
		nodes, ok := update(record.Nodes)
		if !ok {
			continue
		}
		record.Nodes = nodes
		m.immutableSecrets[key] = record
		changed[key] = struct{}{}
	}
}

func (m *bboltKVManager) notifySecretKeysChanged(changed map[string]struct{}) {
	if len(changed) == 0 {
		return
	}
	changes := make([]secretChange, 0, len(changed))
	for key := range changed {
		changes = append(changes, secretChange{kind: secretChangeUpsert, key: key})
	}
	slices.SortFunc(changes, func(a, b secretChange) int {
		return strings.Compare(a.key, b.key)
	})
	m.notifySecretChange(changes...)
}

func (m *bboltKVManager) subscribeSecretChanges(callback func(secretChange)) uint64 {
	m.secretsMu.Lock()
	defer m.secretsMu.Unlock()
	m.secretsNextSub++
	id := m.secretsNextSub
	if m.secretsChangeSubs == nil {
		m.secretsChangeSubs = map[uint64]func(secretChange){}
	}
	m.secretsChangeSubs[id] = callback
	return id
}

func (m *bboltKVManager) unsubscribeSecretChanges(id uint64) {
	m.secretsMu.Lock()
	defer m.secretsMu.Unlock()
	delete(m.secretsChangeSubs, id)
}

func (m *bboltKVManager) notifySecretChange(changes ...secretChange) {
	if len(changes) == 0 {
		return
	}
	m.secretsMu.Lock()
	callbacks := make([]func(secretChange), 0, len(m.secretsChangeSubs))
	for _, callback := range m.secretsChangeSubs {
		callbacks = append(callbacks, callback)
	}
	m.secretsMu.Unlock()

	for _, change := range changes {
		for _, callback := range callbacks {
			callback(change)
		}
	}
}

func (m *bboltKVManager) immutableSecret(key string) (immutableSecret, bool) {
	m.secretsMu.Lock()
	defer m.secretsMu.Unlock()
	record, ok := m.immutableSecrets[key]
	if !ok {
		return immutableSecret{}, false
	}
	record.Nodes = normalizeSecretNodes(record.Nodes)
	return record, true
}

func (m *bboltKVManager) cloneImmutableSecrets() map[string]immutableSecret {
	m.secretsMu.Lock()
	defer m.secretsMu.Unlock()
	return cloneImmutableSecrets(m.immutableSecrets)
}

func cloneImmutableSecrets(in map[string]immutableSecret) map[string]immutableSecret {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]immutableSecret, len(in))
	for key, record := range in {
		record.Nodes = normalizeSecretNodes(record.Nodes)
		out[key] = record
	}
	return out
}

func encodeSecretRecord(record secretRecord) ([]byte, error) {
	if !utf8.ValidString(record.Value) {
		return nil, storage.ErrSecretInvalidValue
	}
	if !validSecretNodeNames(record.Nodes) {
		return nil, storage.ErrSecretInvalidName
	}
	record.Nodes = normalizeSecretNodes(record.Nodes)
	return json.Marshal(record)
}

func decodeSecretRecord(data []byte) (secretRecord, error) {
	var record secretRecord
	if err := json.Unmarshal(data, &record); err != nil {
		return secretRecord{}, fmt.Errorf("%w: decode secret: %v", storage.ErrSecretInvalidValue, err)
	}
	if !utf8.ValidString(record.Value) {
		return secretRecord{}, storage.ErrSecretInvalidValue
	}
	if !validSecretNodeNames(record.Nodes) {
		return secretRecord{}, storage.ErrSecretInvalidName
	}
	record.Nodes = normalizeSecretNodes(record.Nodes)
	return record, nil
}

func secretEntryToAPI(entry secretEffectiveEntry) api.SecretEntry {
	return api.SecretEntry{
		Key:      entry.Key,
		Value:    entry.Value,
		Nodes:    append([]string(nil), entry.Nodes...),
		ReadOnly: entry.ReadOnly,
		Source:   entry.Source,
	}
}

func secretNodeAllowed(nodes []string, node string) bool {
	for _, allowed := range nodes {
		if allowed == node {
			return true
		}
	}
	return false
}

func secretNodeUnion(a, b []string) []string {
	out := append([]string(nil), a...)
	out = append(out, b...)
	return normalizeSecretNodes(out)
}

func renameSecretNode(nodes []string, oldName, newName string) ([]string, bool) {
	next := append([]string(nil), nodes...)
	changed := false
	for i, node := range next {
		if node == oldName {
			next[i] = newName
			changed = true
		}
	}
	if !changed {
		return nil, false
	}
	return normalizeSecretNodes(next), true
}

func deleteSecretNode(nodes []string, name string) ([]string, bool) {
	next := make([]string, 0, len(nodes))
	changed := false
	for _, node := range nodes {
		if node == name {
			changed = true
			continue
		}
		next = append(next, node)
	}
	if !changed {
		return nil, false
	}
	return normalizeSecretNodes(next), true
}

func normalizeSecretNodes(nodes []string) []string {
	if len(nodes) == 0 {
		return nil
	}
	out := append([]string(nil), nodes...)
	slices.Sort(out)
	out = slices.Compact(out)
	return out
}

func validSecretNodeNames(nodes []string) bool {
	for _, node := range nodes {
		if !validSecretNodeName(node) {
			return false
		}
	}
	return true
}

func validSecretNodeName(name string) bool {
	return pasta.ValidateNodeName(name) == nil
}

func validSecretName(name string) bool {
	return name != "" && utf8.ValidString(name)
}

func randomSecretKey() (string, error) {
	var data [secretGeneratedKeyBytes]byte
	if _, err := rand.Read(data[:]); err != nil {
		return "", err
	}
	return "secret-" + hex.EncodeToString(data[:]), nil
}

func mapBboltSecretError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, bberrors.ErrDatabaseNotOpen), errors.Is(err, bberrors.ErrTxClosed):
		return storage.ErrSecretClosed
	case errors.Is(err, bberrors.ErrBucketNameRequired), errors.Is(err, bberrors.ErrKeyRequired), errors.Is(err, bberrors.ErrKeyTooLarge):
		return storage.ErrSecretInvalidName
	case errors.Is(err, bberrors.ErrValueTooLarge):
		return storage.ErrSecretInvalidValue
	default:
		return err
	}
}

var errSecretNotFound = errors.New("secret was not found")
