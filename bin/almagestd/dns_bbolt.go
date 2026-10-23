package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	gdns "github.com/asciimoth/gonnect/dns"
	"github.com/kataras/golog"
	"go.etcd.io/bbolt"
	bberrors "go.etcd.io/bbolt/errors"
)

const dnsMemoryCacheMaxEntries = 256

var (
	_ gdns.CacheStorage = (*bboltDNSCacheStorage)(nil)

	errDNSClosed       = errors.New("dns cache: database is closed")
	errDNSInvalidName  = errors.New("dns cache: invalid cache name or key")
	errDNSInvalidValue = errors.New("dns cache: invalid value")
)

type dnsCacheChangeKind string

const (
	dnsCacheChangeCacheUpsert dnsCacheChangeKind = "cache_upsert"
	dnsCacheChangeCacheDelete dnsCacheChangeKind = "cache_delete"
	dnsCacheChangeKeyUpsert   dnsCacheChangeKind = "key_upsert"
	dnsCacheChangeKeyDelete   dnsCacheChangeKind = "key_delete"
)

type dnsCacheChange struct {
	kind      dnsCacheChangeKind
	cache     string
	key       string
	raw       []byte
	expiresAt time.Time
}

func newDNSKeyUpsertChange(cache, key string, raw []byte, expiresAt time.Time) dnsCacheChange {
	return dnsCacheChange{
		kind:      dnsCacheChangeKeyUpsert,
		cache:     cache,
		key:       key,
		raw:       append([]byte(nil), raw...),
		expiresAt: expiresAt,
	}
}

// bboltDNSCacheStorage stores one named DNS cache under the fixed
// dns/<cache-name>/<entry-key> bbolt hierarchy. The bbolt JSON value is the
// source of truth; entries holds a bounded set of decoded dns.Message copies to
// avoid reparsing hot cache hits.
type bboltDNSCacheStorage struct {
	manager *bboltKVManager
	name    string
	invalid bool

	mu      sync.Mutex
	seq     uint64
	entries map[string]dnsMemoryCacheEntry
}

type dnsMemoryCacheEntry struct {
	msg      *gdns.Message
	expires  time.Time
	raw      []byte
	lastUsed uint64
}

type dnsStoredCacheEntry struct {
	msg     *gdns.Message
	expires time.Time
}

type dnsJSONCacheEntry struct {
	ExpiresAt string         `json:"expires_at,omitempty"`
	Message   dnsJSONMessage `json:"message,omitempty"`
}

type dnsJSONMessage struct {
	ID       uint16 `json:"id,omitempty"`
	Response bool   `json:"response,omitempty"`

	Opcode uint8 `json:"opcode,omitempty"`
	RCode  uint8 `json:"rcode,omitempty"`

	Authoritative      bool `json:"authoritative,omitempty"`
	Truncated          bool `json:"truncated,omitempty"`
	RecursionDesired   bool `json:"recursion_desired,omitempty"`
	RecursionAvailable bool `json:"recursion_available,omitempty"`
	AuthenticatedData  bool `json:"authenticated_data,omitempty"`
	CheckingDisabled   bool `json:"checking_disabled,omitempty"`

	Questions   []dnsJSONQuestion `json:"questions,omitempty"`
	Answers     []dnsJSONResource `json:"answers,omitempty"`
	Authorities []dnsJSONResource `json:"authorities,omitempty"`
	Additionals []dnsJSONResource `json:"additionals,omitempty"`
}

type dnsJSONQuestion struct {
	Name  string `json:"name,omitempty"`
	Type  uint16 `json:"type,omitempty"`
	Class uint16 `json:"class,omitempty"`
}

type dnsJSONResource struct {
	Name  string `json:"name,omitempty"`
	Type  uint16 `json:"type,omitempty"`
	Class uint16 `json:"class,omitempty"`
	TTL   uint32 `json:"ttl,omitempty"`
	Data  []byte `json:"data,omitempty"`
}

func (m *bboltKVManager) Storage(name string) gdns.CacheStorage {
	golog.Debugf("DNS cache storage: access requested name=%q", name)
	return m.dnsStorage(name)
}

