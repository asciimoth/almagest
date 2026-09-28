package main

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/asciimoth/almagest/bin/api"
	gdns "github.com/asciimoth/gonnect/dns"
	"go.etcd.io/bbolt"
)

type dnsSubscriptionKind string

const (
	dnsSubscriptionCaches dnsSubscriptionKind = "caches"
	dnsSubscriptionKeys   dnsSubscriptionKind = "keys"
	dnsSubscriptionValue  dnsSubscriptionKind = "value"
)

var errDNSInvalidRecord = errors.New("dns cache: invalid record")

type dnsSubscription struct {
	id    uint64
	kind  dnsSubscriptionKind
	cache string
	key   string
}

// handleDNSMessage mirrors the KV admin surface's subscription shape, but DNS
// update events are incremental: cache subscribers receive one cache change, key
// subscribers one key change, and value subscribers one value state change.
func (s *apiSession) handleDNSMessage(msg api.Message) (api.Message, error) {
	manager := s.admin.dns
	if manager == nil {
		return newRequestError(msg.ID, api.ErrorUnavailable, "dns cache database is unavailable")
	}

	switch api.DNSMethod(msg.Method) {
	case api.MethodDNSCachesSnapshot:
		snapshot, err := manager.dnsCachesSnapshot()
		if err != nil {
			return dnsRequestError(msg.ID, err)
		}
		return api.NewResponse(msg.ID, snapshot)

	case api.MethodDNSSubscribeCaches:
		snapshot, err := manager.dnsCachesSnapshot()
		if err != nil {
			return dnsRequestError(msg.ID, err)
		}
		id := s.addDNSSubscription(dnsSubscription{kind: dnsSubscriptionCaches})
		return api.NewResponse(msg.ID, api.DNSSubscribeCachesResponse{
			SubscriptionID: id,
			Snapshot:       snapshot,
		})

	case api.MethodDNSKeysSnapshot:
		req, err := decodePayload[api.DNSKeysRequest](msg)
		if err != nil {
			return newRequestError(msg.ID, api.ErrorInvalidPayload, err.Error())
		}
		snapshot, err := manager.dnsKeysSnapshot(req.Cache)
		if err != nil {
			return dnsRequestError(msg.ID, err)
		}
		return api.NewResponse(msg.ID, snapshot)

	case api.MethodDNSSubscribeKeys:
		req, err := decodePayload[api.DNSKeysRequest](msg)
		if err != nil {
			return newRequestError(msg.ID, api.ErrorInvalidPayload, err.Error())
		}
		if !validDNSName(req.Cache) {
			return api.NewResponse(msg.ID, api.DNSSubscribeKeysResponse{})
		}
		snapshot, err := manager.dnsKeysSnapshot(req.Cache)
		if err != nil {
			return api.NewResponse(msg.ID, api.DNSSubscribeKeysResponse{})
		}
		id := s.addDNSSubscription(dnsSubscription{
			kind:  dnsSubscriptionKeys,
			cache: req.Cache,
		})
		return api.NewResponse(msg.ID, api.DNSSubscribeKeysResponse{
			SubscriptionID: id,
			Snapshot:       snapshot,
		})

	case api.MethodDNSValueSnapshot:
		req, err := decodePayload[api.DNSValueRequest](msg)
		if err != nil {
			return newRequestError(msg.ID, api.ErrorInvalidPayload, err.Error())
		}
		snapshot, err := manager.dnsValueSnapshot(req.Cache, req.Key)
		if err != nil {
			return dnsRequestError(msg.ID, err)
		}
		return api.NewResponse(msg.ID, snapshot)

	case api.MethodDNSSubscribeValue:
		req, err := decodePayload[api.DNSValueRequest](msg)
		if err != nil {
			return newRequestError(msg.ID, api.ErrorInvalidPayload, err.Error())
		}
		if !validDNSName(req.Cache) || !validDNSName(req.Key) {
			return api.NewResponse(msg.ID, api.DNSSubscribeValueResponse{})
		}
		snapshot, err := manager.dnsValueSnapshot(req.Cache, req.Key)
		if err != nil {
			return api.NewResponse(msg.ID, api.DNSSubscribeValueResponse{})
		}
		id := s.addDNSSubscription(dnsSubscription{
			kind:  dnsSubscriptionValue,
			cache: req.Cache,
			key:   req.Key,
		})
		return api.NewResponse(msg.ID, api.DNSSubscribeValueResponse{
			SubscriptionID: id,
			Snapshot:       snapshot,
		})

	case api.MethodDNSUnsubscribe:
		req, err := decodePayload[api.DNSSubscriptionRequest](msg)
		if err != nil {
			return newRequestError(msg.ID, api.ErrorInvalidPayload, err.Error())
		}
		s.removeDNSSubscriptionTree(req.SubscriptionID)
		return api.NewResponse(msg.ID, struct{}{})

	case api.MethodDNSSet:
		req, err := decodePayload[api.DNSSetRequest](msg)
		if err != nil {
			return newRequestError(msg.ID, api.ErrorInvalidPayload, err.Error())
		}
		key, err := manager.setDNSRecord(req.Cache, req.Key, req.Record)
		if err != nil {
			return dnsRequestError(msg.ID, err)
		}
		snapshot, err := manager.dnsValueSnapshot(req.Cache, key)
		if err != nil {
			return dnsRequestError(msg.ID, err)
		}
		return api.NewResponse(msg.ID, api.DNSSetResponse{Key: key, Snapshot: snapshot})

	case api.MethodDNSDelete:
		req, err := decodePayload[api.DNSValueRequest](msg)
		if err != nil {
			return newRequestError(msg.ID, api.ErrorInvalidPayload, err.Error())
		}
		if err := manager.deleteDNSValue(req.Cache, req.Key); err != nil {
			return dnsRequestError(msg.ID, err)
		}
		return api.NewResponse(msg.ID, struct{}{})

	case api.MethodDNSDrop:
		req, err := decodePayload[api.DNSKeysRequest](msg)
		if err != nil {
			return newRequestError(msg.ID, api.ErrorInvalidPayload, err.Error())
		}
		if err := manager.dropDNSCache(req.Cache); err != nil {
			return dnsRequestError(msg.ID, err)
		}
		return api.NewResponse(msg.ID, struct{}{})

	default:
		return newRequestError(msg.ID, api.ErrorInvalidMethod, "api method is not implemented")
	}
}

