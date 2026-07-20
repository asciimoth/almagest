package main

import (
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/asciimoth/almagest/bin/api"
	"go.etcd.io/bbolt"
)

type dnsSubscriptionKind string

const (
	dnsSubscriptionCaches dnsSubscriptionKind = "caches"
	dnsSubscriptionKeys   dnsSubscriptionKind = "keys"
	dnsSubscriptionValue  dnsSubscriptionKind = "value"
)

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

func (m *bboltKVManager) dnsKeyInfo(cache, key string) (api.DNSKeyInfo, bool, error) {
	if !validDNSName(cache) || !validDNSName(key) {
		return api.DNSKeyInfo{}, false, errDNSInvalidName
	}
	raw, found, err := m.getDNSCacheEntryRaw(cache, key)
	if err != nil || !found {
		return api.DNSKeyInfo{}, false, err
	}
	entry, err := unmarshalDNSCacheEntry(raw)
	if err != nil {
		return api.DNSKeyInfo{}, false, err
	}
	if m.dnsCacheEntryExpired(entry.expires, time.Now()) {
		m.deleteExpiredAPIEntries([]dnsExpiredAPIEntry{{cache: cache, key: key, raw: raw}})
		return api.DNSKeyInfo{}, false, nil
	}
	return dnsKeyInfoFromRaw(key, raw, entry), true, nil
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
		if change.kind != dnsCacheChangeCacheUpsert {
			return nil
		}
		event, err = api.NewEvent(api.SurfaceDNS, string(api.EventDNSCachesUpdate), api.DNSCachesEvent{
			SubscriptionID: sub.id,
			Change:         api.DNSCacheChange{Name: change.cache},
		})
	case dnsSubscriptionKeys:
		if sub.cache != change.cache {
			return nil
		}
		switch change.kind {
		case dnsCacheChangeKeyUpsert:
			info, found, infoErr := s.admin.dns.dnsKeyInfo(change.cache, change.key)
			if infoErr != nil || !found {
				return infoErr
			}
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
		snapshot, snapshotErr := s.admin.dns.dnsValueSnapshot(sub.cache, sub.key)
		if snapshotErr != nil {
			return snapshotErr
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
	case errors.Is(err, errDNSInvalidValue):
		return newRequestError(id, api.ErrorInternal, err.Error())
	default:
		return newRequestError(id, api.ErrorInternal, err.Error())
	}
}
