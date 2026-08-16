package main

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"github.com/asciimoth/almagest/lib/storage"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/asciimoth/almagest/bin/api"
	"go.etcd.io/bbolt"
)

type kvSubscriptionKind string

const (
	kvSubscriptionStores  kvSubscriptionKind = "stores"
	kvSubscriptionEntries kvSubscriptionKind = "entries"
	kvSubscriptionValue   kvSubscriptionKind = "value"
)

type kvSubscription struct {
	id    uint64
	kind  kvSubscriptionKind
	store api.KVStoreSelector
	path  []string
	key   string
}

func (s *apiSession) handleKVMessage(msg api.Message) (api.Message, error) {
	if s.admin.kv == nil {
		return newRequestError(msg.ID, api.ErrorUnavailable, "kv database is unavailable")
	}

	switch api.KVMethod(msg.Method) {
	case api.MethodKVStoresSnapshot:
		return api.NewResponse(msg.ID, s.admin.kv.storesSnapshot())

	case api.MethodKVSubscribeStores:
		snapshot := s.admin.kv.storesSnapshot()
		id := s.addKVSubscription(kvSubscription{kind: kvSubscriptionStores})
		return api.NewResponse(msg.ID, api.KVSubscribeStoresResponse{
			SubscriptionID: id,
			Snapshot:       snapshot,
		})

	case api.MethodKVEntriesSnapshot:
		req, err := decodePayload[api.KVEntriesRequest](msg)
		if err != nil {
			return newRequestError(msg.ID, api.ErrorInvalidPayload, err.Error())
		}
		snapshot, err := s.admin.kv.entriesSnapshot(context.Background(), req.Store, req.Path)
		if err != nil {
			return kvRequestError(msg.ID, err)
		}
		return api.NewResponse(msg.ID, snapshot)

	case api.MethodKVSubscribeEntries:
		req, err := decodePayload[api.KVEntriesRequest](msg)
		if err != nil {
			return newRequestError(msg.ID, api.ErrorInvalidPayload, err.Error())
		}
		selector, ok := normalizeKVStoreSelector(req.Store)
		if !ok || !validKVPath(req.Path) {
			return api.NewResponse(msg.ID, api.KVSubscribeEntriesResponse{})
		}
		snapshot, err := s.admin.kv.entriesSnapshot(context.Background(), selector, req.Path)
		if err != nil {
			return api.NewResponse(msg.ID, api.KVSubscribeEntriesResponse{})
		}
		id := s.addKVSubscription(kvSubscription{
			kind:  kvSubscriptionEntries,
			store: selector,
			path:  cloneKVPath(req.Path),
		})
		return api.NewResponse(msg.ID, api.KVSubscribeEntriesResponse{
			SubscriptionID: id,
			Snapshot:       snapshot,
		})

	case api.MethodKVValueSnapshot:
		req, err := decodePayload[api.KVValueRequest](msg)
		if err != nil {
			return newRequestError(msg.ID, api.ErrorInvalidPayload, err.Error())
		}
		snapshot, err := s.admin.kv.valueSnapshot(context.Background(), req.Store, req.Path, req.Key)
		if err != nil {
			return kvRequestError(msg.ID, err)
		}
		return api.NewResponse(msg.ID, snapshot)

	case api.MethodKVSubscribeValue:
		req, err := decodePayload[api.KVValueRequest](msg)
		if err != nil {
			return newRequestError(msg.ID, api.ErrorInvalidPayload, err.Error())
		}
		selector, ok := normalizeKVStoreSelector(req.Store)
		if !ok || !validKVPath(req.Path) || !validKVName(req.Key) {
			return api.NewResponse(msg.ID, api.KVSubscribeValueResponse{})
		}
		snapshot, err := s.admin.kv.valueSnapshot(context.Background(), selector, req.Path, req.Key)
		if err != nil {
			return api.NewResponse(msg.ID, api.KVSubscribeValueResponse{})
		}
		id := s.addKVSubscription(kvSubscription{
			kind:  kvSubscriptionValue,
			store: selector,
			path:  cloneKVPath(req.Path),
			key:   req.Key,
		})
		return api.NewResponse(msg.ID, api.KVSubscribeValueResponse{
			SubscriptionID: id,
			Snapshot:       snapshot,
		})

	case api.MethodKVUnsubscribe:
		req, err := decodePayload[api.KVSubscriptionRequest](msg)
		if err != nil {
			return newRequestError(msg.ID, api.ErrorInvalidPayload, err.Error())
		}
		s.removeKVSubscriptionTree(req.SubscriptionID)
		return api.NewResponse(msg.ID, struct{}{})

	case api.MethodKVEnsure:
		req, err := decodePayload[api.KVEntriesRequest](msg)
		if err != nil {
			return newRequestError(msg.ID, api.ErrorInvalidPayload, err.Error())
		}
		if err := s.admin.kv.ensure(context.Background(), req.Store, req.Path); err != nil {
			return kvRequestError(msg.ID, err)
		}
		return api.NewResponse(msg.ID, struct{}{})

	case api.MethodKVSet:
		req, err := decodePayload[api.KVSetRequest](msg)
		if err != nil {
			return newRequestError(msg.ID, api.ErrorInvalidPayload, err.Error())
		}
		value, err := kValueFromAPI(req.Value)
		if err != nil {
			return kvRequestError(msg.ID, err)
		}
		if err := s.admin.kv.set(context.Background(), req.Store, req.Path, req.Key, value); err != nil {
			return kvRequestError(msg.ID, err)
		}
		snapshot, err := s.admin.kv.valueSnapshot(context.Background(), req.Store, req.Path, req.Key)
		if err != nil {
			return kvRequestError(msg.ID, err)
		}
		return api.NewResponse(msg.ID, snapshot)

	case api.MethodKVDelete:
		req, err := decodePayload[api.KVValueRequest](msg)
		if err != nil {
			return newRequestError(msg.ID, api.ErrorInvalidPayload, err.Error())
		}
		if err := s.admin.kv.delete(context.Background(), req.Store, req.Path, req.Key); err != nil {
			return kvRequestError(msg.ID, err)
		}
		return api.NewResponse(msg.ID, struct{}{})

	case api.MethodKVDrop:
		req, err := decodePayload[api.KVEntriesRequest](msg)
		if err != nil {
			return newRequestError(msg.ID, api.ErrorInvalidPayload, err.Error())
		}
		if err := s.admin.kv.drop(context.Background(), req.Store, req.Path); err != nil {
			return kvRequestError(msg.ID, err)
		}
		return api.NewResponse(msg.ID, struct{}{})

	default:
		return newRequestError(msg.ID, api.ErrorInvalidMethod, "api method is not implemented")
	}
}