func (m *bboltKVManager) dnsCachesSnapshot() (api.DNSCachesSnapshot, error) {
	var snapshot api.DNSCachesSnapshot
	err := mapBboltDNSError(m.db.View(func(tx *bbolt.Tx) error {
		root := tx.Bucket([]byte(dnsRootBucketName))
		if root == nil {
			return nil
		}
		return root.ForEachBucket(func(name []byte) error {
			snapshot.Caches = append(snapshot.Caches, api.DNSCacheInfo{Name: string(name)})
			return nil
		})
	}))
	if err != nil {
		return api.DNSCachesSnapshot{}, err
	}
	slices.SortFunc(snapshot.Caches, func(a, b api.DNSCacheInfo) int {
		return strings.Compare(a.Name, b.Name)
	})
	return snapshot, nil
}

func (m *bboltKVManager) dnsKeysSnapshot(cache string) (api.DNSKeysSnapshot, error) {
	if !validDNSName(cache) {
		return api.DNSKeysSnapshot{}, errDNSInvalidName
	}
	now := time.Now()
	var (
		snapshot api.DNSKeysSnapshot
		expired  []dnsExpiredAPIEntry
	)
	err := mapBboltDNSError(m.db.View(func(tx *bbolt.Tx) error {
		bucket := dnsCacheBucket(tx, cache)
		if bucket == nil {
			return nil
		}
		snapshot.Exists = true
		return bucket.ForEach(func(k, v []byte) error {
			key := string(k)
			entry, err := unmarshalDNSCacheEntry(v)
			if err != nil {
				return nil
			}
			if m.dnsCacheEntryExpired(entry.expires, now) {
				expired = append(expired, dnsExpiredAPIEntry{cache: cache, key: key, raw: append([]byte(nil), v...)})
				return nil
			}
			snapshot.Keys = append(snapshot.Keys, dnsKeyInfoFromRaw(key, v, entry))
			return nil
		})
	}))
	if err != nil {
		return api.DNSKeysSnapshot{}, err
	}
	slices.SortFunc(snapshot.Keys, func(a, b api.DNSKeyInfo) int {
		return strings.Compare(a.Key, b.Key)
	})
	m.deleteExpiredAPIEntries(expired)
	return snapshot, nil
}

