package main

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/asciimoth/almagest/lib"
	"go.etcd.io/bbolt"
	bberrors "go.etcd.io/bbolt/errors"
)

const (
	kvDBFileName       = "kv.db"
	kvDataBucketName   = "data"
	dnsRootBucketName  = "dns"
	kvPluginBucketName = "plugin data"
)

const (
	kvValueHiddenFlag byte = 1 << iota
	kvValueStringFlag
)

var (
	_ almagest.KVManager       = (*bboltKVManager)(nil)
	_ almagest.DnsCacheManager = (*bboltKVManager)(nil)
	_ almagest.KVStore         = (*bboltKVStore)(nil)
	_ almagest.KVReadTx        = (*bboltKVReadTx)(nil)
	_ almagest.KVWriteTx       = (*bboltKVWriteTx)(nil)
)

type bboltKVManager struct {
	db *bbolt.DB

	mu         sync.Mutex
	nextSub    uint64
	changeSubs map[uint64]func()

	dnsMu         sync.Mutex
	dnsStores     map[string]*bboltDNSCacheStorage
	dnsNextSub    uint64
	dnsChangeSubs map[uint64]func(dnsCacheChange)
}

type bboltKVStore struct {
	manager        *bboltKVManager
	path           []string
	protectedDepth int
	invalid        bool
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
}

func openKVManager(path string) (*bboltKVManager, error) {
	db, err := bbolt.Open(path, 0o600, &bbolt.Options{Timeout: time.Second})
	if err != nil {
		return nil, fmt.Errorf("open kv database: %w", err)
	}
	manager := &bboltKVManager{
		db:            db,
		changeSubs:    map[uint64]func(){},
		dnsStores:     map[string]*bboltDNSCacheStorage{},
		dnsChangeSubs: map[uint64]func(dnsCacheChange){},
	}
	if err := manager.initRootBuckets(); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := manager.cleanupExpiredDNS(time.Now()); err != nil {
		_ = db.Close()
		return nil, err
	}
	return manager, nil
}

