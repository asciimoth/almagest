package networknodes_test

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"slices"
	"sync"
	"testing"
	"time"

	almagest "github.com/asciimoth/almagest/lib"
	dnsnodes "github.com/asciimoth/almagest/lib/dns"
	"github.com/asciimoth/almagest/lib/logging"
	socksnodes "github.com/asciimoth/almagest/lib/socks"
	"github.com/asciimoth/almagest/lib/storage"
	"github.com/asciimoth/almagest/lib/testkit"
	"github.com/asciimoth/almagest/lib/testkit/dnse2etest"
	"github.com/asciimoth/almagest/lib/testkit/e2etest"
	"github.com/asciimoth/almagest/lib/testkit/httpe2etest"
	"github.com/asciimoth/almagest/lib/testkit/sockstest"
	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/formular"
	"github.com/asciimoth/gonnect/dns"
	sysnetdebug "github.com/asciimoth/gonnect/sysnet/debug"
	"github.com/asciimoth/pasta/pasta/std"
	"github.com/asciimoth/socksgo"
	"github.com/asciimoth/socksgo/protocol"
)

type httpE2E struct {
	*e2etest.NodeE2E
	*httpe2etest.HTTPOps
}

func newHTTPE2E(t *testing.T) *httpE2E {
	t.Helper()
	return newHTTPE2EFromNodeE2E(e2etest.NewNodeE2E(t))
}

func newHTTPE2EFromNodeE2E(e *e2etest.NodeE2E) *httpE2E {
	return &httpE2E{NodeE2E: e, HTTPOps: httpe2etest.NewHTTPOps(e)}
}

func httpTestPort() int {
	return e2etest.TestPort()
}

func Eventually(t *testing.T, condition func() bool, format string, args ...any) {
	testkit.Eventually(t, condition, format, args...)
}

func (e *httpE2E) addHTTPServer(name string, port int) uint64 {
	return e.AddHTTPServer(name, port)
}

func (e *httpE2E) addHTTPClient(name string, port int) uint64 {
	return e.AddHTTPClient(name, port)
}

func (e *httpE2E) addStringConstant(name, value string) uint64 {
	return e.AddStringConstant(name, value)
}

func (e *httpE2E) connectByPortName(leftNode uint64, leftDir, leftName string, rightNode uint64, rightDir, rightName string) uint64 {
	return e.ConnectByPortName(leftNode, leftDir, leftName, rightNode, rightDir, rightName)
}

func (e *httpE2E) sendClientRequest(client uint64) {
	e.SendClientRequest(client)
}

func (e *httpE2E) waitServerListening(server uint64, address string) {
	e.WaitServerListening(server, address)
}

func (e *httpE2E) waitClientResponse(client uint64, status, response string) {
	e.WaitClientResponse(client, status, response)
}

func (e *httpE2E) assertClientRequestReturns(client uint64, response string, timeout time.Duration) {
	e.AssertClientRequestReturns(client, response, timeout)
}

func (e *httpE2E) waitClientFailure(client uint64, wantErrPart string) {
	e.WaitClientFailure(client, wantErrPart)
}

func (e *httpE2E) waitClientAnyFailure(client uint64) {
	e.WaitClientAnyFailure(client)
}

func (e *httpE2E) waitClientReady(client uint64) {
	e.WaitClientReady(client)
}

func (e *httpE2E) waitClientRequestField(client uint64, field, want string) {
	e.WaitClientRequestField(client, field, want)
}

func (e *httpE2E) waitSocksLog(socks uint64, wantPart string) {
	e.WaitSocksLog(socks, wantPart)
}

type dnsE2E struct {
	*e2etest.NodeE2E
	*dnse2etest.DNSOps
}

func newDNSE2EWithDNSCacheManager(t *testing.T, cfg configer.Config, manager storage.DnsCacheManager) *dnsE2E {
	t.Helper()
	if cfg == nil {
		cfg = configer.NewMemory(nil)
	}
	pool := e2etest.NewTestDebugPool(t)
	logf := logging.NewDebugLogFactory(16384)
	sys := e2etest.NewDebugSystem()
	engine, err := almagest.BuildEngine(context.Background(), cfg, logf, sys, nil, manager, nil, nil, nil, pool, true)
	if err != nil {
		t.Fatalf("BuildEngine: %v", err)
	}
	t.Cleanup(func() {
		if err := engine.Close(); err != nil {
			t.Errorf("engine.Close error = %v, want nil", err)
		}
	})
	e := e2etest.NewNodeE2EFromEngine(t, engine, logf, sys, nil)
	return &dnsE2E{NodeE2E: e, DNSOps: dnse2etest.NewDNSOps(e)}
}

func (e *dnsE2E) applyStaticDNSRecords(node uint64, records string) {
	e.ApplyStaticRecords(node, records)
}

func (e *dnsE2E) setDNSQueryRequest(client uint64, name, qtype string) {
	e.SetQueryRequest(client, name, qtype)
}

func (e *dnsE2E) sendDNSRequest(client uint64) {
	e.SendRequest(client)
}

func (e *dnsE2E) waitDNSResponse(client uint64, status string, wantParts ...string) {
	e.WaitResponse(client, status, wantParts...)
}

