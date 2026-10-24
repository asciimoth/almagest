package webdemo

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/asciimoth/almagest/bin/api"
	almagest "github.com/asciimoth/almagest/lib"
	"github.com/asciimoth/almagest/lib/storage"
	"github.com/asciimoth/configer/hujson"
	"github.com/asciimoth/formular"
	"github.com/asciimoth/pasta/pasta"
)

var ErrConfigConflict = errors.New("web demo config revision conflict")

// NewLiveBackend creates an admin API backed by a running engine and the
// storage services used by that engine. The backend takes ownership of engine.
func NewLiveBackend(engine almagest.Engine, stores *Storage, config *ConfigStore, logs *LogFactory) (*Backend, error) {
	if engine == nil || stores == nil || config == nil || logs == nil {
		return nil, fmt.Errorf("web demo backend: missing dependency")
	}
	backend := &Backend{
		engine: engine, stores: stores, config: config, logs: logs,
		nextSubscriptionID: 1, engineSubscriptions: map[uint64]uint64{}, logSubscriptions: map[uint64]uint64{},
		dnsSubscriptions: map[uint64]dnsSubscription{}, kvSubscriptions: map[uint64]kvSubscription{},
		secretSubscriptions: map[uint64]struct{}{},
	}
	if dns, ok := stores.DNS.(*memoryDNSManager); ok {
		dns.mu.Lock()
		dns.onChange = backend.queueRuntimeDNSChange
		dns.mu.Unlock()
	}
	if kv, ok := stores.KV.(*kvManager); ok {
		kv.mu.Lock()
		kv.onChange = backend.queueKVSubscriptionSnapshots
		kv.mu.Unlock()
	}
	if secrets, ok := stores.Secrets.(*secretsStorage); ok {
		secrets.mu.Lock()
		secrets.onChange = backend.queueSecretStorageChanges
		secrets.mu.Unlock()
	}
	return backend, nil
}

func (b *Backend) liveRead(msg api.Message) (api.Message, error) { //nolint:cyclop
	switch msg.Surface {
	case api.SurfaceAdmin:
		return b.handleAdmin(msg)
	case api.SurfaceConfig:
		return b.handleConfig(msg)
	case api.SurfaceDNS:
		return b.handleDNS(msg)
	case api.SurfaceEngine:
		return b.handleEngine(msg)
	case api.SurfaceKV:
		return b.handleKV(msg)
	case api.SurfaceLog:
		return b.handleLog(msg)
	case api.SurfaceSecrets:
		return b.handleSecrets(msg)
	case api.SurfaceSnapshots:
		return b.handleSnapshots(msg)
	case api.SurfaceTLS:
		return b.handleTLS(msg)
	default:
		return requestError(msg.ID, api.ErrorInvalidMethod, "api surface is not implemented")
	}
}

func (b *Backend) handleAdmin(msg api.Message) (api.Message, error) {
	switch api.AdminMethod(msg.Method) {
	case api.MethodAdminHello:
		if _, err := payload[api.HelloRequest](msg); err != nil {
			return invalidPayload(msg.ID, err)
		}
		return api.NewResponse(msg.ID, api.HelloResponse{
			Version: api.Version, Daemon: "webdemo", ExecutionMode: "direct",
			ProjectVersionStatus: api.ProjectVersionStatus{Version: "webdemo", Highlight: api.ProjectVersionHighlightNone},
			Surfaces:             liveSurfaceCatalog(),
		})
	case api.MethodAdminPing:
		req, err := payload[api.PingRequest](msg)
		if err != nil {
			return invalidPayload(msg.ID, err)
		}
		return api.NewResponse(msg.ID, api.PingResponse(req))
	case api.MethodAdminColorMap:
		return api.NewResponse(msg.ID, map[string]any{})
	default:
		return requestError(msg.ID, api.ErrorInvalidMethod, "api method is not implemented")
	}
}

func (b *Backend) handleConfig(msg api.Message) (api.Message, error) {
	switch api.ConfigMethod(msg.Method) {
	case api.MethodConfigSnapshot:
		text, revision := b.config.Snapshot()
		return api.NewResponse(msg.ID, api.ConfigSnapshot{Text: text, Revision: revision})
	case api.MethodConfigUpdate:
		req, err := payload[api.ConfigUpdateRequest](msg)
		if err != nil {
			return invalidPayload(msg.ID, err)
		}
		_, currentRevision := b.config.Snapshot()
		if req.Revision != "" && req.Revision != currentRevision {
			return requestError(msg.ID, api.ErrorConflict, ErrConfigConflict.Error())
		}
		text := req.Text
		if strings.TrimSpace(text) == "" {
			text = "{}"
		}
		next, err := hujson.Parse([]byte(text))
		if err != nil {
			return invalidPayload(msg.ID, err)
		}
		previous, err := b.config.Parse()
		if err != nil {
			return requestError(msg.ID, api.ErrorInternal, err.Error())
		}
		changes, err := b.engine.ReconcileConfig(next)
		if err != nil {
			return requestError(msg.ID, api.ErrorBadRequest, err.Error())
		}
		if err = b.engine.SaveConfig(next); err != nil {
			_, _ = b.engine.ReconcileConfig(previous)
			return requestError(msg.ID, api.ErrorInternal, err.Error())
		}
		_, err = b.config.Store(next.Pack(), req.Revision)
		if err != nil {
			_, _ = b.engine.ReconcileConfig(previous)
			if errors.Is(err, ErrConfigConflict) {
				return requestError(msg.ID, api.ErrorConflict, err.Error())
			}
			return requestError(msg.ID, api.ErrorInternal, err.Error())
		}
		storedText, revision := b.config.Snapshot()
		return api.NewResponse(msg.ID, api.ConfigUpdateResponse{
			Snapshot: api.ConfigSnapshot{Text: storedText, Revision: revision}, Changes: changes,
		})
	default:
		return requestError(msg.ID, api.ErrorInvalidMethod, "api method is not implemented")
	}
}

