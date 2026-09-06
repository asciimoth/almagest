package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/asciimoth/almagest/lib/storage"
	"github.com/kataras/golog"
	"go.etcd.io/bbolt"
	bberrors "go.etcd.io/bbolt/errors"
)

const (
	kvDBFileName       = "kv.db"
	kvDataBucketName   = "data"
	kvOwnersBucketName = "kv owners"
	dnsRootBucketName  = "dns"
	kvPluginBucketName = "plugin data"
	secretsBucketName  = "secrets"
	tlsRootBucketName  = "tls"
)

const (
	kvValueHiddenFlag byte = 1 << iota
	kvValueStringFlag
)

var (
	_ storage.KVManager             = (*bboltKVManager)(nil)
	_ storage.DnsCacheManager       = (*bboltKVManager)(nil)
	_ storage.DnsCacheStorageLister = (*bboltKVManager)(nil)
	_ storage.SecretsStorage        = (*bboltKVManager)(nil)
	_ storage.TlsConfigStorage      = (*bboltKVManager)(nil)
	_ storage.KVStore               = (*bboltKVStore)(nil)
	_ storage.KVReadTx              = (*bboltKVReadTx)(nil)
	_ storage.KVWriteTx             = (*bboltKVWriteTx)(nil)
)

type bboltKVManager struct {
	db *bbolt.DB

	disableDNSCacheExpiration bool

	mu         sync.Mutex
	nextSub    uint64
	changeSubs map[uint64]func()

	// kvOwnerMu serializes owner lifecycle changes with writes through owned
	// store handles. This prevents a late write from restoring an old owner name
	// after a rename or recreating data after its owner was deleted.
	kvOwnerMu   sync.RWMutex
	kvOwnerRefs map[*bboltKVOwner]struct{}

	dnsMu         sync.Mutex
	dnsStores     map[string]*bboltDNSCacheStorage
	dnsNextSub    uint64
	dnsChangeSubs map[uint64]func(dnsCacheChange)

	secretsMu         sync.Mutex
	immutableSecrets  map[string]immutableSecret
	secretsNextSub    uint64
	secretsChangeSubs map[uint64]func(secretChange)

	tlsMu         sync.Mutex
	immutableTLS  immutableTLSData
	builtinCA     *tlsBuiltinCA
	tlsWatchers   map[string]map[string]struct{}
	tlsCAWatchers map[string]struct{}
	tlsNextSub    uint64
	tlsChangeSubs map[uint64]func(tlsChange)
}

type bboltKVStore struct {
	manager        *bboltKVManager
	path           []string
	protectedDepth int
	invalid        bool
	invalidOwner   bool
	owner          *bboltKVOwner
	ownershipDepth int
}

// bboltKVOwner is shared by an owned store and all of its subtrees. Its fields
// are protected by the manager's kvOwnerMu.
type bboltKVOwner struct {
	name   string
	active bool
}

type bboltKVReader struct {
	tx      *bbolt.Tx
	path    []string
	invalid bool
}

type bboltKVReadTx struct {
	bboltKVReader
}

type bboltKVWriteTx struct {
	bboltKVReader
	protectedDepth int
	owner          string
	ownershipDepth int
}

// bboltKVManagerOptions controls daemon-local persistence behavior that is not
// part of the workspace graph. The options are fixed for the lifetime of the
// opened manager.
type bboltKVManagerOptions struct {
	// DisableDNSCacheExpiration keeps DNS cache entries readable even after the
	// expiration timestamp stored from their DNS TTL. Expiration timestamps are
	// still persisted and reported by the admin API.
	DisableDNSCacheExpiration bool
	// ImmutableSecrets overlays protected config and environment-provided
	// secrets on top of mutable bbolt entries. These entries are cloned during
	// open and then maintained in memory for node rename/delete cleanup.
	ImmutableSecrets map[string]immutableSecret
	// ImmutableTLS overlays protected config and OS-provided TLS data on top of
	// mutable bbolt entries. The overlay is cloned during open and then kept
	// only in memory.
	ImmutableTLS immutableTLSData
	// BuiltinCA is the daemon-owned CA keypair. Only its public certificate is
	// added to the effective TLS certificate catalog.
	BuiltinCA *tlsBuiltinCA
}

