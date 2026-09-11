package tunnodes_test

import (
	"context"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	almagest "github.com/asciimoth/almagest/lib"
	dnsnodes "github.com/asciimoth/almagest/lib/dns"
	httpnodes "github.com/asciimoth/almagest/lib/http"
	"github.com/asciimoth/almagest/lib/links"
	"github.com/asciimoth/almagest/lib/logging"
	networknodes "github.com/asciimoth/almagest/lib/network"
	"github.com/asciimoth/almagest/lib/nodekit"
	socksnodes "github.com/asciimoth/almagest/lib/socks"
	"github.com/asciimoth/almagest/lib/storage"
	"github.com/asciimoth/almagest/lib/testkit"
	"github.com/asciimoth/almagest/lib/testkit/dnse2etest"
	"github.com/asciimoth/almagest/lib/testkit/e2etest"
	"github.com/asciimoth/almagest/lib/testkit/httpe2etest"
	tunnodes "github.com/asciimoth/almagest/lib/tun"
	"github.com/asciimoth/bufpool"
	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/formular"
	"github.com/asciimoth/gonnect/dns"
	"github.com/asciimoth/gonnect/sysnet"
	sysnetdebug "github.com/asciimoth/gonnect/sysnet/debug"
	"github.com/asciimoth/gonnect/tun"
	"github.com/asciimoth/pasta/pasta"
	"github.com/asciimoth/pasta/pasta/std"
)

const (
	E2EWorkspace = e2etest.E2EWorkspace

	TypeNetwork = links.TypeNetwork
	TypeTun     = links.TypeTun

	NodeTypeHTTPClient = httpnodes.NodeTypeHTTPClient
	NodeTypeHTTPServer = httpnodes.NodeTypeHTTPServer

	NodeTypeMockNetwork = networknodes.NodeTypeMockNetwork
	NodeTypeOutNetwork  = networknodes.NodeTypeOutNetwork
	NodeTypeSpoofer     = networknodes.NodeTypeSpoofer

	NodeTypeSocksClient = socksnodes.NodeTypeSocksClient
	NodeTypeSocksServer = socksnodes.NodeTypeSocksServer

	NodeTypeDefaultTun        = tunnodes.NodeTypeDefaultTun
	NodeTypeTun               = tunnodes.NodeTypeTun
	NodeTypeTunConfigStripper = tunnodes.NodeTypeTunConfigStripper
	NodeTypeTunCopy           = tunnodes.NodeTypeTunCopy
	NodeTypeTunJoiner         = tunnodes.NodeTypeTunJoiner
	NodeTypeTunSplitter       = tunnodes.NodeTypeTunSplitter
	NodeTypeVTun              = tunnodes.NodeTypeVTun

	tunSplitterConfigBlockID            = tunnodes.TunSplitterConfigBlockID
	tunSplitterRulesField               = tunnodes.TunSplitterRulesField
	tunSplitterDNSCacheStorageNameField = tunnodes.TunSplitterDNSCacheStorageNameField

	DebugLogLevelError = logging.DebugLogLevelError
)

type (
	Engine = almagest.Engine

	NetworkCloser     = links.NetworkCloser
	NetworkPayload    = links.NetworkPayload
	TunPayload        = links.TunPayload
	TunConfig         = links.TunConfig
	TunSourceRoute    = links.TunSourceRoute
	TunConfigByLink   = links.TunConfigByLink
	TunOutputCache    = links.TunOutputCache
	e2eSecretsStorage = e2etest.SecretsStorage
)

var ErrTunPortBusy = links.ErrTunPortBusy

func BuildEngine(ctx context.Context, cfg configer.Config, logf logging.LogFactory, sys sysnet.System, kv storage.KVManager, dns storage.DnsCacheManager, secrets storage.SecretsStorage, tlsConfigs storage.TlsConfigStorage, extra []pasta.NodeClass, pool bufpool.Pool, debug bool) (almagest.Engine, error) {
	return almagest.BuildEngine(ctx, cfg, logf, sys, kv, dns, secrets, tlsConfigs, extra, pool, debug)
}

func NewDebugLogFactory(buffer int) *logging.DebugLogFactory {
	return logging.NewDebugLogFactory(buffer)
}

