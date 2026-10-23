// Package webdemo implements the read-only admin API used by the browser demo.
package webdemo

import (
	"fmt"
	"sync"

	"github.com/asciimoth/almagest/bin/api"
)

const mutationRejectedMessage = "the web demo backend is read-only"

// Backend serves a small, in-process subset of the admin API.
type Backend struct {
	mu                 sync.Mutex
	snapshot           api.WorkspaceSnapshot
	nextSubscriptionID uint64
}

// NewBackend creates a backend that exposes the specified node classes.
func NewBackend(snapshot api.WorkspaceSnapshot) *Backend {
	snapshot.Nodes = mapOrEmpty(snapshot.Nodes)
	snapshot.Ports = mapOrEmpty(snapshot.Ports)
	snapshot.Links = mapOrEmpty(snapshot.Links)
	if snapshot.Classes == nil {
		snapshot.Classes = []api.NodeClassSnapshot{}
	}
	return &Backend{snapshot: snapshot, nextSubscriptionID: 1}
}

// Handle accepts one JSON admin request and returns one JSON response.
func (b *Backend) Handle(data []byte) []byte {
	b.mu.Lock()
	defer b.mu.Unlock()

	msg, err := api.UnmarshalMessage(data)
	if err != nil {
		return marshalError("0", api.ErrorInvalidPayload, err.Error())
	}
	if msg.Kind != api.MessageRequest {
		return marshalError(msg.ID, api.ErrorBadRequest, "web demo API accepts request messages")
	}

	var response api.Message
	if isMutation(msg.Surface, msg.Method) {
		response, err = api.NewErrorResponse(msg.ID, api.ErrorMutationRejected, mutationRejectedMessage, nil)
	} else {
		response, err = b.read(msg)
	}
	if err != nil {
		return marshalError(msg.ID, api.ErrorInternal, err.Error())
	}
	encoded, err := api.MarshalMessage(response)
	if err != nil {
		return marshalError(msg.ID, api.ErrorInternal, err.Error())
	}
	return encoded
}

func (b *Backend) read(msg api.Message) (api.Message, error) {
	if hasNoItem(msg.Surface, msg.Method) {
		return api.NewErrorResponse(msg.ID, api.ErrorNotFound, "the web demo contains no matching data", nil)
	}
	result, ok := b.readResult(msg.Surface, msg.Method)
	if !ok {
		return api.NewErrorResponse(msg.ID, api.ErrorInvalidMethod, "api method is not implemented", nil)
	}
	return api.NewResponse(msg.ID, result)
}

