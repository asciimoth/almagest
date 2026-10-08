//go:build windows && winintegration

package integration_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/asciimoth/almagest/bin/api"
	"github.com/tailscale/hujson"
	"golang.org/x/sys/windows"
	"golang.zx2c4.com/wireguard/ipc/namedpipe"
)

// These tests are ports of the portable and hermetic Docker cases listed in
// WINVM_E2E_PORTING_PLAN.md. They deliberately use the production daemon and
// CLI. The graph fixtures are shared with the Docker cases so the two gates do
// not silently test different graph shapes.

func TestWindowsBatch1AdminStorageSurfaceValidation(t *testing.T) {
	d := newBatch1Daemon(t, "admin_storage_surface_validation", true, nil)
	d.start()
	defer d.stop()

	for _, call := range []rawCall{
		{api.SurfaceSecrets, "snapshot", `{}`},
		{api.SurfaceSecrets, "set_value", `{"key":"surface-secret","value":"one"}`},
		{api.SurfaceSecrets, "rename", `{"key":"surface-secret","new_key":"surface-renamed"}`},
		{api.SurfaceKV, "ensure", `{"path":["surface","nested"]}`},
		{api.SurfaceKV, "set", `{"path":["surface","nested"],"key":"binary","value":{"data":"AAEC/w==","encoding":"base64","hidden":true}}`},
		{api.SurfaceDNS, "set", `{"cache":"surface","record":{"name":"address.surface.test","type":"A","ttl":60,"value":"192.0.2.1"}}`},
		{api.SurfaceTLS, "snapshot", `{}`},
		{api.SurfaceTLS, "builtin_ca", `{}`},
	} {
		d.raw(call)
	}
	for _, call := range []rawCall{
		{api.SurfaceSecrets, "set_value", `{"key":"","value":"x"}`},
		{api.SurfaceKV, "set", `{"path":[],"key":"bad","value":{"data":"!","encoding":"base64"}}`},
		{api.SurfaceDNS, "set", `{"cache":"surface","record":{"name":"x.test","type":"MX","ttl":1,"value":"bad"}}`},
		{api.SurfaceTLS, "set_policy", `"wrong-type"`},
	} {
		d.rawFails(call)
	}
	d.raw(rawCall{api.SurfaceKV, "value_snapshot", `{"path":["surface","nested"],"key":"binary"}`})
	d.raw(rawCall{api.SurfaceDNS, "keys_snapshot", `{"cache":"surface"}`})
}

func TestWindowsBatch1AdminSubscriptionTrees(t *testing.T) {
	d := newBatch1Daemon(t, "admin_subscription_trees", true, nil)
	d.start()
	defer d.stop()
	c := openBatch1Admin(t, d)
	defer c.close()

	root := decodeResult[api.KVSubscribeStoresResponse](t, c.request(api.SurfaceKV, string(api.MethodKVSubscribeStores), nil))
	entries := decodeResult[api.KVSubscribeEntriesResponse](t, c.request(api.SurfaceKV, string(api.MethodKVSubscribeEntries), api.KVEntriesRequest{Path: []string{"tree"}}))
	value := decodeResult[api.KVSubscribeValueResponse](t, c.request(api.SurfaceKV, string(api.MethodKVSubscribeValue), api.KVValueRequest{Path: []string{"tree"}, Key: "leaf"}))
	if root.SubscriptionID == 0 || entries.SubscriptionID == 0 || value.SubscriptionID == 0 {
		t.Fatal("KV subscription tree returned a zero subscription ID")
	}
	c.request(api.SurfaceKV, string(api.MethodKVUnsubscribe), api.KVSubscriptionRequest{SubscriptionID: entries.SubscriptionID})
	c.request(api.SurfaceKV, string(api.MethodKVUnsubscribe), api.KVSubscriptionRequest{SubscriptionID: entries.SubscriptionID})
	c.request(api.SurfaceKV, string(api.MethodKVUnsubscribe), api.KVSubscriptionRequest{SubscriptionID: root.SubscriptionID})
	c.request(api.SurfaceKV, string(api.MethodKVSet), api.KVSetRequest{Path: []string{"tree"}, Key: "leaf", Value: api.KVValue{Data: "updated", String: true}})

	dnsRoot := decodeResult[api.DNSSubscribeCachesResponse](t, c.request(api.SurfaceDNS, string(api.MethodDNSSubscribeCaches), nil))
	dnsKeys := decodeResult[api.DNSSubscribeKeysResponse](t, c.request(api.SurfaceDNS, string(api.MethodDNSSubscribeKeys), api.DNSKeysRequest{Cache: "tree"}))
	dnsValue := decodeResult[api.DNSSubscribeValueResponse](t, c.request(api.SurfaceDNS, string(api.MethodDNSSubscribeValue), api.DNSValueRequest{Cache: "tree", Key: "absent"}))
	if dnsRoot.SubscriptionID == 0 || dnsKeys.SubscriptionID == 0 || dnsValue.SubscriptionID == 0 {
		t.Fatal("DNS subscription tree returned a zero subscription ID")
	}
	c.request(api.SurfaceDNS, string(api.MethodDNSUnsubscribe), api.DNSSubscriptionRequest{SubscriptionID: dnsKeys.SubscriptionID})
	c.request(api.SurfaceDNS, string(api.MethodDNSUnsubscribe), api.DNSSubscriptionRequest{SubscriptionID: dnsRoot.SubscriptionID})
	secret := decodeResult[api.SecretsSubscribeResponse](t, c.request(api.SurfaceSecrets, string(api.MethodSecretsSubscribe), nil))
	c.request(api.SurfaceSecrets, string(api.MethodSecretsUnsubscribe), api.SecretsSubscriptionRequest{SubscriptionID: secret.SubscriptionID})
	tls := decodeResult[api.TLSSubscribeResponse](t, c.request(api.SurfaceTLS, string(api.MethodTLSSubscribe), nil))
	c.request(api.SurfaceTLS, string(api.MethodTLSUnsubscribe), api.TLSSubscriptionRequest{SubscriptionID: tls.SubscriptionID})
	c.request(api.SurfaceDNS, string(api.MethodDNSSet), api.DNSSetRequest{Cache: "tree", Record: api.DNSRecordInput{Name: "tree.test", Type: "A", TTL: 60, Value: "192.0.2.1"}})
	c.expectNoEvent()
}