func (m *bboltKVManager) storesSnapshot() api.KVStoresSnapshot {
	stores := []api.KVStoreInfo{{Kind: api.KVStoreDefault}}
	_ = m.db.View(func(tx *bbolt.Tx) error {
		root := tx.Bucket([]byte(kvPluginBucketName))
		if root == nil {
			return nil
		}
		return root.ForEachBucket(func(name []byte) error {
			stores = append(stores, api.KVStoreInfo{
				Kind: api.KVStorePlugin,
				Name: string(name),
			})
			return nil
		})
	})
	slices.SortFunc(stores[1:], func(a, b api.KVStoreInfo) int {
		return strings.Compare(a.Name, b.Name)
	})
	return api.KVStoresSnapshot{Stores: stores}
}

func (m *bboltKVManager) entriesSnapshot(ctx context.Context, selector api.KVStoreSelector, path []string) (api.KVEntriesSnapshot, error) {
	store, err := m.selectedStore(selector)
	if err != nil {
		return api.KVEntriesSnapshot{}, err
	}
	if !validKVPath(path) {
		return api.KVEntriesSnapshot{}, storage.ErrKVInvalidName
	}
	var snapshot api.KVEntriesSnapshot
	err = store.Subtree(path...).View(ctx, func(tx storage.KVReadTx) error {
		snapshot.Exists = tx.Exists()
		if !snapshot.Exists {
			return nil
		}
		if err := tx.ForEachSubtree(func(name string) error {
			snapshot.Entries = append(snapshot.Entries, api.KVEntryInfo{
				Kind: api.KVEntryBucket,
				Name: name,
			})
			return nil
		}); err != nil {
			return err
		}
		return tx.ForEachValue(func(key string, value storage.KValue) error {
			snapshot.Entries = append(snapshot.Entries, api.KVEntryInfo{
				Kind:   api.KVEntryValue,
				Name:   key,
				Size:   len(value.Data),
				Hidden: value.Hidden,
				String: value.String,
			})
			return nil
		})
	})
	if err != nil {
		return api.KVEntriesSnapshot{}, err
	}
	slices.SortFunc(snapshot.Entries, func(a, b api.KVEntryInfo) int {
		if a.Name != b.Name {
			return strings.Compare(a.Name, b.Name)
		}
		return strings.Compare(string(a.Kind), string(b.Kind))
	})
	return snapshot, nil
}