func (b *Backend) readResult(surface api.Surface, method string) (any, bool) { //nolint:cyclop
	switch surface {
	case api.SurfaceAdmin:
		switch api.AdminMethod(method) {
		case api.MethodAdminHello:
			return api.HelloResponse{
				Version:       api.Version,
				Daemon:        "webdemo",
				ExecutionMode: "direct",
				ProjectVersionStatus: api.ProjectVersionStatus{
					Version:   "webdemo",
					Highlight: api.ProjectVersionHighlightNone,
				},
				Surfaces: surfaceCatalog(),
			}, true
		case api.MethodAdminPing:
			return api.PingResponse{}, true
		case api.MethodAdminColorMap:
			return map[string]any{}, true
		case api.MethodAdminLocaleSnapshot:
			return api.LocaleSnapshot{
				Requested: "en",
				Effective: "en",
				Available: []api.AvailableLocale{{Locale: "en", Name: "English"}},
				Panel:     map[string]string{},
				Revision:  1,
			}, true
		}
	case api.SurfaceConfig:
		if api.ConfigMethod(method) == api.MethodConfigSnapshot {
			return api.ConfigSnapshot{Text: "", Revision: "", ReadOnly: true}, true
		}
	case api.SurfaceDNS:
		switch api.DNSMethod(method) {
		case api.MethodDNSCachesSnapshot:
			return api.DNSCachesSnapshot{}, true
		case api.MethodDNSKeysSnapshot:
			return api.DNSKeysSnapshot{Exists: false}, true
		case api.MethodDNSValueSnapshot:
			return api.DNSValueSnapshot{Found: false}, true
		case api.MethodDNSSubscribeCaches:
			return api.DNSSubscribeCachesResponse{SubscriptionID: b.subscriptionID(), Snapshot: api.DNSCachesSnapshot{}}, true
		case api.MethodDNSSubscribeKeys:
			return api.DNSSubscribeKeysResponse{SubscriptionID: b.subscriptionID(), Snapshot: api.DNSKeysSnapshot{Exists: false}}, true
		case api.MethodDNSSubscribeValue:
			return api.DNSSubscribeValueResponse{SubscriptionID: b.subscriptionID(), Snapshot: api.DNSValueSnapshot{Found: false}}, true
		case api.MethodDNSUnsubscribe:
			return struct{}{}, true
		}
	case api.SurfaceDocs:
		switch api.DocsMethod(method) {
		case api.MethodDocsTree:
			return api.DocsTreeResponse{Locale: "en", Available: []api.AvailableLocale{}, Topics: []api.DocsTopic{}, MarkMessages: map[api.DocsPageMark]string{}}, true
		case api.MethodDocsPage:
			return api.DocsPageResponse{Locale: "en"}, true
		}
	case api.SurfaceEngine:
		switch api.EngineMethod(method) {
		case api.MethodEngineWorkspaceSnapshot:
			return api.WorkspaceSnapshotResponse{Snapshot: b.snapshot}, true
		case api.MethodEngineWorkersSnapshot:
			return map[string]any{"nodes": map[string]any{}, "workers": map[string]any{}}, true
		case api.MethodEngineSubscribeNotifications:
			return api.SubscribeNotificationsResponse{SubscriptionID: b.subscriptionID()}, true
		case api.MethodEngineUnsubscribeNotifications,
			api.MethodEngineRequestFullSnapshot,
			api.MethodEngineSubscribeNodeMenu,
			api.MethodEngineUnsubscribeNodeMenu:
			return struct{}{}, true
		case api.MethodEngineCopy:
			return api.CopyResponse{Data: ""}, true
		case api.MethodEngineNodeClassLongDescription:
			return api.NodeClassLongDescriptionResponse{Description: ""}, true
		}
	case api.SurfaceKV:
		switch api.KVMethod(method) {
		case api.MethodKVStoresSnapshot:
			return api.KVStoresSnapshot{Stores: []api.KVStoreInfo{}}, true
		case api.MethodKVEntriesSnapshot:
			return api.KVEntriesSnapshot{Exists: false}, true
		case api.MethodKVValueSnapshot:
			return api.KVValueSnapshot{Found: false}, true
		case api.MethodKVSubscribeStores:
			return api.KVSubscribeStoresResponse{SubscriptionID: b.subscriptionID(), Snapshot: api.KVStoresSnapshot{Stores: []api.KVStoreInfo{}}}, true
		case api.MethodKVSubscribeEntries:
			return api.KVSubscribeEntriesResponse{SubscriptionID: b.subscriptionID(), Snapshot: api.KVEntriesSnapshot{Exists: false}}, true
		case api.MethodKVSubscribeValue:
			return api.KVSubscribeValueResponse{SubscriptionID: b.subscriptionID(), Snapshot: api.KVValueSnapshot{Found: false}}, true
		case api.MethodKVUnsubscribe:
			return struct{}{}, true
		}
	case api.SurfaceLog:
		switch api.LogMethod(method) {
		case api.MethodLogSubscribe:
			return api.SubscribeLogsResponse{SubscriptionID: b.subscriptionID(), Entries: []api.LogEntry{}}, true
		case api.MethodLogUnsubscribe:
			return struct{}{}, true
		}
	case api.SurfaceSecrets:
		switch api.SecretsMethod(method) {
		case api.MethodSecretsSnapshot:
			return api.SecretsSnapshot{}, true
		case api.MethodSecretsSubscribe:
			return api.SecretsSubscribeResponse{SubscriptionID: b.subscriptionID(), Snapshot: api.SecretsSnapshot{}}, true
		case api.MethodSecretsUnsubscribe:
			return struct{}{}, true
		}
	case api.SurfaceSnapshots:
		switch api.SnapshotsMethod(method) {
		case api.MethodSnapshotsList:
			return api.SnapshotsListResponse{Snapshots: []api.SnapshotInfo{}}, true
		}
	case api.SurfaceTLS:
		switch api.TLSMethod(method) {
		case api.MethodTLSSnapshot:
			return api.TLSSnapshot{}, true
		case api.MethodTLSSubscribe:
			return api.TLSSubscribeResponse{SubscriptionID: b.subscriptionID(), Snapshot: api.TLSSnapshot{}}, true
		case api.MethodTLSUnsubscribe:
			return struct{}{}, true
		}
	}
	return nil, false
}

