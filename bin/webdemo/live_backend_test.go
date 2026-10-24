package webdemo

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/asciimoth/almagest/bin/api"
	almagest "github.com/asciimoth/almagest/lib"
	"github.com/asciimoth/almagest/lib/storage"
	"github.com/asciimoth/formular"
	"github.com/asciimoth/gonnect"
	gdns "github.com/asciimoth/gonnect/dns"
	sysnetdebug "github.com/asciimoth/gonnect/sysnet/debug"
	"github.com/asciimoth/pasta/pasta"
)

func newTestLiveBackend(t *testing.T) (*Backend, *memoryPersistence) {
	t.Helper()
	persistence := newMemoryPersistence()
	persistence.values[configStorageKey] = []byte("{}")
	return newTestLiveBackendWithPersistence(t, persistence)
}

func newDefaultTestLiveBackend(t *testing.T) (*Backend, *memoryPersistence) {
	t.Helper()
	return newTestLiveBackendWithPersistence(t, newMemoryPersistence())
}

func newTestLiveBackendWithPersistence(t *testing.T, persistence *memoryPersistence) (*Backend, *memoryPersistence) {
	t.Helper()
	stores, err := NewStorage(persistence)
	if err != nil {
		t.Fatal(err)
	}
	config, err := NewConfigStore(persistence)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Parse()
	if err != nil {
		t.Fatal(err)
	}
	network := gonnect.NewLoopbackNetwork()
	network.AllowAnyHost = true
	logs := NewLogFactory(256, nil)
	engine, err := almagest.BuildEngine(context.Background(), cfg, logs, &sysnetdebug.System{OutNetwork: network, LocalNetwork: network}, stores.KV, stores.DNS, stores.Secrets, stores.TLS, nil, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	backend, err := NewLiveBackend(engine, stores, config, logs)
	if err != nil {
		_ = engine.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = engine.Close() })
	return backend, persistence
}

func TestLiveBackendMutatesAndPersistsEngine(t *testing.T) {
	backend, persistence := newTestLiveBackend(t)
	for _, class := range []string{"almagest/MockNetwork", "almagest/HttpServer", "almagest/HttpClient"} {
		response := liveCall(t, backend, api.SurfaceEngine, string(api.MethodEngineAddNode), api.AddNodeRequest{Workspace: almagest.MainWorkspace, Class: class})
		if response.Error != nil {
			t.Fatalf("add %s: %#v", class, response.Error)
		}
	}
	response := liveCall(t, backend, api.SurfaceEngine, string(api.MethodEngineWorkspaceSnapshot), api.WorkspaceRequest{Workspace: almagest.MainWorkspace})
	var result api.WorkspaceSnapshotResponse
	decodeResult(t, response, &result)
	if len(result.Snapshot.Nodes) != 3 {
		t.Fatalf("nodes = %d, want 3", len(result.Snapshot.Nodes))
	}
	raw := persistence.values[configStorageKey]
	if len(raw) == 0 || string(raw) == "{}" {
		t.Fatalf("saved config = %q", raw)
	}
}

