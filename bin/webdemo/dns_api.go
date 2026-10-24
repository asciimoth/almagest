package webdemo

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/asciimoth/almagest/bin/api"
	gdns "github.com/asciimoth/gonnect/dns"
)

type dnsAdminEntry struct {
	msg     *gdns.Message
	expires time.Time
}

type dnsSubscriptionKind uint8

const (
	dnsSubscribeCaches dnsSubscriptionKind = iota + 1
	dnsSubscribeKeys
	dnsSubscribeValue
)

type dnsSubscription struct {
	kind  dnsSubscriptionKind
	cache string
	key   string
}

func (b *Backend) handleDNS(msg api.Message) (api.Message, error) { //nolint:cyclop
	manager, ok := b.stores.DNS.(*memoryDNSManager)
	if !ok {
		return requestError(msg.ID, api.ErrorUnavailable, "dns cache is unavailable")
	}
	switch api.DNSMethod(msg.Method) {
	case api.MethodDNSCachesSnapshot:
		return api.NewResponse(msg.ID, manager.apiCaches())
	case api.MethodDNSSubscribeCaches:
		id := b.subscriptionID()
		b.subscriptionMu.Lock()
		b.dnsSubscriptions[id] = dnsSubscription{kind: dnsSubscribeCaches}
		b.subscriptionMu.Unlock()
		return api.NewResponse(msg.ID, api.DNSSubscribeCachesResponse{SubscriptionID: id, Snapshot: manager.apiCaches()})
	case api.MethodDNSKeysSnapshot, api.MethodDNSSubscribeKeys:
		req, err := payload[api.DNSKeysRequest](msg)
		if err != nil {
			return invalidPayload(msg.ID, err)
		}
		s, err := manager.apiKeys(req.Cache)
		if err != nil {
			return requestError(msg.ID, api.ErrorBadRequest, err.Error())
		}
		if api.DNSMethod(msg.Method) == api.MethodDNSSubscribeKeys {
			id := b.subscriptionID()
			b.subscriptionMu.Lock()
			b.dnsSubscriptions[id] = dnsSubscription{kind: dnsSubscribeKeys, cache: req.Cache}
			b.subscriptionMu.Unlock()
			return api.NewResponse(msg.ID, api.DNSSubscribeKeysResponse{SubscriptionID: id, Snapshot: s})
		}
		return api.NewResponse(msg.ID, s)
	case api.MethodDNSValueSnapshot, api.MethodDNSSubscribeValue:
		req, err := payload[api.DNSValueRequest](msg)
		if err != nil {
			return invalidPayload(msg.ID, err)
		}
		s, err := manager.apiValue(req.Cache, req.Key)
		if err != nil {
			return requestError(msg.ID, api.ErrorBadRequest, err.Error())
		}
		if api.DNSMethod(msg.Method) == api.MethodDNSSubscribeValue {
			id := b.subscriptionID()
			b.subscriptionMu.Lock()
			b.dnsSubscriptions[id] = dnsSubscription{kind: dnsSubscribeValue, cache: req.Cache, key: req.Key}
			b.subscriptionMu.Unlock()
			return api.NewResponse(msg.ID, api.DNSSubscribeValueResponse{SubscriptionID: id, Snapshot: s})
		}
		return api.NewResponse(msg.ID, s)
	case api.MethodDNSUnsubscribe:
		req, err := payload[api.DNSSubscriptionRequest](msg)
		if err != nil {
			return invalidPayload(msg.ID, err)
		}
		b.subscriptionMu.Lock()
		delete(b.dnsSubscriptions, req.SubscriptionID)
		b.subscriptionMu.Unlock()
		return api.NewResponse(msg.ID, struct{}{})
	case api.MethodDNSSet:
		req, err := payload[api.DNSSetRequest](msg)
		if err != nil {
			return invalidPayload(msg.ID, err)
		}
		key, err := manager.apiSet(req.Cache, req.Key, req.Record)
		if err != nil {
			return requestError(msg.ID, api.ErrorBadRequest, err.Error())
		}
		b.queueDNSCacheChange(req.Cache, false)
		if req.Key != "" && req.Key != key {
			b.queueDNSKeyChange(manager, req.Cache, req.Key, true)
			b.queueDNSValue(manager, req.Cache, req.Key)
		}
		b.queueDNSKeyChange(manager, req.Cache, key, false)
		b.queueDNSValue(manager, req.Cache, key)
		snapshot, _ := manager.apiValue(req.Cache, key)
		return api.NewResponse(msg.ID, api.DNSSetResponse{Key: key, Snapshot: snapshot})
	case api.MethodDNSDelete:
		req, err := payload[api.DNSValueRequest](msg)
		if err != nil {
			return invalidPayload(msg.ID, err)
		}
		if err = manager.apiDelete(req.Cache, req.Key); err != nil {
			return requestError(msg.ID, api.ErrorBadRequest, err.Error())
		}
		b.queueDNSKeyChange(manager, req.Cache, req.Key, true)
		b.queueDNSValue(manager, req.Cache, req.Key)
		return api.NewResponse(msg.ID, struct{}{})
	case api.MethodDNSDrop:
		req, err := payload[api.DNSKeysRequest](msg)
		if err != nil {
			return invalidPayload(msg.ID, err)
		}
		keys, _ := manager.apiKeys(req.Cache)
		if err = manager.apiDrop(req.Cache); err != nil {
			return requestError(msg.ID, api.ErrorBadRequest, err.Error())
		}
		for _, key := range keys.Keys {
			b.queueDNSKeyChange(manager, req.Cache, key.Key, true)
			b.queueDNSValue(manager, req.Cache, key.Key)
		}
		b.queueDNSCacheChange(req.Cache, true)
		return api.NewResponse(msg.ID, struct{}{})
	default:
		return requestError(msg.ID, api.ErrorInvalidMethod, "api method is not implemented")
	}
}