func TestWindowsBatch1ConfigEditingAPI(t *testing.T) {
	d := newBatch1Daemon(t, "config_editing_api", true, reservePorts(t, 1081, 1082, 8080, 8081, 8082))
	d.start()
	defer d.stop()

	before := decodeCLI[api.ConfigSnapshot](t, d.cliJSON("config", "snapshot"))
	if before.Revision == "" || before.ReadOnly || strings.Contains(before.Text, "config-api-protected-value") {
		t.Fatalf("invalid editable config snapshot: %#v", before)
	}
	wantSOCKSHTTPHost(t, d.batchPort(1081), "alpha", d.batchPort(8080), "router-a")
	wantSOCKSHTTPHost(t, d.batchPort(1082), "127.0.0.1", d.batchPort(8081), "stable")
	updated := setBatch1NodeValues(t, before.Text, "Router", map[string]any{
		"rules": "ADDR_S alpha\nSLOT 2\n\nTRUE\nSLOT 1",
		"Pos":   `{"x":10,"y":20}`,
	})
	path := filepath.Join(d.root, "config-update.hujson")
	writePrivateWindowsFile(t, path, []byte(updated))
	d.cliJSON("config", "apply", "--revision", before.Revision, path)
	d.cliFails("config", "apply", "--revision", before.Revision, path)
	after := decodeCLI[api.ConfigSnapshot](t, d.cliJSON("config", "snapshot"))
	if after.Revision == before.Revision || strings.Contains(after.Text, "config-api-protected-value") {
		t.Fatal("config update did not advance revision or exposed protected data")
	}
	wantSOCKSHTTPHost(t, d.batchPort(1081), "alpha", d.batchPort(8080), "router-b")
	wantSOCKSHTTPHost(t, d.batchPort(1082), "127.0.0.1", d.batchPort(8081), "stable")
	d.stop()
	d.start()
	restarted := decodeCLI[api.ConfigSnapshot](t, d.cliJSON("config", "snapshot"))
	if restarted.Revision != after.Revision {
		t.Fatalf("config revision changed across restart: %q != %q", restarted.Revision, after.Revision)
	}
	wantSOCKSHTTPHost(t, d.batchPort(1081), "alpha", d.batchPort(8080), "router-b")
}

func TestWindowsBatch1DNSAdminExpiration(t *testing.T) {
	d := newBatch1Daemon(t, "dns_admin_expiration", true, nil)
	d.start()
	defer d.stop()
	set := decodeRaw[api.DNSSetResponse](t, d.raw(rawCall{api.SurfaceDNS, "set", `{"cache":"expiration","record":{"name":"short.test","type":"A","class":"CH","ttl":5,"value":"192.0.2.10"}}`}))
	if set.Key == "" || !set.Snapshot.Found {
		t.Fatalf("short-lived DNS record was not stored: %#v", set)
	}
	waitFor(t, "expired DNS value removal", func() bool {
		value := decodeRaw[api.DNSValueSnapshot](t, d.raw(rawCall{api.SurfaceDNS, "value_snapshot", fmt.Sprintf(`{"cache":"expiration","key":%q}`, set.Key)}))
		return !value.Found
	})
	d.rawFails(rawCall{api.SurfaceDNS, "set", `{"cache":"expiration","record":{"name":"bad.test","type":"SRV","class":"BAD","ttl":1,"value":"bad"}}`})
}

func TestWindowsBatch1EngineSurfaceValidation(t *testing.T) {
	d := newBatch1Daemon(t, "engine_surface_validation", true, nil)
	d.start()
	defer d.stop()
	workspace := decodeCLI[api.WorkspaceSnapshotResponse](t, d.cliJSON("engine", "workspace-snapshot"))
	if len(workspace.Snapshot.Classes) == 0 {
		t.Fatal("engine returned no registered node classes")
	}
	for _, class := range workspace.Snapshot.Classes {
		if class.Class == "" {
			t.Fatal("engine returned a class without a name")
		}
		d.cliJSON("engine", "node-class-snapshot", "--class", class.Class)
		d.cliJSON("engine", "node-class-long-description", "--class", class.Class)
	}
	d.rawFails(rawCall{api.SurfaceEngine, "workspace_snapshot", `{"workspace":"missing"}`})
	d.rawFails(rawCall{api.SurfaceEngine, "add_node", `"wrong-type"`})
	d.cliJSON("admin", "ping", "--nonce", "after-engine-validation")
}