func TestLiveBackendKVSecretsAndDNS(t *testing.T) {
	backend, _ := newTestLiveBackend(t)
	setKV := liveCall(t, backend, api.SurfaceKV, string(api.MethodKVSet), api.KVSetRequest{Key: "key", Value: api.KVValue{Data: "value", Encoding: "string", String: true}})
	if setKV.Error != nil {
		t.Fatalf("set KV: %#v", setKV.Error)
	}
	value := liveCall(t, backend, api.SurfaceKV, string(api.MethodKVValueSnapshot), api.KVValueRequest{Key: "key"})
	var kv api.KVValueSnapshot
	decodeResult(t, value, &kv)
	if !kv.Found || kv.Value.Data != "value" {
		t.Fatalf("KV = %#v", kv)
	}

	secret := liveCall(t, backend, api.SurfaceSecrets, string(api.MethodSecretsSetValue), api.SecretsSetValueRequest{Key: "demo", Value: "secret"})
	if secret.Error != nil {
		t.Fatalf("set secret: %#v", secret.Error)
	}
	snapshot := liveCall(t, backend, api.SurfaceSecrets, string(api.MethodSecretsSnapshot), struct{}{})
	var secrets api.SecretsSnapshot
	decodeResult(t, snapshot, &secrets)
	if len(secrets.Secrets) != 1 || secrets.Secrets[0].Value != "secret" {
		t.Fatalf("secrets = %#v", secrets)
	}

	dns := liveCall(t, backend, api.SurfaceDNS, string(api.MethodDNSSet), api.DNSSetRequest{Cache: "demo", Record: api.DNSRecordInput{Name: "example.test", Type: "A", TTL: 60, Value: "192.0.2.1"}})
	if dns.Error != nil {
		t.Fatalf("set DNS: %#v", dns.Error)
	}
	var set api.DNSSetResponse
	decodeResult(t, dns, &set)
	valueDNS := liveCall(t, backend, api.SurfaceDNS, string(api.MethodDNSValueSnapshot), api.DNSValueRequest{Cache: "demo", Key: set.Key})
	var dnsValue api.DNSValueSnapshot
	decodeResult(t, valueDNS, &dnsValue)
	if !dnsValue.Found {
		t.Fatalf("DNS = %#v", dnsValue)
	}
}

func TestLiveBackendQueuesStorageSubscriptionEvents(t *testing.T) {
	backend, _ := newTestLiveBackend(t)

	dnsSub := liveCall(t, backend, api.SurfaceDNS, string(api.MethodDNSSubscribeCaches), struct{}{})
	var dnsSubscription api.DNSSubscribeCachesResponse
	decodeResult(t, dnsSub, &dnsSubscription)
	kvSub := liveCall(t, backend, api.SurfaceKV, string(api.MethodKVSubscribeStores), struct{}{})
	var kvSubscription api.KVSubscribeStoresResponse
	decodeResult(t, kvSub, &kvSubscription)
	secretSub := liveCall(t, backend, api.SurfaceSecrets, string(api.MethodSecretsSubscribe), struct{}{})
	var secretSubscription api.SecretsSubscribeResponse
	decodeResult(t, secretSub, &secretSubscription)

	liveCall(t, backend, api.SurfaceDNS, string(api.MethodDNSSet), api.DNSSetRequest{Cache: "events", Record: api.DNSRecordInput{Name: "event.test", Type: "A", TTL: 60, Value: "192.0.2.2"}})
	liveCall(t, backend, api.SurfaceKV, string(api.MethodKVSet), api.KVSetRequest{Key: "event", Value: api.KVValue{Data: "dmFsdWU=", Encoding: "base64"}})
	liveCall(t, backend, api.SurfaceSecrets, string(api.MethodSecretsSetValue), api.SecretsSetValueRequest{Key: "event", Value: "value"})

	var events []api.Message
	if err := json.Unmarshal(backend.DrainEvents(), &events); err != nil {
		t.Fatal(err)
	}
	want := map[api.Surface]bool{api.SurfaceDNS: false, api.SurfaceKV: false, api.SurfaceSecrets: false}
	for _, event := range events {
		if _, tracked := want[event.Surface]; tracked {
			want[event.Surface] = true
		}
	}
	for surface, found := range want {
		if !found {
			t.Fatalf("no %s subscription event in %#v", surface, events)
		}
	}
}