func (b *Backend) queueDNSCacheChange(cache string, deleted bool) {
	b.subscriptionMu.RLock()
	defer b.subscriptionMu.RUnlock()
	for id, subscription := range b.dnsSubscriptions {
		if subscription.kind != dnsSubscribeCaches {
			continue
		}
		event, err := api.NewEvent(api.SurfaceDNS, string(api.EventDNSCachesUpdate), api.DNSCachesEvent{SubscriptionID: id, Change: api.DNSCacheChange{Name: cache, Deleted: deleted}})
		if err == nil {
			b.queueEvent(event)
		}
	}
}

func (b *Backend) queueDNSKeyChange(manager *memoryDNSManager, cache, key string, deleted bool) {
	var info *api.DNSKeyInfo
	if !deleted {
		snapshot, err := manager.apiKeys(cache)
		if err == nil {
			for i := range snapshot.Keys {
				if snapshot.Keys[i].Key == key {
					value := snapshot.Keys[i]
					info = &value
					break
				}
			}
		}
	}
	b.subscriptionMu.RLock()
	defer b.subscriptionMu.RUnlock()
	for id, subscription := range b.dnsSubscriptions {
		if subscription.kind != dnsSubscribeKeys || subscription.cache != cache {
			continue
		}
		event, err := api.NewEvent(api.SurfaceDNS, string(api.EventDNSKeysUpdate), api.DNSKeysEvent{SubscriptionID: id, Cache: cache, Change: api.DNSKeyChange{Key: key, Deleted: deleted, Entry: info}})
		if err == nil {
			b.queueEvent(event)
		}
	}
}

func (b *Backend) queueDNSValue(manager *memoryDNSManager, cache, key string) {
	snapshot, err := manager.apiValue(cache, key)
	if err != nil {
		return
	}
	b.subscriptionMu.RLock()
	defer b.subscriptionMu.RUnlock()
	for id, subscription := range b.dnsSubscriptions {
		if subscription.kind != dnsSubscribeValue || subscription.cache != cache || subscription.key != key {
			continue
		}
		event, eventErr := api.NewEvent(api.SurfaceDNS, string(api.EventDNSValueUpdate), api.DNSValueEvent{SubscriptionID: id, Cache: cache, Key: key, Found: snapshot.Found, Value: snapshot.Value})
		if eventErr == nil {
			b.queueEvent(event)
		}
	}
}