func (m *bboltKVManager) valueSnapshot(ctx context.Context, selector api.KVStoreSelector, path []string, key string) (api.KVValueSnapshot, error) {
	store, err := m.selectedStore(selector)
	if err != nil {
		return api.KVValueSnapshot{}, err
	}
	if !validKVPath(path) || !validKVName(key) {
		return api.KVValueSnapshot{}, storage.ErrKVInvalidName
	}
	var snapshot api.KVValueSnapshot
	err = store.Subtree(path...).View(ctx, func(tx storage.KVReadTx) error {
		value, found, err := tx.Get(key)
		if err != nil || !found {
			return err
		}
		apiValue, err := kValueToAPI(*value)
		if err != nil {
			return err
		}
		snapshot.Found = true
		snapshot.Value = &apiValue
		return nil
	})
	return snapshot, err
}

func (m *bboltKVManager) ensure(ctx context.Context, selector api.KVStoreSelector, path []string) error {
	store, err := m.selectedStore(selector)
	if err != nil {
		return err
	}
	if !validKVPath(path) {
		return storage.ErrKVInvalidName
	}
	return store.Subtree(path...).Update(ctx, func(tx storage.KVWriteTx) error {
		return tx.Ensure()
	})
}

func (m *bboltKVManager) set(ctx context.Context, selector api.KVStoreSelector, path []string, key string, value storage.KValue) error {
	store, err := m.selectedStore(selector)
	if err != nil {
		return err
	}
	if !validKVPath(path) || !validKVName(key) {
		return storage.ErrKVInvalidName
	}
	return store.Subtree(path...).Update(ctx, func(tx storage.KVWriteTx) error {
		return tx.Set(key, value)
	})
}

func (m *bboltKVManager) delete(ctx context.Context, selector api.KVStoreSelector, path []string, key string) error {
	store, err := m.selectedStore(selector)
	if err != nil {
		return err
	}
	if !validKVPath(path) || !validKVName(key) {
		return storage.ErrKVInvalidName
	}
	return store.Subtree(path...).Update(ctx, func(tx storage.KVWriteTx) error {
		return tx.Delete(key)
	})
}

func (m *bboltKVManager) drop(ctx context.Context, selector api.KVStoreSelector, path []string) error {
	store, err := m.selectedStore(selector)
	if err != nil {
		return err
	}
	if !validKVPath(path) {
		return storage.ErrKVInvalidName
	}
	return store.Subtree(path...).Update(ctx, func(tx storage.KVWriteTx) error {
		return tx.Drop()
	})
}

