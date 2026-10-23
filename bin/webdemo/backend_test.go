package webdemo

import (
	"encoding/json"
	"sync"
	"testing"

	"github.com/asciimoth/almagest/bin/api"
)

func TestBackendReturnsOnlyNodeClassesInWorkspaceSnapshot(t *testing.T) {
	want := api.NodeClassSnapshot{
		Class:            "almagest/MockNetwork",
		ShortDescription: "Virtual network mock",
		PrimaryType:      "almagest/network",
		InitialPorts:     nil,
	}
	backend := NewBackend(api.WorkspaceSnapshot{Classes: []api.NodeClassSnapshot{want}})
	response := call(t, backend, api.SurfaceEngine, string(api.MethodEngineWorkspaceSnapshot))
	if response.Error != nil {
		t.Fatalf("workspace snapshot error = %#v", response.Error)
	}
	var result api.WorkspaceSnapshotResponse
	decodeResult(t, response, &result)
	if len(result.Snapshot.Classes) != 1 || result.Snapshot.Classes[0].Class != want.Class {
		t.Fatalf("classes = %#v, want %#v", result.Snapshot.Classes, []api.NodeClassSnapshot{want})
	}
	if result.Snapshot.Nodes == nil || result.Snapshot.Ports == nil || result.Snapshot.Links == nil {
		t.Fatalf("workspace maps must be empty objects: %#v", result.Snapshot)
	}
	if len(result.Snapshot.Nodes)+len(result.Snapshot.Ports)+len(result.Snapshot.Links) != 0 {
		t.Fatalf("workspace contains graph data: %#v", result.Snapshot)
	}
}

func TestBackendReturnsTypedEmptyReadResults(t *testing.T) {
	backend := NewBackend(api.WorkspaceSnapshot{})
	tests := []struct {
		name    string
		surface api.Surface
		method  string
		check   func(*testing.T, json.RawMessage)
	}{
		{"hello", api.SurfaceAdmin, string(api.MethodAdminHello), checkJSONFields("version", "execution_mode", "project_version_status")},
		{"locale", api.SurfaceAdmin, string(api.MethodAdminLocaleSnapshot), checkJSONFields("available", "panel", "revision")},
		{"config", api.SurfaceConfig, string(api.MethodConfigSnapshot), checkJSONFields("text", "revision", "read_only")},
		{"dns caches", api.SurfaceDNS, string(api.MethodDNSCachesSnapshot), checkJSONObject},
		{"dns keys", api.SurfaceDNS, string(api.MethodDNSKeysSnapshot), checkJSONFields("exists")},
		{"dns value", api.SurfaceDNS, string(api.MethodDNSValueSnapshot), checkJSONFields("found")},
		{"docs tree", api.SurfaceDocs, string(api.MethodDocsTree), checkJSONFields("available", "topics", "mark_messages")},
		{"workers", api.SurfaceEngine, string(api.MethodEngineWorkersSnapshot), checkJSONFields("nodes", "workers")},
		{"copy", api.SurfaceEngine, string(api.MethodEngineCopy), checkJSONFields("data")},
		{"description", api.SurfaceEngine, string(api.MethodEngineNodeClassLongDescription), checkJSONFields("description")},
		{"kv stores", api.SurfaceKV, string(api.MethodKVStoresSnapshot), checkJSONFields("stores")},
		{"kv entries", api.SurfaceKV, string(api.MethodKVEntriesSnapshot), checkJSONFields("exists")},
		{"kv value", api.SurfaceKV, string(api.MethodKVValueSnapshot), checkJSONFields("found")},
		{"logs", api.SurfaceLog, string(api.MethodLogSubscribe), checkJSONFields("subscription_id", "cache_size", "entries")},
		{"secrets", api.SurfaceSecrets, string(api.MethodSecretsSnapshot), checkJSONObject},
		{"snapshots", api.SurfaceSnapshots, string(api.MethodSnapshotsList), checkJSONFields("snapshots")},
		{"tls", api.SurfaceTLS, string(api.MethodTLSSnapshot), checkJSONObject},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := call(t, backend, test.surface, test.method)
			if response.Error != nil {
				t.Fatalf("read error = %#v", response.Error)
			}
			test.check(t, response.Result)
		})
	}
}