func openKVManager(path string) (*bboltKVManager, error) {
	return openKVManagerWithOptions(path, bboltKVManagerOptions{})
}

func openKVManagerWithOptions(path string, options bboltKVManagerOptions) (*bboltKVManager, error) {
	existing, err := bboltDatabaseExists(path)
	if err != nil {
		return nil, err
	}
	if existing {
		golog.Debugf("bbolt database: opening existing database at %s", path)
	} else {
		golog.Debugf("bbolt database: creating new database at %s", path)
	}
	db, err := bbolt.Open(path, 0o600, &bbolt.Options{Timeout: time.Second})
	if err != nil {
		return nil, fmt.Errorf("open kv database: %w", err)
	}
	immutableTLS := cloneImmutableTLSData(options.ImmutableTLS)
	builtinCA := cloneTLSBuiltinCA(options.BuiltinCA)
	if builtinCA != nil {
		immutableTLS = withTLSBuiltinCA(immutableTLS, builtinCA)
	}
	manager := &bboltKVManager{
		db:                        db,
		disableDNSCacheExpiration: options.DisableDNSCacheExpiration,
		changeSubs:                map[uint64]func(){},
		kvOwnerRefs:               map[*bboltKVOwner]struct{}{},
		dnsStores:                 map[string]*bboltDNSCacheStorage{},
		dnsChangeSubs:             map[uint64]func(dnsCacheChange){},
		immutableSecrets:          cloneImmutableSecrets(options.ImmutableSecrets),
		secretsChangeSubs:         map[uint64]func(secretChange){},
		immutableTLS:              immutableTLS,
		builtinCA:                 builtinCA,
		tlsWatchers:               map[string]map[string]struct{}{},
		tlsCAWatchers:             map[string]struct{}{},
		tlsChangeSubs:             map[uint64]func(tlsChange){},
	}
	if err := manager.initRootBuckets(); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := manager.cleanupExpiredDNS(time.Now()); err != nil {
		_ = db.Close()
		return nil, err
	}
	golog.Debugf("bbolt database: ready at %s", path)
	return manager, nil
}

func bboltDatabaseExists(path string) (bool, error) {
	info, err := os.Stat(path)
	if err == nil {
		if info.IsDir() {
			return false, fmt.Errorf("open kv database: %s is a directory", path)
		}
		return true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return false, fmt.Errorf("stat kv database: %w", err)
}

func (m *bboltKVManager) initRootBuckets() error {
	return mapBboltKVError(m.db.Update(func(tx *bbolt.Tx) error {
		if _, err := tx.CreateBucketIfNotExists([]byte(kvDataBucketName)); err != nil {
			return err
		}
		if _, err := tx.CreateBucketIfNotExists([]byte(kvOwnersBucketName)); err != nil {
			return err
		}
		if _, err := tx.CreateBucketIfNotExists([]byte(dnsRootBucketName)); err != nil {
			return err
		}
		if _, err := tx.CreateBucketIfNotExists([]byte(kvPluginBucketName)); err != nil {
			return err
		}
		if _, err := tx.CreateBucketIfNotExists([]byte(secretsBucketName)); err != nil {
			return err
		}
		tlsRoot, err := tx.CreateBucketIfNotExists([]byte(tlsRootBucketName))
		if err != nil {
			return err
		}
		if _, err := tlsRoot.CreateBucketIfNotExists([]byte(tlsCertsBucketName)); err != nil {
			return err
		}
		if _, err := tlsRoot.CreateBucketIfNotExists([]byte(tlsPoliciesBucketName)); err != nil {
			return err
		}
		return nil
	}))
}

func (m *bboltKVManager) Close() error {
	if m == nil || m.db == nil {
		return nil
	}
	return mapBboltKVError(m.db.Close())
}

func (m *bboltKVManager) DefaultStore() storage.KVStore {
	golog.Debug("KV storage: accessing default store")
	return &bboltKVStore{
		manager:        m,
		path:           []string{kvDataBucketName},
		protectedDepth: 1,
	}
}

func (m *bboltKVManager) PluginStore(name string) storage.KVStore {
	invalid := !validKVName(name)
	golog.Debugf("KV storage: accessing plugin store name=%q invalid=%t", name, invalid)
	return &bboltKVStore{
		manager:        m,
		path:           []string{kvPluginBucketName, name},
		protectedDepth: 2,
		invalid:        invalid,
	}
}

func (m *bboltKVManager) subscribeChanges(callback func()) uint64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.nextSub++
	id := m.nextSub
	m.changeSubs[id] = callback
	return id
}