func (b *Backend) queueRuntimeDNSChange(cache, key string, deleted bool) {
	b.queueDNSCacheChange(cache, false)
	b.queueDNSKeyChange(b.stores.DNS.(*memoryDNSManager), cache, key, deleted)
	b.queueDNSValue(b.stores.DNS.(*memoryDNSManager), cache, key)
}

func validDNSAPIName(name string) bool { return name != "" && utf8Safe(name) }
func utf8Safe(value string) bool {
	for _, r := range value {
		if r < ' ' {
			return false
		}
	}
	return true
}

func (m *memoryDNSManager) apiCaches() api.DNSCachesSnapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.expireLocked()
	names := make([]string, 0, len(m.stores))
	for name := range m.stores {
		names = append(names, name)
	}
	slices.Sort(names)
	result := api.DNSCachesSnapshot{Caches: make([]api.DNSCacheInfo, 0, len(names))}
	for _, name := range names {
		result.Caches = append(result.Caches, api.DNSCacheInfo{Name: name})
	}
	return result
}
func (m *memoryDNSManager) apiKeys(cache string) (api.DNSKeysSnapshot, error) {
	if !validDNSAPIName(cache) {
		return api.DNSKeysSnapshot{}, fmt.Errorf("invalid dns cache name")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.expireLocked()
	values, exists := m.adminEntries[cache]
	result := api.DNSKeysSnapshot{Exists: exists || m.stores[cache] != nil}
	for key, entry := range values {
		raw, _ := json.Marshal(entry.msg)
		result.Keys = append(result.Keys, api.DNSKeyInfo{Key: key, Size: len(raw), ExpiresAt: entry.expires.UTC().Format(time.RFC3339Nano)})
	}
	slices.SortFunc(result.Keys, func(a, c api.DNSKeyInfo) int { return strings.Compare(a.Key, c.Key) })
	return result, nil
}
func (m *memoryDNSManager) apiValue(cache, key string) (api.DNSValueSnapshot, error) {
	if !validDNSAPIName(cache) || !validDNSAPIName(key) {
		return api.DNSValueSnapshot{}, fmt.Errorf("invalid dns cache name")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.expireLocked()
	entry, found := m.adminEntries[cache][key]
	if !found {
		return api.DNSValueSnapshot{}, nil
	}
	raw, _ := json.Marshal(entry.msg)
	return api.DNSValueSnapshot{Found: true, Value: &api.DNSValue{JSON: string(raw), Size: len(raw), ExpiresAt: entry.expires.UTC().Format(time.RFC3339Nano)}}, nil
}
func (m *memoryDNSManager) apiSet(cache, previous string, record api.DNSRecordInput) (string, error) {
	if !validDNSAPIName(cache) {
		return "", fmt.Errorf("invalid dns cache name")
	}
	message, key, err := dnsMessageFromAPI(record)
	if err != nil {
		return "", err
	}
	now := time.Now()
	expires := now.Add(time.Duration(record.TTL) * time.Second)
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.adminEntries == nil {
		m.adminEntries = map[string]map[string]dnsAdminEntry{}
	}
	if m.adminEntries[cache] == nil {
		m.adminEntries[cache] = map[string]dnsAdminEntry{}
	}
	store := m.stores[cache]
	if store == nil {
		store = &memoryDNSStorage{manager: m, name: cache, memory: gdns.NewMemoryStorage()}
		m.stores[cache] = store
	}
	if previous != "" && previous != key {
		delete(m.adminEntries[cache], previous)
		store.memory.Delete(previous)
	}
	m.adminEntries[cache][key] = dnsAdminEntry{msg: message.Copy(), expires: expires}
	store.memory.Set(key, message, now)
	return key, nil
}
func (m *memoryDNSManager) apiDelete(cache, key string) error {
	if !validDNSAPIName(cache) || !validDNSAPIName(key) {
		return fmt.Errorf("invalid dns cache name")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.adminEntries[cache], key)
	if store := m.stores[cache]; store != nil {
		store.memory.Delete(key)
	}
	return nil
}
func (m *memoryDNSManager) apiDrop(cache string) error {
	if !validDNSAPIName(cache) {
		return fmt.Errorf("invalid dns cache name")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if store := m.stores[cache]; store != nil {
		for key := range m.adminEntries[cache] {
			store.memory.Delete(key)
		}
	}
	delete(m.adminEntries, cache)
	delete(m.stores, cache)
	return nil
}
func (m *memoryDNSManager) expireLocked() {
	now := time.Now()
	for cache, values := range m.adminEntries {
		for key, entry := range values {
			if !now.Before(entry.expires) {
				delete(values, key)
				if store := m.stores[cache]; store != nil {
					store.memory.Delete(key)
				}
			}
		}
	}
}

func dnsMessageFromAPI(record api.DNSRecordInput) (*gdns.Message, string, error) {
	if record.TTL == 0 {
		return nil, "", fmt.Errorf("dns ttl must be greater than zero")
	}
	name := strings.TrimSpace(record.Name)
	if name == "" {
		return nil, "", fmt.Errorf("invalid dns record name")
	}
	if !strings.HasSuffix(name, ".") {
		name += "."
	}
	typ, err := dnsType(record.Type)
	if err != nil {
		return nil, "", err
	}
	class, err := dnsClass(record.Class)
	if err != nil {
		return nil, "", err
	}
	data, err := dnsData(typ, record.Value)
	if err != nil {
		return nil, "", err
	}
	question := gdns.Question{Name: name, Type: typ, Class: class}
	message := &gdns.Message{Response: true, RCode: gdns.RCodeSuccess, Authoritative: true, RecursionDesired: true, RecursionAvailable: true, Questions: []gdns.Question{question}, Answers: []gdns.Resource{{Name: name, Type: typ, Class: class, TTL: record.TTL, Data: data}}}
	return message, fmt.Sprintf("%s|%d|%d", name, typ, class), nil
}
func dnsType(value string) (uint16, error) {
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
		return 0, fmt.Errorf("unsupported dns type %q", value)
	}
}
func dnsClass(value string) (uint16, error) {
	switch strings.ToUpper(strings.TrimSpace(value)) {
	case "", "IN":
		return gdns.ClassIN, nil
	case "CH":
		return 3, nil
	case "HS":
		return 4, nil
	}
	n, err := strconv.ParseUint(value, 10, 16)
	return uint16(n), err
}
func dnsData(typ uint16, value string) ([]byte, error) {
	value = strings.TrimSpace(value)
	switch typ {
	case gdns.TypeA:
		addr, err := netip.ParseAddr(value)
		if err != nil || !addr.Is4() {
			return nil, fmt.Errorf("invalid A value")
		}
		return addr.AsSlice(), nil
	case gdns.TypeAAAA:
		addr, err := netip.ParseAddr(value)
		if err != nil || !addr.Is6() || addr.Is4() {
			return nil, fmt.Errorf("invalid AAAA value")
		}
		return addr.AsSlice(), nil
	case gdns.TypeCNAME, gdns.TypeNS, gdns.TypePTR:
		if value == "" {
			return nil, fmt.Errorf("invalid dns name")
		}
		if !strings.HasSuffix(value, ".") {
			value += "."
		}
		return []byte(value), nil
	case gdns.TypeTXT:
		return []byte(value), nil
	case gdns.TypeMX:
		fields := strings.Fields(value)
		if len(fields) != 2 {
			return nil, fmt.Errorf("MX value must be PREF HOST")
		}
		n, err := strconv.ParseUint(fields[0], 10, 16)
		if err != nil {
			return nil, err
		}
		host := fields[1]
		if !strings.HasSuffix(host, ".") {
			host += "."
		}
		data := make([]byte, 2)
		binary.BigEndian.PutUint16(data, uint16(n))
		return append(data, host...), nil
	case gdns.TypeSRV:
		fields := strings.Fields(value)
		if len(fields) != 4 {
			return nil, fmt.Errorf("SRV value must be PRIORITY WEIGHT PORT TARGET")
		}
		data := make([]byte, 6)
		for i := range 3 {
			n, err := strconv.ParseUint(fields[i], 10, 16)
			if err != nil {
				return nil, err
			}
			binary.BigEndian.PutUint16(data[i*2:], uint16(n))
		}
		target := fields[3]
		if !strings.HasSuffix(target, ".") {
			target += "."
		}
		return append(data, target...), nil
	}
	return nil, fmt.Errorf("unsupported dns type")
}