func (b *Backend) handleEngine(msg api.Message) (api.Message, error) { //nolint:cyclop,maintidx
	switch api.EngineMethod(msg.Method) {
	case api.MethodEngineWorkspaceSnapshot:
		req, err := payload[api.WorkspaceRequest](msg)
		if err != nil {
			return invalidPayload(msg.ID, err)
		}
		s, ok := b.engine.WorkspaceSnapshot(req.Workspace)
		if !ok {
			return requestError(msg.ID, api.ErrorNotFound, "workspace was not found")
		}
		return api.NewResponse(msg.ID, api.WorkspaceSnapshotResponse{Snapshot: api.NewWorkspaceSnapshot(s, nil)})
	case api.MethodEngineWorkersSnapshot:
		req, err := payload[api.WorkspaceRequest](msg)
		if err != nil {
			return invalidPayload(msg.ID, err)
		}
		s, ok := b.engine.WorkspaceSnapshot(req.Workspace)
		if !ok {
			return requestError(msg.ID, api.ErrorNotFound, "workspace was not found")
		}
		workers, ok := b.engine.WorkersSnapshot(req.Workspace)
		if !ok {
			return requestError(msg.ID, api.ErrorNotFound, "workspace was not found")
		}
		nodes := map[uint64]pasta.NodeSnapshot{}
		for _, worker := range workers {
			if node, found := s.Nodes[worker.Node]; found {
				nodes[worker.Node] = node
			}
		}
		return api.NewResponse(msg.ID, api.WorkersSnapshotResponse{Nodes: nodes, Workers: workers})
	case api.MethodEngineNodeClassSnapshot:
		req, err := payload[api.ClassRequest](msg)
		if err != nil {
			return invalidPayload(msg.ID, err)
		}
		s, ok := b.engine.NodeClassSnapshot(req.Workspace, req.Class)
		if !ok {
			return requestError(msg.ID, api.ErrorNotFound, "node class was not found")
		}
		return api.NewResponse(msg.ID, api.NodeClassSnapshotResponse{Snapshot: api.NewNodeClassSnapshot(s, false)})
	case api.MethodEngineNodeSnapshot:
		req, err := payload[api.IDRequest](msg)
		if err != nil {
			return invalidPayload(msg.ID, err)
		}
		s, ok := b.engine.NodeSnapshot(req.Workspace, req.ID)
		if !ok {
			return requestError(msg.ID, api.ErrorNotFound, "node was not found")
		}
		return api.NewResponse(msg.ID, api.NodeSnapshotResponse{Snapshot: s})
	case api.MethodEnginePortSnapshot:
		req, err := payload[api.IDRequest](msg)
		if err != nil {
			return invalidPayload(msg.ID, err)
		}
		s, ok := b.engine.PortSnapshot(req.Workspace, req.ID)
		if !ok {
			return requestError(msg.ID, api.ErrorNotFound, "port was not found")
		}
		return api.NewResponse(msg.ID, api.PortSnapshotResponse{Snapshot: s})
	case api.MethodEngineLinkSnapshot:
		req, err := payload[api.IDRequest](msg)
		if err != nil {
			return invalidPayload(msg.ID, err)
		}
		s, ok := b.engine.LinkSnapshot(req.Workspace, req.ID)
		if !ok {
			return requestError(msg.ID, api.ErrorNotFound, "link was not found")
		}
		return api.NewResponse(msg.ID, api.LinkSnapshotResponse{Snapshot: s})
	case api.MethodEngineNodeClassLongDescription:
		req, err := payload[api.ClassRequest](msg)
		if err != nil {
			return invalidPayload(msg.ID, err)
		}
		d, ok := b.engine.NodeClassLongDescription(req.Workspace, req.Class)
		if !ok {
			return requestError(msg.ID, api.ErrorNotFound, "node class was not found")
		}
		return api.NewResponse(msg.ID, api.NodeClassLongDescriptionResponse{Description: d})
	case api.MethodEngineSubscribeNotifications:
		if _, err := payload[api.WorkspaceRequest](msg); err != nil {
			return invalidPayload(msg.ID, err)
		}
		publicID := b.subscriptionID()
		engineID := b.engine.SubscribeNotifications(func(notification almagest.EngineNotification) { b.queueEngineNotification(publicID, notification) })
		if engineID == 0 {
			return requestError(msg.ID, api.ErrorUnavailable, "notification subscription failed")
		}
		b.engineSubscriptions[publicID] = engineID
		return api.NewResponse(msg.ID, api.SubscribeNotificationsResponse{SubscriptionID: publicID})
	case api.MethodEngineUnsubscribeNotifications:
		req, err := payload[api.SubscriptionRequest](msg)
		if err != nil {
			return invalidPayload(msg.ID, err)
		}
		if id := b.engineSubscriptions[req.SubscriptionID]; id != 0 {
			b.engine.UnsubscribeNotifications(id)
			delete(b.engineSubscriptions, req.SubscriptionID)
		}
		return api.NewResponse(msg.ID, struct{}{})
	case api.MethodEngineRequestFullSnapshot:
		req, err := payload[api.SubscriptionRequest](msg)
		if err != nil {
			return invalidPayload(msg.ID, err)
		}
		if id := b.engineSubscriptions[req.SubscriptionID]; id != 0 {
			b.engine.RequestFullSnapshot(id)
		}
		return api.NewResponse(msg.ID, struct{}{})
	case api.MethodEngineSubscribeNodeMenu, api.MethodEngineUnsubscribeNodeMenu:
		req, err := payload[api.NodeMenuSubscriptionRequest](msg)
		if err != nil {
			return invalidPayload(msg.ID, err)
		}
		engineID := b.engineSubscriptions[req.SubscriptionID]
		if api.EngineMethod(msg.Method) == api.MethodEngineSubscribeNodeMenu {
			b.engine.SubscribeNodeMenu(req.Workspace, req.Node, engineID)
		} else {
			b.engine.UnsubscribeNodeMenu(req.Workspace, req.Node, engineID)
		}
		return api.NewResponse(msg.ID, struct{}{})
	case api.MethodEngineAddNode:
		req, err := payload[api.AddNodeRequest](msg)
		if err != nil {
			return invalidPayload(msg.ID, err)
		}
		id, err := b.engine.AddNode(req.Workspace, req.Class, req.Name)
		if err != nil {
			return requestError(msg.ID, api.ErrorBadRequest, err.Error())
		}
		return b.engineMutationResponse(msg.ID, api.AddNodeResponse{ID: id})
	case api.MethodEngineAddLink:
		req, err := payload[api.AddLinkRequest](msg)
		if err != nil {
			return invalidPayload(msg.ID, err)
		}
		id, typ, err := b.engine.AddLink(req.Workspace, req.PortA, req.PortB)
		if err != nil {
			return requestError(msg.ID, api.ErrorBadRequest, err.Error())
		}
		return b.engineMutationResponse(msg.ID, api.AddLinkResponse{ID: id, Type: typ})
	case api.MethodEngineReplaceNodeWithPlaceholder:
		req, err := payload[api.IDRequest](msg)
		if err != nil {
			return invalidPayload(msg.ID, err)
		}
		if err = b.engine.ReplaceNodeWithPlaceholder(req.Workspace, req.ID); err != nil {
			return requestError(msg.ID, api.ErrorBadRequest, err.Error())
		}
		return b.engineMutationResponse(msg.ID, struct{}{})
	case api.MethodEngineRemoveNode:
		req, err := payload[api.IDRequest](msg)
		if err != nil {
			return invalidPayload(msg.ID, err)
		}
		b.engine.RemoveNode(req.Workspace, req.ID)
		return b.engineMutationResponse(msg.ID, struct{}{})
	case api.MethodEngineRemoveNodes:
		req, err := payload[api.IDsRequest](msg)
		if err != nil {
			return invalidPayload(msg.ID, err)
		}
		b.engine.RemoveNodes(req.Workspace, req.IDs)
		return b.engineMutationResponse(msg.ID, struct{}{})
	case api.MethodEngineRemoveNodesByClass:
		req, err := payload[api.ClassRequest](msg)
		if err != nil {
			return invalidPayload(msg.ID, err)
		}
		b.engine.RemoveNodesByClass(req.Workspace, req.Class)
		return b.engineMutationResponse(msg.ID, struct{}{})
	case api.MethodEngineRemoveLink:
		req, err := payload[api.IDRequest](msg)
		if err != nil {
			return invalidPayload(msg.ID, err)
		}
		b.engine.RemoveLink(req.Workspace, req.ID)
		return b.engineMutationResponse(msg.ID, struct{}{})
	case api.MethodEngineSetNodePosition:
		req, err := payload[api.SetNodePositionRequest](msg)
		if err != nil {
			return invalidPayload(msg.ID, err)
		}
		b.engine.SetNodePosition(req.Workspace, req.ID, req.Position)
		return b.engineMutationResponse(msg.ID, struct{}{})
	case api.MethodEngineMoveNodes:
		req, err := payload[api.MoveNodesRequest](msg)
		if err != nil {
			return invalidPayload(msg.ID, err)
		}
		b.engine.MoveNodes(req.Workspace, req.Positions)
		return b.engineMutationResponse(msg.ID, struct{}{})
	case api.MethodEngineSetNodeName:
		req, err := payload[api.SetNodeNameRequest](msg)
		if err != nil {
			return invalidPayload(msg.ID, err)
		}
		if err = b.engine.RenameNode(req.Workspace, req.ID, req.Name); err != nil {
			return requestError(msg.ID, api.ErrorBadRequest, err.Error())
		}
		return b.engineMutationResponse(msg.ID, struct{}{})
	case api.MethodEngineRemoveNodePopup, api.MethodEngineRemoveNodePopups, api.MethodEngineRemoveNodePopupsByType:
		req, err := payload[api.NodePopupRequest](msg)
		if err != nil {
			return invalidPayload(msg.ID, err)
		}
		switch api.EngineMethod(msg.Method) {
		case api.MethodEngineRemoveNodePopup:
			b.engine.RemoveNodePopup(req.Workspace, req.Node, req.Popup)
		case api.MethodEngineRemoveNodePopups:
			b.engine.RemoveNodePopups(req.Workspace, req.Node)
		case api.MethodEngineRemoveNodePopupsByType:
			b.engine.RemoveNodePopupsByType(req.Workspace, req.Node, req.Type)
		}
		return b.engineMutationResponse(msg.ID, struct{}{})
	case api.MethodEngineCopy:
		req, err := payload[api.CopyRequest](msg)
		if err != nil {
			return invalidPayload(msg.ID, err)
		}
		return api.NewResponse(msg.ID, api.CopyResponse{Data: b.engine.Copy(req.Workspace, req.IDs)})
	case api.MethodEnginePaste:
		req, err := payload[api.PasteRequest](msg)
		if err != nil {
			return invalidPayload(msg.ID, err)
		}
		ids := b.engine.Paste(req.Workspace, req.Data)
		return b.engineMutationResponse(msg.ID, api.PasteResponse{IDs: ids})
	case api.MethodEngineTriggerNode:
		req, err := payload[api.IDRequest](msg)
		if err != nil {
			return invalidPayload(msg.ID, err)
		}
		if err = b.engine.Trigger(req.Workspace, req.ID); err != nil {
			return requestError(msg.ID, api.ErrorBadRequest, err.Error())
		}
		return api.NewResponse(msg.ID, struct{}{})
	case api.MethodEngineSendInbox:
		req, err := payload[api.SendInboxRequest](msg)
		if err != nil {
			return invalidPayload(msg.ID, err)
		}
		b.engine.SendInbox(req.Workspace, req.InboxMessage())
		return api.NewResponse(msg.ID, struct{}{})
	case api.MethodEngineUndo, api.MethodEngineRedo:
		req, err := payload[api.WorkspaceRequest](msg)
		if err != nil {
			return invalidPayload(msg.ID, err)
		}
		if api.EngineMethod(msg.Method) == api.MethodEngineUndo {
			b.engine.Undo(req.Workspace)
		} else {
			b.engine.Redo(req.Workspace)
		}
		return b.engineMutationResponse(msg.ID, struct{}{})
	case api.MethodEngineSendNodeFormularMessage:
		req, err := payload[api.SendNodeFormularMessageRequest](msg)
		if err != nil {
			return invalidPayload(msg.ID, err)
		}
		message, err := decodeFormularMessage(req.Message)
		if err != nil {
			return invalidPayload(msg.ID, err)
		}
		_ = b.engine.SendNodeFormularMsgAndWait(req.Workspace, req.Node, message)
		return b.engineMutationResponse(msg.ID, struct{}{})
	default:
		return requestError(msg.ID, api.ErrorInvalidMethod, "api method is not implemented")
	}
}