func (m *bboltKVManager) dnsValueSnapshot(cache, key string) (api.DNSValueSnapshot, error) {
	if !validDNSName(cache) || !validDNSName(key) {
		return api.DNSValueSnapshot{}, errDNSInvalidName
	}
	raw, found, err := m.getDNSCacheEntryRaw(cache, key)
	if err != nil || !found {
		return api.DNSValueSnapshot{}, err
	}
	entry, err := unmarshalDNSCacheEntry(raw)
	if err != nil {
		return api.DNSValueSnapshot{}, err
	}
	if m.dnsCacheEntryExpired(entry.expires, time.Now()) {
		m.deleteExpiredAPIEntries([]dnsExpiredAPIEntry{{cache: cache, key: key, raw: raw}})
		return api.DNSValueSnapshot{}, nil
	}
	value := dnsValueFromRaw(raw, entry)
	return api.DNSValueSnapshot{
		Found: true,
		Value: &value,
	}, nil
}

func dnsKeyInfoFromRaw(key string, raw []byte, entry dnsStoredCacheEntry) api.DNSKeyInfo {
	return api.DNSKeyInfo{
		Key:       key,
		Size:      len(raw),
		ExpiresAt: entry.expires.UTC().Format(time.RFC3339Nano),
	}
}

func dnsValueFromRaw(raw []byte, entry dnsStoredCacheEntry) api.DNSValue {
	return api.DNSValue{
		JSON:      string(raw),
		Size:      len(raw),
		ExpiresAt: entry.expires.UTC().Format(time.RFC3339Nano),
	}
}

func (m *bboltKVManager) setDNSRecord(cache, previousKey string, record api.DNSRecordInput) (string, error) {
	if !validDNSName(cache) || (previousKey != "" && !validDNSName(previousKey)) {
		return "", errDNSInvalidName
	}
	entry, key, err := dnsStoredEntryFromAPI(record, time.Now())
	if err != nil {
		return "", err
	}
	raw, err := marshalDNSCacheEntry(entry)
	if err != nil {
		return "", err
	}
	created := false
	deletedPrevious := false
	err = mapBboltDNSError(m.db.Update(func(tx *bbolt.Tx) error {
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
		if previousKey != "" && previousKey != key && bucket.Get([]byte(previousKey)) != nil {
			if err := bucket.Delete([]byte(previousKey)); err != nil {
				return err
			}
			deletedPrevious = true
		}
		return bucket.Put([]byte(key), raw)
	}))
	if err != nil {
		return "", err
	}
	if storage := m.lookupDNSStorage(cache); storage != nil {
		if deletedPrevious {
			storage.memoryDelete(previousKey)
		}
		storage.memoryPut(key, entry.msg, entry.expires, raw)
	}
	changes := make([]dnsCacheChange, 0, 3)
	if created {
		changes = append(changes, dnsCacheChange{kind: dnsCacheChangeCacheUpsert, cache: cache})
	}
	if deletedPrevious {
		changes = append(changes, dnsCacheChange{kind: dnsCacheChangeKeyDelete, cache: cache, key: previousKey})
	}
	changes = append(changes, newDNSKeyUpsertChange(cache, key, raw, entry.expires))
	m.notifyDNSChange(changes...)
	return key, nil
}