func (m *bboltKVManager) selectedStore(selector api.KVStoreSelector) (storage.KVStore, error) {
	selector, ok := normalizeKVStoreSelector(selector)
	if !ok {
		return nil, storage.ErrKVInvalidName
	}
	switch selector.Kind {
	case api.KVStoreDefault:
		return m.DefaultStore(), nil
	case api.KVStorePlugin:
		return m.PluginStore(selector.Name), nil
	default:
		return nil, storage.ErrKVInvalidName
	}
}

func normalizeKVStoreSelector(selector api.KVStoreSelector) (api.KVStoreSelector, bool) {
	if selector.Kind == "" {
		selector.Kind = api.KVStoreDefault
	}
	switch selector.Kind {
	case api.KVStoreDefault:
		if selector.Name != "" {
			return api.KVStoreSelector{}, false
		}
		return api.KVStoreSelector{Kind: api.KVStoreDefault}, true
	case api.KVStorePlugin:
		if !validKVName(selector.Name) {
			return api.KVStoreSelector{}, false
		}
		return selector, true
	default:
		return api.KVStoreSelector{}, false
	}
}

func kValueToAPI(value storage.KValue) (api.KVValue, error) {
	if value.String {
		if !utf8.Valid(value.Data) {
			return api.KVValue{}, storage.ErrKVInvalidValue
		}
		return api.KVValue{
			Data:     string(value.Data),
			Encoding: "string",
			Size:     len(value.Data),
			Hidden:   value.Hidden,
			String:   value.String,
		}, nil
	}
	return api.KVValue{
		Data:     base64.StdEncoding.EncodeToString(value.Data),
		Encoding: "base64",
		Size:     len(value.Data),
		Hidden:   value.Hidden,
		String:   value.String,
	}, nil
}

func kValueFromAPI(value api.KVValue) (storage.KValue, error) {
	encoding := strings.ToLower(value.Encoding)
	if encoding == "" {
		if value.String {
			encoding = "string"
		} else {
			encoding = "base64"
		}
	}
	var data []byte
	switch encoding {
	case "string", "utf8", "plain":
		if value.String && !utf8.ValidString(value.Data) {
			return storage.KValue{}, storage.ErrKVInvalidValue
		}
		data = []byte(value.Data)
	case "base64":
		decoded, err := base64.StdEncoding.DecodeString(value.Data)
		if err != nil {
			return storage.KValue{}, fmt.Errorf("%w: invalid base64 data", storage.ErrKVInvalidValue)
		}
		data = decoded
	default:
		return storage.KValue{}, fmt.Errorf("%w: unsupported encoding %q", storage.ErrKVInvalidValue, value.Encoding)
	}
	return storage.KValue{
		Data:   data,
		Hidden: value.Hidden,
		String: value.String,
	}, nil
}

func (s *apiSession) addKVSubscription(sub kvSubscription) uint64 {
	s.kvMu.Lock()
	defer s.kvMu.Unlock()
	s.kvNextSubscriptionID++
	sub.id = s.kvNextSubscriptionID
	if s.kvSubscriptions == nil {
		s.kvSubscriptions = map[uint64]kvSubscription{}
	}
	s.kvSubscriptions[sub.id] = sub
	if s.kvChangeSubscriptionID == 0 {
		s.kvChangeSubscriptionID = s.admin.kv.subscribeChanges(func() {
			s.sendKVSubscriptionSnapshots()
		})
	}
	return sub.id
}

func (s *apiSession) removeKVSubscriptionTree(id uint64) {
	s.kvMu.Lock()
	defer s.kvMu.Unlock()
	target, ok := s.kvSubscriptions[id]
	if !ok {
		return
	}
	for subID, sub := range s.kvSubscriptions {
		if kvSubscriptionDescendsFrom(target, sub) {
			delete(s.kvSubscriptions, subID)
		}
	}
	if len(s.kvSubscriptions) == 0 && s.kvChangeSubscriptionID != 0 {
		s.admin.kv.unsubscribeChanges(s.kvChangeSubscriptionID)
		s.kvChangeSubscriptionID = 0
	}
}