func TestLiveBackendObservesRuntimeStorageWrites(t *testing.T) {
	backend, _ := newTestLiveBackend(t)
	liveCall(t, backend, api.SurfaceDNS, string(api.MethodDNSSubscribeCaches), struct{}{})
	liveCall(t, backend, api.SurfaceKV, string(api.MethodKVSubscribeStores), struct{}{})
	liveCall(t, backend, api.SurfaceSecrets, string(api.MethodSecretsSubscribe), struct{}{})
	_ = backend.DrainEvents()

	message := &gdns.Message{
		Response: true, RCode: gdns.RCodeSuccess,
		Questions: []gdns.Question{{Name: "runtime.test.", Type: gdns.TypeA, Class: gdns.ClassIN}},
		Answers:   []gdns.Resource{{Name: "runtime.test.", Type: gdns.TypeA, Class: gdns.ClassIN, TTL: 60, Data: []byte{192, 0, 2, 3}}},
	}
	backend.stores.DNS.Storage("runtime").Set("", message, time.Now())
	err := backend.stores.KV.PluginStore("runtime").Update(context.Background(), func(tx storage.KVWriteTx) error {
		return tx.Set("key", storage.KValue{Data: []byte("value"), String: true})
	})
	if err != nil {
		t.Fatal(err)
	}
	secretKey, err := backend.stores.Secrets.NewSecret(storage.Secret{Value: "runtime"}, "Runtime")
	if err != nil {
		t.Fatal(err)
	}

	dnsResponse := liveCall(t, backend, api.SurfaceDNS, string(api.MethodDNSValueSnapshot), api.DNSValueRequest{Cache: "runtime", Key: "runtime.test.|1|1"})
	var dnsSnapshot api.DNSValueSnapshot
	decodeResult(t, dnsResponse, &dnsSnapshot)
	if !dnsSnapshot.Found {
		t.Fatal("runtime DNS write is absent from API")
	}
	secretResponse := liveCall(t, backend, api.SurfaceSecrets, string(api.MethodSecretsSnapshot), struct{}{})
	var secretSnapshot api.SecretsSnapshot
	decodeResult(t, secretResponse, &secretSnapshot)
	if len(secretSnapshot.Secrets) != 1 || secretSnapshot.Secrets[0].Key != secretKey {
		t.Fatalf("runtime secret absent from API: %#v", secretSnapshot)
	}

	var events []api.Message
	if err := json.Unmarshal(backend.DrainEvents(), &events); err != nil {
		t.Fatal(err)
	}
	want := map[api.Surface]bool{api.SurfaceDNS: false, api.SurfaceKV: false, api.SurfaceSecrets: false}
	for _, event := range events {
		if _, tracked := want[event.Surface]; tracked {
			want[event.Surface] = true
		}
	}
	for surface, found := range want {
		if !found {
			t.Fatalf("no runtime %s event in %#v", surface, events)
		}
	}
}

func TestConfigStoreUsesDefaultForMissingConfig(t *testing.T) {
	persistence := newMemoryPersistence()
	store, err := NewConfigStore(persistence)
	if err != nil {
		t.Fatal(err)
	}
	text, _ := store.Snapshot()
	for _, want := range []string{
		`"Mock Network"`, `"almagest/MockNetwork"`,
		`"Http Client"`, `"almagest/HttpClient"`,
		`"Http Server"`, `"almagest/HttpServer"`,
		`"pasta/StringConstant"`,
		`"almagest/VTun"`, `"almagest/Spoofer"`, `"almagest/TunCopy"`,
		`"almagest/DnsServer"`, `"almagest/DnsCache"`, `"almagest/StaticDns"`,
		`"Tun -> [Spoofer] Tun"`, `"Tun -> [Direct TUN Link] TunA"`,
		`"output -> [Http Server] Response"`, `"output -> [TUN HTTP Server] Response"`,
		`"DNS -> [DNS Cache] DNS"`, `"DNS -> [Static DNS] DNS"`,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("default config does not contain %q: %s", want, text)
		}
	}
}

func TestDefaultConfigRunsTunAndDNSDemos(t *testing.T) {
	backend, _ := newDefaultTestLiveBackend(t)
	waitForLog(t, backend.logs, "listening on 10.220.0.10:8081")
	waitForLog(t, backend.logs, "listening on 10.219.0.2:8082")
	waitForLog(t, backend.logs, "listening on udp4/10.219.0.2:53")

	for _, name := range []string{"Http Client", "TUN HTTP Client"} {
		client, found := backend.engine.NodeByName(almagest.MainWorkspace, name)
		if !found {
			t.Fatalf("default %q node was not created", name)
		}
		button, err := json.Marshal(formular.ButtonPressMessage{
			MessageBase: formular.MessageBase{Type: formular.MessageButtonPress, MenuID: pasta.NodeMenuID(client), MenuGeneration: 1},
			BlockID:     "request", ButtonID: "send",
		})
		if err != nil {
			t.Fatal(err)
		}
		trigger := liveCall(t, backend, api.SurfaceEngine, string(api.MethodEngineSendNodeFormularMessage), api.SendNodeFormularMessageRequest{
			Workspace: almagest.MainWorkspace,
			Node:      client,
			Message:   button,
		})
		if trigger.Error != nil {
			t.Fatalf("trigger %q: %#v", name, trigger.Error)
		}
		waitForNodeLog(t, backend.logs, client, "complete: 200 OK")
	}
}