func TestWindowsBatch1LibControlPlaneLifecycle(t *testing.T) {
	d := newBatch1Daemon(t, "lib_control_plane_lifecycle", true, nil)
	d.start()
	assertGraphNodes(t, d, "KV Writer", "KV Reader", "Control Server", "Control Client")
	watch := startCLIWatch(t, d, "engine", "watch", "--full-snapshot")
	waitFor(t, "control-plane full snapshots", func() bool {
		data, _ := os.ReadFile(watch)
		return bytes.Contains(data, []byte("workspace_snapshot")) && bytes.Contains(data, []byte("worker_snapshot"))
	})
	d.stop()
	d.start()
	defer d.stop()
	assertGraphNodes(t, d, "KV Writer", "KV Reader", "Control Server", "Control Client")
}

func TestWindowsBatch1SnapshotsValidation(t *testing.T) {
	d := newBatch1Daemon(t, "snapshots_validation", true, nil)
	d.start()
	defer d.stop()
	for _, operation := range []string{"export", "delete", "restore"} {
		d.cliFails("snapshots", operation, "malformed")
	}
	missing := "00000000-0000-4000-8000-000000000001"
	for _, operation := range []string{"export", "delete", "restore"} {
		d.cliFails("snapshots", operation, missing)
	}
	empty := filepath.Join(d.root, "empty.zip")
	invalid := filepath.Join(d.root, "invalid.zip")
	writePrivateWindowsFile(t, empty, nil)
	writePrivateWindowsFile(t, invalid, []byte("not a zip archive"))
	d.cliFails("snapshots", "import", empty)
	d.cliFails("snapshots", "import", invalid)
	d.cliFails("snapshots", "create", "--name", strings.Repeat("x", 257))
	created := struct {
		UUID string `json:"uuid"`
	}{}
	decodeJSON(t, d.cliJSON("snapshots", "create", "--name", "valid"), &created)
	if created.UUID == "" {
		t.Fatal("snapshot create returned an empty UUID")
	}
	archive := filepath.Join(d.root, "valid.zip")
	d.cliJSON("snapshots", "export", "--output", archive, created.UUID)
	d.cliFails("snapshots", "rename", "--name", strings.Repeat("x", 257), created.UUID)
	d.cliFails("snapshots", "import", archive)
	d.cliJSON("snapshots", "list")
	d.cliJSON("admin", "ping", "--nonce", "snapshot-errors-survived")
}

func TestWindowsBatch1StorageAdminAPI(t *testing.T) {
	d := newBatch1Daemon(t, "storage_admin_api", true, nil)
	d.start()
	d.raw(rawCall{api.SurfaceKV, "set", `{"path":["retained"],"key":"value","value":{"data":"persistent","string":true}}`})
	dns := decodeRaw[api.DNSSetResponse](t, d.raw(rawCall{api.SurfaceDNS, "set", `{"cache":"retained","record":{"name":"retained.test","type":"TXT","ttl":60,"value":"retained"}}`}))
	d.raw(rawCall{api.SurfaceSecrets, "set_value", `{"key":"mutable","value":"retained"}`})
	d.cliJSON("engine", "set-node-name", "--name", "Node One", "--new-name", "Node Renamed")
	d.stop()
	d.start()
	defer d.stop()
	if !bytes.Contains(d.raw(rawCall{api.SurfaceKV, "value_snapshot", `{"path":["retained"],"key":"value"}`}), []byte("persistent")) {
		t.Fatal("KV value did not persist")
	}
	if !decodeRaw[api.DNSValueSnapshot](t, d.raw(rawCall{api.SurfaceDNS, "value_snapshot", fmt.Sprintf(`{"cache":"retained","key":%q}`, dns.Key)})).Found {
		t.Fatal("DNS value did not persist")
	}
	assertGraphNodes(t, d, "Node Renamed")
}

func TestWindowsBatch1DNSGraphLifecycle(t *testing.T) {
	d := runBatch1GraphFixture(t, "dns_graph_lifecycle", nil,
		"Fallback DNS", "DNS Cache", "DNS Router", "Fallback Query", "Router One Query")
	defer d.stop()
	clickAndWait(t, d, "Fallback Query", "request", "send", "10.20.0.1")
	cycleLink(t, d, "Fallback DNS", "slot 2", "Primary DNS", "DNS")
	clickAndWait(t, d, "Fallback Query", "request", "send", "10.20.0.1")
	d.rawFails(rawCall{api.SurfaceEngine, "workspace_snapshot", `{"workspace":"not-main"}`})
}

func TestWindowsBatch1DNSTransportStackLifecycle(t *testing.T) {
	ports := reservePorts(t, 1184)
	d := runBatch1GraphFixture(t, "dns_transport_stack_lifecycle", ports,
		"DNS Server", "DNS Client", "DNS Cache", "DNS Inject", "HTTP Client", "Local Proxy")
	defer d.stop()
	clickAndWait(t, d, "Cache Query", "request", "send", "192.0.2.10")
	clickAndWait(t, d, "HTTP Client", "request", "send", "200 OK")
	cycleLink(t, d, "DNS Server", "DNS", "DNS Log", "DNS")
	clickAndWait(t, d, "Cache Query", "request", "send", "192.0.2.10")
	restartAndAssert(t, d, "DNS Server", "DNS Client", "DNS Inject")
}