func (m *bboltKVManager) deleteDNSValue(cache, key string) error {
	if !validDNSName(cache) || !validDNSName(key) {
		return errDNSInvalidName
	}
	deleted, err := m.deleteDNSCacheEntry(cache, key, nil)
	if err != nil {
		return err
	}
	if !deleted {
		return nil
	}
	if storage := m.lookupDNSStorage(cache); storage != nil {
		storage.memoryDelete(key)
	}
	m.notifyDNSChange(dnsCacheChange{kind: dnsCacheChangeKeyDelete, cache: cache, key: key})
	return nil
}

func (m *bboltKVManager) dropDNSCache(cache string) error {
	if !validDNSName(cache) {
		return errDNSInvalidName
	}
	var keys []string
	dropped := false
	err := mapBboltDNSError(m.db.Update(func(tx *bbolt.Tx) error {
		root := tx.Bucket([]byte(dnsRootBucketName))
		if root == nil {
			return nil
		}
		bucket := root.Bucket([]byte(cache))
		if bucket == nil {
			return nil
		}
		if err := bucket.ForEach(func(k, _ []byte) error {
			keys = append(keys, string(k))
			return nil
		}); err != nil {
			return err
		}
		if err := root.DeleteBucket([]byte(cache)); err != nil {
			return err
		}
		dropped = true
		return nil
	}))
	if err != nil || !dropped {
		return err
	}
	if storage := m.lookupDNSStorage(cache); storage != nil {
		storage.memoryClear()
	}
	changes := make([]dnsCacheChange, 0, len(keys)+1)
	for _, key := range keys {
		changes = append(changes, dnsCacheChange{kind: dnsCacheChangeKeyDelete, cache: cache, key: key})
	}
	changes = append(changes, dnsCacheChange{kind: dnsCacheChangeCacheDelete, cache: cache})
	m.notifyDNSChange(changes...)
	return nil
}

func dnsStoredEntryFromAPI(record api.DNSRecordInput, now time.Time) (dnsStoredCacheEntry, string, error) {
	typ, err := dnsTypeFromAPI(record.Type)
	if err != nil {
		return dnsStoredCacheEntry{}, "", err
	}
	cls, err := dnsClassFromAPI(record.Class)
	if err != nil {
		return dnsStoredCacheEntry{}, "", err
	}
	if record.TTL == 0 {
		return dnsStoredCacheEntry{}, "", fmt.Errorf("%w: ttl must be greater than zero", errDNSInvalidRecord)
	}
	nameText := strings.TrimSpace(record.Name)
	if nameText == "" {
		return dnsStoredCacheEntry{}, "", errDNSInvalidName
	}
	name := dnsAbsName(nameText)
	if !validDNSRecordName(name) {
		return dnsStoredCacheEntry{}, "", errDNSInvalidName
	}
	data, err := dnsRecordDataFromAPI(typ, record.Value)
	if err != nil {
		return dnsStoredCacheEntry{}, "", err
	}
	question := gdns.Question{Name: name, Type: typ, Class: cls}
	msg := &gdns.Message{
		Response:           true,
		Opcode:             gdns.OpcodeQuery,
		RCode:              gdns.RCodeSuccess,
		Authoritative:      true,
		RecursionDesired:   true,
		RecursionAvailable: true,
		Questions:          []gdns.Question{question},
		Answers: []gdns.Resource{{
			Name:  name,
			Type:  typ,
			Class: cls,
			TTL:   record.TTL,
			Data:  data,
		}},
	}
	key := dnsStorageCacheKey(msg)
	if !validDNSName(key) {
		return dnsStoredCacheEntry{}, "", errDNSInvalidName
	}
	return dnsStoredCacheEntry{
		msg:     msg,
		expires: now.Add(time.Duration(record.TTL) * time.Second),
	}, key, nil
}