func TestBackendHandlesEveryAdvertisedMethod(t *testing.T) {
	backend := NewBackend(api.WorkspaceSnapshot{})
	surfaces := map[api.Surface][]string{
		api.SurfaceAdmin:     api.AdminMethods(),
		api.SurfaceConfig:    api.ConfigMethods(),
		api.SurfaceDNS:       api.DNSMethods(),
		api.SurfaceDocs:      api.DocsMethods(),
		api.SurfaceEngine:    api.EngineMethods(),
		api.SurfaceKV:        api.KVMethods(),
		api.SurfaceLog:       api.LogMethods(),
		api.SurfaceSecrets:   api.SecretsMethods(),
		api.SurfaceSnapshots: api.SnapshotsMethods(),
		api.SurfaceTLS:       api.TLSMethods(),
	}
	for surface, methods := range surfaces {
		for _, method := range methods {
			t.Run(string(surface)+"/"+method, func(t *testing.T) {
				response := call(t, backend, surface, method)
				if isMutation(surface, method) {
					if response.Error == nil || response.Error.Code != api.ErrorMutationRejected {
						t.Fatalf("mutation error = %#v, want mutation_rejected", response.Error)
					}
					return
				}
				if response.Error != nil && response.Error.Code == api.ErrorInvalidMethod {
					t.Fatalf("advertised method returned invalid_method: %#v", response.Error)
				}
			})
		}
	}
}

func TestBackendRejectsAllMutations(t *testing.T) {
	backend := NewBackend(api.WorkspaceSnapshot{})
	tests := []struct {
		surface api.Surface
		method  string
	}{
		{api.SurfaceAdmin, string(api.MethodAdminAcquireMutation)},
		{api.SurfaceAdmin, string(api.MethodAdminRenewMutation)},
		{api.SurfaceAdmin, string(api.MethodAdminReleaseMutation)},
		{api.SurfaceAdmin, string(api.MethodAdminSetLocale)},
		{api.SurfaceConfig, string(api.MethodConfigUpdate)},
		{api.SurfaceDNS, string(api.MethodDNSSet)},
		{api.SurfaceDNS, string(api.MethodDNSDelete)},
		{api.SurfaceDNS, string(api.MethodDNSDrop)},
		{api.SurfaceEngine, string(api.MethodEngineAddNode)},
		{api.SurfaceEngine, string(api.MethodEngineAddLink)},
		{api.SurfaceEngine, string(api.MethodEngineRemoveNode)},
		{api.SurfaceEngine, string(api.MethodEngineSendNodeFormularMessage)},
		{api.SurfaceKV, string(api.MethodKVEnsure)},
		{api.SurfaceKV, string(api.MethodKVSet)},
		{api.SurfaceKV, string(api.MethodKVDelete)},
		{api.SurfaceKV, string(api.MethodKVDrop)},
		{api.SurfaceSecrets, string(api.MethodSecretsSetValue)},
		{api.SurfaceSecrets, string(api.MethodSecretsSetNodes)},
		{api.SurfaceSecrets, string(api.MethodSecretsRename)},
		{api.SurfaceSecrets, string(api.MethodSecretsDelete)},
		{api.SurfaceSnapshots, string(api.MethodSnapshotsCreate)},
		{api.SurfaceSnapshots, string(api.MethodSnapshotsDelete)},
		{api.SurfaceSnapshots, string(api.MethodSnapshotsImport)},
		{api.SurfaceSnapshots, string(api.MethodSnapshotsRename)},
		{api.SurfaceSnapshots, string(api.MethodSnapshotsRestore)},
		{api.SurfaceTLS, string(api.MethodTLSSetBuiltinCA)},
		{api.SurfaceTLS, string(api.MethodTLSReloadOSCerts)},
		{api.SurfaceTLS, string(api.MethodTLSSetCert)},
		{api.SurfaceTLS, string(api.MethodTLSRenameCert)},
		{api.SurfaceTLS, string(api.MethodTLSDeleteCert)},
		{api.SurfaceTLS, string(api.MethodTLSSetPolicy)},
		{api.SurfaceTLS, string(api.MethodTLSRenamePolicy)},
		{api.SurfaceTLS, string(api.MethodTLSDeletePolicy)},
	}
	for _, test := range tests {
		t.Run(string(test.surface)+"/"+test.method, func(t *testing.T) {
			response := call(t, backend, test.surface, test.method)
			if response.Error == nil || response.Error.Code != api.ErrorMutationRejected {
				t.Fatalf("error = %#v, want mutation_rejected", response.Error)
			}
		})
	}
}