func hasNoItem(surface api.Surface, method string) bool {
	if surface == api.SurfaceSnapshots && api.SnapshotsMethod(method) == api.MethodSnapshotsExport {
		return true
	}
	if surface == api.SurfaceTLS {
		switch api.TLSMethod(method) {
		case api.MethodTLSBuiltinCA, api.MethodTLSBuiltinCRL:
			return true
		}
	}
	if surface != api.SurfaceEngine {
		return false
	}
	switch api.EngineMethod(method) {
	case api.MethodEngineNodeClassSnapshot,
		api.MethodEngineNodeSnapshot,
		api.MethodEnginePortSnapshot,
		api.MethodEngineLinkSnapshot:
		return true
	default:
		return false
	}
}

func (b *Backend) subscriptionID() uint64 {
	id := b.nextSubscriptionID
	b.nextSubscriptionID++
	return id
}

func isMutation(surface api.Surface, method string) bool {
	switch surface {
	case api.SurfaceAdmin:
		switch api.AdminMethod(method) {
		case api.MethodAdminAcquireMutation, api.MethodAdminRenewMutation, api.MethodAdminReleaseMutation, api.MethodAdminSetLocale:
			return true
		}
	case api.SurfaceDNS:
		switch api.DNSMethod(method) {
		case api.MethodDNSSet, api.MethodDNSDelete, api.MethodDNSDrop:
			return true
		}
	case api.SurfaceKV:
		switch api.KVMethod(method) {
		case api.MethodKVEnsure, api.MethodKVSet, api.MethodKVDelete, api.MethodKVDrop:
			return true
		}
	}
	return api.RequiresMutationLease(surface, method)
}

func surfaceCatalog() map[string]any {
	return map[string]any{
		string(api.SurfaceAdmin):     map[string]any{"methods": api.AdminMethods()},
		string(api.SurfaceConfig):    map[string]any{"methods": api.ConfigMethods()},
		string(api.SurfaceDNS):       map[string]any{"methods": api.DNSMethods()},
		string(api.SurfaceDocs):      map[string]any{"methods": api.DocsMethods()},
		string(api.SurfaceEngine):    map[string]any{"methods": api.EngineMethods()},
		string(api.SurfaceKV):        map[string]any{"methods": api.KVMethods()},
		string(api.SurfaceLog):       map[string]any{"methods": api.LogMethods()},
		string(api.SurfaceSecrets):   map[string]any{"methods": api.SecretsMethods()},
		string(api.SurfaceSnapshots): map[string]any{"methods": api.SnapshotsMethods()},
		string(api.SurfaceSystem):    map[string]any{"methods": []string{}},
		string(api.SurfaceTLS):       map[string]any{"methods": api.TLSMethods()},
	}
}

func marshalError(id string, code api.ErrorCode, message string) []byte {
	if id == "" {
		id = "0"
	}
	response, err := api.NewErrorResponse(id, code, message, nil)
	if err != nil {
		return []byte(fmt.Sprintf(`{"version":"v1","kind":"response","id":%q,"error":{"code":"internal","message":%q}}`, id, err.Error()))
	}
	encoded, err := api.MarshalMessage(response)
	if err != nil {
		return []byte(fmt.Sprintf(`{"version":"v1","kind":"response","id":%q,"error":{"code":"internal","message":%q}}`, id, err.Error()))
	}
	return encoded
}

func mapOrEmpty[K comparable, V any](value map[K]V) map[K]V {
	if value == nil {
		return map[K]V{}
	}
	return value
}