func (b *Backend) engineMutationResponse(id string, result any) (api.Message, error) {
	cfg, err := b.config.Parse()
	if err != nil {
		return requestError(id, api.ErrorInternal, err.Error())
	}
	if err = b.engine.SaveConfig(cfg); err != nil {
		return requestError(id, api.ErrorInternal, err.Error())
	}
	if _, err = b.config.Store(cfg.Pack(), ""); err != nil {
		return requestError(id, api.ErrorInternal, err.Error())
	}
	return api.NewResponse(id, result)
}

func (b *Backend) queueEngineNotification(subscriptionID uint64, notification almagest.EngineNotification) {
	event, err := api.NewEvent(api.SurfaceEngine, string(api.EventEngineNotification), api.EngineNotificationEvent{
		SubscriptionID: subscriptionID, Notification: api.NewEngineNotification(notification, nil),
	})
	if err == nil {
		b.queueEvent(event)
	}
}

func (b *Backend) queueEvent(event api.Message) {
	b.eventMu.Lock()
	b.events = append(b.events, event)
	b.eventMu.Unlock()
}

// DrainEvents returns queued engine events. The Wasm transport calls this
// after each synchronous response.
func (b *Backend) DrainEvents() []byte {
	b.eventMu.Lock()
	defer b.eventMu.Unlock()
	if len(b.events) == 0 {
		return []byte("[]")
	}
	raw, err := json.Marshal(b.events)
	b.events = nil
	if err != nil {
		return []byte("[]")
	}
	return raw
}