func TestConfigStoreTreatsPersistedEmptyConfigAsObject(t *testing.T) {
	persistence := newMemoryPersistence()
	persistence.values[configStorageKey] = []byte(" \n")
	store, err := NewConfigStore(persistence)
	if err != nil {
		t.Fatal(err)
	}
	text, _ := store.Snapshot()
	if text != "{}\n" {
		t.Fatalf("text = %q", text)
	}
}

func TestConfigStoreFormatsHuJSONAndPreservesComments(t *testing.T) {
	persistence := newMemoryPersistence()
	store, err := NewConfigStore(persistence)
	if err != nil {
		t.Fatal(err)
	}
	input := []byte(`{/* keep this comment */ "nodes":{"Mock":{"Class":"almagest/MockNetwork",},},}`)
	if _, err = store.Store(input, ""); err != nil {
		t.Fatal(err)
	}
	text, _ := store.Snapshot()
	if !strings.Contains(text, "/* keep this comment */") {
		t.Fatalf("formatted config lost its comment: %q", text)
	}
	if !strings.Contains(text, "\n\t/* keep this comment */ \"nodes\":") || !strings.HasSuffix(text, "\n") {
		t.Fatalf("config is not pretty formatted: %q", text)
	}
	if string(persistence.values[configStorageKey]) != text {
		t.Fatalf("persisted config differs from snapshot: %q != %q", persistence.values[configStorageKey], text)
	}
}

func TestLiveBackendConfigRunsHTTPDemo(t *testing.T) {
	backend, _ := newTestLiveBackend(t)
	snapshotResponse := liveCall(t, backend, api.SurfaceConfig, string(api.MethodConfigSnapshot), struct{}{})
	var initial api.ConfigSnapshot
	decodeResult(t, snapshotResponse, &initial)
	configText := `{
		"nodes": {
			"Mock": {"Class":"almagest/MockNetwork"},
			"Server": {"Class":"almagest/HttpServer","Links":["Network -> [Mock] Network"],"host":"127.0.0.1","port":18081,"response":"webdemo works"},
			"Client": {"Class":"almagest/HttpClient","Links":["Network -> [Mock] Network"],"url":"http://127.0.0.1:18081/"}
		}
	}`
	update := liveCall(t, backend, api.SurfaceConfig, string(api.MethodConfigUpdate), api.ConfigUpdateRequest{Text: configText, Revision: initial.Revision})
	if update.Error != nil {
		t.Fatalf("config update: %#v", update.Error)
	}

	client, found := backend.engine.NodeByName(almagest.MainWorkspace, "Client")
	if !found {
		t.Fatal("configured HTTP client was not created")
	}
	waitForLog(t, backend.logs, "listening on 127.0.0.1:18081")
	button, err := json.Marshal(formular.ButtonPressMessage{
		MessageBase: formular.MessageBase{Type: formular.MessageButtonPress, MenuID: pasta.NodeMenuID(client), MenuGeneration: 1},
		BlockID:     "request", ButtonID: "send",
	})
	if err != nil {
		t.Fatal(err)
	}
	trigger := liveCall(t, backend, api.SurfaceEngine, string(api.MethodEngineSendNodeFormularMessage), api.SendNodeFormularMessageRequest{Workspace: almagest.MainWorkspace, Node: client, Message: button})
	if trigger.Error != nil {
		t.Fatalf("trigger client: %#v", trigger.Error)
	}
	waitForLog(t, backend.logs, "complete: 200 OK")
	waitForLog(t, backend.logs, "GET /")

	conflict := liveCall(t, backend, api.SurfaceConfig, string(api.MethodConfigUpdate), api.ConfigUpdateRequest{Text: "{}", Revision: initial.Revision})
	if conflict.Error == nil || conflict.Error.Code != api.ErrorConflict {
		t.Fatalf("stale revision error = %#v", conflict.Error)
	}
}