func dnsRecordDataFromAPI(typ uint16, value string) ([]byte, error) {
	value = strings.TrimSpace(value)
	switch typ {
	case gdns.TypeA:
		addr, err := netip.ParseAddr(value)
		if err != nil || !addr.Is4() {
			return nil, fmt.Errorf("%w: A value must be an IPv4 address", errDNSInvalidRecord)
		}
		return append([]byte(nil), addr.AsSlice()...), nil
	case gdns.TypeAAAA:
		addr, err := netip.ParseAddr(value)
		if err != nil || !addr.Is6() || addr.Is4() {
			return nil, fmt.Errorf("%w: AAAA value must be an IPv6 address", errDNSInvalidRecord)
		}
		return append([]byte(nil), addr.AsSlice()...), nil
	case gdns.TypeCNAME, gdns.TypeNS, gdns.TypePTR:
		if value == "" {
			return nil, errDNSInvalidName
		}
		name := dnsAbsName(value)
		if !validDNSRecordName(name) {
			return nil, errDNSInvalidName
		}
		return []byte(name), nil
	case gdns.TypeTXT:
		return []byte(value), nil
	case gdns.TypeMX:
		fields := strings.Fields(value)
		if len(fields) != 2 {
			return nil, fmt.Errorf("%w: MX value must be PREF HOST", errDNSInvalidRecord)
		}
		pref, err := parseDNSUint16API(fields[0], "MX preference")
		if err != nil {
			return nil, err
		}
		host := dnsAbsName(fields[1])
		if !validDNSRecordName(host) {
			return nil, errDNSInvalidName
		}
		data := make([]byte, 2, 2+len(host))
		binary.BigEndian.PutUint16(data, pref)
		return append(data, host...), nil
	case gdns.TypeSRV:
		fields := strings.Fields(value)
		if len(fields) != 4 {
			return nil, fmt.Errorf("%w: SRV value must be PRIORITY WEIGHT PORT TARGET", errDNSInvalidRecord)
		}
		priority, err := parseDNSUint16API(fields[0], "SRV priority")
		if err != nil {
			return nil, err
		}
		weight, err := parseDNSUint16API(fields[1], "SRV weight")
		if err != nil {
			return nil, err
		}
		port, err := parseDNSUint16API(fields[2], "SRV port")
		if err != nil {
			return nil, err
		}
		target := dnsAbsName(fields[3])
		if !validDNSRecordName(target) {
			return nil, errDNSInvalidName
		}
		data := make([]byte, 6, 6+len(target))
		binary.BigEndian.PutUint16(data[0:2], priority)
		binary.BigEndian.PutUint16(data[2:4], weight)
		binary.BigEndian.PutUint16(data[4:6], port)
		return append(data, target...), nil
	default:
		return nil, fmt.Errorf("%w: unsupported DNS type", errDNSInvalidRecord)
	}
}

// validDNSRecordName validates the unescaped text form accepted by the admin
// API. It permits general DNS labels, including underscores and wildcard
// labels. It rejects whitespace, control bytes, non-ASCII bytes, and
// backslashes because this API does not implement presentation-format
// escaping. The canonical trailing dot is included in the 254-byte text limit,
// which maps to the DNS wire-format limit of 255 bytes after label lengths are
// encoded.
func validDNSRecordName(name string) bool {
	if name == "" {
		return false
	}
	name = dnsAbsName(name)
	if name == "." {
		return true
	}
	if len(name) > 254 {
		return false
	}
	labelLength := 0
	for i := range len(name) {
		value := name[i]
		if value == '.' {
			if labelLength == 0 || labelLength > 63 {
				return false
			}
			labelLength = 0
			continue
		}
		if value <= ' ' || value >= 0x7f || value == '\\' {
			return false
		}
		labelLength++
	}
	return labelLength == 0
}

func dnsTypeFromAPI(value string) (uint16, error) {
	switch strings.ToUpper(strings.TrimSpace(value)) {
	case "A":
		return gdns.TypeA, nil
	case "AAAA", "AAA":
		return gdns.TypeAAAA, nil
	case "CNAME":
		return gdns.TypeCNAME, nil
	case "NS":
		return gdns.TypeNS, nil
	case "PTR":
		return gdns.TypePTR, nil
	case "MX":
		return gdns.TypeMX, nil
	case "TXT":
		return gdns.TypeTXT, nil
	case "SRV":
		return gdns.TypeSRV, nil
	default:
		return 0, fmt.Errorf("%w: unsupported DNS type %q", errDNSInvalidRecord, value)
	}
}