func (m *bboltKVManager) initRootBuckets() error {
	return mapBboltKVError(m.db.Update(func(tx *bbolt.Tx) error {
		if _, err := tx.CreateBucketIfNotExists([]byte(kvDataBucketName)); err != nil {
			return err
		}
		if _, err := tx.CreateBucketIfNotExists([]byte(dnsRootBucketName)); err != nil {
			return err
		}
		if _, err := tx.CreateBucketIfNotExists([]byte(kvPluginBucketName)); err != nil {
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

func (m *bboltKVManager) DefaultStore() almagest.KVStore {
	return &bboltKVStore{
		manager:        m,
		path:           []string{kvDataBucketName},
		protectedDepth: 1,
	}
}

func (m *bboltKVManager) PluginStore(name string) almagest.KVStore {
	invalid := !validKVName(name)
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

func (s *bboltKVStore) Subtree(path ...string) almagest.KVStore {
	next := &bboltKVStore{
		manager:        s.manager,
		path:           cloneKVPath(s.path),
		protectedDepth: s.protectedDepth,
		invalid:        s.invalid || !validKVPath(path),
	}
	next.path = append(next.path, path...)
	return next
}

func (s *bboltKVStore) View(ctx context.Context, fn func(almagest.KVReadTx) error) error {
	if s.invalid {
		return almagest.ErrKVInvalidName
	}
	if err := ctx.Err(); err != nil {
		return err
	}
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

func (s *bboltKVStore) Update(ctx context.Context, fn func(almagest.KVWriteTx) error) error {
	if s.invalid {
		return almagest.ErrKVInvalidName
	}
	if err := ctx.Err(); err != nil {
		return err
	}
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
		})
	}))
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

func (r *bboltKVReader) Get(key string) (*almagest.KValue, bool, error) {
	if r.invalid || !validKVName(key) {
		return nil, false, almagest.ErrKVInvalidName
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
	return &value, true, nil
}

func (r *bboltKVReader) ForEachValue(fn func(key string, value almagest.KValue) error) error {
	if r.invalid {
		return almagest.ErrKVInvalidName
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
		return fn(string(k), value)
	}))
}

func (r *bboltKVReader) ForEachSubtree(fn func(name string) error) error {
	if r.invalid {
		return almagest.ErrKVInvalidName
	}
	bucket := r.bucket()
	if bucket == nil {
		return nil
	}
	return mapBboltKVError(bucket.ForEachBucket(func(k []byte) error {
		return fn(string(k))
	}))
}

func (r *bboltKVReadTx) Subtree(path ...string) almagest.KVReadTx {
	next := &bboltKVReadTx{bboltKVReader: bboltKVReader{
		tx:      r.tx,
		path:    cloneKVPath(r.path),
		invalid: r.invalid || !validKVPath(path),
	}}
	next.path = append(next.path, path...)
	return next
}

func (w *bboltKVWriteTx) Subtree(path ...string) almagest.KVWriteTx {
	next := &bboltKVWriteTx{
		bboltKVReader: bboltKVReader{
			tx:      w.tx,
			path:    cloneKVPath(w.path),
			invalid: w.invalid || !validKVPath(path),
		},
		protectedDepth: w.protectedDepth,
	}
	next.path = append(next.path, path...)
	return next
}

func (w *bboltKVWriteTx) Ensure() error {
	if w.invalid {
		return almagest.ErrKVInvalidName
	}
	_, err := ensureBboltBucket(w.tx, w.path)
	return mapBboltKVError(err)
}

func (w *bboltKVWriteTx) Set(key string, value almagest.KValue) error {
	if w.invalid || !validKVName(key) {
		return almagest.ErrKVInvalidName
	}
	data, err := encodeBboltKValue(value)
	if err != nil {
		return err
	}
	bucket, err := ensureBboltBucket(w.tx, w.path)
	if err != nil {
		return mapBboltKVError(err)
	}
	return mapBboltKVError(bucket.Put([]byte(key), data))
}

func (w *bboltKVWriteTx) Delete(key string) error {
	if w.invalid || !validKVName(key) {
		return almagest.ErrKVInvalidName
	}
	bucket := w.bucket()
	if bucket == nil || bucket.Bucket([]byte(key)) != nil {
		return nil
	}
	return mapBboltKVError(bucket.Delete([]byte(key)))
}

func (w *bboltKVWriteTx) Drop() error {
	if w.invalid {
		return almagest.ErrKVInvalidName
	}
	if len(w.path) <= w.protectedDepth {
		return almagest.ErrKVRoot
	}
	parent := bucketAtPath(w.tx, w.path[:len(w.path)-1])
	if parent == nil {
		return nil
	}
	err := parent.DeleteBucket([]byte(w.path[len(w.path)-1]))
	if errors.Is(err, bberrors.ErrBucketNotFound) {
		return nil
	}
	return mapBboltKVError(err)
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
		return nil, almagest.ErrKVInvalidName
	}
	if len(path) == 0 {
		return nil, almagest.ErrKVInvalidName
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

func encodeBboltKValue(value almagest.KValue) ([]byte, error) {
	if value.String && !utf8.Valid(value.Data) {
		return nil, almagest.ErrKVInvalidValue
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

func decodeBboltKValue(data []byte) (almagest.KValue, error) {
	if len(data) == 0 {
		return almagest.KValue{}, fmt.Errorf("decode kv value: missing flags")
	}
	flags := data[0]
	if flags&^(kvValueHiddenFlag|kvValueStringFlag) != 0 {
		return almagest.KValue{}, fmt.Errorf("decode kv value: unsupported flags 0x%02x", flags)
	}
	return almagest.KValue{
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

func mapBboltKVError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, bberrors.ErrDatabaseNotOpen), errors.Is(err, bberrors.ErrTxClosed):
		return almagest.ErrKVClosed
	case errors.Is(err, bberrors.ErrBucketNameRequired), errors.Is(err, bberrors.ErrKeyRequired), errors.Is(err, bberrors.ErrKeyTooLarge):
		return almagest.ErrKVInvalidName
	case errors.Is(err, bberrors.ErrValueTooLarge):
		return almagest.ErrKVInvalidValue
	case errors.Is(err, bberrors.ErrIncompatibleValue):
		return almagest.ErrKVKindConflict
	default:
		return err
	}
}

func openKVManagerInStorage(storagePath string) (*bboltKVManager, error) {
	return openKVManager(filepath.Join(storagePath, kvDBFileName))
}