func (m *bboltKVManager) unsubscribeChanges(id uint64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.changeSubs, id)
}

func (m *bboltKVManager) notifyChange() {
	m.mu.Lock()
	callbacks := make([]func(), 0, len(m.changeSubs))
	for _, callback := range m.changeSubs {
		callbacks = append(callbacks, callback)
	}
	m.mu.Unlock()

	for _, callback := range callbacks {
		callback()
	}
}

func (s *bboltKVStore) Subtree(path ...string) storage.KVStore {
	next := &bboltKVStore{
		manager:        s.manager,
		path:           cloneKVPath(s.path),
		protectedDepth: s.protectedDepth,
		invalid:        s.invalid || !validKVPath(path),
		invalidOwner:   s.invalidOwner,
		owner:          s.owner,
		ownershipDepth: s.ownershipDepth,
	}
	next.path = append(next.path, path...)
	return next
}

func (s *bboltKVStore) OwnedBy(owner string) storage.KVStore {
	next := &bboltKVStore{
		manager:        s.manager,
		path:           cloneKVPath(s.path),
		protectedDepth: s.protectedDepth,
		invalid:        s.invalid,
		invalidOwner:   !validKVOwner(owner),
		ownershipDepth: len(s.path),
	}
	if next.invalidOwner {
		return next
	}
	next.owner = &bboltKVOwner{name: owner, active: true}
	s.manager.kvOwnerMu.Lock()
	s.manager.kvOwnerRefs[next.owner] = struct{}{}
	s.manager.kvOwnerMu.Unlock()
	return next
}

func (s *bboltKVStore) View(ctx context.Context, fn func(storage.KVReadTx) error) error {
	if s.invalid {
		return storage.ErrKVInvalidName
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	golog.Debugf("KV storage: read access path=%q", s.path)
	return mapBboltKVError(s.manager.db.View(func(tx *bbolt.Tx) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		return fn(&bboltKVReadTx{bboltKVReader: bboltKVReader{
			tx:   tx,
			path: cloneKVPath(s.path),
		}})
	}))
}

func (s *bboltKVStore) Update(ctx context.Context, fn func(storage.KVWriteTx) error) error {
	if s.invalid {
		return storage.ErrKVInvalidName
	}
	if s.invalidOwner {
		return storage.ErrKVInvalidOwner
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	owner := ""
	if s.owner != nil {
		s.manager.kvOwnerMu.RLock()
		if !s.owner.active {
			s.manager.kvOwnerMu.RUnlock()
			return storage.ErrKVOwnerDeleted
		}
		owner = s.owner.name
	}
	golog.Debugf("KV storage: write access path=%q", s.path)
	err := mapBboltKVError(s.manager.db.Update(func(tx *bbolt.Tx) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		return fn(&bboltKVWriteTx{
			bboltKVReader: bboltKVReader{
				tx:   tx,
				path: cloneKVPath(s.path),
			},
			protectedDepth: s.protectedDepth,
			owner:          owner,
			ownershipDepth: s.ownershipDepth,
		})
	}))
	if s.owner != nil {
		s.manager.kvOwnerMu.RUnlock()
	}
	if err != nil {
		return err
	}
	s.manager.notifyChange()
	return nil
}

func (r *bboltKVReader) Exists() bool {
	if r.invalid {
		return false
	}
	return r.bucket() != nil
}

func (r *bboltKVReader) Owner() (string, error) {
	if r.invalid {
		return "", storage.ErrKVInvalidName
	}
	if r.bucket() == nil {
		return "", nil
	}
	return bboltKVOwnerAt(r.tx, kvOwnershipBucket, r.path)
}

func (r *bboltKVReader) Get(key string) (*storage.KValue, bool, error) {
	if r.invalid || !validKVName(key) {
		return nil, false, storage.ErrKVInvalidName
	}
	bucket := r.bucket()
	if bucket == nil || bucket.Bucket([]byte(key)) != nil {
		return nil, false, nil
	}
	raw := bucket.Get([]byte(key))
	if raw == nil {
		return nil, false, nil
	}
	value, err := decodeBboltKValue(raw)
	if err != nil {
		return nil, false, err
	}
	value.Owner, err = bboltKVOwnerAt(r.tx, kvOwnershipValue, appendKVPath(r.path, key))
	if err != nil {
		return nil, false, err
	}
	return &value, true, nil
}