// StorageNames returns known DNS cache storage buckets for menu selectors. It
// never creates buckets and returns nil when the database cannot be read.
func (m *bboltKVManager) StorageNames() []string {
	if m == nil || m.db == nil {
		return nil
	}
	var names []string
	if err := m.db.View(func(tx *bbolt.Tx) error {
		root := tx.Bucket([]byte(dnsRootBucketName))
		if root == nil {
			return nil
		}
		return root.ForEachBucket(func(name []byte) error {
			names = append(names, string(name))
			return nil
		})
	}); err != nil {
		return nil
	}
	sort.Strings(names)
	return names
}

func (m *bboltKVManager) dnsStorage(name string) *bboltDNSCacheStorage {
	invalid := m == nil || !validDNSName(name)
	if !invalid {
		m.dnsMu.Lock()
		if m.dnsStores == nil {
			m.dnsStores = map[string]*bboltDNSCacheStorage{}
		}
		if storage := m.dnsStores[name]; storage != nil {
			m.dnsMu.Unlock()
			golog.Debugf("DNS cache storage: reusing storage name=%q", name)
			return storage
		}
		storage := newBboltDNSCacheStorage(m, name, false)
		m.dnsStores[name] = storage
		m.dnsMu.Unlock()

		created, err := m.ensureDNSCacheBucket(name)
		if err == nil && created {
			golog.Debugf("DNS cache storage: created bucket name=%q", name)
			m.notifyDNSChange(dnsCacheChange{kind: dnsCacheChangeCacheUpsert, cache: name})
		} else if err == nil {
			golog.Debugf("DNS cache storage: opened existing bucket name=%q", name)
		} else {
			golog.Debugf("DNS cache storage: bucket access failed name=%q error=%v", name, err)
		}
		return storage
	}
	golog.Debugf("DNS cache storage: invalid access name=%q", name)
	return newBboltDNSCacheStorage(m, name, true)
}

func newBboltDNSCacheStorage(manager *bboltKVManager, name string, invalid bool) *bboltDNSCacheStorage {
	return &bboltDNSCacheStorage{
		manager: manager,
		name:    name,
		invalid: invalid,
		entries: map[string]dnsMemoryCacheEntry{},
	}
}

// Get returns a copy of the stored DNS message when the entry exists and has not
// expired. Expired entries are deleted from both memory and bbolt before a miss
// is reported, matching gonnect/dns.MemoryStorage expiration semantics. When
// the manager disables DNS cache expiration, past expiration timestamps are kept
// as metadata only and do not turn entries into misses.
func (s *bboltDNSCacheStorage) Get(key string, now time.Time) (*gdns.Message, bool) {
	msg, _, found := s.getWithExpiration(key, now)
	return msg, found
}

// getWithExpiration is the bbolt storage's internal read contract for the
// isolation-side memory middleware. The public gonnect CacheStorage contract
// does not expose the durable expiration time. Returning it here prevents a
// read-through layer from incorrectly restarting a persisted entry's TTL.
func (s *bboltDNSCacheStorage) getWithExpiration(key string, now time.Time) (*gdns.Message, time.Time, bool) {
	if s == nil || s.invalid || s.manager == nil || !validDNSName(key) {
		return nil, time.Time{}, false
	}
	msg, expires, raw, found, expired := s.memoryGet(key, now)
	if found && !expired {
		golog.Debugf("DNS cache storage: memory hit cache=%q key=%q", s.name, key)
		return msg, expires, true
	}
	if found && expired {
		golog.Debugf("DNS cache storage: expired memory entry cache=%q key=%q", s.name, key)
		s.deleteExpiredEntry(key, raw)
		return nil, time.Time{}, false
	}

	raw, found, err := s.manager.getDNSCacheEntryRaw(s.name, key)
	if err != nil || !found {
		if err != nil {
			golog.Debugf("DNS cache storage: read failed cache=%q key=%q error=%v", s.name, key, err)
		} else {
			golog.Debugf("DNS cache storage: miss cache=%q key=%q", s.name, key)
		}
		return nil, time.Time{}, false
	}
	entry, err := unmarshalDNSCacheEntry(raw)
	if err != nil {
		golog.Debugf("DNS cache storage: invalid stored entry cache=%q key=%q error=%v", s.name, key, err)
		s.deleteExpiredEntry(key, raw)
		return nil, time.Time{}, false
	}
	if s.dnsCacheEntryExpired(entry.expires, now) {
		golog.Debugf("DNS cache storage: expired stored entry cache=%q key=%q", s.name, key)
		s.deleteExpiredEntry(key, raw)
		return nil, time.Time{}, false
	}
	golog.Debugf("DNS cache storage: bbolt hit cache=%q key=%q", s.name, key)
	s.memoryPut(key, entry.msg, entry.expires, raw)
	return entry.msg.Copy(), entry.expires, true
}

