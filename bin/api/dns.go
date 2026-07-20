package api

type DNSMethod string

const (
	MethodDNSCachesSnapshot  DNSMethod = "caches_snapshot"
	MethodDNSSubscribeCaches DNSMethod = "subscribe_caches"
	MethodDNSKeysSnapshot    DNSMethod = "keys_snapshot"
	MethodDNSSubscribeKeys   DNSMethod = "subscribe_keys"
	MethodDNSValueSnapshot   DNSMethod = "value_snapshot"
	MethodDNSSubscribeValue  DNSMethod = "subscribe_value"
	MethodDNSUnsubscribe     DNSMethod = "unsubscribe"
	MethodDNSSet             DNSMethod = "set"
	MethodDNSDelete          DNSMethod = "delete"
	MethodDNSDrop            DNSMethod = "drop"

	EventDNSCachesUpdate DNSMethod = "caches_update"
	EventDNSKeysUpdate   DNSMethod = "keys_update"
	EventDNSValueUpdate  DNSMethod = "value_update"
)

// DNSMethods returns request methods for the DNS admin surface. Subscribe
// responses include the initial snapshot; later events carry one changed cache,
// key, or value instead of resending full cache snapshots.
func DNSMethods() []string {
	return []string{
		string(MethodDNSCachesSnapshot),
		string(MethodDNSSubscribeCaches),
		string(MethodDNSKeysSnapshot),
		string(MethodDNSSubscribeKeys),
		string(MethodDNSValueSnapshot),
		string(MethodDNSSubscribeValue),
		string(MethodDNSUnsubscribe),
		string(MethodDNSSet),
		string(MethodDNSDelete),
		string(MethodDNSDrop),
	}
}

type DNSCacheInfo struct {
	Name string `json:"name"`
}

type DNSCachesSnapshot struct {
	Caches []DNSCacheInfo `json:"caches,omitempty"`
}

type DNSSubscribeCachesResponse struct {
	SubscriptionID uint64            `json:"subscription_id"`
	Snapshot       DNSCachesSnapshot `json:"snapshot"`
}

type DNSCacheChange struct {
	Name    string `json:"name"`
	Deleted bool   `json:"deleted,omitempty"`
}

type DNSCachesEvent struct {
	SubscriptionID uint64         `json:"subscription_id"`
	Change         DNSCacheChange `json:"change"`
}

type DNSKeysRequest struct {
	Cache string `json:"cache"`
}

type DNSKeyInfo struct {
	Key       string `json:"key"`
	Size      int    `json:"size,omitempty"`
	ExpiresAt string `json:"expires_at,omitempty"`
}

type DNSKeysSnapshot struct {
	Exists bool         `json:"exists"`
	Keys   []DNSKeyInfo `json:"keys,omitempty"`
}

type DNSSubscribeKeysResponse struct {
	SubscriptionID uint64          `json:"subscription_id"`
	Snapshot       DNSKeysSnapshot `json:"snapshot"`
}

type DNSKeyChange struct {
	Key     string      `json:"key"`
	Deleted bool        `json:"deleted,omitempty"`
	Entry   *DNSKeyInfo `json:"entry,omitempty"`
}

type DNSKeysEvent struct {
	SubscriptionID uint64       `json:"subscription_id"`
	Cache          string       `json:"cache"`
	Change         DNSKeyChange `json:"change"`
}

type DNSValueRequest struct {
	Cache string `json:"cache"`
	Key   string `json:"key"`
}

type DNSValue struct {
	JSON      string `json:"json,omitempty"`
	Size      int    `json:"size,omitempty"`
	ExpiresAt string `json:"expires_at,omitempty"`
}

type DNSRecordInput struct {
	Name  string `json:"name"`
	Type  string `json:"type"`
	Class string `json:"class,omitempty"`
	TTL   uint32 `json:"ttl"`
	Value string `json:"value"`
}

type DNSValueSnapshot struct {
	Found bool      `json:"found"`
	Value *DNSValue `json:"value,omitempty"`
}

type DNSSubscribeValueResponse struct {
	SubscriptionID uint64           `json:"subscription_id"`
	Snapshot       DNSValueSnapshot `json:"snapshot"`
}

type DNSValueEvent struct {
	SubscriptionID uint64    `json:"subscription_id"`
	Cache          string    `json:"cache"`
	Key            string    `json:"key"`
	Found          bool      `json:"found"`
	Value          *DNSValue `json:"value,omitempty"`
}

type DNSSetRequest struct {
	Cache  string         `json:"cache"`
	Key    string         `json:"key,omitempty"`
	Record DNSRecordInput `json:"record"`
}

type DNSSetResponse struct {
	Key      string           `json:"key"`
	Snapshot DNSValueSnapshot `json:"snapshot"`
}

type DNSSubscriptionRequest struct {
	SubscriptionID uint64 `json:"subscription_id"`
}