func TestWindowsBatch1GraphHistoryRuntime(t *testing.T) {
	ports := reservePorts(t, 8080)
	d := runBatch1GraphFixture(t, "graph_history_runtime", ports, "History Network", "History Server", "History Client")
	defer d.stop()
	clickAndWait(t, d, "History Client", "request", "send", "200 OK")
	cycleLink(t, d, "History Client", "Network", "History Network", "Network")
	d.cliJSON("engine", "undo")
	d.cliJSON("engine", "redo")
	clickAndWait(t, d, "History Client", "request", "send", "200 OK")
}

func TestWindowsBatch1NetworkRemapperLifecycle(t *testing.T) {
	ports := reservePorts(t, 1181, 1182)
	d := runBatch1GraphFixture(t, "network_remapper_lifecycle", ports, "Remapper", "Proxy One", "Proxy Two", "Mock A", "Mock B")
	defer d.stop()
	wantSOCKSHTTPHost(t, ports[1181], "service.test", 80, "provider-a-port-8080")
	d.cliJSON("graph", "apply-menu", "--name", "Remapper", "--json", `{"config":{"rules":"not a valid remapper program"}}`)
	wantSOCKSHTTPHost(t, ports[1182], "service.test", 80, "provider-a-port-8080")
	d.cliJSON("graph", "apply-menu", "--name", "Remapper", "--json", `{"config":{"rules":"DIAL\nADDR_S service.test\nAND\nPORT 80\nAND\nREMAP DST ADDR_PORT 127.0.0.1:8080"}}`)
	cycleLink(t, d, "Remapper", "Network", "Mock A", "Network")
	wantSOCKSHTTPHost(t, ports[1182], "service.test", 80, "provider-a-port-8080")
}

func TestWindowsBatch1NetworkRouterMockHTTP(t *testing.T) {
	ports := reservePorts(t, 1081)
	d := runBatch1GraphFixture(t, "network_router_mock_http", ports, "LocalProxy 1684", "NetworkRouter 1686", "HttpServer 1701", "HttpServer 1710", "HttpServer 1719")
	defer d.stop()
	wantSOCKSHTTPHost(t, ports[1081], "host1", 80, "Server 1")
	wantSOCKSHTTPHost(t, ports[1081], "host2", 80, "Server 2")
	wantSOCKSHTTPHost(t, ports[1081], "other", 80, "Server 3")
	d.cliJSON("graph", "apply-menu", "--name", "NetworkRouter 1686", "--json", `{"config":{"rules":"invalid router rules"}}`)
	wantSOCKSHTTPHost(t, ports[1081], "host1", 80, "Server 1")
	cycleLink(t, d, "LocalProxy 1684", "proxy", "NetworkRouter 1686", "Network")
	wantSOCKSHTTPHost(t, ports[1081], "host1", 80, "Server 1")
}

func TestWindowsBatch1SelectMockSwitchHTTP(t *testing.T) {
	ports := reservePorts(t, 1081, 1082)
	d := runBatch1GraphFixture(t, "select_mock_switch_http", ports, "BoolConstant 1815", "LocalProxy 1857", "LocalProxy 1859")
	defer d.stop()
	wantSOCKSHTTPHost(t, ports[1081], "127.0.0.1", 8080, "Server A")
	wantSOCKSHTTPHost(t, ports[1082], "127.0.0.1", 8080, "Server B")
	d.cliJSON("graph", "apply-menu", "--name", "BoolConstant 1815", "--json", `{"state":{"value":true}}`)
	wantSOCKSHTTPHost(t, ports[1081], "127.0.0.1", 8080, "Server B")
	wantSOCKSHTTPHost(t, ports[1082], "127.0.0.1", 8080, "Server A")
}

func TestWindowsBatch1StaticDNSRecords(t *testing.T) {
	d := runBatch1GraphFixture(t, "static_dns_records", nil, "Static DNS", "Fallback DNS", "DNS Query")
	defer d.stop()
	clickAndWait(t, d, "DNS Query", "request", "send", "NOERROR")
	cycleLink(t, d, "Static DNS", "Fallback", "Fallback DNS", "DNS")
	d.cliJSON("graph", "apply-menu", "--name", "Static DNS", "--json", `{"config":{"records":"broken A not-an-address"}}`)
	clickAndWait(t, d, "DNS Query", "request", "send", "NOERROR")
	d.cliJSON("graph", "apply-menu", "--name", "Static DNS", "--json", `{"config":{"ttl":-1}}`)
	clickAndWait(t, d, "DNS Query", "request", "send", "NOERROR")
}

func TestWindowsBatch1TunMiddlewareLifecycle(t *testing.T) {
	d := runBatch1GraphFixture(t, "tun_middleware_lifecycle", nil, "Client VTun", "Client Stripper", "Client Firewall", "Lifecycle Copy", "Server VTun", "Lifecycle Spoofer")
	defer d.stop()
	clickAndWait(t, d, "Lifecycle Client", "request", "send", "200 OK")
	cycleLink(t, d, "Client Firewall", "Tun", "Lifecycle Copy", "TunA")
	clickAndWait(t, d, "Lifecycle Client", "request", "send", "200 OK")
	restartAndAssert(t, d, "Client VTun", "Client Firewall", "Lifecycle Copy")
}

func TestWindowsBatch1UDPLifecycle(t *testing.T) {
	ports := reservePorts(t, 19091, 19092, 19093, 19094, 19095)
	d := runBatch1GraphFixture(t, "udp_lifecycle", ports, "UDP Network", "UDP Log", "UDP Server", "UDP Client")
	defer d.stop()
	clickAndWait(t, d, "UDP Client", "config", "send", "pong:initial-ping")
	cycleLink(t, d, "UDP Client", "Network", "UDP Log", "Network")
	clickAndWait(t, d, "UDP Client", "config", "send", "pong:initial-ping")
	restartAndAssert(t, d, "UDP Server", "UDP Client")
}

