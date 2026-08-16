package dnsnodes_test

import (
	"context"
	"slices"
	"sync"
	"testing"

	dnsnodes "github.com/asciimoth/almagest/lib/dns"
	"github.com/asciimoth/almagest/lib/storage"
	"github.com/asciimoth/almagest/lib/testkit"

	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/formular"
	"github.com/asciimoth/gonnect/dns"
	"github.com/asciimoth/pasta/pasta/std"
)

func TestDnsCachePortsMenuAndSaveConfigE2E(t *testing.T) {
	e := newDNSE2EWithDNSCacheManager(t, configer.NewMemory(nil), newMemoryDNSCacheManager())
	cache := e.AddNode(dnsnodes.NodeTypeDnsCache, "cache")
	e.SubscribeMenus(cache)

	snapshot, ok := e.Engine.NodeSnapshot(E2EWorkspace, cache)
	if !ok {
		t.Fatalf("NodeSnapshot(%d) missing", cache)
	}
	if len(snapshot.LeftPorts) != 1 || len(snapshot.RightPorts) != 1 {
		t.Fatalf("DnsCache ports = left %v right %v, want one of each", snapshot.LeftPorts, snapshot.RightPorts)
	}
	assertDNSCachePort(t, e.Engine, snapshot.LeftPorts[0], "left")
	assertDNSCachePort(t, e.Engine, snapshot.RightPorts[0], "right")
	e.waitDNSCacheConfig(cache, dnsnodes.DefaultDNSCacheStorageName, false, false)
	e.WaitNodeLabel(cache, "cache-only DNS")

	e.applyDNSCacheConfig(cache, "shared", true, true)
	e.WaitNodeLabel(cache, "waiting for DNS")

	cfg := configer.NewMemory(nil)
	if err := e.Engine.SaveConfig(cfg); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	nodeCfg := cfg.View(configer.Path{"nodes", "cache"})
	if got := configer.String(nodeCfg, configer.Path{dnsnodes.DNSCacheStorageNameField}, ""); got != "shared" {
		t.Fatalf("saved storageName = %q, want shared", got)
	}
	if got := configer.Bool(nodeCfg, configer.Path{dnsnodes.DNSCacheReverseLookupsField}, false); !got {
		t.Fatal("saved reverseLookups = false, want true")
	}
	if got := configer.Bool(nodeCfg, configer.Path{dnsnodes.DNSCacheStorageWriteOnlyField}, false); !got {
		t.Fatal("saved writeOnly = false, want true")
	}
}

func TestDnsCacheServesCachedStaticDNSAfterInputDetachE2E(t *testing.T) {
	e := newDNSE2EWithDNSCacheManager(t, configer.NewMemory(nil), newMemoryDNSCacheManager())
	static := e.AddNode(dnsnodes.NodeTypeStaticDNS, "static")
	cache := e.AddNode(dnsnodes.NodeTypeDnsCache, "cache")
	client := e.AddNode(dnsnodes.NodeTypeDNSQuery, "client")
	e.SubscribeMenus(static, cache, client)
	e.applyStaticDNSRecords(static, "cached.test A 10.90.0.1")
	e.setDNSQueryRequest(client, "cached.test.", "A")

	inputLink := e.MustAddTypedLinkByPortName(dnsnodes.TypeDNS, static, "left", "DNS", cache, "right", "DNS")
	e.MustAddTypedLinkByPortName(dnsnodes.TypeDNS, cache, "left", "DNS", client, "right", "DNS")
	e.WaitNodeLabel(cache, "caching DNS")

	e.sendDNSRequest(client)
	e.waitDNSResponse(client, "NOERROR", "10.90.0.1")

	e.Engine.RemoveLink(E2EWorkspace, inputLink)
	e.WaitNodeLabel(cache, "cache-only DNS")
	e.sendDNSRequest(client)
	e.waitDNSResponse(client, "NOERROR", "10.90.0.1")

	e.setDNSQueryRequest(client, "missing-cache.test.", "A")
	e.sendDNSRequest(client)
	e.waitDNSFailure(client, "no upstream attached")
}

func TestDnsCacheWriteOnlyWarmsSharedStorageE2E(t *testing.T) {
	manager := newMemoryDNSCacheManager()
	e := newDNSE2EWithDNSCacheManager(t, configer.NewMemory(nil), manager)
	static := e.AddNode(dnsnodes.NodeTypeStaticDNS, "static")
	writer := e.AddNode(dnsnodes.NodeTypeDnsCache, "writer")
	reader := e.AddNode(dnsnodes.NodeTypeDnsCache, "reader")
	writerClient := e.AddNode(dnsnodes.NodeTypeDNSQuery, "writer-client")
	readerClient := e.AddNode(dnsnodes.NodeTypeDNSQuery, "reader-client")
	e.SubscribeMenus(static, writer, reader, writerClient, readerClient)

	e.applyStaticDNSRecords(static, "shared-cache.test A 10.91.0.1")
	e.applyDNSCacheConfig(writer, "shared-storage", false, true)
	e.applyDNSCacheConfig(reader, "shared-storage", false, false)
	e.setDNSQueryRequest(writerClient, "shared-cache.test.", "A")
	e.setDNSQueryRequest(readerClient, "shared-cache.test.", "A")

	inputLink := e.MustAddTypedLinkByPortName(dnsnodes.TypeDNS, static, "left", "DNS", writer, "right", "DNS")
	e.MustAddTypedLinkByPortName(dnsnodes.TypeDNS, writer, "left", "DNS", writerClient, "right", "DNS")
	e.MustAddTypedLinkByPortName(dnsnodes.TypeDNS, reader, "left", "DNS", readerClient, "right", "DNS")
	e.WaitNodeLabel(writer, "warming DNS cache")
	e.WaitNodeLabel(reader, "cache-only DNS")

	e.sendDNSRequest(writerClient)
	e.waitDNSResponse(writerClient, "NOERROR", "10.91.0.1")

	e.Engine.RemoveLink(E2EWorkspace, inputLink)
	e.WaitNodeLabel(writer, "waiting for DNS")
	e.sendDNSRequest(writerClient)
	e.waitDNSFailure(writerClient, "no upstream attached")

	e.sendDNSRequest(readerClient)
	e.waitDNSResponse(readerClient, "NOERROR", "10.91.0.1")
}