type kvSubscriptionKind uint8

const (
	kvSubscribeStores kvSubscriptionKind = iota + 1
	kvSubscribeEntries
	kvSubscribeValue
)

type kvSubscription struct {
	kind  kvSubscriptionKind
	store api.KVStoreSelector
	path  []string
	key   string
}

func (b *Backend) handleKV(msg api.Message) (api.Message, error) { //nolint:cyclop
	switch api.KVMethod(msg.Method) {
	case api.MethodKVStoresSnapshot:
		return api.NewResponse(msg.ID, b.kvStoresSnapshot())
	case api.MethodKVEntriesSnapshot, api.MethodKVSubscribeEntries:
		req, err := payload[api.KVEntriesRequest](msg)
		if err != nil {
			return invalidPayload(msg.ID, err)
		}
		s, err := b.kvEntriesSnapshot(req)
		if err != nil {
			return requestError(msg.ID, api.ErrorBadRequest, err.Error())
		}
		if api.KVMethod(msg.Method) == api.MethodKVSubscribeEntries {
			id := b.subscriptionID()
			b.subscriptionMu.Lock()
			b.kvSubscriptions[id] = kvSubscription{kind: kvSubscribeEntries, store: req.Store, path: append([]string(nil), req.Path...)}
			b.subscriptionMu.Unlock()
			return api.NewResponse(msg.ID, api.KVSubscribeEntriesResponse{SubscriptionID: id, Snapshot: s})
		}
		return api.NewResponse(msg.ID, s)
	case api.MethodKVValueSnapshot, api.MethodKVSubscribeValue:
		req, err := payload[api.KVValueRequest](msg)
		if err != nil {
			return invalidPayload(msg.ID, err)
		}
		s, err := b.kvValueSnapshot(req)
		if err != nil {
			return requestError(msg.ID, api.ErrorBadRequest, err.Error())
		}
		if api.KVMethod(msg.Method) == api.MethodKVSubscribeValue {
			id := b.subscriptionID()
			b.subscriptionMu.Lock()
			b.kvSubscriptions[id] = kvSubscription{kind: kvSubscribeValue, store: req.Store, path: append([]string(nil), req.Path...), key: req.Key}
			b.subscriptionMu.Unlock()
			return api.NewResponse(msg.ID, api.KVSubscribeValueResponse{SubscriptionID: id, Snapshot: s})
		}
		return api.NewResponse(msg.ID, s)
	case api.MethodKVSubscribeStores:
		id := b.subscriptionID()
		b.subscriptionMu.Lock()
		b.kvSubscriptions[id] = kvSubscription{kind: kvSubscribeStores}
		b.subscriptionMu.Unlock()
		return api.NewResponse(msg.ID, api.KVSubscribeStoresResponse{SubscriptionID: id, Snapshot: b.kvStoresSnapshot()})
	case api.MethodKVUnsubscribe:
		req, err := payload[api.KVSubscriptionRequest](msg)
		if err != nil {
			return invalidPayload(msg.ID, err)
		}
		b.subscriptionMu.Lock()
		delete(b.kvSubscriptions, req.SubscriptionID)
		b.subscriptionMu.Unlock()
		return api.NewResponse(msg.ID, struct{}{})
	case api.MethodKVEnsure:
		req, err := payload[api.KVEntriesRequest](msg)
		if err != nil {
			return invalidPayload(msg.ID, err)
		}
		store, err := b.kvStore(req.Store)
		if err == nil {
			err = store.Subtree(req.Path...).Update(context.Background(), func(tx storage.KVWriteTx) error { return tx.Ensure() })
		}
		if err != nil {
			return requestError(msg.ID, api.ErrorBadRequest, err.Error())
		}
		return api.NewResponse(msg.ID, struct{}{})
	case api.MethodKVSet:
		req, err := payload[api.KVSetRequest](msg)
		if err != nil {
			return invalidPayload(msg.ID, err)
		}
		value, err := kvValueFromAPI(req.Value)
		if err == nil {
			var store storage.KVStore
			store, err = b.kvStore(req.Store)
			if err == nil {
				err = store.Subtree(req.Path...).Update(context.Background(), func(tx storage.KVWriteTx) error { return tx.Set(req.Key, value) })
			}
		}
		if err != nil {
			return requestError(msg.ID, api.ErrorBadRequest, err.Error())
		}
		return api.NewResponse(msg.ID, b.mustKVValueSnapshot(api.KVValueRequest{Store: req.Store, Path: req.Path, Key: req.Key}))
	case api.MethodKVDelete:
		req, err := payload[api.KVValueRequest](msg)
		if err != nil {
			return invalidPayload(msg.ID, err)
		}
		store, err := b.kvStore(req.Store)
		if err == nil {
			err = store.Subtree(req.Path...).Update(context.Background(), func(tx storage.KVWriteTx) error { return tx.Delete(req.Key) })
		}
		if err != nil {
			return requestError(msg.ID, api.ErrorBadRequest, err.Error())
		}
		return api.NewResponse(msg.ID, struct{}{})
	case api.MethodKVDrop:
		req, err := payload[api.KVEntriesRequest](msg)
		if err != nil {
			return invalidPayload(msg.ID, err)
		}
		store, err := b.kvStore(req.Store)
		if err == nil {
			err = store.Subtree(req.Path...).Update(context.Background(), func(tx storage.KVWriteTx) error { return tx.Drop() })
		}
		if err != nil {
			return requestError(msg.ID, api.ErrorBadRequest, err.Error())
		}
		return api.NewResponse(msg.ID, struct{}{})
	default:
		return requestError(msg.ID, api.ErrorInvalidMethod, "api method is not implemented")
	}
}

