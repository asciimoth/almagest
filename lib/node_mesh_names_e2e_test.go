package almagest

import (
	"testing"

	"github.com/asciimoth/configer/configer"
)

func TestMeshNamesStaticFallbackWithDNSQueryE2E(t *testing.T) {
	e := newDNSE2E(t)
	mock := e.AddNode(NodeTypeMockNetwork, "mock")
	fallback := e.AddNode(NodeTypeStaticDNS, "fallback")
	mesh := e.AddNode(NodeTypeMeshNames, "mesh")
	client := e.AddNode(NodeTypeDNSQuery, "client")
	e.SubscribeMenus(fallback, client)

	e.applyStaticDNSRecords(fallback, "ordinary.mesh.test A 10.111.0.1")
	e.setDNSQueryRequest(client, "ordinary.mesh.test.", "A")
	e.MustAddTypedLinkByPortName(TypeNetwork, mock, "left", "Network", mesh, "right", "Network")
	e.MustAddTypedLinkByPortName(TypeDNS, fallback, "left", "DNS", mesh, "right", "Fallback DNS")
	e.MustAddTypedLinkByPortName(TypeDNS, mesh, "left", "DNS", client, "right", "DNS")
	e.WaitNodeLabel(mesh, "resolving MeshNames with fallback")

	e.sendDNSRequest(client)
	e.waitDNSResponse(client, "NOERROR", "10.111.0.1")

	e.setDNSQueryRequest(client, meshNamesTestMeship+".", "AAAA")
	e.sendDNSRequest(client)
	e.waitDNSResponse(client, "NOERROR", meshNamesTestAuthority)
}

func TestMeshNamesWithoutNetworkLinkDoesNotPublishDirectMeshipE2E(t *testing.T) {
	e := newDNSE2E(t)
	mesh := e.AddNode(NodeTypeMeshNames, "mesh")
	client := e.AddNode(NodeTypeDNSQuery, "client")
	e.SubscribeMenus(client)

	e.setDNSQueryRequest(client, meshNamesTestMeship+".", "AAAA")
	e.MustAddTypedLinkByPortName(TypeDNS, mesh, "left", "DNS", client, "right", "DNS")
	e.WaitNodeLabel(mesh, "waiting for network")

	e.sendDNSRequest(client)
	e.waitDNSFailure(client, "dns is not ready")
}

func TestMeshNamesWorksThroughDnsCacheAndRouterE2E(t *testing.T) {
	e := newDNSE2EWithDNSCacheManager(t, configer.NewMemory(nil), newMemoryDNSCacheManager())
	mock := e.AddNode(NodeTypeMockNetwork, "mock")
	mesh := e.AddNode(NodeTypeMeshNames, "mesh")
	cache := e.AddNode(NodeTypeDnsCache, "cache")
	router := e.AddNode(NodeTypeDnsRouter, "router")
	client := e.AddNode(NodeTypeDNSQuery, "client")
	e.SubscribeMenus(cache, router, client)

	e.applyDNSCacheConfig(cache, "mesh-cache", false, false)
	e.applyDNSRouterRules(router, "SUFFIX meship SLOT 1")
	e.setDNSQueryRequest(client, meshNamesTestMeship+".", "AAAA")

	e.MustAddTypedLinkByPortName(TypeNetwork, mock, "left", "Network", mesh, "right", "Network")
	cacheInput := e.MustAddTypedLinkByPortName(TypeDNS, mesh, "left", "DNS", cache, "right", "DNS")
	e.MustAddTypedLinkByPortName(TypeDNS, cache, "left", "DNS", router, "right", "slot 1")
	e.MustAddTypedLinkByPortName(TypeDNS, router, "left", "DNS", client, "right", "DNS")
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
	mock := e.AddNode(NodeTypeMockNetwork, "mock")
	fallback := e.AddNode(NodeTypeStaticDNS, "fallback")
	mesh := e.AddNode(NodeTypeMeshNames, "mesh")
	clientName := "client"
	client := e.AddNode(NodeTypeDNSQuery, clientName)
	e.SubscribeMenus(fallback, client)

	e.applyStaticDNSRecords(fallback, "restore.mesh.test A 10.112.0.1")
	e.setDNSQueryRequest(client, "restore.mesh.test.", "A")
	e.MustAddTypedLinkByPortName(TypeNetwork, mock, "left", "Network", mesh, "right", "Network")
	e.MustAddTypedLinkByPortName(TypeDNS, fallback, "left", "DNS", mesh, "right", "Fallback DNS")
	e.MustAddTypedLinkByPortName(TypeDNS, mesh, "left", "DNS", client, "right", "DNS")
	e.sendDNSRequest(client)
	e.waitDNSResponse(client, "NOERROR", "10.112.0.1")

	cfg := configer.NewMemory(nil)
	if err := e.Engine.SaveConfig(cfg); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}

	restored := newDNSE2EFromConfig(t, cfg)
	restoredClient := nodeIDByName(t, restored.Engine, clientName)
	restoredMesh := nodeIDByName(t, restored.Engine, "mesh")
	restored.SubscribeMenus(restoredClient)
	restored.WaitNodeLabel(restoredMesh, "resolving MeshNames with fallback")
	restored.sendDNSRequest(restoredClient)
	restored.waitDNSResponse(restoredClient, "NOERROR", "10.112.0.1")
}