func TestBrowserLogFactoryCachesAndStreamsEntries(t *testing.T) {
	var browserEntries []api.LogEntry
	logs := NewLogFactory(2, func(entry api.LogEntry) { browserEntries = append(browserEntries, entry) })
	var streamed []api.LogEntry
	_, initial := logs.Subscribe(func(entry api.LogEntry) { streamed = append(streamed, entry) })
	if len(initial) != 0 {
		t.Fatalf("initial entries = %#v", initial)
	}
	logger := logs.NodeLogger(7, "test/Class")
	logger.Info("one")
	logger.Warn("two")
	logger.Err("three")
	if len(browserEntries) != 3 || len(streamed) != 3 {
		t.Fatalf("browser=%d streamed=%d", len(browserEntries), len(streamed))
	}
	cached := logs.Snapshot()
	if len(cached) != 2 || cached[0].Message != "two" || cached[1].Message != "three" {
		t.Fatalf("cache = %#v", cached)
	}
	if cached[1].NodeID != 7 || cached[1].NodeClass != "test/Class" {
		t.Fatalf("source metadata = %#v", cached[1])
	}
}

func TestLiveBackendLogAPIReturnsCacheAndEvents(t *testing.T) {
	backend, _ := newTestLiveBackend(t)
	backend.logs.EngineLogger().Info("cached")
	response := liveCall(t, backend, api.SurfaceLog, string(api.MethodLogSubscribe), struct{}{})
	var subscription api.SubscribeLogsResponse
	decodeResult(t, response, &subscription)
	if len(subscription.Entries) == 0 || subscription.Entries[len(subscription.Entries)-1].Message != "cached" {
		t.Fatalf("cached log response = %#v", subscription)
	}
	backend.logs.EngineLogger().Warn("live")
	var events []api.Message
	if err := json.Unmarshal(backend.DrainEvents(), &events); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, event := range events {
		if event.Surface == api.SurfaceLog && event.Method == string(api.EventLogEntry) {
			found = true
		}
	}
	if !found {
		t.Fatalf("no live log event in %#v", events)
	}
}

func TestLiveBackendLogAPIReturnsEmptyArrayForEmptyCache(t *testing.T) {
	backend, _ := newTestLiveBackend(t)
	response := liveCall(t, backend, api.SurfaceLog, string(api.MethodLogSubscribe), struct{}{})
	if !strings.Contains(string(response.Result), `"entries":[]`) {
		t.Fatalf("log subscription result = %s", response.Result)
	}
}