func (b *Backend) queueKVSubscriptionSnapshots() {
	b.subscriptionMu.RLock()
	defer b.subscriptionMu.RUnlock()
	for id, subscription := range b.kvSubscriptions {
		var (
			event api.Message
			err   error
		)
		switch subscription.kind {
		case kvSubscribeStores:
			event, err = api.NewEvent(api.SurfaceKV, string(api.EventKVStoresUpdate), api.KVStoresEvent{SubscriptionID: id, Snapshot: b.kvStoresSnapshot()})
		case kvSubscribeEntries:
			snapshot, snapshotErr := b.kvEntriesSnapshot(api.KVEntriesRequest{Store: subscription.store, Path: subscription.path})
			if snapshotErr != nil {
				continue
			}
			event, err = api.NewEvent(api.SurfaceKV, string(api.EventKVEntriesUpdate), api.KVEntriesEvent{SubscriptionID: id, Snapshot: snapshot})
		case kvSubscribeValue:
			snapshot, snapshotErr := b.kvValueSnapshot(api.KVValueRequest{Store: subscription.store, Path: subscription.path, Key: subscription.key})
			if snapshotErr != nil {
				continue
			}
			event, err = api.NewEvent(api.SurfaceKV, string(api.EventKVValueUpdate), api.KVValueEvent{SubscriptionID: id, Snapshot: snapshot})
		}
		if err == nil {
			b.queueEvent(event)
		}
	}
}