// Set persists successful DNS responses with a non-zero answer TTL. The JSON
// value is deliberately readable: zero-value message fields are omitted and
// resource Data uses JSON's standard base64 encoding for byte slices.
func (s *bboltDNSCacheStorage) Set(key string, msg *gdns.Message, now time.Time) {
	if s == nil || s.invalid || s.manager == nil || msg == nil || !msg.Response || msg.RCode != gdns.RCodeSuccess {
		return
	}
	if key == "" {
		key = dnsStorageCacheKey(msg)
	}
	if !validDNSName(key) {
		return
	}
	ttl := dnsMessageMinTTL(msg)
	if ttl == 0 {
		return
	}
	expires := now.Add(time.Duration(ttl) * time.Second)
	raw, err := marshalDNSCacheEntry(dnsStoredCacheEntry{
		msg:     msg.Copy(),
		expires: expires,
	})
	if err != nil {
		return
	}
	created, err := s.manager.putDNSCacheEntry(s.name, key, raw)
	if err != nil {
		golog.Debugf("DNS cache storage: write failed cache=%q key=%q error=%v", s.name, key, err)
		return
	}
	golog.Debugf("DNS cache storage: wrote entry cache=%q key=%q created_cache=%t", s.name, key, created)
	s.memoryPut(key, msg, expires, raw)
	changes := []dnsCacheChange{newDNSKeyUpsertChange(s.name, key, raw, expires)}
	if created {
		changes = append([]dnsCacheChange{{kind: dnsCacheChangeCacheUpsert, cache: s.name}}, changes...)
	}
	s.manager.notifyDNSChange(changes...)
}

func (s *bboltDNSCacheStorage) Delete(key string) {
	if s == nil || s.invalid || s.manager == nil || !validDNSName(key) {
		return
	}
	s.memoryDelete(key)
	deleted, err := s.manager.deleteDNSCacheEntry(s.name, key, nil)
	if err == nil && deleted {
		golog.Debugf("DNS cache storage: deleted entry cache=%q key=%q", s.name, key)
		s.manager.notifyDNSChange(dnsCacheChange{kind: dnsCacheChangeKeyDelete, cache: s.name, key: key})
	} else if err != nil {
		golog.Debugf("DNS cache storage: delete failed cache=%q key=%q error=%v", s.name, key, err)
	}
}

func (s *bboltDNSCacheStorage) memoryGet(key string, now time.Time) (*gdns.Message, time.Time, []byte, bool, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.entries[key]
	if !ok {
		return nil, time.Time{}, nil, false, false
	}
	if s.dnsCacheEntryExpired(entry.expires, now) {
		delete(s.entries, key)
		return nil, entry.expires, append([]byte(nil), entry.raw...), true, true
	}
	s.seq++
	entry.lastUsed = s.seq
	s.entries[key] = entry
	return entry.msg.Copy(), entry.expires, nil, true, false
}

func (s *bboltDNSCacheStorage) memoryPut(key string, msg *gdns.Message, expires time.Time, raw []byte) {
	if msg == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.entries == nil {
		s.entries = map[string]dnsMemoryCacheEntry{}
	}
	s.seq++
	s.entries[key] = dnsMemoryCacheEntry{
		msg:      msg.Copy(),
		expires:  expires,
		raw:      append([]byte(nil), raw...),
		lastUsed: s.seq,
	}
	s.evictMemoryLocked()
}

func (s *bboltDNSCacheStorage) memoryDelete(key string) {
	s.mu.Lock()
	delete(s.entries, key)
	s.mu.Unlock()
}

func (s *bboltDNSCacheStorage) memoryClear() {
	s.mu.Lock()
	s.entries = map[string]dnsMemoryCacheEntry{}
	s.mu.Unlock()
}