func (r *bboltKVReader) ForEachValue(fn func(key string, value storage.KValue) error) error {
	if r.invalid {
		return storage.ErrKVInvalidName
	}
	bucket := r.bucket()
	if bucket == nil {
		return nil
	}
	return mapBboltKVError(bucket.ForEach(func(k, v []byte) error {
		if v == nil {
			return nil
		}
		value, err := decodeBboltKValue(v)
		if err != nil {
			return err
		}
		value.Owner, err = bboltKVOwnerAt(r.tx, kvOwnershipValue, appendKVPath(r.path, string(k)))
		if err != nil {
			return err
		}
		return fn(string(k), value)
	}))
}

func (r *bboltKVReader) ForEachSubtree(fn func(name string) error) error {
	if r.invalid {
		return storage.ErrKVInvalidName
	}
	bucket := r.bucket()
	if bucket == nil {
		return nil
	}
	return mapBboltKVError(bucket.ForEachBucket(func(k []byte) error {
		return fn(string(k))
	}))
}

func (r *bboltKVReadTx) Subtree(path ...string) storage.KVReadTx {
	next := &bboltKVReadTx{bboltKVReader: bboltKVReader{
		tx:      r.tx,
		path:    cloneKVPath(r.path),
		invalid: r.invalid || !validKVPath(path),
	}}
	next.path = append(next.path, path...)
	return next
}

func (w *bboltKVWriteTx) Subtree(path ...string) storage.KVWriteTx {
	next := &bboltKVWriteTx{
		bboltKVReader: bboltKVReader{
			tx:      w.tx,
			path:    cloneKVPath(w.path),
			invalid: w.invalid || !validKVPath(path),
		},
		protectedDepth: w.protectedDepth,
		owner:          w.owner,
		ownershipDepth: w.ownershipDepth,
	}
	next.path = append(next.path, path...)
	return next
}

func (w *bboltKVWriteTx) Ensure() error {
	if w.invalid {
		return storage.ErrKVInvalidName
	}
	_, err := ensureBboltBucket(w.tx, w.path)
	if err != nil {
		return mapBboltKVError(err)
	}
	return mapBboltKVError(w.claimSubtrees())
}

func (w *bboltKVWriteTx) Set(key string, value storage.KValue) error {
	if w.invalid || !validKVName(key) {
		return storage.ErrKVInvalidName
	}
	data, err := encodeBboltKValue(value)
	if err != nil {
		return err
	}
	bucket, err := ensureBboltBucket(w.tx, w.path)
	if err != nil {
		return mapBboltKVError(err)
	}
	if err := w.claimSubtrees(); err != nil {
		return mapBboltKVError(err)
	}
	if err := bucket.Put([]byte(key), data); err != nil {
		return mapBboltKVError(err)
	}
	if w.owner == "" {
		return nil
	}
	return mapBboltKVError(putBboltKVOwner(w.tx, kvOwnershipValue, appendKVPath(w.path, key), w.owner))
}

func (w *bboltKVWriteTx) Delete(key string) error {
	if w.invalid || !validKVName(key) {
		return storage.ErrKVInvalidName
	}
	bucket := w.bucket()
	if bucket != nil && bucket.Bucket([]byte(key)) != nil {
		return nil
	}
	if bucket != nil {
		if err := bucket.Delete([]byte(key)); err != nil {
			return mapBboltKVError(err)
		}
	}
	return mapBboltKVError(deleteBboltKVOwner(w.tx, kvOwnershipValue, appendKVPath(w.path, key)))
}

func (w *bboltKVWriteTx) Drop() error {
	if w.invalid {
		return storage.ErrKVInvalidName
	}
	if len(w.path) <= w.protectedDepth {
		return storage.ErrKVRoot
	}
	parent := bucketAtPath(w.tx, w.path[:len(w.path)-1])
	if parent == nil {
		return mapBboltKVError(deleteBboltKVOwnershipSubtree(w.tx, w.path))
	}
	err := parent.DeleteBucket([]byte(w.path[len(w.path)-1]))
	if errors.Is(err, bberrors.ErrBucketNotFound) {
		return mapBboltKVError(deleteBboltKVOwnershipSubtree(w.tx, w.path))
	}
	if err != nil {
		return mapBboltKVError(err)
	}
	return mapBboltKVError(deleteBboltKVOwnershipSubtree(w.tx, w.path))
}