func (b *Backend) kvStore(selector api.KVStoreSelector) (storage.KVStore, error) {
	if selector.Kind == "" || selector.Kind == api.KVStoreDefault {
		if selector.Name != "" {
			return nil, storage.ErrKVInvalidName
		}
		return b.stores.KV.DefaultStore(), nil
	}
	if selector.Kind == api.KVStorePlugin && selector.Name != "" {
		return b.stores.KV.PluginStore(selector.Name), nil
	}
	return nil, storage.ErrKVInvalidName
}

func (b *Backend) kvStoresSnapshot() api.KVStoresSnapshot {
	stores := []api.KVStoreInfo{{Kind: api.KVStoreDefault}}
	manager, ok := b.stores.KV.(*kvManager)
	if !ok {
		return api.KVStoresSnapshot{Stores: stores}
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	plugins := nodeAt(manager.state.Root, []string{"plugins"})
	if plugins != nil {
		for name := range plugins.Subtrees {
			stores = append(stores, api.KVStoreInfo{Kind: api.KVStorePlugin, Name: name})
		}
	}
	slices.SortFunc(stores[1:], func(a, c api.KVStoreInfo) int { return strings.Compare(a.Name, c.Name) })
	return api.KVStoresSnapshot{Stores: stores}
}

func (b *Backend) kvEntriesSnapshot(req api.KVEntriesRequest) (snapshot api.KVEntriesSnapshot, err error) {
	store, err := b.kvStore(req.Store)
	if err != nil {
		return snapshot, err
	}
	err = store.Subtree(req.Path...).View(context.Background(), func(tx storage.KVReadTx) error {
		snapshot.Exists = tx.Exists()
		if !snapshot.Exists {
			return nil
		}
		if err := tx.ForEachSubtree(func(name string) error {
			owner, e := tx.Subtree(name).Owner()
			if e == nil {
				snapshot.Entries = append(snapshot.Entries, api.KVEntryInfo{Kind: api.KVEntryBucket, Name: name, Owner: owner})
			}
			return e
		}); err != nil {
			return err
		}
		return tx.ForEachValue(func(name string, value storage.KValue) error {
			snapshot.Entries = append(snapshot.Entries, api.KVEntryInfo{Kind: api.KVEntryValue, Name: name, Owner: value.Owner, Size: len(value.Data), Hidden: value.Hidden, String: value.String})
			return nil
		})
	})
	slices.SortFunc(snapshot.Entries, func(a, c api.KVEntryInfo) int { return strings.Compare(a.Name, c.Name) })
	return snapshot, err
}

func (b *Backend) kvValueSnapshot(req api.KVValueRequest) (snapshot api.KVValueSnapshot, err error) {
	store, err := b.kvStore(req.Store)
	if err != nil {
		return snapshot, err
	}
	err = store.Subtree(req.Path...).View(context.Background(), func(tx storage.KVReadTx) error {
		value, found, e := tx.Get(req.Key)
		if e != nil || !found {
			return e
		}
		out := kvValueToAPI(*value)
		snapshot = api.KVValueSnapshot{Found: true, Value: &out}
		return nil
	})
	return snapshot, err
}
func (b *Backend) mustKVValueSnapshot(req api.KVValueRequest) api.KVValueSnapshot {
	s, _ := b.kvValueSnapshot(req)
	return s
}
func kvValueToAPI(value storage.KValue) api.KVValue {
	encoding, data := "base64", base64.StdEncoding.EncodeToString(value.Data)
	if value.String {
		encoding, data = "string", string(value.Data)
	}
	return api.KVValue{Data: data, Encoding: encoding, Owner: value.Owner, Size: len(value.Data), Hidden: value.Hidden, String: value.String}
}
func kvValueFromAPI(value api.KVValue) (storage.KValue, error) {
	encoding := strings.ToLower(value.Encoding)
	if encoding == "" {
		if value.String {
			encoding = "string"
		} else {
			encoding = "base64"
		}
	}
	var data []byte
	var err error
	switch encoding {
	case "string", "utf8", "plain":
		if !utf8.ValidString(value.Data) {
			return storage.KValue{}, storage.ErrKVInvalidValue
		}
		data = []byte(value.Data)
	case "base64":
		data, err = base64.StdEncoding.DecodeString(value.Data)
	default:
		err = storage.ErrKVInvalidValue
	}
	return storage.KValue{Data: data, Hidden: value.Hidden, String: value.String}, err
}

func (b *Backend) handleLog(msg api.Message) (api.Message, error) {
	switch api.LogMethod(msg.Method) {
	case api.MethodLogSubscribe:
		publicID := b.subscriptionID()
		logID, entries := b.logs.Subscribe(b.queueLogEntry)
		b.logSubscriptions[publicID] = logID
		return api.NewResponse(msg.ID, api.SubscribeLogsResponse{SubscriptionID: publicID, CacheSize: b.logs.CacheSize(), Entries: entries})
	case api.MethodLogUnsubscribe:
		req, err := payload[api.SubscriptionRequest](msg)
		if err != nil {
			return invalidPayload(msg.ID, err)
		}
		if id := b.logSubscriptions[req.SubscriptionID]; id != 0 {
			b.logs.Unsubscribe(id)
			delete(b.logSubscriptions, req.SubscriptionID)
		}
		return api.NewResponse(msg.ID, struct{}{})
	default:
		return requestError(msg.ID, api.ErrorInvalidMethod, "api method is not implemented")
	}
}

func (b *Backend) queueLogEntry(entry api.LogEntry) {
	event, err := api.NewEvent(api.SurfaceLog, string(api.EventLogEntry), entry)
	if err == nil {
		b.queueEvent(event)
	}
}

func (b *Backend) handleSecrets(msg api.Message) (api.Message, error) { //nolint:cyclop
	secrets, ok := b.stores.Secrets.(*secretsStorage)
	if !ok {
		return requestError(msg.ID, api.ErrorUnavailable, "secrets storage is unavailable")
	}
	switch api.SecretsMethod(msg.Method) {
	case api.MethodSecretsSnapshot, api.MethodSecretsSubscribe:
		snapshot := secrets.apiSnapshot()
		if api.SecretsMethod(msg.Method) == api.MethodSecretsSubscribe {
			id := b.subscriptionID()
			b.subscriptionMu.Lock()
			b.secretSubscriptions[id] = struct{}{}
			b.subscriptionMu.Unlock()
			return api.NewResponse(msg.ID, api.SecretsSubscribeResponse{SubscriptionID: id, Snapshot: snapshot})
		}
		return api.NewResponse(msg.ID, snapshot)
	case api.MethodSecretsUnsubscribe:
		req, err := payload[api.SecretsSubscriptionRequest](msg)
		if err != nil {
			return invalidPayload(msg.ID, err)
		}
		b.subscriptionMu.Lock()
		delete(b.secretSubscriptions, req.SubscriptionID)
		b.subscriptionMu.Unlock()
		return api.NewResponse(msg.ID, struct{}{})
	case api.MethodSecretsSetValue:
		req, err := payload[api.SecretsSetValueRequest](msg)
		if err != nil {
			return invalidPayload(msg.ID, err)
		}
		entry, err := secrets.apiSetValue(req.Key, req.Value)
		if err != nil {
			return requestError(msg.ID, api.ErrorBadRequest, err.Error())
		}
		return api.NewResponse(msg.ID, entry)
	case api.MethodSecretsSetNodes:
		req, err := payload[api.SecretsSetNodesRequest](msg)
		if err != nil {
			return invalidPayload(msg.ID, err)
		}
		for _, name := range req.Nodes {
			if _, found := b.engine.NodeByName(almagest.MainWorkspace, name); !found {
				return requestError(msg.ID, api.ErrorBadRequest, "secret allows unknown node")
			}
		}
		entry, err := secrets.apiSetNodes(req.Key, req.Nodes)
		if err != nil {
			return requestError(msg.ID, api.ErrorBadRequest, err.Error())
		}
		return api.NewResponse(msg.ID, entry)
	case api.MethodSecretsRename:
		req, err := payload[api.SecretsRenameRequest](msg)
		if err != nil {
			return invalidPayload(msg.ID, err)
		}
		entry, err := secrets.apiRename(req.Key, req.NewKey)
		if err != nil {
			return requestError(msg.ID, api.ErrorBadRequest, err.Error())
		}
		return api.NewResponse(msg.ID, entry)
	case api.MethodSecretsDelete:
		req, err := payload[api.SecretKeyRequest](msg)
		if err != nil {
			return invalidPayload(msg.ID, err)
		}
		if err = secrets.apiDelete(req.Key); err != nil {
			return requestError(msg.ID, api.ErrorBadRequest, err.Error())
		}
		return api.NewResponse(msg.ID, struct{}{})
	default:
		return requestError(msg.ID, api.ErrorInvalidMethod, "api method is not implemented")
	}
}

func (b *Backend) queueSecretChange(change api.SecretChange) {
	b.subscriptionMu.RLock()
	defer b.subscriptionMu.RUnlock()
	for id := range b.secretSubscriptions {
		event, err := api.NewEvent(api.SurfaceSecrets, string(api.EventSecretsUpdate), api.SecretsEvent{SubscriptionID: id, Change: change})
		if err == nil {
			b.queueEvent(event)
		}
	}
}

func (b *Backend) queueSecretStorageChanges(before, after map[string]persistedSecret) {
	for key := range before {
		if _, exists := after[key]; !exists {
			b.queueSecretChange(api.SecretChange{Key: key, Deleted: true})
		}
	}
	for key, record := range after {
		previous, existed := before[key]
		if existed && previous.Value == record.Value && slices.Equal(previous.Nodes, record.Nodes) {
			continue
		}
		entry := api.SecretEntry{Key: key, Value: record.Value, Nodes: append([]string(nil), record.Nodes...), Source: api.SecretSourceMutable}
		b.queueSecretChange(api.SecretChange{Key: key, Entry: &entry})
	}
}

func decodeFormularMessage(data json.RawMessage) (any, error) {
	if len(data) == 0 {
		return nil, errors.New("missing formular message")
	}
	var base formular.MessageBase
	if err := json.Unmarshal(data, &base); err != nil {
		return nil, err
	}
	switch base.Type {
	case formular.MessageFieldUpdate:
		return decodeAs[formular.FieldUpdateMessage](data)
	case formular.MessageFieldValidate:
		return decodeAs[formular.FieldValidateMessage](data)
	case formular.MessageFormApply:
		return decodeAs[formular.FormApplyMessage](data)
	case formular.MessageButtonPress:
		return decodeAs[formular.ButtonPressMessage](data)
	case formular.MessageAutocompleteRequest:
		return decodeAs[formular.AutocompleteRequestMessage](data)
	case formular.MessageDialogResponse:
		return decodeAs[formular.DialogResponseMessage](data)
	default:
		return nil, fmt.Errorf("unsupported formular message type %q", base.Type)
	}
}
func decodeAs[T any](raw json.RawMessage) (out T, err error) { err = json.Unmarshal(raw, &out); return }
func payload[T any](msg api.Message) (T, error)              { return api.DecodePayload[T](msg.Payload) }
func invalidPayload(id string, err error) (api.Message, error) {
	return requestError(id, api.ErrorInvalidPayload, err.Error())
}
func requestError(id string, code api.ErrorCode, message string) (api.Message, error) {
	return api.NewErrorResponse(id, code, message, nil)
}

func liveSurfaceCatalog() map[string]any {
	admin := []string{string(api.MethodAdminHello), string(api.MethodAdminPing), string(api.MethodAdminColorMap)}
	return map[string]any{
		string(api.SurfaceAdmin): map[string]any{"methods": admin}, string(api.SurfaceConfig): map[string]any{"methods": api.ConfigMethods()},
		string(api.SurfaceDNS): map[string]any{"methods": api.DNSMethods()}, string(api.SurfaceEngine): map[string]any{"methods": api.EngineMethods()},
		string(api.SurfaceKV): map[string]any{"methods": api.KVMethods()}, string(api.SurfaceLog): map[string]any{"methods": api.LogMethods()},
		string(api.SurfaceSecrets):   map[string]any{"methods": api.SecretsMethods()},
		string(api.SurfaceSnapshots): map[string]any{"methods": api.SnapshotsMethods()},
		string(api.SurfaceTLS):       map[string]any{"methods": api.TLSMethods()},
	}
}