func (s *bboltDNSCacheStorage) evictMemoryLocked() {
	for len(s.entries) > dnsMemoryCacheMaxEntries {
		var oldestKey string
		var oldestSeq uint64
		first := true
		for key, entry := range s.entries {
			if first || entry.lastUsed < oldestSeq {
				first = false
				oldestKey = key
				oldestSeq = entry.lastUsed
			}
		}
		delete(s.entries, oldestKey)
	}
}

func (s *bboltDNSCacheStorage) deleteExpiredEntry(key string, raw []byte) {
	s.memoryDelete(key)
	deleted, err := s.manager.deleteDNSCacheEntry(s.name, key, raw)
	if err == nil && deleted {
		s.manager.notifyDNSChange(dnsCacheChange{kind: dnsCacheChangeKeyDelete, cache: s.name, key: key})
	}
}

func (m *bboltKVManager) ensureDNSCacheBucket(name string) (bool, error) {
	if m == nil || !validDNSName(name) {
		return false, errDNSInvalidName
	}
	created := false
	err := m.updateBackground(func(tx *bbolt.Tx) error {
		root, err := tx.CreateBucketIfNotExists([]byte(dnsRootBucketName))
		if err != nil {
			return err
		}
		if root.Bucket([]byte(name)) != nil {
			return nil
		}
		if _, err := root.CreateBucket([]byte(name)); err != nil {
			return err
		}
		created = true
		return nil
	})
	return created, mapBboltDNSError(err)
}

func (m *bboltKVManager) putDNSCacheEntry(cache, key string, raw []byte) (bool, error) {
	if m == nil || !validDNSName(cache) || !validDNSName(key) {
		return false, errDNSInvalidName
	}
	created := false
	err := m.updateBackground(func(tx *bbolt.Tx) error {
		root, err := tx.CreateBucketIfNotExists([]byte(dnsRootBucketName))
		if err != nil {
			return err
		}
		bucket := root.Bucket([]byte(cache))
		if bucket == nil {
			bucket, err = root.CreateBucket([]byte(cache))
			if err != nil {
				return err
			}
			created = true
		}
		return bucket.Put([]byte(key), raw)
	})
	return created, mapBboltDNSError(err)
}

func (m *bboltKVManager) getDNSCacheEntryRaw(cache, key string) ([]byte, bool, error) {
	if m == nil || !validDNSName(cache) || !validDNSName(key) {
		return nil, false, errDNSInvalidName
	}
	var raw []byte
	err := m.db.View(func(tx *bbolt.Tx) error {
		bucket := dnsCacheBucket(tx, cache)
		if bucket == nil {
			return nil
		}
		value := bucket.Get([]byte(key))
		if value == nil {
			return nil
		}
		raw = append([]byte(nil), value...)
		return nil
	})
	if err != nil {
		return nil, false, mapBboltDNSError(err)
	}
	return raw, raw != nil, nil
}

func (m *bboltKVManager) deleteDNSCacheEntry(cache, key string, expected []byte) (bool, error) {
	if m == nil || !validDNSName(cache) || !validDNSName(key) {
		return false, errDNSInvalidName
	}
	deleted := false
	err := m.updateBackground(func(tx *bbolt.Tx) error {
		bucket := dnsCacheBucket(tx, cache)
		if bucket == nil {
			return nil
		}
		current := bucket.Get([]byte(key))
		if current == nil {
			return nil
		}
		if expected != nil && !bytes.Equal(current, expected) {
			return nil
		}
		if err := bucket.Delete([]byte(key)); err != nil {
			return err
		}
		deleted = true
		return nil
	})
	return deleted, mapBboltDNSError(err)
}

func (m *bboltKVManager) cleanupExpiredDNS(now time.Time) error {
	if m == nil {
		return nil
	}
	if m.disableDNSCacheExpiration {
		return nil
	}
	return mapBboltDNSError(m.updateBackground(func(tx *bbolt.Tx) error {
		root := tx.Bucket([]byte(dnsRootBucketName))
		if root == nil {
			return errNoDatabaseChanges
		}
		changed := false
		err := root.ForEachBucket(func(cacheName []byte) error {
			bucket := root.Bucket(cacheName)
			if bucket == nil {
				return nil
			}
			var expired [][]byte
			if err := bucket.ForEach(func(k, v []byte) error {
				entry, err := unmarshalDNSCacheEntry(v)
				if err != nil {
					return nil
				}
				if dnsCacheEntryExpired(entry.expires, now) {
					expired = append(expired, append([]byte(nil), k...))
				}
				return nil
			}); err != nil {
				return err
			}
			for _, key := range expired {
				if err := bucket.Delete(key); err != nil {
					return err
				}
				changed = true
			}
			return nil
		})
		if err == nil && !changed {
			return errNoDatabaseChanges
		}
		return err
	}))
}