func TestBackendReturnsNotFoundForAbsentItems(t *testing.T) {
	backend := NewBackend(api.WorkspaceSnapshot{})
	for _, test := range []struct {
		surface api.Surface
		method  string
	}{
		{api.SurfaceEngine, string(api.MethodEngineNodeClassSnapshot)},
		{api.SurfaceEngine, string(api.MethodEngineNodeSnapshot)},
		{api.SurfaceEngine, string(api.MethodEnginePortSnapshot)},
		{api.SurfaceEngine, string(api.MethodEngineLinkSnapshot)},
		{api.SurfaceSnapshots, string(api.MethodSnapshotsExport)},
		{api.SurfaceTLS, string(api.MethodTLSBuiltinCA)},
		{api.SurfaceTLS, string(api.MethodTLSBuiltinCRL)},
	} {
		response := call(t, backend, test.surface, test.method)
		if response.Error == nil || response.Error.Code != api.ErrorNotFound {
			t.Fatalf("%s/%s error = %#v, want not_found", test.surface, test.method, response.Error)
		}
	}
}

func TestBackendRejectsInvalidMessages(t *testing.T) {
	backend := NewBackend(api.WorkspaceSnapshot{})
	for _, input := range []string{"not-json", `{}`, `{"version":"v1","kind":"event","surface":"admin","method":"ping"}`} {
		response, err := api.UnmarshalMessage(backend.Handle([]byte(input)))
		if err != nil {
			t.Fatalf("response to %q is invalid: %v", input, err)
		}
		if response.Error == nil {
			t.Fatalf("response to %q has no error", input)
		}
	}
	response := call(t, backend, api.SurfaceAdmin, "missing")
	if response.Error == nil || response.Error.Code != api.ErrorInvalidMethod {
		t.Fatalf("unknown method error = %#v", response.Error)
	}
}

func TestBackendAllocatesUniqueSubscriptionIDsConcurrently(t *testing.T) {
	backend := NewBackend(api.WorkspaceSnapshot{})
	const count = 32
	ids := make(chan uint64, count)
	var wg sync.WaitGroup
	for range count {
		wg.Go(func() {
			response := call(t, backend, api.SurfaceEngine, string(api.MethodEngineSubscribeNotifications))
			var result api.SubscribeNotificationsResponse
			decodeResult(t, response, &result)
			ids <- result.SubscriptionID
		})
	}
	wg.Wait()
	close(ids)
	seen := map[uint64]bool{}
	for id := range ids {
		if id == 0 || seen[id] {
			t.Fatalf("invalid or duplicate subscription ID %d", id)
		}
		seen[id] = true
	}
}

func call(t *testing.T, backend *Backend, surface api.Surface, method string) api.Message {
	t.Helper()
	request, err := api.NewRequest("request-id", surface, method, struct{}{})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := api.MarshalMessage(request)
	if err != nil {
		t.Fatal(err)
	}
	response, err := api.UnmarshalMessage(backend.Handle(encoded))
	if err != nil {
		t.Fatalf("invalid response: %v", err)
	}
	if response.ID != request.ID {
		t.Fatalf("response ID = %q, want %q", response.ID, request.ID)
	}
	return response
}

func decodeResult(t *testing.T, response api.Message, target any) {
	t.Helper()
	if response.Error != nil {
		t.Fatalf("response error = %#v", response.Error)
	}
	if err := json.Unmarshal(response.Result, target); err != nil {
		t.Fatalf("decode result: %v", err)
	}
}

func checkJSONObject(t *testing.T, raw json.RawMessage) {
	t.Helper()
	var value map[string]any
	if err := json.Unmarshal(raw, &value); err != nil || value == nil {
		t.Fatalf("result = %s, want JSON object: %v", raw, err)
	}
}

func checkJSONFields(fields ...string) func(*testing.T, json.RawMessage) {
	return func(t *testing.T, raw json.RawMessage) {
		t.Helper()
		var value map[string]any
		if err := json.Unmarshal(raw, &value); err != nil {
			t.Fatalf("decode result: %v", err)
		}
		for _, field := range fields {
			if _, ok := value[field]; !ok {
				t.Errorf("result %s does not contain %q", raw, field)
			}
		}
	}
}
