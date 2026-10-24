package dnsnodes_test

import (
	dnsnodes "github.com/asciimoth/almagest/lib/dns"
	"github.com/asciimoth/almagest/lib/links"
	networknodes "github.com/asciimoth/almagest/lib/network"
	"github.com/asciimoth/almagest/lib/testkit"
	"testing"

	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/formular"
)

const (
	meshNamesTestAuthority = "200:6fc8:9220:f400:5cc2:305a:4ac6:967e"
	meshNamesTestMeship    = "aiag7sesed2aaxgcgbnevruwpy.meship"
)

func TestMeshNamesStaticFallbackWithDNSQueryE2E(t *testing.T) {
	e := newDNSE2E(t)
	mock := e.AddNode(networknodes.NodeTypeMockNetwork, "mock")
	fallback := e.AddNode(dnsnodes.NodeTypeStaticDNS, "fallback")
	mesh := e.AddNode(dnsnodes.NodeTypeMeshNames, "mesh")
	client := e.AddNode(dnsnodes.NodeTypeDNSQuery, "client")
	e.SubscribeMenus(fallback, client)

	e.applyStaticDNSRecords(fallback, "ordinary.mesh.test A 10.111.0.1")
	e.setDNSQueryRequest(client, "ordinary.mesh.test.", "A")
	e.MustAddTypedLinkByPortName(links.TypeNetwork, mock, "left", "Network", mesh, "right", "Network")
	e.MustAddTypedLinkByPortName(dnsnodes.TypeDNS, fallback, "left", "DNS", mesh, "right", "Fallback DNS")
	e.MustAddTypedLinkByPortName(dnsnodes.TypeDNS, mesh, "left", "DNS", client, "right", "DNS")
	e.WaitNodeLabel(mesh, "resolving MeshNames with fallback")

	e.sendDNSRequest(client)
	e.waitDNSResponse(client, "NOERROR", "10.111.0.1")

	e.setDNSQueryRequest(client, meshNamesTestMeship+".", "AAAA")
	e.sendDNSRequest(client)
	e.waitDNSResponse(client, "NOERROR", meshNamesTestAuthority)
}

func TestMeshNamesWithoutNetworkLinkResolvesDirectMeshipE2E(t *testing.T) {
	e := newDNSE2E(t)
	mesh := e.AddNode(dnsnodes.NodeTypeMeshNames, "mesh")
	client := e.AddNode(dnsnodes.NodeTypeDNSQuery, "client")
	e.SubscribeMenus(client)

	e.setDNSQueryRequest(client, meshNamesTestMeship+".", "AAAA")
	e.MustAddTypedLinkByPortName(dnsnodes.TypeDNS, mesh, "left", "DNS", client, "right", "DNS")
	e.WaitNodeLabel(mesh, "resolving local MeshNames")

	e.sendDNSRequest(client)
	e.waitDNSResponse(client, "NOERROR", meshNamesTestAuthority)
}

func TestMeshNamesSchemeControlsApplySaveAndRestoreE2E(t *testing.T) {
	e := newDNSE2E(t)
	mesh := e.AddNode(dnsnodes.NodeTypeMeshNames, "mesh")
	e.SubscribeMenus(mesh)

	fields := []string{
		dnsnodes.MeshNamesDisableMeshnameField,
		dnsnodes.MeshNamesDisableMeshipField,
		dnsnodes.MeshNamesDisablePKYggField,
		dnsnodes.MeshNamesDisableYggField,
		dnsnodes.MeshNamesDisableIPv6LiteralNetField,
	}
	for _, field := range fields {
		assertMeshNamesBoolMenuValue(t, e, mesh, field, false)
	}

	e.Engine.SendNodeFormularMsg(E2EWorkspace, mesh, formular.FormApplyMessage{
		MessageBase: testkit.FormMessageBase(formular.MessageFormApply, mesh),
		BlockID:     dnsnodes.MeshNamesConfigBlockID,
		Values: map[string]any{
			dnsnodes.MeshNamesDisableMeshnameField:       true,
			dnsnodes.MeshNamesDisableMeshipField:         true,
			dnsnodes.MeshNamesDisablePKYggField:          true,
			dnsnodes.MeshNamesDisableYggField:            true,
			dnsnodes.MeshNamesDisableIPv6LiteralNetField: true,
		},
	})
	for _, field := range fields {
		assertMeshNamesBoolMenuValue(t, e, mesh, field, true)
	}

	cfg := configer.NewMemory(nil)
	if err := e.Engine.SaveConfig(cfg); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	nodeCfg := cfg.View(configer.Path{"nodes", "mesh"})
	for _, field := range fields {
		if !configer.Bool(nodeCfg, configer.Path{field}, false) {
			t.Fatalf("saved %s = false, want true", field)
		}
	}

	restored := newDNSE2EFromConfig(t, cfg)
	restoredMesh := testkit.NodeIDByName(t, restored.Engine, E2EWorkspace, "mesh")
	restored.SubscribeMenus(restoredMesh)
	for _, field := range fields {
		assertMeshNamesBoolMenuValue(t, restored, restoredMesh, field, true)
	}
}