func (s *bboltDNSCacheStorage) dnsCacheEntryExpired(expires, now time.Time) bool {
	if s != nil && s.manager != nil && s.manager.disableDNSCacheExpiration {
		return false
	}
	return dnsCacheEntryExpired(expires, now)
}

func (m *bboltKVManager) dnsCacheEntryExpired(expires, now time.Time) bool {
	if m != nil && m.disableDNSCacheExpiration {
		return false
	}
	return dnsCacheEntryExpired(expires, now)
}

func (m *bboltKVManager) subscribeDNSChanges(callback func(dnsCacheChange)) uint64 {
	m.dnsMu.Lock()
	defer m.dnsMu.Unlock()
	m.dnsNextSub++
	id := m.dnsNextSub
	if m.dnsChangeSubs == nil {
		m.dnsChangeSubs = map[uint64]func(dnsCacheChange){}
	}
	m.dnsChangeSubs[id] = callback
	return id
}

func (m *bboltKVManager) unsubscribeDNSChanges(id uint64) {
	m.dnsMu.Lock()
	defer m.dnsMu.Unlock()
	delete(m.dnsChangeSubs, id)
}

func (m *bboltKVManager) notifyDNSChange(changes ...dnsCacheChange) {
	if m == nil || len(changes) == 0 {
		return
	}
	m.dnsMu.Lock()
	callbacks := make([]func(dnsCacheChange), 0, len(m.dnsChangeSubs))
	for _, callback := range m.dnsChangeSubs {
		callbacks = append(callbacks, callback)
	}
	m.dnsMu.Unlock()

	for _, change := range changes {
		for _, callback := range callbacks {
			callback(change)
		}
	}
}

func dnsCacheBucket(tx *bbolt.Tx, cache string) *bbolt.Bucket {
	root := tx.Bucket([]byte(dnsRootBucketName))
	if root == nil {
		return nil
	}
	return root.Bucket([]byte(cache))
}

func marshalDNSCacheEntry(entry dnsStoredCacheEntry) ([]byte, error) {
	if entry.msg == nil || entry.expires.IsZero() {
		return nil, errDNSInvalidValue
	}
	return json.Marshal(dnsJSONCacheEntry{
		ExpiresAt: entry.expires.UTC().Format(time.RFC3339Nano),
		Message:   dnsMessageToJSON(entry.msg),
	})
}

func unmarshalDNSCacheEntry(raw []byte) (dnsStoredCacheEntry, error) {
	var entry dnsJSONCacheEntry
	if err := json.Unmarshal(raw, &entry); err != nil {
		return dnsStoredCacheEntry{}, fmt.Errorf("%w: invalid json: %v", errDNSInvalidValue, err)
	}
	expires, err := time.Parse(time.RFC3339Nano, entry.ExpiresAt)
	if err != nil {
		return dnsStoredCacheEntry{}, fmt.Errorf("%w: invalid expiration: %v", errDNSInvalidValue, err)
	}
	return dnsStoredCacheEntry{
		msg:     dnsMessageFromJSON(entry.Message),
		expires: expires,
	}, nil
}

func dnsMessageToJSON(msg *gdns.Message) dnsJSONMessage {
	if msg == nil {
		return dnsJSONMessage{}
	}
	return dnsJSONMessage{
		ID:                 msg.ID,
		Response:           msg.Response,
		Opcode:             msg.Opcode,
		RCode:              msg.RCode,
		Authoritative:      msg.Authoritative,
		Truncated:          msg.Truncated,
		RecursionDesired:   msg.RecursionDesired,
		RecursionAvailable: msg.RecursionAvailable,
		AuthenticatedData:  msg.AuthenticatedData,
		CheckingDisabled:   msg.CheckingDisabled,
		Questions:          dnsQuestionsToJSON(msg.Questions),
		Answers:            dnsResourcesToJSON(msg.Answers),
		Authorities:        dnsResourcesToJSON(msg.Authorities),
		Additionals:        dnsResourcesToJSON(msg.Additionals),
	}
}