func NewNodeE2E(t *testing.T) *NodeE2E {
	return e2etest.NewNodeE2E(t)
}

func newE2EDebugSystem() *sysnetdebug.System {
	return e2etest.NewDebugSystem()
}

func newE2ETestDebugPool(t *testing.T) *e2etest.DebugPool {
	return e2etest.NewTestDebugPool(t)
}

func newE2ESecretsStorage() *e2eSecretsStorage {
	return e2etest.NewSecretsStorage()
}

func NewNodeE2EFromEngine(t *testing.T, engine almagest.Engine, logf *logging.DebugLogFactory, sys *sysnetdebug.System, secrets *e2eSecretsStorage) *NodeE2E {
	return e2etest.NewNodeE2EFromEngine(t, engine, logf, sys, secrets)
}

func httpTestPort() int {
	return e2etest.TestPort()
}

func itoa(i int) string {
	return strconv.Itoa(i)
}

func FormMessageBase(messageType string, node uint64) formular.MessageBase {
	return testkit.FormMessageBase(messageType, node)
}

func Eventually(t *testing.T, condition func() bool, format string, args ...any) {
	testkit.Eventually(t, condition, format, args...)
}

func NetworkPort(direction, name string) pasta.Port {
	return links.NetworkPort(direction, name)
}

func TunPort(direction, name string) pasta.Port {
	return links.TunPort(direction, name)
}

func TunPreLinkAdd(w *pasta.Workspace, port uint64, linkType string) error {
	return links.TunPreLinkAdd(w, port, linkType)
}

func BindTunResource(w *pasta.Workspace, node, link uint64, t tun.Tun) {
	links.BindTunResource(w, node, link, t)
}

func TunIsReady(t tun.Tun) bool {
	return links.TunIsReady(t)
}

func SameTunInstance(a, b tun.Tun) bool {
	return links.SameTunInstance(a, b)
}

func SameTunSource(a, b tun.Tun) bool {
	return links.SameTunSource(a, b)
}

func FindItem(block formular.Block, id string) (formular.Item, bool) {
	return testkit.FindItem(block, id)
}

func newOutDNSTestProvider(t *testing.T, recordsText string) dns.Interface {
	t.Helper()
	provider, err := dnsnodes.NewStaticProvider(recordsText, dnsnodes.DefaultStaticDNSTTL)
	if err != nil {
		t.Fatalf("NewStaticProvider(%q): %v", recordsText, err)
	}
	t.Cleanup(func() {
		if err := provider.Close(); err != nil {
			t.Errorf("OutDNS provider Close error = %v, want nil", err)
		}
	})
	return provider
}

func newOutDNSE2E(t *testing.T, cfg configer.Config, recordsText string, manager storage.DnsCacheManager) *dnsE2E {
	t.Helper()
	source := newOutDNSTestProvider(t, recordsText)
	if cfg == nil {
		cfg = configer.NewMemory(nil)
	}
	pool := newE2ETestDebugPool(t)
	logf := NewDebugLogFactory(16384)
	sys := newE2EDebugSystem()
	sys.OutDNSProvider = source
	engine, err := BuildEngine(context.Background(), cfg, logf, sys, nil, manager, nil, nil, nil, pool, true)
	if err != nil {
		t.Fatalf("BuildEngine: %v", err)
	}
	t.Cleanup(func() {
		if err := engine.Close(); err != nil {
			t.Errorf("engine.Close error = %v, want nil", err)
		}
	})
	return newDNSE2EFromNodeE2E(NewNodeE2EFromEngine(t, engine, logf, sys, nil))
}

func dnsTestQuery(name string, typ uint16) *dns.Message {
	return dnse2etest.TestQuery(name, typ)
}

func assertDNSAnswer(t *testing.T, d dns.Interface, name string, qtype uint16, want string) {
	t.Helper()
	if !dnsnodes.DNSIsReady(d) {
		t.Fatal("DNS wrapper is not ready before query")
	}
	resp, err := dns.Query(context.Background(), d, dnse2etest.TestQuery(name, qtype))
	if err != nil {
		t.Fatalf("DNS query failed: %v", err)
	}
	if resp == nil || resp.RCode != dns.RCodeSuccess {
		t.Fatalf("DNS response = %#v, want success", resp)
	}
	if got := dnsnodes.FormatMessage(resp); !strings.Contains(got, want) {
		t.Fatalf("DNS response = %q, want to contain %q", got, want)
	}
}

