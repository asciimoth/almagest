package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/asciimoth/almagest/bin/api"
	almagest "github.com/asciimoth/almagest/lib"
	"github.com/asciimoth/almagest/lib/logging"
	"github.com/asciimoth/almagest/lib/storage"
	"github.com/asciimoth/configer/configer"
	sysnetdebug "github.com/asciimoth/gonnect/sysnet/debug"
	"github.com/asciimoth/pasta/pasta"
)

func TestAdminAPIRunAndStopWebServer(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	admin := &AdminAPI{webListener: listener}
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	admin.Run(ctx, cancel, &wg, nil, nil, nil)

	resp, err := http.Get("http://" + listener.Addr().String() + "/") //nolint:gosec,noctx
	if err != nil {
		cancel()
		wg.Wait()
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET / status = %s", resp.Status)
	}
	cancel()
	wg.Wait()
	admin.Stop()
	admin.Stop()
}

func TestPrepareAdminAPIAndWebListeners(t *testing.T) {
	prepared, err := PrepareAdminAPI(&Config{})
	if err != nil {
		t.Fatal(err)
	}
	prepared.Stop()

	listener, err := listenWebAdmin("127.0.0.1:0", AdminPeerFiltersConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	listener, err = listenWebAdmin("127.0.0.1:0", AdminPeerFiltersConfig{UIDs: []uint32{uint32(os.Getuid())}})
	if err != nil {
		t.Fatal(err)
	}
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestWebAdminOriginPolicy(t *testing.T) {
	for _, test := range []struct {
		name        string
		allow       bool
		origin      string
		wantHeaders bool
		wantStatus  int
	}{
		{name: "protected", wantHeaders: true, wantStatus: http.StatusNoContent},
		{
			name:        "cross origin denied",
			origin:      "https://example.invalid",
			wantHeaders: true,
			wantStatus:  http.StatusForbidden,
		},
		{
			name:       "cross origin enabled",
			allow:      true,
			origin:     "https://example.invalid",
			wantStatus: http.StatusNoContent,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusNoContent)
			})
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodGet, "/", nil)
			if test.origin != "" {
				request.Header.Set("Origin", test.origin)
			}
			webAdminOriginPolicy(next, test.allow).ServeHTTP(
				recorder,
				request,
			)
			if recorder.Code != test.wantStatus {
				t.Errorf("status = %d, want %d", recorder.Code, test.wantStatus)
			}
			for name, value := range map[string]string{
				"Content-Security-Policy":      "frame-ancestors 'none'",
				"Cross-Origin-Resource-Policy": "same-origin",
				"X-Frame-Options":              "DENY",
			} {
				got := recorder.Header().Get(name)
				if test.wantHeaders && got != value {
					t.Errorf("%s = %q, want %q", name, got, value)
				}
				if !test.wantHeaders && got != "" {
					t.Errorf("%s = %q, want empty", name, got)
				}
			}
		})
	}
}

