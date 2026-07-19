package api

type KVMethod string

const (
	MethodKVStoresSnapshot   KVMethod = "stores_snapshot"
	MethodKVSubscribeStores  KVMethod = "subscribe_stores"
	MethodKVEntriesSnapshot  KVMethod = "entries_snapshot"
	MethodKVSubscribeEntries KVMethod = "subscribe_entries"
	MethodKVValueSnapshot    KVMethod = "value_snapshot"
	MethodKVSubscribeValue   KVMethod = "subscribe_value"
	MethodKVUnsubscribe      KVMethod = "unsubscribe"
	MethodKVEnsure           KVMethod = "ensure"
	MethodKVSet              KVMethod = "set"
	MethodKVDelete           KVMethod = "delete"
	MethodKVDrop             KVMethod = "drop"

	EventKVStoresUpdate  KVMethod = "stores_update"
	EventKVEntriesUpdate KVMethod = "entries_update"
	EventKVValueUpdate   KVMethod = "value_update"
)

func KVMethods() []string {
	return []string{
		string(MethodKVStoresSnapshot),
		string(MethodKVSubscribeStores),
		string(MethodKVEntriesSnapshot),
		string(MethodKVSubscribeEntries),
		string(MethodKVValueSnapshot),
		string(MethodKVSubscribeValue),
		string(MethodKVUnsubscribe),
		string(MethodKVEnsure),
		string(MethodKVSet),
		string(MethodKVDelete),
		string(MethodKVDrop),
	}
}

type KVStoreKind string

const (
	KVStoreDefault KVStoreKind = "default"
	KVStorePlugin  KVStoreKind = "plugin"
)

type KVStoreSelector struct {
	Kind KVStoreKind `json:"kind,omitempty"`
	Name string      `json:"name,omitempty"`
}

type KVStoreInfo struct {
	Kind KVStoreKind `json:"kind"`
	Name string      `json:"name,omitempty"`
}

type KVStoresSnapshot struct {
	Stores []KVStoreInfo `json:"stores"`
}

type KVStoresEvent struct {
	SubscriptionID uint64           `json:"subscription_id"`
	Snapshot       KVStoresSnapshot `json:"snapshot"`
}

type KVEntryKind string

const (
	KVEntryBucket KVEntryKind = "bucket"
	KVEntryValue  KVEntryKind = "value"
)

type KVEntryInfo struct {
	Kind   KVEntryKind `json:"kind"`
	Name   string      `json:"name"`
	Size   int         `json:"size,omitempty"`
	Hidden bool        `json:"hidden,omitempty"`
	String bool        `json:"string,omitempty"`
}

type KVEntriesRequest struct {
	Store KVStoreSelector `json:"store,omitempty"`
	Path  []string        `json:"path,omitempty"`
}

type KVEntriesSnapshot struct {
	Exists  bool          `json:"exists"`
	Entries []KVEntryInfo `json:"entries,omitempty"`
}

type KVSubscribeEntriesResponse struct {
	SubscriptionID uint64            `json:"subscription_id"`
	Snapshot       KVEntriesSnapshot `json:"snapshot"`
}

type KVEntriesEvent struct {
	SubscriptionID uint64            `json:"subscription_id"`
	Snapshot       KVEntriesSnapshot `json:"snapshot"`
}

type KVValue struct {
	Data     string `json:"data,omitempty"`
	Encoding string `json:"encoding,omitempty"`
	Size     int    `json:"size,omitempty"`
	Hidden   bool   `json:"hidden,omitempty"`
	String   bool   `json:"string,omitempty"`
}

type KVValueRequest struct {
	Store KVStoreSelector `json:"store,omitempty"`
	Path  []string        `json:"path,omitempty"`
	Key   string          `json:"key"`
}

type KVValueSnapshot struct {
	Found bool     `json:"found"`
	Value *KVValue `json:"value,omitempty"`
}

type KVSubscribeStoresResponse struct {
	SubscriptionID uint64           `json:"subscription_id"`
	Snapshot       KVStoresSnapshot `json:"snapshot"`
}

type KVSubscribeValueResponse struct {
	SubscriptionID uint64          `json:"subscription_id"`
	Snapshot       KVValueSnapshot `json:"snapshot"`
}

type KVValueEvent struct {
	SubscriptionID uint64          `json:"subscription_id"`
	Snapshot       KVValueSnapshot `json:"snapshot"`
}

type KVSubscriptionRequest struct {
	SubscriptionID uint64 `json:"subscription_id"`
}

type KVSetRequest struct {
	Store KVStoreSelector `json:"store,omitempty"`
	Path  []string        `json:"path,omitempty"`
	Key   string          `json:"key"`
	Value KVValue         `json:"value"`
}