func TestDnsCacheReverseLookupToggleE2E(t *testing.T) {
	e := newDNSE2EWithDNSCacheManager(t, configer.NewMemory(nil), newMemoryDNSCacheManager())
	static := e.AddNode(dnsnodes.NodeTypeStaticDNS, "static")
	cache := e.AddNode(dnsnodes.NodeTypeDnsCache, "cache")
	client := e.AddNode(dnsnodes.NodeTypeDNSQuery, "client")
	e.SubscribeMenus(static, cache, client)

	e.applyStaticDNSRecords(static, "reverse-cache.test A 10.92.0.7")
	e.applyDNSCacheConfig(cache, "reverse-storage", true, false)
	e.setDNSQueryRequest(client, "reverse-cache.test.", "A")
	inputLink := e.MustAddTypedLinkByPortName(dnsnodes.TypeDNS, static, "left", "DNS", cache, "right", "DNS")
	e.MustAddTypedLinkByPortName(dnsnodes.TypeDNS, cache, "left", "DNS", client, "right", "DNS")

	e.sendDNSRequest(client)
	e.waitDNSResponse(client, "NOERROR", "10.92.0.7")

	e.Engine.RemoveLink(E2EWorkspace, inputLink)
	e.WaitNodeLabel(cache, "cache-only DNS")
	e.setDNSQueryRequest(client, "7.0.92.10.in-addr.arpa.", "PTR")
	e.sendDNSRequest(client)
	e.waitDNSResponse(client, "NOERROR", "reverse-cache.test.")
}

func TestDnsCacheSaveRestorePreservesConfigAndLinksE2E(t *testing.T) {
	e := newDNSE2EWithDNSCacheManager(t, configer.NewMemory(nil), newMemoryDNSCacheManager())
	static := e.AddNode(dnsnodes.NodeTypeStaticDNS, "static")
	cache := e.AddNode(dnsnodes.NodeTypeDnsCache, "cache")
	client := e.AddNode(dnsnodes.NodeTypeDNSQuery, "client")
	e.SubscribeMenus(static, cache, client)

	e.applyStaticDNSRecords(static, "restore-cache.test A 10.93.0.1")
	e.applyDNSCacheConfig(cache, "restore-storage", true, false)
	e.setDNSQueryRequest(client, "restore-cache.test.", "A")
	e.MustAddTypedLinkByPortName(dnsnodes.TypeDNS, static, "left", "DNS", cache, "right", "DNS")
	e.MustAddTypedLinkByPortName(dnsnodes.TypeDNS, cache, "left", "DNS", client, "right", "DNS")
	e.sendDNSRequest(client)
	e.waitDNSResponse(client, "NOERROR", "10.93.0.1")

	cfg := configer.NewMemory(nil)
	if err := e.Engine.SaveConfig(cfg); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}

	restored := newDNSE2EWithDNSCacheManager(t, cfg, newMemoryDNSCacheManager())
	restoredCache := testkit.NodeIDByName(t, restored.Engine, E2EWorkspace, "cache")
	restoredClient := testkit.NodeIDByName(t, restored.Engine, E2EWorkspace, "client")
	restored.SubscribeMenus(restoredCache, restoredClient)
	restored.waitDNSCacheConfig(restoredCache, "restore-storage", true, false)
	restored.WaitNodeLabel(restoredCache, "caching DNS")

	restored.sendDNSRequest(restoredClient)
	restored.waitDNSResponse(restoredClient, "NOERROR", "10.93.0.1")
}

func newDNSE2EWithDNSCacheManager(t *testing.T, cfg configer.Config, manager storage.DnsCacheManager) *dnsE2E {
	t.Helper()
	if cfg == nil {
		cfg = configer.NewMemory(nil)
	}
	pool := newE2ETestDebugPool(t)
	logf := NewDebugLogFactory(16384)
	sys := newE2EDebugSystem()
	engine, err := BuildEngine(context.Background(), cfg, logf, sys, nil, manager, nil, nil, nil, pool, true)
	if err != nil {
		t.Fatalf("BuildEngine: %v", err)
	}
	t.Cleanup(func() {
		if err := engine.Close(); err != nil {
			t.Errorf("engine.Close error = %v, want nil", err)
		}
	})

	e := NewNodeE2EFromEngine(t, engine, logf, sys)
	return newDNSE2EFromNodeE2E(e)
}

func assertDNSCachePort(t *testing.T, engine Engine, port uint64, direction string) {
	t.Helper()
	snapshot, ok := engine.PortSnapshot(E2EWorkspace, port)
	if !ok {
		t.Fatalf("PortSnapshot(%d) missing", port)
	}
	if snapshot.Direction != direction || snapshot.Name != "DNS" || len(snapshot.Types) != 1 || snapshot.Types[0] != dnsnodes.TypeDNS {
		t.Fatalf("DNS cache port = %#v, want %s DNS %s", snapshot, direction, dnsnodes.TypeDNS)
	}
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