func (s *apiSession) closeKVSubscriptions() {
	s.kvMu.Lock()
	defer s.kvMu.Unlock()
	if s.kvChangeSubscriptionID != 0 && s.admin.kv != nil {
		s.admin.kv.unsubscribeChanges(s.kvChangeSubscriptionID)
	}
	s.kvChangeSubscriptionID = 0
	s.kvSubscriptions = nil
}

func (s *apiSession) sendKVSubscriptionSnapshots() {
	subs := s.kvSubscriptionSnapshot()
	for _, sub := range subs {
		_ = s.sendKVSubscriptionSnapshot(sub)
	}
}

func (s *apiSession) kvSubscriptionSnapshot() []kvSubscription {
	s.kvMu.Lock()
	defer s.kvMu.Unlock()
	out := make([]kvSubscription, 0, len(s.kvSubscriptions))
	for _, sub := range s.kvSubscriptions {
		sub.path = cloneKVPath(sub.path)
		out = append(out, sub)
	}
	return out
}

func (s *apiSession) sendKVSubscriptionSnapshot(sub kvSubscription) error {
	var (
		event api.Message
		err   error
	)
	switch sub.kind {
	case kvSubscriptionStores:
		event, err = api.NewEvent(api.SurfaceKV, string(api.EventKVStoresUpdate), api.KVStoresEvent{
			SubscriptionID: sub.id,
			Snapshot:       s.admin.kv.storesSnapshot(),
		})
	case kvSubscriptionEntries:
		snapshot, snapshotErr := s.admin.kv.entriesSnapshot(context.Background(), sub.store, sub.path)
		if snapshotErr != nil {
			return snapshotErr
		}
		event, err = api.NewEvent(api.SurfaceKV, string(api.EventKVEntriesUpdate), api.KVEntriesEvent{
			SubscriptionID: sub.id,
			Snapshot:       snapshot,
		})
	case kvSubscriptionValue:
		snapshot, snapshotErr := s.admin.kv.valueSnapshot(context.Background(), sub.store, sub.path, sub.key)
		if snapshotErr != nil {
			return snapshotErr
		}
		event, err = api.NewEvent(api.SurfaceKV, string(api.EventKVValueUpdate), api.KVValueEvent{
			SubscriptionID: sub.id,
			Snapshot:       snapshot,
		})
	default:
		return nil
	}
	if err != nil {
		return err
	}
	return s.send(event)
}

func kvSubscriptionDescendsFrom(target, sub kvSubscription) bool {
	if target.kind == kvSubscriptionStores {
		return true
	}
	if target.store.Kind != sub.store.Kind || target.store.Name != sub.store.Name {
		return false
	}
	switch target.kind {
	case kvSubscriptionEntries:
		switch sub.kind {
		case kvSubscriptionEntries, kvSubscriptionValue:
			return kvPathHasPrefix(sub.path, target.path)
		default:
			return false
		}
	case kvSubscriptionValue:
		return sub.kind == kvSubscriptionValue &&
			slices.Equal(sub.path, target.path) &&
			sub.key == target.key
	default:
		return false
	}
}

func kvPathHasPrefix(path, prefix []string) bool {
	return len(path) >= len(prefix) && slices.Equal(path[:len(prefix)], prefix)
}

func kvRequestError(id string, err error) (api.Message, error) {
	switch {
	case errors.Is(err, storage.ErrKVClosed):
		return newRequestError(id, api.ErrorUnavailable, err.Error())
	case errors.Is(err, storage.ErrKVInvalidName), errors.Is(err, storage.ErrKVInvalidValue):
		return newRequestError(id, api.ErrorBadRequest, err.Error())
	case errors.Is(err, storage.ErrKVKindConflict), errors.Is(err, storage.ErrKVRoot):
		return newRequestError(id, api.ErrorConflict, err.Error())
	default:
		return newRequestError(id, api.ErrorInternal, err.Error())
	}
}