func (e *dnsE2E) applyDNSCacheConfig(node uint64, storageName string, reverseLookups, writeOnly bool) {
	e.T.Helper()
	e.Engine.SendNodeFormularMsg(e2etest.E2EWorkspace, node, formular.FormApplyMessage{
		MessageBase: testkit.FormMessageBase(formular.MessageFormApply, node),
		BlockID:     dnsnodes.DNSCacheConfigBlockID,
		Values: map[string]any{
			dnsnodes.DNSCacheStorageNameField:      storageName,
			dnsnodes.DNSCacheReverseLookupsField:   reverseLookups,
			dnsnodes.DNSCacheStorageWriteOnlyField: writeOnly,
		},
	})
	testkit.Eventually(e.T, func() bool {
		return e.dnsCacheFieldString(node, dnsnodes.DNSCacheStorageNameField) == storageName &&
			e.dnsCacheFieldBool(node, dnsnodes.DNSCacheReverseLookupsField) == reverseLookups &&
			e.dnsCacheFieldBool(node, dnsnodes.DNSCacheStorageWriteOnlyField) == writeOnly
	}, "DnsCache config %q reverse=%v writeOnly=%v", storageName, reverseLookups, writeOnly)
}

func (e *dnsE2E) dnsCacheFieldString(node uint64, field string) string {
	item, ok := e.MenuItem(node, dnsnodes.DNSCacheConfigBlockID, field)
	if !ok || item.Field == nil {
		return ""
	}
	value, _ := std.StringFromPayload(item.Value)
	return value
}

func (e *dnsE2E) dnsCacheFieldBool(node uint64, field string) bool {
	item, ok := e.MenuItem(node, dnsnodes.DNSCacheConfigBlockID, field)
	if !ok || item.Field == nil {
		return false
	}
	value, _ := formular.BoolValue(item.Value)
	return value
}

type dnsCacheWarmQuery struct {
	name  string
	wantA string
}

func warmDNSCacheReverseLookupsForRouting(
	t *testing.T,
	e *e2etest.NodeE2E,
	prefix string,
	storageName string,
	records string,
	queries ...dnsCacheWarmQuery,
) {
	t.Helper()
	dnsE := &dnsE2E{NodeE2E: e, DNSOps: dnse2etest.NewDNSOps(e)}
	static := e.AddNode(dnsnodes.NodeTypeStaticDNS, prefix+"-static-dns")
	cache := e.AddNode(dnsnodes.NodeTypeDnsCache, prefix+"-dns-cache")
	query := e.AddNode(dnsnodes.NodeTypeDNSQuery, prefix+"-dns-query")
	e.SubscribeMenus(static, cache, query)

	dnsE.applyStaticDNSRecords(static, records)
	dnsE.applyDNSCacheConfig(cache, storageName, true, false)
	e.MustAddTypedLinkByPortName(dnsnodes.TypeDNS, static, "left", "DNS", cache, "right", "DNS")
	e.MustAddTypedLinkByPortName(dnsnodes.TypeDNS, cache, "left", "DNS", query, "right", "DNS")
	e.WaitNodeLabel(cache, "caching DNS")

	for _, q := range queries {
		dnsE.setDNSQueryRequest(query, q.name, "A")
		dnsE.sendDNSRequest(query)
		dnsE.waitDNSResponse(query, "NOERROR", q.wantA)
	}
}

type memoryDNSCacheManager struct {
	mu       sync.Mutex
	storages map[string]dns.CacheStorage
}

func newMemoryDNSCacheManager() *memoryDNSCacheManager {
	return &memoryDNSCacheManager{storages: map[string]dns.CacheStorage{}}
}

func (m *memoryDNSCacheManager) Storage(name string) dns.CacheStorage {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.storages == nil {
		m.storages = map[string]dns.CacheStorage{}
	}
	storage := m.storages[name]
	if storage == nil {
		storage = dns.NewMemoryStorage()
		m.storages[name] = storage
	}
	return storage
}

func (m *memoryDNSCacheManager) StorageNames() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	names := make([]string, 0, len(m.storages))
	for name := range m.storages {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

type localProxyE2E struct {
	*httpE2E
	sys *sysnetdebug.System
}

func (e *localProxyE2E) addLocalProxy(name string, port int, users []sockstest.User) uint64 {
	e.T.Helper()
	id := e.AddNode(socksnodes.NodeTypeLocalProxy, name)
	values := map[string]any{
		"socksHost": "127.0.0.1",
		"socksPort": port,
		"wsHost":    "",
		"wsPort":    0,
		"wsPath":    "/socks",
	}
	if users != nil {
		values["users"] = sockstest.ServerUserValues(users)
	}
	e.Engine.SendNodeFormularMsg(e2etest.E2EWorkspace, id, formular.FormApplyMessage{
		MessageBase: testkit.FormMessageBase(formular.MessageFormApply, id),
		BlockID:     "config",
		Values:      values,
	})
	return id
}

func (e *localProxyE2E) waitLocalProxyHTTPGet(socksPort, httpPort int, auth *protocol.AuthMethods) string {
	e.T.Helper()
	var body string
	var err error
	testkit.Eventually(e.T, func() bool {
		body, err = localProxyHTTPGet(e.sys, socksPort, httpPort, auth)
		return err == nil
	}, "local proxy HTTP GET via %d to %d, last error: %v", socksPort, httpPort, err)
	return body
}

func localProxyHTTPGet(sys *sysnetdebug.System, socksPort, httpPort int, auth *protocol.AuthMethods) (string, error) {
	client := &socksgo.Client{
		SocksVersion: "5",
		ProxyAddr:    fmt.Sprintf("127.0.0.1:%d", socksPort),
		Auth:         auth,
		Filter:       func(_, _ string) bool { return false },
		Dialer:       sys.LocalNet().Dial,
	}
	conn, err := client.Dial(context.Background(), "tcp", fmt.Sprintf("127.0.0.1:%d", httpPort))
	if err != nil {
		return "", err
	}
	defer conn.Close() //nolint:errcheck
	if _, err := fmt.Fprintf(conn, "GET / HTTP/1.1\r\nHost: 127.0.0.1:%d\r\nConnection: close\r\n\r\n", httpPort); err != nil {
		return "", err
	}
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close() //nolint:errcheck
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	return string(data), nil
}