func dnsClassFromAPI(value string) (uint16, error) {
	switch strings.ToUpper(strings.TrimSpace(value)) {
	case "", "IN":
		return gdns.ClassIN, nil
	case "CH":
		return 3, nil
	case "HS":
		return 4, nil
	default:
		n, err := strconv.Atoi(value)
		if err != nil || n < 0 || n > 65535 {
			return 0, fmt.Errorf("%w: unsupported DNS class %q", errDNSInvalidRecord, value)
		}
		return uint16(n), nil
	}
}

func parseDNSUint16API(value, label string) (uint16, error) {
	n, err := strconv.Atoi(value)
	if err != nil || n < 0 || n > 65535 {
		return 0, fmt.Errorf("%w: %s must be between 0 and 65535", errDNSInvalidRecord, label)
	}
	return uint16(n), nil
}

type dnsExpiredAPIEntry struct {
	cache string
	key   string
	raw   []byte
}

func (m *bboltKVManager) deleteExpiredAPIEntries(entries []dnsExpiredAPIEntry) {
	for _, entry := range entries {
		deleted, err := m.deleteDNSCacheEntry(entry.cache, entry.key, entry.raw)
		if err == nil && deleted {
			if storage := m.lookupDNSStorage(entry.cache); storage != nil {
				storage.memoryDelete(entry.key)
			}
			m.notifyDNSChange(dnsCacheChange{kind: dnsCacheChangeKeyDelete, cache: entry.cache, key: entry.key})
		}
	}
}

func (m *bboltKVManager) lookupDNSStorage(cache string) *bboltDNSCacheStorage {
	m.dnsMu.Lock()
	defer m.dnsMu.Unlock()
	return m.dnsStores[cache]
}

func (s *apiSession) addDNSSubscription(sub dnsSubscription) uint64 {
	s.dnsMu.Lock()
	defer s.dnsMu.Unlock()
	s.dnsNextSubscriptionID++
	sub.id = s.dnsNextSubscriptionID
	if s.dnsSubscriptions == nil {
		s.dnsSubscriptions = map[uint64]dnsSubscription{}
	}
	s.dnsSubscriptions[sub.id] = sub
	if s.dnsChangeSubscriptionID == 0 {
		s.dnsChangeSubscriptionID = s.admin.dns.subscribeDNSChanges(func(change dnsCacheChange) {
			s.sendDNSChange(change)
		})
	}
	return sub.id
}

func (s *apiSession) removeDNSSubscriptionTree(id uint64) {
	s.dnsMu.Lock()
	defer s.dnsMu.Unlock()
	target, ok := s.dnsSubscriptions[id]
	if !ok {
		return
	}
	for subID, sub := range s.dnsSubscriptions {
		if dnsSubscriptionDescendsFrom(target, sub) {
			delete(s.dnsSubscriptions, subID)
		}
	}
	if len(s.dnsSubscriptions) == 0 && s.dnsChangeSubscriptionID != 0 {
		s.admin.dns.unsubscribeDNSChanges(s.dnsChangeSubscriptionID)
		s.dnsChangeSubscriptionID = 0
	}
}

func (s *apiSession) closeDNSSubscriptions() {
	s.dnsMu.Lock()
	defer s.dnsMu.Unlock()
	if s.dnsChangeSubscriptionID != 0 && s.admin.dns != nil {
		s.admin.dns.unsubscribeDNSChanges(s.dnsChangeSubscriptionID)
	}
	s.dnsChangeSubscriptionID = 0
	s.dnsSubscriptions = nil
}

func (s *apiSession) sendDNSChange(change dnsCacheChange) {
	subs := s.dnsSubscriptionSnapshot()
	for _, sub := range subs {
		_ = s.sendDNSSubscriptionChange(sub, change)
	}
}