func dnsMessageFromJSON(msg dnsJSONMessage) *gdns.Message {
	return &gdns.Message{
		ID:                 msg.ID,
		Response:           msg.Response,
		Opcode:             msg.Opcode,
		RCode:              msg.RCode,
		Authoritative:      msg.Authoritative,
		Truncated:          msg.Truncated,
		RecursionDesired:   msg.RecursionDesired,
		RecursionAvailable: msg.RecursionAvailable,
		AuthenticatedData:  msg.AuthenticatedData,
		CheckingDisabled:   msg.CheckingDisabled,
		Questions:          dnsQuestionsFromJSON(msg.Questions),
		Answers:            dnsResourcesFromJSON(msg.Answers),
		Authorities:        dnsResourcesFromJSON(msg.Authorities),
		Additionals:        dnsResourcesFromJSON(msg.Additionals),
	}
}

func dnsQuestionsToJSON(in []gdns.Question) []dnsJSONQuestion {
	if len(in) == 0 {
		return nil
	}
	out := make([]dnsJSONQuestion, len(in))
	for i, q := range in {
		out[i] = dnsJSONQuestion{Name: q.Name, Type: q.Type, Class: q.Class}
	}
	return out
}

func dnsQuestionsFromJSON(in []dnsJSONQuestion) []gdns.Question {
	if len(in) == 0 {
		return nil
	}
	out := make([]gdns.Question, len(in))
	for i, q := range in {
		out[i] = gdns.Question{Name: q.Name, Type: q.Type, Class: q.Class}
	}
	return out
}

func dnsResourcesToJSON(in []gdns.Resource) []dnsJSONResource {
	if len(in) == 0 {
		return nil
	}
	out := make([]dnsJSONResource, len(in))
	for i, rr := range in {
		out[i] = dnsJSONResource{
			Name:  rr.Name,
			Type:  rr.Type,
			Class: rr.Class,
			TTL:   rr.TTL,
			Data:  append([]byte(nil), rr.Data...),
		}
	}
	return out
}

func dnsResourcesFromJSON(in []dnsJSONResource) []gdns.Resource {
	if len(in) == 0 {
		return nil
	}
	out := make([]gdns.Resource, len(in))
	for i, rr := range in {
		out[i] = gdns.Resource{
			Name:  rr.Name,
			Type:  rr.Type,
			Class: rr.Class,
			TTL:   rr.TTL,
			Data:  append([]byte(nil), rr.Data...),
		}
	}
	return out
}

func dnsMessageMinTTL(msg *gdns.Message) uint32 {
	if msg == nil {
		return 0
	}
	var ttl uint32
	for _, rr := range msg.Answers {
		if ttl == 0 || rr.TTL < ttl {
			ttl = rr.TTL
		}
	}
	return ttl
}

func dnsStorageCacheKey(msg *gdns.Message) string {
	if msg == nil || len(msg.Questions) == 0 {
		return ""
	}
	q := msg.Questions[0]
	return fmt.Sprintf("%s|%d|%d", dnsAbsName(q.Name), q.Type, q.Class)
}

func dnsAbsName(name string) string {
	if name == "" || name == "." {
		return "."
	}
	if strings.HasSuffix(name, ".") {
		return name
	}
	return name + "."
}

func dnsCacheEntryExpired(expires, now time.Time) bool {
	return !expires.IsZero() && !now.Before(expires)
}

func validDNSName(name string) bool {
	return name != "" && utf8.ValidString(name)
}

func mapBboltDNSError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, bberrors.ErrDatabaseNotOpen), errors.Is(err, bberrors.ErrTxClosed):
		return errDNSClosed
	case errors.Is(err, bberrors.ErrBucketNameRequired), errors.Is(err, bberrors.ErrKeyRequired), errors.Is(err, bberrors.ErrKeyTooLarge):
		return errDNSInvalidName
	case errors.Is(err, bberrors.ErrValueTooLarge), errors.Is(err, bberrors.ErrIncompatibleValue):
		return errDNSInvalidValue
	default:
		return err
	}
}