func (w *bboltKVWriteTx) claimSubtrees() error {
	if w.owner == "" {
		return nil
	}
	depth := max(w.ownershipDepth, w.protectedDepth+1)
	for ; depth <= len(w.path); depth++ {
		if err := putBboltKVOwner(w.tx, kvOwnershipBucket, w.path[:depth], w.owner); err != nil {
			return err
		}
	}
	return nil
}

func (r *bboltKVReader) bucket() *bbolt.Bucket {
	if r.invalid {
		return nil
	}
	return bucketAtPath(r.tx, r.path)
}

func bucketAtPath(tx *bbolt.Tx, path []string) *bbolt.Bucket {
	if len(path) == 0 {
		return nil
	}
	bucket := tx.Bucket([]byte(path[0]))
	for _, name := range path[1:] {
		if bucket == nil {
			return nil
		}
		bucket = bucket.Bucket([]byte(name))
	}
	return bucket
}

func ensureBboltBucket(tx *bbolt.Tx, path []string) (*bbolt.Bucket, error) {
	if !validKVPath(path) {
		return nil, storage.ErrKVInvalidName
	}
	if len(path) == 0 {
		return nil, storage.ErrKVInvalidName
	}
	bucket, err := tx.CreateBucketIfNotExists([]byte(path[0]))
	if err != nil {
		return nil, err
	}
	for _, name := range path[1:] {
		bucket, err = bucket.CreateBucketIfNotExists([]byte(name))
		if err != nil {
			return nil, err
		}
	}
	return bucket, nil
}

func encodeBboltKValue(value storage.KValue) ([]byte, error) {
	if value.String && !utf8.Valid(value.Data) {
		return nil, storage.ErrKVInvalidValue
	}
	data := make([]byte, len(value.Data)+1)
	if value.Hidden {
		data[0] |= kvValueHiddenFlag
	}
	if value.String {
		data[0] |= kvValueStringFlag
	}
	copy(data[1:], value.Data)
	return data, nil
}

func decodeBboltKValue(data []byte) (storage.KValue, error) {
	if len(data) == 0 {
		return storage.KValue{}, fmt.Errorf("decode kv value: missing flags")
	}
	flags := data[0]
	if flags&^(kvValueHiddenFlag|kvValueStringFlag) != 0 {
		return storage.KValue{}, fmt.Errorf("decode kv value: unsupported flags 0x%02x", flags)
	}
	return storage.KValue{
		Data:   append([]byte(nil), data[1:]...),
		Hidden: flags&kvValueHiddenFlag != 0,
		String: flags&kvValueStringFlag != 0,
	}, nil
}

func validKVPath(path []string) bool {
	for _, name := range path {
		if !validKVName(name) {
			return false
		}
	}
	return true
}

func validKVName(name string) bool {
	return name != ""
}

func cloneKVPath(path []string) []string {
	return append([]string(nil), path...)
}

func appendKVPath(path []string, name string) []string {
	out := make([]string, len(path)+1)
	copy(out, path)
	out[len(path)] = name
	return out
}

func mapBboltKVError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, bberrors.ErrDatabaseNotOpen), errors.Is(err, bberrors.ErrTxClosed):
		return storage.ErrKVClosed
	case errors.Is(err, bberrors.ErrBucketNameRequired), errors.Is(err, bberrors.ErrKeyRequired), errors.Is(err, bberrors.ErrKeyTooLarge):
		return storage.ErrKVInvalidName
	case errors.Is(err, bberrors.ErrValueTooLarge):
		return storage.ErrKVInvalidValue
	case errors.Is(err, bberrors.ErrIncompatibleValue):
		return storage.ErrKVKindConflict
	default:
		return err
	}
}

func openKVManagerInStorage(storagePath string, options bboltKVManagerOptions) (*bboltKVManager, error) {
	return openKVManagerWithOptions(filepath.Join(storagePath, kvDBFileName), options)
}