func TestEngineWorkspaceSnapshotAPIClassesAreSortedList(t *testing.T) {
	engine, err := almagest.BuildEngine(
		context.Background(),
		configer.NewMemory(nil),
		logging.NewDebugLogFactory(16),
		&sysnetdebug.System{},
		nil,
		nil, nil,
		nil,

		[]pasta.NodeClass{
			adminAPINodeClass{name: "zeta/Untyped"},
			adminAPINodeClass{name: "alpha/Beta", primaryType: "alpha/value"},
			adminAPINodeClass{name: "alpha/Alpha", primaryType: "alpha/value"},
		},
		nil,
		false)

	if err != nil {
		t.Fatalf("BuildEngine error = %v, want nil", err)
	}
	t.Cleanup(func() {
		if err := engine.Close(); err != nil {
			t.Errorf("engine.Close error = %v, want nil", err)
		}
	})
	session := (&AdminAPI{experimentalNodeTypes: map[string]bool{
		"alpha/Beta": true,
	}}).newAPISession(engine, nil, nil)
	req := mustRequest(t, "1", api.SurfaceEngine, string(api.MethodEngineWorkspaceSnapshot), api.WorkspaceRequest{Workspace: almagest.MainWorkspace})

	resp, err := session.handleMessage(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Error != nil {
		t.Fatalf("response error = %#v, want nil", resp.Error)
	}

	var raw struct {
		Snapshot struct {
			Classes json.RawMessage `json:"classes"`
		} `json:"snapshot"`
	}
	if err := json.Unmarshal(resp.Result, &raw); err != nil {
		t.Fatal(err)
	}
	if trimmed := bytes.TrimSpace(raw.Snapshot.Classes); len(trimmed) == 0 || trimmed[0] != '[' {
		t.Fatalf("classes JSON = %s, want array", raw.Snapshot.Classes)
	}

	decoded, err := api.DecodePayload[api.WorkspaceSnapshotResponse](resp.Result)
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i < len(decoded.Snapshot.Classes); i++ {
		if api.CompareNodeClassSnapshots(decoded.Snapshot.Classes[i-1], decoded.Snapshot.Classes[i]) > 0 {
			t.Fatalf("classes are not sorted at indexes %d and %d: %#v then %#v",
				i-1, i, decoded.Snapshot.Classes[i-1], decoded.Snapshot.Classes[i])
		}
	}
	for _, class := range decoded.Snapshot.Classes {
		if class.Experimental != (class.Class == "alpha/Beta") {
			t.Fatalf("class %q experimental = %t, want %t", class.Class, class.Experimental, class.Class == "alpha/Beta")
		}
	}
}

func TestWorkersSnapshotResponseKeepsOnlyLiveWorkerParents(t *testing.T) {
	started := time.Date(2026, 6, 30, 12, 0, 0, 0, time.UTC)
	resp := workersSnapshotResponse(
		map[uint64]pasta.NodeSnapshot{
			10: {Name: "live", Class: "test/live"},
			20: {Name: "placeholder", Class: "test/placeholder", Placeholder: true},
			99: {Name: "idle", Class: "test/idle"},
		},
		map[uint64]almagest.WorkerSnapshot{
			1: {ID: 1, Node: 10, WorkerName: "live worker", StartedAt: started},
			2: {ID: 2, Node: 20, WorkerName: "placeholder worker", StartedAt: started},
			3: {ID: 3, Node: 30, Name: "gone", Class: "test/gone", WorkerName: "orphan", StartedAt: started, Orphan: true},
		},
	)

	if len(resp.Workers) != 3 {
		t.Fatalf("workers = %d, want 3", len(resp.Workers))
	}
	if _, ok := resp.Nodes[10]; !ok {
		t.Fatal("live worker parent missing")
	}
	if node, ok := resp.Nodes[20]; !ok || !node.Placeholder {
		t.Fatalf("placeholder parent = %#v, want present placeholder", node)
	}
	if _, ok := resp.Nodes[30]; ok {
		t.Fatal("gone worker parent should not be present")
	}
	if _, ok := resp.Nodes[99]; ok {
		t.Fatal("idle node should not be included")
	}
}

func TestKVSubscriptionSendsSnapshotEventsAndUnsubscribesDescendants(t *testing.T) {
	manager, err := openKVManager(filepath.Join(t.TempDir(), "kv.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close() //nolint:errcheck

	var events []api.Message
	session := (&AdminAPI{kv: manager}).newAPISession(nil, nil, func(msg api.Message) error {
		if msg.Kind == api.MessageEvent && msg.Surface == api.SurfaceKV {
			events = append(events, msg)
		}
		return nil
	})
	defer session.close()

	subEntries := mustRequest(t, "1", api.SurfaceKV, string(api.MethodKVSubscribeEntries), api.KVEntriesRequest{})
	resp, err := session.handleMessage(subEntries)
	if err != nil {
		t.Fatal(err)
	}
	entriesResp, err := api.DecodePayload[api.KVSubscribeEntriesResponse](resp.Result)
	if err != nil {
		t.Fatal(err)
	}
	if entriesResp.SubscriptionID == 0 || !entriesResp.Snapshot.Exists {
		t.Fatalf("entries subscribe response = %#v, want live subscription and root snapshot", entriesResp)
	}

	subValue := mustRequest(t, "2", api.SurfaceKV, string(api.MethodKVSubscribeValue), api.KVValueRequest{
		Path: []string{"nested"},
		Key:  "key",
	})
	resp, err = session.handleMessage(subValue)
	if err != nil {
		t.Fatal(err)
	}
	valueResp, err := api.DecodePayload[api.KVSubscribeValueResponse](resp.Result)
	if err != nil {
		t.Fatal(err)
	}
	if valueResp.SubscriptionID == 0 || valueResp.Snapshot.Found {
		t.Fatalf("value subscribe response = %#v, want live missing-value subscription", valueResp)
	}

	events = nil
	setReq := mustRequest(t, "3", api.SurfaceKV, string(api.MethodKVSet), api.KVSetRequest{
		Key: "root",
		Value: api.KVValue{
			Data:     "value",
			Encoding: "string",
			String:   true,
		},
	})
	if _, err := session.handleMessage(setReq); err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Fatalf("events after set = %d, want one snapshot per subscription", len(events))
	}
	var entriesEvent api.KVEntriesEvent
	for _, event := range events {
		if event.Method == string(api.EventKVEntriesUpdate) {
			if err := json.Unmarshal(event.Payload, &entriesEvent); err != nil {
				t.Fatal(err)
			}
		}
	}
	if entriesEvent.SubscriptionID != entriesResp.SubscriptionID || len(entriesEvent.Snapshot.Entries) != 1 || entriesEvent.Snapshot.Entries[0].Name != "root" {
		t.Fatalf("entries event = %#v, want full root snapshot with root key", entriesEvent)
	}

	unsubReq := mustRequest(t, "4", api.SurfaceKV, string(api.MethodKVUnsubscribe), api.KVSubscriptionRequest{
		SubscriptionID: entriesResp.SubscriptionID,
	})
	if _, err := session.handleMessage(unsubReq); err != nil {
		t.Fatal(err)
	}
	events = nil
	setNested := mustRequest(t, "5", api.SurfaceKV, string(api.MethodKVSet), api.KVSetRequest{
		Path: []string{"nested"},
		Key:  "key",
		Value: api.KVValue{
			Data:     "nested",
			Encoding: "string",
			String:   true,
		},
	})
	if _, err := session.handleMessage(setNested); err != nil {
		t.Fatal(err)
	}
	if len(events) != 0 {
		t.Fatalf("events after ancestor unsubscribe = %d, want descendant subscriptions removed", len(events))
	}
}

func TestKVSubscriptionObservesDirectStoreUpdate(t *testing.T) {
	manager, err := openKVManager(filepath.Join(t.TempDir(), "kv.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close() //nolint:errcheck

	var events []api.Message
	session := (&AdminAPI{kv: manager}).newAPISession(nil, nil, func(msg api.Message) error {
		if msg.Kind == api.MessageEvent && msg.Surface == api.SurfaceKV {
			events = append(events, msg)
		}
		return nil
	})
	defer session.close()

	subscribe := mustRequest(t, "1", api.SurfaceKV, string(api.MethodKVSubscribeEntries), api.KVEntriesRequest{
		Path: []string{"nodes", "amnesia"},
	})
	response, err := session.handleMessage(subscribe)
	if err != nil {
		t.Fatal(err)
	}
	result, err := api.DecodePayload[api.KVSubscribeEntriesResponse](response.Result)
	if err != nil {
		t.Fatal(err)
	}
	if result.SubscriptionID == 0 {
		t.Fatal("entries subscription ID is zero")
	}

	store := manager.DefaultStore().Subtree("nodes", "amnesia", "node-storage-id").OwnedBy("Amnesia")
	if err := store.Update(context.Background(), func(tx storage.KVWriteTx) error {
		return tx.Set("installation_uuid", storage.KValue{
			Data:   []byte("runtime-generated"),
			Hidden: true,
			String: true,
		})
	}); err != nil {
		t.Fatal(err)
	}

	if len(events) != 1 {
		t.Fatalf("events after direct store update = %d, want 1", len(events))
	}
	var event api.KVEntriesEvent
	if err := json.Unmarshal(events[0].Payload, &event); err != nil {
		t.Fatal(err)
	}
	if event.SubscriptionID != result.SubscriptionID || len(event.Snapshot.Entries) != 1 {
		t.Fatalf("entries event = %#v, want the new Amnesia subtree", event)
	}
	entry := event.Snapshot.Entries[0]
	if entry.Kind != api.KVEntryBucket || entry.Name != "node-storage-id" {
		t.Fatalf("entry = %#v, want node-storage-id subtree", entry)
	}
	if entry.Owner != "Amnesia" {
		t.Fatalf("entry owner = %q, want Amnesia", entry.Owner)
	}
	valueSnapshot, err := manager.valueSnapshot(context.Background(), api.KVStoreSelector{}, []string{
		"nodes", "amnesia", "node-storage-id",
	}, "installation_uuid")
	if err != nil {
		t.Fatal(err)
	}
	if !valueSnapshot.Found || valueSnapshot.Value == nil || valueSnapshot.Value.Owner != "Amnesia" {
		t.Fatalf("value snapshot = %#v, want owner Amnesia", valueSnapshot)
	}
}

func TestKVAPIDispatchAllSnapshotAndMutationMethods(t *testing.T) {
	manager, err := openKVManager(filepath.Join(t.TempDir(), "kv.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close() //nolint:errcheck
	session := (&AdminAPI{kv: manager}).newAPISession(nil, nil, func(api.Message) error { return nil })
	defer session.close()

	call := func(id string, method api.KVMethod, payload any) api.Message {
		t.Helper()
		resp, callErr := session.handleMessage(mustRequest(t, id, api.SurfaceKV, string(method), payload))
		if callErr != nil || resp.Error != nil {
			t.Fatalf("%s response = %#v, error = %v", method, resp, callErr)
		}
		return resp
	}
	call("1", api.MethodKVStoresSnapshot, nil)
	call("2", api.MethodKVSubscribeStores, nil)
	call("3", api.MethodKVEnsure, api.KVEntriesRequest{Path: []string{"nested"}})
	call("4", api.MethodKVSet, api.KVSetRequest{Path: []string{"nested"}, Key: "text", Value: api.KVValue{Data: "hello", Encoding: "string", String: true}})
	call("5", api.MethodKVSet, api.KVSetRequest{Path: []string{"nested"}, Key: "binary", Value: api.KVValue{Data: "AQID", Encoding: "base64"}})
	call("6", api.MethodKVEntriesSnapshot, api.KVEntriesRequest{Path: []string{"nested"}})
	call("7", api.MethodKVValueSnapshot, api.KVValueRequest{Path: []string{"nested"}, Key: "text"})
	call("8", api.MethodKVDelete, api.KVValueRequest{Path: []string{"nested"}, Key: "text"})
	call("9", api.MethodKVDrop, api.KVEntriesRequest{Path: []string{"nested"}})

	plugin := api.KVStoreSelector{Kind: api.KVStorePlugin, Name: "plugin"}
	call("10", api.MethodKVEnsure, api.KVEntriesRequest{Store: plugin, Path: []string{"data"}})
	stores := call("11", api.MethodKVStoresSnapshot, nil)
	decoded, err := api.DecodePayload[api.KVStoresSnapshot](stores.Result)
	if err != nil || len(decoded.Stores) != 2 || decoded.Stores[1].Name != "plugin" {
		t.Fatalf("stores snapshot = %#v, error = %v", decoded, err)
	}
}

func TestKVAPIDispatchValidationErrors(t *testing.T) {
	manager, err := openKVManager(filepath.Join(t.TempDir(), "kv.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close() //nolint:errcheck
	session := (&AdminAPI{kv: manager}).newAPISession(nil, nil, nil)
	tests := []api.Message{
		mustRequest(t, "path", api.SurfaceKV, string(api.MethodKVEnsure), api.KVEntriesRequest{Path: []string{""}}),
		mustRequest(t, "key", api.SurfaceKV, string(api.MethodKVValueSnapshot), api.KVValueRequest{Key: ""}),
		mustRequest(t, "encoding", api.SurfaceKV, string(api.MethodKVSet), api.KVSetRequest{Key: "key", Value: api.KVValue{Encoding: "unknown"}}),
		mustRequest(t, "store", api.SurfaceKV, string(api.MethodKVEntriesSnapshot), api.KVEntriesRequest{Store: api.KVStoreSelector{Kind: "unknown"}}),
		mustRequest(t, "method", api.SurfaceKV, "unknown", nil),
		{Kind: api.MessageRequest, ID: "payload", Surface: api.SurfaceKV, Method: string(api.MethodKVDelete), Payload: json.RawMessage(`{`)},
	}
	for _, req := range tests {
		resp, callErr := session.handleMessage(req)
		if callErr != nil || resp.Error == nil {
			t.Fatalf("request %s response = %#v, error = %v", req.ID, resp, callErr)
		}
	}
	unavailable := (&AdminAPI{}).newAPISession(nil, nil, nil)
	resp, err := unavailable.handleMessage(mustRequest(t, "unavailable", api.SurfaceKV, string(api.MethodKVStoresSnapshot), nil))
	if err != nil || resp.Error == nil || resp.Error.Code != api.ErrorUnavailable {
		t.Fatalf("unavailable response = %#v, error = %v", resp, err)
	}
}

func TestEngineBatchNodeAPIDispatch(t *testing.T) {
	engine, err := almagest.BuildEngine(
		context.Background(),
		configer.NewMemory(nil),
		logging.NewDebugLogFactory(16),
		&sysnetdebug.System{},
		nil,
		nil, nil,
		nil,

		[]pasta.NodeClass{adminAPIBatchNodeClass{}},
		nil,
		false)

	if err != nil {
		t.Fatalf("BuildEngine error = %v, want nil", err)
	}
	t.Cleanup(func() {
		if err := engine.Close(); err != nil {
			t.Errorf("engine.Close error = %v, want nil", err)
		}
	})
	session := (&AdminAPI{}).newAPISession(engine, nil, nil)

	first, err := engine.AddNode(almagest.MainWorkspace, "test/AdminAPIBatchNode", "first")
	if err != nil {
		t.Fatalf("AddNode first error = %v, want nil", err)
	}
	second, err := engine.AddNode(almagest.MainWorkspace, "test/AdminAPIBatchNode", "second")
	if err != nil {
		t.Fatalf("AddNode second error = %v, want nil", err)
	}

	moveReq, err := api.NewRequest("1", api.SurfaceEngine, string(api.MethodEngineMoveNodes), api.MoveNodesRequest{
		Workspace: almagest.MainWorkspace,
		Positions: map[uint64]string{
			first:  "10 20",
			second: "30 40",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	moveResp, err := session.handleMessage(moveReq)
	if err != nil {
		t.Fatal(err)
	}
	if moveResp.Error != nil {
		t.Fatalf("move response error = %#v, want nil", moveResp.Error)
	}
	if got, ok := engine.NodeSnapshot(almagest.MainWorkspace, second); !ok || got.Position != "30 40" {
		t.Fatalf("second snapshot = %#v, %v; want position 30 40", got, ok)
	}

	removeReq, err := api.NewRequest("2", api.SurfaceEngine, string(api.MethodEngineRemoveNodes), api.IDsRequest{
		Workspace: almagest.MainWorkspace,
		IDs:       []uint64{first, second},
	})
	if err != nil {
		t.Fatal(err)
	}
	removeResp, err := session.handleMessage(removeReq)
	if err != nil {
		t.Fatal(err)
	}
	if removeResp.Error != nil {
		t.Fatalf("remove response error = %#v, want nil", removeResp.Error)
	}
	if _, ok := engine.NodeSnapshot(almagest.MainWorkspace, first); ok {
		t.Fatal("first node still exists after remove_nodes dispatch")
	}
	if _, ok := engine.NodeSnapshot(almagest.MainWorkspace, second); ok {
		t.Fatal("second node still exists after remove_nodes dispatch")
	}
}

func TestAdminAndLogAPIDispatch(t *testing.T) {
	factory := NewLogFactory(LogCacheConfig{Info: 2})
	factory.level = logLevelEnabled{info: true}
	admin := &AdminAPI{projectVersionStatus: api.ProjectVersionStatus{Version: "test"}}
	var events []api.Message
	session := admin.newAPISession(nil, factory, func(msg api.Message) error {
		events = append(events, msg)
		return nil
	})
	defer session.close()

	requests := []api.Message{
		mustRequest(t, "1", api.SurfaceAdmin, string(api.MethodAdminHello), api.HelloRequest{ClientName: "test"}),
		mustRequest(t, "2", api.SurfaceAdmin, string(api.MethodAdminPing), api.PingRequest{Nonce: "nonce"}),
		mustRequest(t, "3", api.SurfaceAdmin, string(api.MethodAdminColorMap), nil),
	}
	for _, req := range requests {
		resp, err := session.handleMessage(req)
		if err != nil || resp.Error != nil {
			t.Fatalf("%s response = %#v, error = %v", req.Method, resp, err)
		}
	}

	acquire := mustRequest(t, "4", api.SurfaceAdmin, string(api.MethodAdminAcquireMutation), api.AcquireMutationRequest{Holder: "first", TTLMilliseconds: 1000})
	resp, err := session.handleMessage(acquire)
	if err != nil || resp.Error != nil {
		t.Fatalf("acquire response = %#v, error = %v", resp, err)
	}
	lease, err := api.DecodePayload[api.AcquireMutationResponse](resp.Result)
	if err != nil || !lease.Acquired || lease.Lease == nil {
		t.Fatalf("acquire result = %#v, error = %v", lease, err)
	}
	resp, err = session.handleMessage(mustRequest(t, "5", api.SurfaceAdmin, string(api.MethodAdminAcquireMutation), api.AcquireMutationRequest{Holder: "second"}))
	busy, decodeErr := api.DecodePayload[api.AcquireMutationResponse](resp.Result)
	if err != nil || decodeErr != nil || busy.Acquired || busy.Active == nil {
		t.Fatalf("busy acquire = %#v, response error = %v, decode error = %v", busy, err, decodeErr)
	}
	resp, err = session.handleMessage(mustRequest(t, "6", api.SurfaceAdmin, string(api.MethodAdminRenewMutation), api.RenewMutationRequest{LeaseID: lease.Lease.ID, TTLMilliseconds: 2000}))
	if err != nil || resp.Error != nil {
		t.Fatalf("renew response = %#v, error = %v", resp, err)
	}
	resp, err = session.handleMessage(mustRequest(t, "7", api.SurfaceAdmin, string(api.MethodAdminReleaseMutation), api.ReleaseMutationRequest{LeaseID: lease.Lease.ID}))
	if err != nil || resp.Error != nil || admin.mutationLease != nil {
		t.Fatalf("release response = %#v, error = %v, lease = %#v", resp, err, admin.mutationLease)
	}
	resp, err = session.handleMessage(mustRequest(t, "8", api.SurfaceAdmin, string(api.MethodAdminRenewMutation), api.RenewMutationRequest{LeaseID: "gone"}))
	if err != nil || resp.Error == nil || resp.Error.Code != api.ErrorConflict {
		t.Fatalf("missing renew response = %#v, error = %v", resp, err)
	}

	resp, err = session.handleMessage(mustRequest(t, "9", api.SurfaceLog, string(api.MethodLogSubscribe), nil))
	if err != nil || resp.Error != nil {
		t.Fatalf("log subscribe response = %#v, error = %v", resp, err)
	}
	sub, err := api.DecodePayload[api.SubscribeLogsResponse](resp.Result)
	if err != nil || sub.SubscriptionID == 0 {
		t.Fatalf("log subscription = %#v, error = %v", sub, err)
	}
	factory.DaemonLogger().Info("published")
	if len(events) != 1 || events[0].Method != string(api.EventLogEntry) {
		t.Fatalf("events = %#v", events)
	}
	resp, err = session.handleMessage(mustRequest(t, "10", api.SurfaceLog, string(api.MethodLogUnsubscribe), api.SubscriptionRequest{SubscriptionID: sub.SubscriptionID}))
	if err != nil || resp.Error != nil || len(session.logSubscriptions) != 0 {
		t.Fatalf("log unsubscribe response = %#v, error = %v", resp, err)
	}
}

func TestEngineAPIDispatchCoreMethods(t *testing.T) {
	engine, err := almagest.BuildEngine(
		context.Background(), configer.NewMemory(nil), logging.NewDebugLogFactory(16), &sysnetdebug.System{},
		nil, nil, nil, nil, []pasta.NodeClass{adminAPIBatchNodeClass{}}, nil, false,
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = engine.Close() })
	var events []api.Message
	session := (&AdminAPI{}).newAPISession(engine, nil, func(msg api.Message) error {
		events = append(events, msg)
		return nil
	})
	defer session.close()

	call := func(id, method string, payload any) api.Message {
		t.Helper()
		resp, callErr := session.handleMessage(mustRequest(t, id, api.SurfaceEngine, method, payload))
		if callErr != nil || resp.Error != nil {
			t.Fatalf("%s response = %#v, error = %v", method, resp, callErr)
		}
		return resp
	}
	add := call("1", string(api.MethodEngineAddNode), api.AddNodeRequest{Workspace: almagest.MainWorkspace, Class: "test/AdminAPIBatchNode", Name: "node"})
	added, err := api.DecodePayload[api.AddNodeResponse](add.Result)
	if err != nil || added.ID == 0 {
		t.Fatalf("add response = %#v, error = %v", added, err)
	}
	nodeID := added.ID

	subscribe := call("2", string(api.MethodEngineSubscribeNotifications), api.WorkspaceRequest{Workspace: almagest.MainWorkspace})
	sub, err := api.DecodePayload[api.SubscribeNotificationsResponse](subscribe.Result)
	if err != nil || sub.SubscriptionID == 0 {
		t.Fatalf("subscribe response = %#v, error = %v", sub, err)
	}
	call("3", string(api.MethodEngineRequestFullSnapshot), api.SubscriptionRequest(sub))
	call("4", string(api.MethodEngineSubscribeNodeMenu), api.NodeMenuSubscriptionRequest{Workspace: almagest.MainWorkspace, Node: nodeID, SubscriptionID: sub.SubscriptionID})
	call("5", string(api.MethodEngineUnsubscribeNodeMenu), api.NodeMenuSubscriptionRequest{Workspace: almagest.MainWorkspace, Node: nodeID, SubscriptionID: sub.SubscriptionID})

	call("6", string(api.MethodEngineWorkspaceSnapshot), api.WorkspaceRequest{Workspace: almagest.MainWorkspace})
	call("7", string(api.MethodEngineWorkersSnapshot), api.WorkspaceRequest{Workspace: almagest.MainWorkspace})
	call("8", string(api.MethodEngineNodeClassSnapshot), api.ClassRequest{Workspace: almagest.MainWorkspace, Class: "test/AdminAPIBatchNode"})
	call("9", string(api.MethodEngineNodeClassLongDescription), api.ClassRequest{Workspace: almagest.MainWorkspace, Class: "test/AdminAPIBatchNode"})
	call("10", string(api.MethodEngineNodeSnapshot), api.IDRequest{Workspace: almagest.MainWorkspace, ID: nodeID})
	call("11", string(api.MethodEngineSetNodePosition), api.SetNodePositionRequest{Workspace: almagest.MainWorkspace, ID: nodeID, Position: "10 20"})
	call("12", string(api.MethodEngineSetNodeName), api.SetNodeNameRequest{Workspace: almagest.MainWorkspace, ID: nodeID, Name: "renamed"})
	call("13", string(api.MethodEngineRemoveNodePopup), api.NodePopupRequest{Workspace: almagest.MainWorkspace, Node: nodeID, Popup: 1})
	call("14", string(api.MethodEngineRemoveNodePopups), api.NodePopupRequest{Workspace: almagest.MainWorkspace, Node: nodeID})
	call("15", string(api.MethodEngineRemoveNodePopupsByType), api.NodePopupRequest{Workspace: almagest.MainWorkspace, Node: nodeID, Type: "notice"})
	call("16", string(api.MethodEngineCopy), api.CopyRequest{Workspace: almagest.MainWorkspace, IDs: []uint64{nodeID}})
	call("17", string(api.MethodEngineSendInbox), api.SendInboxRequest{Workspace: almagest.MainWorkspace, Node: nodeID, Payload: json.RawMessage(`{"ok":true}`)})
	call("18", string(api.MethodEngineSendNodeFormularMessage), api.SendNodeFormularMessageRequest{Workspace: almagest.MainWorkspace, Node: nodeID, Message: json.RawMessage(`{"type":"button.press","menuId":"node:1","blockId":"b","buttonId":"x"}`)})
	call("19", string(api.MethodEngineUndo), api.WorkspaceRequest{Workspace: almagest.MainWorkspace})
	call("20", string(api.MethodEngineRedo), api.WorkspaceRequest{Workspace: almagest.MainWorkspace})
	call("21", string(api.MethodEngineUnsubscribeNotifications), api.SubscriptionRequest(sub))
	if len(session.engineSubscriptions) != 0 {
		t.Fatalf("engine subscriptions = %#v, want empty", session.engineSubscriptions)
	}
}

func TestAPIDispatchRejectsBadMessages(t *testing.T) {
	session := (&AdminAPI{}).newAPISession(nil, nil, nil)
	tests := []api.Message{
		{Kind: api.MessageEvent, ID: "event"},
		mustRequest(t, "surface", api.Surface("unknown"), "method", nil),
		{Kind: api.MessageRequest, ID: "admin-payload", Surface: api.SurfaceAdmin, Method: string(api.MethodAdminPing), Payload: json.RawMessage(`{`)},
		mustRequest(t, "admin-method", api.SurfaceAdmin, "unknown", nil),
	}
	for _, msg := range tests {
		resp, err := session.handleMessage(msg)
		if err != nil || resp.Error == nil {
			t.Fatalf("message %#v response = %#v, error = %v", msg, resp, err)
		}
	}
	resp, err := newRequestError("", api.ErrorBadRequest, "bad")
	if err != nil || resp.ID != "0" {
		t.Fatalf("newRequestError empty ID = %#v, %v", resp, err)
	}
	if mutationLeaseTTL(0) != 30*time.Second || mutationLeaseTTL(5) != 5*time.Millisecond {
		t.Fatal("mutationLeaseTTL returned an incorrect value")
	}
}

func TestAPISurfacesRejectMalformedPayloadsForEveryMethod(t *testing.T) {
	bad := func(surface api.Surface, method string) api.Message {
		return api.Message{Kind: api.MessageRequest, ID: method, Surface: surface, Method: method, Payload: json.RawMessage(`{`)}
	}
	engineSession := (&AdminAPI{}).newAPISession(nil, nil, nil)
	for _, method := range api.EngineMethods() {
		resp, err := engineSession.handleEngineMessage(bad(api.SurfaceEngine, method))
		if err != nil || resp.Error == nil || resp.Error.Code != api.ErrorInvalidPayload {
			t.Errorf("engine %s response = %#v, error = %v", method, resp, err)
		}
	}

	manager, err := openKVManager(filepath.Join(t.TempDir(), "kv.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close() //nolint:errcheck
	send := func(api.Message) error { return nil }
	session := (&AdminAPI{kv: manager, dns: manager, secrets: manager, tls: manager}).newAPISession(nil, nil, send)
	defer session.close()

	for _, method := range api.KVMethods() {
		resp, callErr := session.handleKVMessage(bad(api.SurfaceKV, method))
		if callErr != nil {
			t.Errorf("kv %s error = %v", method, callErr)
		} else if method != string(api.MethodKVStoresSnapshot) && method != string(api.MethodKVSubscribeStores) && resp.Error == nil {
			t.Errorf("kv %s accepted malformed payload", method)
		}
	}
	for _, method := range api.DNSMethods() {
		resp, callErr := session.handleDNSMessage(bad(api.SurfaceDNS, method))
		if callErr != nil {
			t.Errorf("dns %s error = %v", method, callErr)
		} else if method != string(api.MethodDNSCachesSnapshot) && method != string(api.MethodDNSSubscribeCaches) && resp.Error == nil {
			t.Errorf("dns %s accepted malformed payload", method)
		}
	}
	for _, method := range api.SecretsMethods() {
		resp, callErr := session.handleSecretsMessage(bad(api.SurfaceSecrets, method))
		if callErr != nil {
			t.Errorf("secrets %s error = %v", method, callErr)
		} else if method != string(api.MethodSecretsSnapshot) && method != string(api.MethodSecretsSubscribe) && resp.Error == nil {
			t.Errorf("secrets %s accepted malformed payload", method)
		}
	}
	for _, method := range api.TLSMethods() {
		resp, callErr := session.handleTLSMessage(bad(api.SurfaceTLS, method))
		if callErr != nil {
			t.Errorf("tls %s error = %v", method, callErr)
		} else if method != string(api.MethodTLSSnapshot) && method != string(api.MethodTLSSubscribe) && method != string(api.MethodTLSBuiltinCA) && method != string(api.MethodTLSReloadOSCerts) && resp.Error == nil {
			t.Errorf("tls %s accepted malformed payload", method)
		}
	}

	snapshotSession := (&AdminAPI{}).newAPISession(nil, nil, nil)
	for _, method := range api.SnapshotsMethods() {
		if method == string(api.MethodSnapshotsList) {
			continue
		}
		resp, callErr := snapshotSession.handleSnapshotsMessage(bad(api.SurfaceSnapshots, method))
		if callErr != nil || resp.Error == nil {
			t.Errorf("snapshots %s response = %#v, error = %v", method, resp, callErr)
		}
	}
}

type adminAPIBatchNodeClass struct{}

func (adminAPIBatchNodeClass) ClassName() string {
	return "test/AdminAPIBatchNode"
}

func (adminAPIBatchNodeClass) ShortDescription() string {
	return "Admin API batch node test"
}

func (adminAPIBatchNodeClass) LongDescription() string {
	return "Tests admin API batch node dispatch."
}

func (adminAPIBatchNodeClass) DefaultNodeParams() pasta.NodeClassParams {
	return pasta.NodeClassParams{}
}

func (adminAPIBatchNodeClass) NewNode(configer.Config, ...*pasta.NodeClassState) (pasta.Node, error) {
	return &adminAPIBatchNode{}, nil
}

type adminAPINodeClass struct {
	name        string
	primaryType string
}

func (c adminAPINodeClass) ClassName() string {
	return c.name
}

func (adminAPINodeClass) ShortDescription() string {
	return "Admin API node test"
}

func (adminAPINodeClass) LongDescription() string {
	return "Tests admin API snapshot serialization."
}

func (c adminAPINodeClass) DefaultNodeParams() pasta.NodeClassParams {
	return pasta.NodeClassParams{PrimaryType: c.primaryType}
}

func (adminAPINodeClass) NewNode(configer.Config, ...*pasta.NodeClassState) (pasta.Node, error) {
	return &adminAPIBatchNode{}, nil
}

type adminAPIBatchNode struct {
	pasta.BasicNode
	_ byte
}

func mustRequest(t *testing.T, id string, surface api.Surface, method string, payload any) api.Message {
	t.Helper()
	msg, err := api.NewRequest(id, surface, method, payload)
	if err != nil {
		t.Fatal(err)
	}
	return msg
}