func restoredRight(restored *pasta.NodeInitData, index int) uint64 {
	if restored == nil || index < 0 || index >= len(restored.RightPorts) {
		return 0
	}
	return restored.RightPorts[index]
}

type NodeE2E = e2etest.NodeE2E

type httpE2E struct {
	*NodeE2E
	*httpe2etest.HTTPOps
}

func newHTTPE2E(t *testing.T) *httpE2E {
	t.Helper()
	return newHTTPE2EFromNodeE2E(NewNodeE2E(t))
}

func newHTTPE2EFromNodeE2E(e *NodeE2E) *httpE2E {
	return &httpE2E{NodeE2E: e, HTTPOps: httpe2etest.NewHTTPOps(e)}
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

func (e *httpE2E) assertClientRequestWorks(client uint64) {
	e.AssertClientRequestWorks(client)
}

func (e *httpE2E) assertClientRequestReturns(client uint64, response string, timeout time.Duration) {
	e.AssertClientRequestReturns(client, response, timeout)
}

func (e *httpE2E) clientLabel(client uint64) string {
	return e.ClientLabel(client)
}

func (e *httpE2E) waitClientFailure(client uint64, wantErrPart string) {
	e.WaitClientFailure(client, wantErrPart)
}

func (e *httpE2E) waitClientRequestField(client uint64, field, want string) {
	e.WaitClientRequestField(client, field, want)
}

func (e *httpE2E) waitServerLog(server uint64, wantPart string) {
	e.WaitServerLog(server, wantPart)
}

func (e *httpE2E) assertServerLog(server uint64, wantPart string) {
	e.AssertServerLog(server, wantPart)
}

func (e *httpE2E) clientStatus(client uint64) string {
	return e.ClientStatus(client)
}

func (e *httpE2E) clientResultField(client uint64, field string) string {
	return e.ClientResultField(client, field)
}

func (e *httpE2E) mustNetworkLink(leftNode uint64, leftDir, leftName string, rightNode uint64, rightDir, rightName string) uint64 {
	e.T.Helper()
	left := e.NodePort(leftNode, leftDir, leftName)
	right := e.NodePort(rightNode, rightDir, rightName)
	link, snapshot, ok := e.Engine.LinkByPorts(E2EWorkspace, left, right)
	if !ok {
		e.T.Fatalf("missing link between %d/%s/%s and %d/%s/%s", leftNode, leftDir, leftName, rightNode, rightDir, rightName)
	}
	if snapshot.Type != TypeNetwork {
		e.T.Fatalf("link %d type = %q, want %q", link, snapshot.Type, TypeNetwork)
	}
	return link
}

func (e *httpE2E) mustNodeByName(name string) uint64 {
	e.T.Helper()
	id, ok := e.Engine.NodeByName(E2EWorkspace, name)
	if !ok {
		e.T.Fatalf("node %q missing", name)
	}
	return id
}

func (e *httpE2E) nodeName(node uint64) string {
	e.T.Helper()
	snapshot, ok := e.Engine.NodeSnapshot(E2EWorkspace, node)
	if !ok {
		e.T.Fatalf("NodeSnapshot(%d) missing", node)
	}
	return snapshot.Name
}

func (e *httpE2E) assertNodesLinked(a, b uint64) {
	e.T.Helper()
	if !e.Engine.NodesConnected(E2EWorkspace, a, b) {
		e.T.Fatalf("nodes %d and %d are not linked", a, b)
	}
}

func newNetworkRouterConfigE2E(t *testing.T, cfg configer.Config) *httpE2E {
	t.Helper()
	return newHTTPE2EFromNodeE2E(e2etest.NewNodeE2EWithConfig(t, cfg))
}

type dnsE2E struct {
	*NodeE2E
	*dnse2etest.DNSOps
}

func newDNSE2EWithDNSCacheManager(t *testing.T, cfg configer.Config, manager storage.DnsCacheManager) *dnsE2E {
	t.Helper()
	pool := newE2ETestDebugPool(t)
	logf := NewDebugLogFactory(16384)
	sys := newE2EDebugSystem()
	engine, err := BuildEngine(context.Background(), cfg, logf, sys, nil, manager, nil, nil, nil, pool, true)
	if err != nil {
		t.Fatalf("BuildEngine restore: %v", err)
	}
	t.Cleanup(func() {
		if err := engine.Close(); err != nil {
			t.Errorf("engine.Close error = %v, want nil", err)
		}
	})
	return newDNSE2EFromNodeE2E(NewNodeE2EFromEngine(t, engine, logf, sys, nil))
}

func newDNSE2EFromNodeE2E(e *NodeE2E) *dnsE2E {
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
	e.Engine.SendNodeFormularMsg(E2EWorkspace, node, formular.FormApplyMessage{
		MessageBase: FormMessageBase(formular.MessageFormApply, node),
		BlockID:     dnsnodes.DNSCacheConfigBlockID,
		Values: map[string]any{
			dnsnodes.DNSCacheStorageNameField:      storageName,
			dnsnodes.DNSCacheReverseLookupsField:   reverseLookups,
			dnsnodes.DNSCacheStorageWriteOnlyField: writeOnly,
		},
	})
	e.waitDNSCacheConfig(node, storageName, reverseLookups, writeOnly)
}

func (e *dnsE2E) waitDNSCacheConfig(node uint64, storageName string, reverseLookups, writeOnly bool) {
	e.T.Helper()
	Eventually(e.T, func() bool {
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

func warmDNSCacheReverseLookupsForRouting(t *testing.T, e *NodeE2E, prefix string, storageName string, records string, queries ...dnsCacheWarmQuery) {
	t.Helper()
	dnsE := newDNSE2EFromNodeE2E(e)
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

type NodeBase struct {
	pasta.BasicNode
	w  *pasta.Workspace
	id uint64
}

func (b *NodeBase) InitBaseNode(w *pasta.Workspace, _ pasta.Logger, id uint64) {
	b.w = w
	b.id = id
}

func (b *NodeBase) SetLabel(label string) error {
	if b == nil || b.w == nil || b.id == 0 {
		return nil
	}
	return b.w.SetNodeLabelLocked(b.id, label)
}

func (b *NodeBase) SetPrimary(primaryType string) error {
	if b == nil || b.w == nil || b.id == 0 {
		return nil
	}
	return b.w.SetNodePrimaryLocked(b.id, primaryType)
}

func (b *NodeBase) ForEachPortLink(port uint64, fn func(link uint64)) {
	if b == nil || b.w == nil || port == 0 || fn == nil {
		return
	}
	snapshot, ok := b.w.PortSnapshotLocked(port)
	if !ok {
		return
	}
	for _, link := range snapshot.Links {
		fn(link)
	}
}

func (b *NodeBase) EmitIfLinked(link uint64, payload any) bool {
	if b == nil || b.w == nil || link == 0 {
		return false
	}
	if _, ok := b.w.LinkSnapshotLocked(link); !ok {
		return false
	}
	b.w.EmitEventLocked(b.id, link, payload)
	return true
}

type engine struct {
	ctx  context.Context
	pool bufpool.Pool
	sys  sysnet.System
}

func (e *engine) Context() context.Context {
	if e != nil && e.ctx != nil {
		return e.ctx
	}
	return context.Background()
}

func (e *engine) Pool() bufpool.Pool {
	if e == nil {
		return nil
	}
	return e.pool
}

func (e *engine) System() sysnet.System {
	if e == nil {
		return nil
	}
	return e.sys
}

func (*engine) KVManager() storage.KVManager                   { return nil }
func (*engine) DnsCacheManager() storage.DnsCacheManager       { return nil }
func (*engine) SecretsStorage() storage.SecretsStorage         { return nil }
func (*engine) TlsConfigStorage() storage.TlsConfigStorage     { return nil }
func (*engine) WorkerSpawner(uint64) nodekit.WorkerSpawner     { return nil }
func (*engine) ObserveNodeSnapshot(uint64, pasta.NodeSnapshot) {}