func (s *apiSession) dnsSubscriptionSnapshot() []dnsSubscription {
	s.dnsMu.Lock()
	defer s.dnsMu.Unlock()
	out := make([]dnsSubscription, 0, len(s.dnsSubscriptions))
	for _, sub := range s.dnsSubscriptions {
		out = append(out, sub)
	}
	return out
}

func (s *apiSession) sendDNSSubscriptionChange(sub dnsSubscription, change dnsCacheChange) error {
	var (
		event api.Message
		err   error
	)
	switch sub.kind {
	case dnsSubscriptionCaches:
		if change.kind != dnsCacheChangeCacheUpsert && change.kind != dnsCacheChangeCacheDelete {
			return nil
		}
		event, err = api.NewEvent(api.SurfaceDNS, string(api.EventDNSCachesUpdate), api.DNSCachesEvent{
			SubscriptionID: sub.id,
			Change:         api.DNSCacheChange{Name: change.cache, Deleted: change.kind == dnsCacheChangeCacheDelete},
		})
	case dnsSubscriptionKeys:
		if sub.cache != change.cache {
			return nil
		}
		switch change.kind {
		case dnsCacheChangeKeyUpsert:
			info := dnsKeyInfoFromRaw(change.key, change.raw, dnsStoredCacheEntry{expires: change.expiresAt})
			event, err = api.NewEvent(api.SurfaceDNS, string(api.EventDNSKeysUpdate), api.DNSKeysEvent{
				SubscriptionID: sub.id,
				Cache:          sub.cache,
				Change:         api.DNSKeyChange{Key: change.key, Entry: &info},
			})
		case dnsCacheChangeKeyDelete:
			event, err = api.NewEvent(api.SurfaceDNS, string(api.EventDNSKeysUpdate), api.DNSKeysEvent{
				SubscriptionID: sub.id,
				Cache:          sub.cache,
				Change:         api.DNSKeyChange{Key: change.key, Deleted: true},
			})
		default:
			return nil
		}
	case dnsSubscriptionValue:
		if sub.cache != change.cache || sub.key != change.key {
			return nil
		}
		if change.kind != dnsCacheChangeKeyUpsert && change.kind != dnsCacheChangeKeyDelete {
			return nil
		}
		snapshot := api.DNSValueSnapshot{}
		if change.kind == dnsCacheChangeKeyUpsert {
			value := dnsValueFromRaw(change.raw, dnsStoredCacheEntry{expires: change.expiresAt})
			snapshot.Found = true
			snapshot.Value = &value
		}
		event, err = api.NewEvent(api.SurfaceDNS, string(api.EventDNSValueUpdate), api.DNSValueEvent{
			SubscriptionID: sub.id,
			Cache:          sub.cache,
			Key:            sub.key,
			Found:          snapshot.Found,
			Value:          snapshot.Value,
		})
	default:
		return nil
	}
	if err != nil {
		return err
	}
	return s.send(event)
}

func dnsSubscriptionDescendsFrom(target, sub dnsSubscription) bool {
	switch target.kind {
	case dnsSubscriptionCaches:
		return true
	case dnsSubscriptionKeys:
		return target.cache == sub.cache &&
			(sub.kind == dnsSubscriptionKeys || sub.kind == dnsSubscriptionValue)
	case dnsSubscriptionValue:
		return sub.kind == dnsSubscriptionValue &&
			target.cache == sub.cache &&
			target.key == sub.key
	default:
		return false
	}
}

func dnsRequestError(id string, err error) (api.Message, error) {
	switch {
	case errors.Is(err, errDNSClosed):
		return newRequestError(id, api.ErrorUnavailable, err.Error())
	case errors.Is(err, errDNSInvalidName):
		return newRequestError(id, api.ErrorBadRequest, err.Error())
	case errors.Is(err, errDNSInvalidRecord):
		return newRequestError(id, api.ErrorBadRequest, err.Error())
	case errors.Is(err, errDNSInvalidValue):
		return newRequestError(id, api.ErrorInternal, err.Error())
	default:
		return newRequestError(id, api.ErrorInternal, err.Error())
	}
}