func TestMeshNamesWorksThroughDnsCacheAndRouterE2E(t *testing.T) {
	e := newDNSE2EWithDNSCacheManager(t, configer.NewMemory(nil), newMemoryDNSCacheManager())
	mock := e.AddNode(networknodes.NodeTypeMockNetwork, "mock")
	mesh := e.AddNode(dnsnodes.NodeTypeMeshNames, "mesh")
	cache := e.AddNode(dnsnodes.NodeTypeDnsCache, "cache")
	router := e.AddNode(dnsnodes.NodeTypeDnsRouter, "router")
	client := e.AddNode(dnsnodes.NodeTypeDNSQuery, "client")
	e.SubscribeMenus(cache, router, client)

	e.applyDNSCacheConfig(cache, "mesh-cache", false, false)
	e.applyDNSRouterRules(router, `ADDR_RE \.meship\.$
BACKEND slot 1`)
	e.setDNSQueryRequest(client, meshNamesTestMeship+".", "AAAA")

	e.MustAddTypedLinkByPortName(links.TypeNetwork, mock, "left", "Network", mesh, "right", "Network")
	cacheInput := e.MustAddTypedLinkByPortName(dnsnodes.TypeDNS, mesh, "left", "DNS", cache, "right", "DNS")
	e.MustAddTypedLinkByPortName(dnsnodes.TypeDNS, cache, "left", "DNS", router, "right", "slot 1")
	e.MustAddTypedLinkByPortName(dnsnodes.TypeDNS, router, "left", "DNS", client, "right", "DNS")
	e.WaitNodeLabel(mesh, "resolving MeshNames")
	e.WaitNodeLabel(cache, "caching DNS")
	e.WaitNodeLabel(router, "routing DNS")

	e.sendDNSRequest(client)
	e.waitDNSResponse(client, "NOERROR", meshNamesTestAuthority)

	e.Engine.RemoveLink(E2EWorkspace, cacheInput)
	e.WaitNodeLabel(cache, "cache-only DNS")
	e.sendDNSRequest(client)
	e.waitDNSResponse(client, "NOERROR", meshNamesTestAuthority)
}

func TestMeshNamesSaveRestorePreservesLinksE2E(t *testing.T) {
	e := newDNSE2E(t)
	mock := e.AddNode(networknodes.NodeTypeMockNetwork, "mock")
	fallback := e.AddNode(dnsnodes.NodeTypeStaticDNS, "fallback")
	mesh := e.AddNode(dnsnodes.NodeTypeMeshNames, "mesh")
	clientName := "client"
	client := e.AddNode(dnsnodes.NodeTypeDNSQuery, clientName)
	e.SubscribeMenus(fallback, client)

	e.applyStaticDNSRecords(fallback, "restore.mesh.test A 10.112.0.1")
	e.setDNSQueryRequest(client, "restore.mesh.test.", "A")
	e.MustAddTypedLinkByPortName(links.TypeNetwork, mock, "left", "Network", mesh, "right", "Network")
	e.MustAddTypedLinkByPortName(dnsnodes.TypeDNS, fallback, "left", "DNS", mesh, "right", "Fallback DNS")
	e.MustAddTypedLinkByPortName(dnsnodes.TypeDNS, mesh, "left", "DNS", client, "right", "DNS")
	e.sendDNSRequest(client)
	e.waitDNSResponse(client, "NOERROR", "10.112.0.1")

	cfg := configer.NewMemory(nil)
	if err := e.Engine.SaveConfig(cfg); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}

	restored := newDNSE2EFromConfig(t, cfg)
	restoredClient := testkit.NodeIDByName(t, restored.Engine, E2EWorkspace, clientName)
	restoredMesh := testkit.NodeIDByName(t, restored.Engine, E2EWorkspace, "mesh")
	restored.SubscribeMenus(restoredClient)
	restored.WaitNodeLabel(restoredMesh, "resolving MeshNames with fallback")
	restored.sendDNSRequest(restoredClient)
	restored.waitDNSResponse(restoredClient, "NOERROR", "10.112.0.1")
}

func assertMeshNamesBoolMenuValue(t *testing.T, e *dnsE2E, node uint64, field string, want bool) {
	t.Helper()
	Eventually(t, func() bool {
		item, ok := e.MenuItem(node, dnsnodes.MeshNamesConfigBlockID, field)
		if !ok || item.Field == nil {
			return false
		}
		value, ok := formular.BoolValue(item.Value)
		return ok && value == want
	}, "MeshNames field %s = %t", field, want)
}