func TestWindowsBatch1WireGuardWGConfigLifecycle(t *testing.T) {
	ports := reservePorts(t, 51870, 51871)
	d := runBatch1GraphFixture(t, "wireguard_wgconfig_lifecycle", ports, "WireGuard Client", "Client Config", "WireGuard Server", "Server Config", "HTTP Client")
	defer d.stop()
	clickAndWait(t, d, "HTTP Client", "request", "send", "200 OK")
	cycleLink(t, d, "Client Config", "Network", "Host Network", "Network")
	clickAndWait(t, d, "HTTP Client", "request", "send", "200 OK")
	restartAndAssert(t, d, "Client Config", "Server Config")
}

func TestWindowsBatch1LocalNestedWireGuard(t *testing.T) {
	ports := reservePorts(t, 26120)
	d := runBatch1GraphFixture(t, "local_nested_wireguard", ports, "WireGuard Client 1501", "WireGuard Server 1502", "Inner WireGuard Client 1507", "Inner WireGuard Server 1508", "HttpClient 1612", "UDPPingClient 1506")
	defer d.stop()
	const nestedNetworkTimeout = 90 * time.Second
	clickAndWaitTimeout(t, d, nestedNetworkTimeout, "UDPPingClient 1506", "config", "send", "pong:nested-wireguard-udp-ping")
	clickAndWaitTimeout(t, d, nestedNetworkTimeout, "HttpClient 1612", "request", "send", "200 OK")
}

func TestWindowsBatch1LocalProxyMockHTTP(t *testing.T) {
	ports := reservePorts(t, 1081, 8091)
	d := runBatch1GraphFixture(t, "local_proxy_mock_http", ports, "HttpServer 1001", "LocalProxy 1002", "MockNetwork 1003", "LogNetwork 1004")
	defer d.stop()
	wantSOCKSHTTP(t, ports[1081], ports[8091], "Debug http server response")
	d.cliJSON("graph", "apply-menu", "--name", "HTTP Response", "--json", `{"state":{"value":"generation-1"}}`)
	d.cliJSON("graph", "apply-menu", "--name", "HTTP Response", "--json", `{"state":{"value":"final-generation"}}`)
	wantSOCKSHTTP(t, ports[1081], ports[8091], "final-generation")
	cycleLink(t, d, "HttpServer 1001", "Network", "LogNetwork 1004", "Network")
	wantSOCKSHTTP(t, ports[1081], ports[8091], "final-generation")
	restartAndAssert(t, d, "HttpServer 1001", "LocalProxy 1002")
	wantSOCKSHTTP(t, ports[1081], ports[8091], "final-generation")
}

func TestWindowsBatch1SocksChainNestedMockHTTP(t *testing.T) {
	ports := reservePorts(t, 1081, 1082, 1083, 1084)
	d := runBatch1GraphFixture(t, "socks_chain_nested_mock_http", ports, "LocalProxy 2013", "SocksClient 2096", "SocksClient 2015", "LocalProxy 2094", "HttpServer 2043")
	defer d.stop()
	wantSOCKSHTTPHost(t, ports[1081], "127.0.0.1", 8080, "Debug http server response")
	cycleLink(t, d, "SocksClient 2096", "out", "SocksClient 2015", "Network")
	wantSOCKSHTTPHost(t, ports[1081], "127.0.0.1", 8080, "Debug http server response")
	restartAndAssert(t, d, "SocksClient 2096", "LocalProxy 2013")
}

func TestWindowsBatch1WireGuardSocksLocal(t *testing.T) {
	ports := reservePorts(t, 1081, 51820, 51821)
	d := runBatch1GraphFixture(t, "wireguard_socks_local", ports, "WireGuard Client", "WireGuard Server", "VTun Client", "VTun Server", "SocksServer 70", "HTTP Client")
	defer d.stop()
	clickAndWait(t, d, "HTTP Client", "request", "send", "200 OK")
}

type rawCall struct {
	surface api.Surface
	method  string
	payload string
}

func newBatch1Daemon(t *testing.T, caseName string, persistent bool, ports map[int]int) *windowsDaemon {
	t.Helper()
	root := newPrivateWindowsTestDir(t)
	pipe := fmt.Sprintf("almagest-b1-%d-%d", os.Getpid(), time.Now().UnixNano())
	regular := readBatch1Fixture(t, caseName, "alm.hujson")
	protected := batch1ProtectedConfig(t, caseName, pipe)
	regular = rewriteBatch1Ports(t, regular, ports)
	protected = rewriteBatch1Ports(t, protected, ports)
	d := newWindowsDaemon(t, root, pipe, regular, protected)
	d.temporary = !persistent
	d.batchPorts = ports
	return d
}

func runBatch1GraphFixture(t *testing.T, caseName string, ports map[int]int, names ...string) *windowsDaemon {
	t.Helper()
	d := newBatch1Daemon(t, caseName, false, ports)
	d.start()
	assertGraphNodes(t, d, names...)
	return d
}

func readBatch1Fixture(t *testing.T, caseName, name string) string {
	t.Helper()
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot find Batch 1 integration source")
	}
	path := filepath.Join(filepath.Dir(source), "..", "..", "e2e", "cases", caseName, name)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read shared %s fixture %s: %v", caseName, name, err)
	}
	return string(data)
}

