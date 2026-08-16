package dnsnodes_test

import (
	"context"
	"testing"

	dnsnodes "github.com/asciimoth/almagest/lib/dns"
	"github.com/asciimoth/almagest/lib/storage"
	"github.com/asciimoth/almagest/lib/testkit"

	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/gonnect/dns"
)

func TestOutDnsResolvesSystemDefaultDNSE2E(t *testing.T) {
	e := newOutDNSE2E(t, nil, "default-out.test A 10.133.0.1", nil)
	out := e.AddNode(dnsnodes.NodeTypeOutDNS, "out")
	query := e.AddNode(dnsnodes.NodeTypeDNSQuery, "query")
	e.SubscribeMenus(query)
	e.setDNSQueryRequest(query, "default-out.test.", "A")

	e.MustAddTypedLinkByPortName(dnsnodes.TypeDNS, out, "left", "DNS", query, "right", "DNS")
	e.sendDNSRequest(query)
	e.waitDNSResponse(query, "NOERROR", "10.133.0.1")
}

func TestOutDnsWorksThroughDnsCacheAndRouterE2E(t *testing.T) {
	manager := newMemoryDNSCacheManager()
	e := newOutDNSE2E(t, nil, "cached-out.test A 10.133.0.2", manager)
	out := e.AddNode(dnsnodes.NodeTypeOutDNS, "out")
	cache := e.AddNode(dnsnodes.NodeTypeDnsCache, "cache")
	router := e.AddNode(dnsnodes.NodeTypeDnsRouter, "router")
	query := e.AddNode(dnsnodes.NodeTypeDNSQuery, "query")
	e.SubscribeMenus(cache, router, query)
	e.setDNSQueryRequest(query, "cached-out.test.", "A")

	inputLink := e.MustAddTypedLinkByPortName(dnsnodes.TypeDNS, out, "left", "DNS", cache, "right", "DNS")
	e.MustAddTypedLinkByPortName(dnsnodes.TypeDNS, cache, "left", "DNS", router, "right", "slot 1")
	e.MustAddTypedLinkByPortName(dnsnodes.TypeDNS, router, "left", "DNS", query, "right", "DNS")
	e.WaitNodeLabel(cache, "caching DNS")
	e.WaitNodeLabel(router, "routing DNS")

	e.sendDNSRequest(query)
	e.waitDNSResponse(query, "NOERROR", "10.133.0.2")

	e.Engine.RemoveLink(E2EWorkspace, inputLink)
	e.WaitNodeLabel(cache, "cache-only DNS")
	e.sendDNSRequest(query)
	e.waitDNSResponse(query, "NOERROR", "10.133.0.2")
}

func TestOutDnsCopyPastePreservesLinksE2E(t *testing.T) {
	e := newOutDNSE2E(t, nil, "copy-out.test A 10.133.0.3", nil)
	out := e.AddNode(dnsnodes.NodeTypeOutDNS, "out")
	query := e.AddNode(dnsnodes.NodeTypeDNSQuery, "query")
	e.SubscribeMenus(query)
	e.setDNSQueryRequest(query, "copy-out.test.", "A")
	e.MustAddTypedLinkByPortName(dnsnodes.TypeDNS, out, "left", "DNS", query, "right", "DNS")
	e.sendDNSRequest(query)
	e.waitDNSResponse(query, "NOERROR", "10.133.0.3")

	clip := e.Engine.Copy(E2EWorkspace, []uint64{out, query})
	if clip == "" {
		t.Fatal("Copy returned empty clipboard")
	}
	pasted := e.Engine.Paste(E2EWorkspace, clip)
	if len(pasted) != 2 {
		t.Fatalf("Paste returned %d nodes (%v), want 2", len(pasted), pasted)
	}
	pastedQuery := testkit.PastedNodeByClass(t, e.Engine, E2EWorkspace, pasted, dnsnodes.NodeTypeDNSQuery)
	e.SubscribeMenus(pastedQuery)
	e.sendDNSRequest(pastedQuery)
	e.waitDNSResponse(pastedQuery, "NOERROR", "10.133.0.3")
}

func TestOutDnsSaveRestorePreservesLinksE2E(t *testing.T) {
	const records = "restore-out.test A 10.133.0.4"
	e := newOutDNSE2E(t, nil, records, nil)
	out := e.AddNode(dnsnodes.NodeTypeOutDNS, "out")
	query := e.AddNode(dnsnodes.NodeTypeDNSQuery, "query")
	e.SubscribeMenus(query)
	e.setDNSQueryRequest(query, "restore-out.test.", "A")
	e.MustAddTypedLinkByPortName(dnsnodes.TypeDNS, out, "left", "DNS", query, "right", "DNS")
	e.sendDNSRequest(query)
	e.waitDNSResponse(query, "NOERROR", "10.133.0.4")

	cfg := configer.NewMemory(nil)
	if err := e.Engine.SaveConfig(cfg); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}

	restored := newOutDNSE2E(t, cfg, records, nil)
	restoredQuery := testkit.NodeIDByName(t, restored.Engine, E2EWorkspace, "query")
	restored.SubscribeMenus(restoredQuery)
	restored.sendDNSRequest(restoredQuery)
	restored.waitDNSResponse(restoredQuery, "NOERROR", "10.133.0.4")
}

func TestOutDnsLinkAttachDetachReattachE2E(t *testing.T) {
	e := newOutDNSE2E(t, nil, "lifecycle-out.test A 10.133.0.5", nil)
	out := e.AddNode(dnsnodes.NodeTypeOutDNS, "out")
	query := e.AddNode(dnsnodes.NodeTypeDNSQuery, "query")
	e.SubscribeMenus(query)
	e.setDNSQueryRequest(query, "lifecycle-out.test.", "A")

	link := e.MustAddTypedLinkByPortName(dnsnodes.TypeDNS, out, "left", "DNS", query, "right", "DNS")
	e.sendDNSRequest(query)
	e.waitDNSResponse(query, "NOERROR", "10.133.0.5")

	e.Engine.RemoveLink(E2EWorkspace, link)
	e.waitDNSReady(query)
	e.sendDNSRequest(query)
	e.waitDNSFailure(query, "dns is not ready")

	e.MustAddTypedLinkByPortName(dnsnodes.TypeDNS, out, "left", "DNS", query, "right", "DNS")
	e.waitDNSReady(query)
	e.sendDNSRequest(query)
	e.waitDNSResponse(query, "NOERROR", "10.133.0.5")
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

	e := NewNodeE2EFromEngine(t, engine, logf, sys)
	return newDNSE2EFromNodeE2E(e)
}