func waitForLog(t *testing.T, logs *LogFactory, part string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		for _, entry := range logs.Snapshot() {
			if strings.Contains(entry.Message, part) {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("log does not contain %q: %#v", part, logs.Snapshot())
}

func waitForNodeLog(t *testing.T, logs *LogFactory, node uint64, part string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		for _, entry := range logs.Snapshot() {
			if entry.NodeID == node && strings.Contains(entry.Message, part) {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("node %d log does not contain %q: %#v", node, part, logs.Snapshot())
}

func liveCall(t *testing.T, backend *Backend, surface api.Surface, method string, payload any) api.Message {
	t.Helper()
	request, err := api.NewRequest("live", surface, method, payload)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := api.MarshalMessage(request)
	if err != nil {
		t.Fatal(err)
	}
	response, err := api.UnmarshalMessage(backend.Handle(raw))
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func TestLiveBackendDoesNotAdvertiseLocaleOrSystemSurfaces(t *testing.T) {
	backend, _ := newTestLiveBackend(t)
	response := liveCall(t, backend, api.SurfaceAdmin, string(api.MethodAdminHello), api.HelloRequest{})
	var hello api.HelloResponse
	if err := json.Unmarshal(response.Result, &hello); err != nil {
		t.Fatal(err)
	}
	if _, ok := hello.Surfaces[string(api.SurfaceSystem)]; ok {
		t.Fatal("system surface is advertised")
	}
	if _, ok := hello.Surfaces[string(api.SurfaceSnapshots)]; !ok {
		t.Fatal("snapshots surface is not advertised")
	}
	if _, ok := hello.Surfaces[string(api.SurfaceTLS)]; !ok {
		t.Fatal("TLS surface is not advertised")
	}
	admin := hello.Surfaces[string(api.SurfaceAdmin)].(map[string]any)
	for _, method := range admin["methods"].([]any) {
		if method == string(api.MethodAdminLocaleSnapshot) || method == string(api.MethodAdminSetLocale) {
			t.Fatal("locale method is advertised")
		}
	}
}

func TestLiveBackendTLSPlaceholderIsEmptyAndReadOnly(t *testing.T) {
	backend, _ := newTestLiveBackend(t)
	response := liveCall(t, backend, api.SurfaceTLS, string(api.MethodTLSSnapshot), struct{}{})
	if response.Error != nil || !strings.Contains(string(response.Result), `"certs":[]`) || !strings.Contains(string(response.Result), `"policies":[]`) {
		t.Fatalf("TLS snapshot = %#v", response)
	}

	subscribe := liveCall(t, backend, api.SurfaceTLS, string(api.MethodTLSSubscribe), struct{}{})
	var subscription api.TLSSubscribeResponse
	decodeResult(t, subscribe, &subscription)
	if subscription.SubscriptionID == 0 || len(subscription.Snapshot.Certs) != 0 || len(subscription.Snapshot.Policies) != 0 {
		t.Fatalf("TLS subscription = %#v", subscription)
	}
	unsubscribed := liveCall(t, backend, api.SurfaceTLS, string(api.MethodTLSUnsubscribe), api.TLSSubscriptionRequest{SubscriptionID: subscription.SubscriptionID})
	if unsubscribed.Error != nil {
		t.Fatalf("TLS unsubscribe = %#v", unsubscribed.Error)
	}

	builtin := liveCall(t, backend, api.SurfaceTLS, string(api.MethodTLSBuiltinCA), struct{}{})
	if builtin.Error == nil || builtin.Error.Code != api.ErrorNotFound {
		t.Fatalf("built-in CA error = %#v", builtin.Error)
	}
	for _, method := range api.TLSMethods() {
		if !api.IsTLSMutation(method) {
			continue
		}
		mutation := liveCall(t, backend, api.SurfaceTLS, method, struct{}{})
		if mutation.Error == nil || mutation.Error.Code != api.ErrorForbidden {
			t.Fatalf("TLS mutation %q error = %#v", method, mutation.Error)
		}
	}
}

func TestLiveBackendSnapshotsPlaceholderIsEmptyAndReadOnly(t *testing.T) {
	backend, _ := newTestLiveBackend(t)
	response := liveCall(t, backend, api.SurfaceSnapshots, string(api.MethodSnapshotsList), struct{}{})
	if response.Error != nil || !strings.Contains(string(response.Result), `"snapshots":[]`) {
		t.Fatalf("snapshots list = %#v", response)
	}

	exported := liveCall(t, backend, api.SurfaceSnapshots, string(api.MethodSnapshotsExport), api.SnapshotUUIDRequest{UUID: "missing"})
	if exported.Error == nil || exported.Error.Code != api.ErrorNotFound {
		t.Fatalf("snapshot export error = %#v", exported.Error)
	}
	for _, method := range api.SnapshotsMethods() {
		if !api.IsSnapshotsMutation(method) {
			continue
		}
		mutation := liveCall(t, backend, api.SurfaceSnapshots, method, struct{}{})
		if mutation.Error == nil || mutation.Error.Code != api.ErrorForbidden {
			t.Fatalf("snapshot mutation %q error = %#v", method, mutation.Error)
		}
	}
}