func batch1ProtectedConfig(t *testing.T, caseName, pipe string) string {
	t.Helper()
	data, err := hujson.Standardize([]byte(readBatch1Fixture(t, caseName, "protected_alm.hujson")))
	if err != nil {
		t.Fatalf("standardize %s protected config: %v", caseName, err)
	}
	var original map[string]any
	if err = json.Unmarshal(data, &original); err != nil {
		t.Fatalf("decode %s protected config: %v", caseName, err)
	}
	result := map[string]any{
		"Admin":  map[string]any{"Pipe": `\\.\pipe\` + pipe},
		"System": map[string]any{"Type": "debug"},
	}
	if caseName == "socks_chain_nested_mock_http" {
		result["System"] = map[string]any{
			"Type":  "debug",
			"Debug": map[string]any{"NativeOutNetwork": true},
		}
	}
	for _, key := range []string{"Secrets", "TLS", "CommandRunner"} {
		if value, found := original[key]; found {
			result[key] = value
		}
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

func rewriteBatch1Ports(t *testing.T, config string, ports map[int]int) string {
	t.Helper()
	if len(ports) == 0 {
		return config
	}
	standard, err := hujson.Standardize([]byte(config))
	if err != nil {
		t.Fatalf("standardize fixture before port allocation: %v", err)
	}
	var document any
	if err = json.Unmarshal(standard, &document); err != nil {
		t.Fatalf("decode fixture before port allocation: %v", err)
	}
	var rewrite func(any, string) any
	rewrite = func(value any, key string) any {
		switch typed := value.(type) {
		case map[string]any:
			for childKey, child := range typed {
				typed[childKey] = rewrite(child, childKey)
			}
			return typed
		case []any:
			for index, child := range typed {
				typed[index] = rewrite(child, key)
			}
			return typed
		case float64:
			if strings.Contains(strings.ToLower(key), "port") {
				if current, found := ports[int(typed)]; found {
					return float64(current)
				}
			}
			return typed
		case string:
			for old, current := range ports {
				typed = strings.ReplaceAll(typed, ":"+strconv.Itoa(old), ":"+strconv.Itoa(current))
			}
			return typed
		default:
			return value
		}
	}
	document = rewrite(document, "")
	encoded, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

func setBatch1NodeValues(t *testing.T, config, node string, values map[string]any) string {
	t.Helper()
	standard, err := hujson.Standardize([]byte(config))
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err = json.Unmarshal(standard, &document); err != nil {
		t.Fatal(err)
	}
	nodes, ok := document["nodes"].(map[string]any)
	if !ok {
		t.Fatal("editable config has no nodes object")
	}
	entry, ok := nodes[node].(map[string]any)
	if !ok {
		t.Fatalf("editable config has no node %q", node)
	}
	for key, value := range values {
		entry[key] = value
	}
	encoded, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

func (d *windowsDaemon) batchPort(original int) int {
	if current, found := d.batchPorts[original]; found {
		return current
	}
	d.t.Fatalf("internal Batch 1 port mapping for %d is unavailable", original)
	return 0
}

func reservePorts(t *testing.T, ports ...int) map[int]int {
	t.Helper()
	result := make(map[int]int, len(ports))
	for _, old := range ports {
		for {
			candidate := reserveTCPPort(t)
			duplicate := false
			for _, assigned := range result {
				if assigned == candidate {
					duplicate = true
					break
				}
			}
			if duplicate {
				continue
			}
			packet, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: candidate})
			if err != nil {
				continue
			}
			if err = packet.Close(); err != nil {
				t.Fatal(err)
			}
			result[old] = candidate
			break
		}
	}
	return result
}

func (d *windowsDaemon) raw(call rawCall) []byte {
	d.t.Helper()
	return d.cliJSON("raw", "--surface", string(call.surface), "--method", call.method, "--payload", call.payload)
}

func (d *windowsDaemon) rawFails(call rawCall) {
	d.t.Helper()
	d.cliFails("raw", "--surface", string(call.surface), "--method", call.method, "--payload", call.payload)
}

func (d *windowsDaemon) cliFails(arguments ...string) {
	d.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), integrationTimeout)
	defer cancel()
	args := append([]string{"--url", d.endpoint}, arguments...)
	output, err := exec.CommandContext(ctx, d.cli, args...).CombinedOutput()
	if err == nil {
		d.t.Fatalf("production CLI %q unexpectedly succeeded: %s", strings.Join(arguments, " "), output)
	}
}

func decodeCLI[T any](t *testing.T, data []byte) T {
	t.Helper()
	var value T
	decodeJSON(t, data, &value)
	return value
}

func decodeRaw[T any](t *testing.T, data []byte) T {
	t.Helper()
	return decodeCLI[T](t, data)
}

func assertGraphNodes(t *testing.T, d *windowsDaemon, names ...string) {
	t.Helper()
	graph := struct {
		Nodes map[string]json.RawMessage `json:"nodes"`
	}{}
	decodeJSON(t, d.cliJSON("graph", "map"), &graph)
	for _, name := range names {
		if _, found := graph.Nodes[name]; !found {
			t.Fatalf("graph fixture is missing node %q; graph=%s", name, d.cliJSON("graph", "map"))
		}
	}
}

func clickAndWait(t *testing.T, d *windowsDaemon, node, block, button, marker string) {
	t.Helper()
	clickAndWaitTimeout(t, d, integrationTimeout, node, block, button, marker)
}

func clickAndWaitTimeout(t *testing.T, d *windowsDaemon, timeout time.Duration, node, block, button, marker string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	attempted := false
	var output []byte
	for time.Now().Before(deadline) {
		output = d.cliJSON("graph", "menu", "--name", node)
		running := bytes.Contains(output, []byte("Status: running..."))
		if attempted && !running && bytes.Contains(output, []byte(marker)) {
			return
		}
		if !running {
			d.cliJSON("graph", "click-menu-button", "--name", node, "--block", block, "--button", button)
			attempted = true
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s menu result containing %q; last menu: %s\ndaemon log:\n%s", node, marker, output, d.readLog())
}

func restartAndAssert(t *testing.T, d *windowsDaemon, names ...string) {
	t.Helper()
	d.stop()
	d.start()
	assertGraphNodes(t, d, names...)
}

func cycleLink(t *testing.T, d *windowsDaemon, nodeA, portA, nodeB, portB string) {
	t.Helper()
	snapshot := decodeCLI[api.WorkspaceSnapshotResponse](t, d.cliJSON("engine", "workspace-snapshot")).Snapshot
	var idA, idB uint64
	for id, node := range snapshot.Nodes {
		switch node.Name {
		case nodeA:
			idA = id
		case nodeB:
			idB = id
		}
	}
	var linkID uint64
	var directionA, directionB string
	for id, link := range snapshot.Links {
		left := snapshot.Ports[link.LeftPort]
		right := snapshot.Ports[link.RightPort]
		if link.LeftPortNode == idA && left.Name == portA && link.RightPortNode == idB && right.Name == portB {
			linkID, directionA, directionB = id, left.Direction, right.Direction
			break
		}
		if link.LeftPortNode == idB && left.Name == portB && link.RightPortNode == idA && right.Name == portA {
			linkID = id
			directionA, directionB = right.Direction, left.Direction
			break
		}
	}
	if idA == 0 || idB == 0 || linkID == 0 {
		t.Fatalf("cannot find link %s.%s <-> %s.%s", nodeA, portA, nodeB, portB)
	}
	d.cliJSON("graph", "delete-link", "--id", strconv.FormatUint(linkID, 10))
	snapshot = decodeCLI[api.WorkspaceSnapshotResponse](t, d.cliJSON("engine", "workspace-snapshot")).Snapshot
	portIDA := namedPortID(snapshot, idA, directionA, portA)
	portIDB := namedPortID(snapshot, idB, directionB, portB)
	if portIDA == 0 || portIDB == 0 {
		t.Fatalf("cannot restore link %s.%s <-> %s.%s", nodeA, portA, nodeB, portB)
	}
	d.cliJSON("graph", "add-link", "--port-a", strconv.FormatUint(portIDA, 10), "--port-b", strconv.FormatUint(portIDB, 10))
}

func namedPortID(snapshot api.WorkspaceSnapshot, nodeID uint64, direction, name string) uint64 {
	for id, port := range snapshot.Ports {
		if port.Node == nodeID && port.Direction == direction && port.Name == name {
			return id
		}
	}
	return 0
}

func wantSOCKSHTTPHost(t *testing.T, socksPort int, host string, port int, body string) {
	t.Helper()
	deadline := time.Now().Add(integrationTimeout)
	var lastErr error
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp4", fmt.Sprintf("127.0.0.1:%d", socksPort), time.Second)
		if err != nil {
			lastErr = fmt.Errorf("connect to SOCKS listener: %w", err)
			time.Sleep(50 * time.Millisecond)
			continue
		}
		_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
		if _, err = conn.Write([]byte{5, 1, 0}); err != nil {
			lastErr = fmt.Errorf("write SOCKS greeting: %w", err)
			_ = conn.Close()
			time.Sleep(50 * time.Millisecond)
			continue
		}
		hello := make([]byte, 2)
		if _, err = io.ReadFull(conn, hello); err != nil || !bytes.Equal(hello, []byte{5, 0}) {
			lastErr = fmt.Errorf("read SOCKS greeting %v: %w", hello, err)
			_ = conn.Close()
			time.Sleep(50 * time.Millisecond)
			continue
		}
		request := []byte{5, 1, 0, 3, byte(len(host))}
		request = append(request, host...)
		request = append(request, byte(port>>8), byte(port))
		if _, err = conn.Write(request); err != nil {
			lastErr = fmt.Errorf("write SOCKS request: %w", err)
			_ = conn.Close()
			time.Sleep(50 * time.Millisecond)
			continue
		}
		if err = readBatch1SOCKSReply(conn); err != nil {
			lastErr = fmt.Errorf("read SOCKS reply: %w", err)
			_ = conn.Close()
			time.Sleep(50 * time.Millisecond)
			continue
		}
		if _, err = fmt.Fprintf(conn, "GET / HTTP/1.0\r\nHost: %s\r\n\r\n", host); err != nil {
			lastErr = fmt.Errorf("write HTTP request: %w", err)
			_ = conn.Close()
			time.Sleep(50 * time.Millisecond)
			continue
		}
		data, err := io.ReadAll(conn)
		_ = conn.Close()
		if err == nil && bytes.Contains(data, []byte(body)) {
			return
		}
		lastErr = fmt.Errorf("read HTTP response: %w; response=%q", err, data)
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for SOCKS HTTP request to %s: %v", host, lastErr)
}

func readBatch1SOCKSReply(reader io.Reader) error {
	header := make([]byte, 4)
	if _, err := io.ReadFull(reader, header); err != nil {
		return err
	}
	if header[0] != 5 || header[1] != 0 {
		return fmt.Errorf("SOCKS reply version=%d status=%d", header[0], header[1])
	}
	addressLength := 0
	switch header[3] {
	case 1:
		addressLength = 4
	case 3:
		length := []byte{0}
		if _, err := io.ReadFull(reader, length); err != nil {
			return err
		}
		addressLength = int(length[0])
	case 4:
		addressLength = 16
	default:
		return fmt.Errorf("SOCKS reply address type=%d", header[3])
	}
	_, err := io.CopyN(io.Discard, reader, int64(addressLength+2))
	return err
}

type batch1AdminClient struct {
	t      *testing.T
	conn   net.Conn
	codec  *api.Codec
	nextID uint64
	events []api.Message
}

func openBatch1Admin(t *testing.T, d *windowsDaemon) *batch1AdminClient {
	t.Helper()
	var owner *windows.SID
	if isLocalSystem(t) {
		var err error
		owner, err = windows.CreateWellKnownSid(windows.WinLocalSystemSid)
		if err != nil {
			t.Fatal(err)
		}
	} else {
		user, err := windows.GetCurrentProcessToken().GetTokenUser()
		if err != nil {
			t.Fatal(err)
		}
		owner, err = user.User.Sid.Copy()
		if err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), integrationTimeout)
	defer cancel()
	conn, err := (&namedpipe.DialConfig{ExpectedOwner: owner}).DialContext(ctx, `\\.\pipe\`+d.pipe)
	if err != nil {
		t.Fatalf("connect persistent named-pipe admin client: %v", err)
	}
	return &batch1AdminClient{t: t, conn: conn, codec: api.NewReadWriteCodec(conn)}
}

func (c *batch1AdminClient) close() {
	c.t.Helper()
	if err := c.conn.Close(); err != nil {
		c.t.Errorf("close persistent admin connection: %v", err)
	}
}

func (c *batch1AdminClient) request(surface api.Surface, method string, payload any) json.RawMessage {
	c.t.Helper()
	c.nextID++
	id := strconv.FormatUint(c.nextID, 10)
	request, err := api.NewRequest(id, surface, method, payload)
	if err != nil {
		c.t.Fatal(err)
	}
	_ = c.conn.SetDeadline(time.Now().Add(integrationTimeout))
	if err = c.codec.Write(request); err != nil {
		c.t.Fatalf("write persistent admin request: %v", err)
	}
	for {
		message, readErr := c.codec.Read()
		if readErr != nil {
			c.t.Fatalf("read persistent admin response: %v", readErr)
		}
		if message.Kind == api.MessageEvent {
			c.events = append(c.events, message)
			continue
		}
		if message.ID != id {
			c.t.Fatalf("persistent admin response ID = %q, want %q", message.ID, id)
		}
		if message.Error != nil {
			c.t.Fatalf("persistent admin request %s.%s: %s: %s", surface, method, message.Error.Code, message.Error.Message)
		}
		_ = c.conn.SetDeadline(time.Time{})
		return message.Result
	}
}

func (c *batch1AdminClient) expectNoEvent() {
	c.t.Helper()
	for _, event := range c.events {
		if isBatch1StorageEvent(event) {
			c.t.Fatalf("received event after subscription tree removal: %#v", event)
		}
	}
	deadline := time.Now().Add(250 * time.Millisecond)
	for {
		_ = c.conn.SetReadDeadline(deadline)
		message, err := c.codec.Read()
		if err != nil {
			_ = c.conn.SetReadDeadline(time.Time{})
			return
		}
		if isBatch1StorageEvent(message) {
			c.t.Fatalf("received event after subscription tree removal: %#v", message)
		}
	}
}

func isBatch1StorageEvent(message api.Message) bool {
	if message.Kind != api.MessageEvent {
		return false
	}
	switch message.Surface {
	case api.SurfaceKV:
		return message.Method == string(api.EventKVStoresUpdate) ||
			message.Method == string(api.EventKVEntriesUpdate) ||
			message.Method == string(api.EventKVValueUpdate)
	case api.SurfaceDNS:
		return message.Method == string(api.EventDNSCachesUpdate) ||
			message.Method == string(api.EventDNSKeysUpdate) ||
			message.Method == string(api.EventDNSValueUpdate)
	case api.SurfaceSecrets:
		return message.Method == string(api.EventSecretsUpdate)
	case api.SurfaceTLS:
		return message.Method == string(api.EventTLSUpdate)
	default:
		return false
	}
}

func decodeResult[T any](t *testing.T, raw json.RawMessage) T {
	t.Helper()
	var value T
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatalf("decode admin result %q: %v", raw, err)
	}
	return value
}

func startCLIWatch(t *testing.T, d *windowsDaemon, arguments ...string) string {
	t.Helper()
	path := filepath.Join(d.root, fmt.Sprintf("watch-%d.jsonl", time.Now().UnixNano()))
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	command := d.cliCommand(arguments...)
	command.Stdout, command.Stderr = file, file
	if err = command.Start(); err != nil {
		_ = file.Close()
		t.Fatalf("start persistent CLI watch: %v", err)
	}
	t.Cleanup(func() {
		_ = command.Process.Kill()
		_ = command.Wait()
		_ = file.Close()
	})
	return path
}
