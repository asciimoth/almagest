package almagest

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/formular"
	sysnetdebug "github.com/asciimoth/gonnect/sysnet/debug"
	"github.com/asciimoth/pasta/pasta"
	"github.com/asciimoth/pasta/pasta/std"
)

const (
	networkRouterSlot1Response      = "Response from network 1"
	networkRouterSlot2Response      = "Response from network 2"
	networkRouterReverseDNSResponse = "Response from reverse DNS cache route"
)

type networkRouterE2EFixture struct {
	*httpE2E

	router  uint64
	backend [3]uint64
	server  [3]uint64
	client  uint64
	port    [3]int
}

func TestNetworkRouterE2ELinksDetachAttachReattach(t *testing.T) {
	f := setupNetworkRouterE2E(t)
	f.assertRouterRequest(1, networkRouterSlot1Response)
	f.assertRouterRequest(2, networkRouterSlot2Response)

	clientLink := f.mustNetworkLink(f.router, "left", "Network", f.client, "right", "Network")
	f.Engine.RemoveLink(E2EWorkspace, clientLink)
	f.sendClientRequest(f.client)
	f.waitClientFailure(f.client, "network is not ready")
	f.connectByPortName(f.router, "left", "Network", f.client, "right", "Network")
	f.waitClientReady(f.client)
	f.assertRouterRequest(1, networkRouterSlot1Response)

	slot2Link := f.mustNetworkLink(f.backend[2], "left", "Network", f.router, "right", "slot 2")
	f.Engine.RemoveLink(E2EWorkspace, slot2Link)
	f.assertRouterRequestFails(2)

	f.connectByPortName(f.backend[2], "left", "Network", f.router, "right", "slot 2")
	f.assertRouterRequest(2, networkRouterSlot2Response)

	slot2Link = f.mustNetworkLink(f.backend[2], "left", "Network", f.router, "right", "slot 2")
	f.Engine.RemoveLink(E2EWorkspace, slot2Link)
	f.connectByPortName(f.backend[1], "left", "Network", f.router, "right", "slot 2")
	f.assertRouterRequestFails(2)

	slot2Link = f.mustNetworkLink(f.backend[1], "left", "Network", f.router, "right", "slot 2")
	f.Engine.RemoveLink(E2EWorkspace, slot2Link)
	f.connectByPortName(f.backend[2], "left", "Network", f.router, "right", "slot 2")
	f.assertRouterRequest(2, networkRouterSlot2Response)
}

func TestNetworkRouterE2EConfigSaveRestore(t *testing.T) {
	f := setupNetworkRouterE2E(t)
	f.assertRouterRequest(1, networkRouterSlot1Response)
	f.assertRouterRequest(2, networkRouterSlot2Response)
	routerName := f.nodeName(f.router)
	clientName := f.nodeName(f.client)
	backend1Name := f.nodeName(f.backend[1])
	backend2Name := f.nodeName(f.backend[2])
	server1Name := f.nodeName(f.server[1])
	server2Name := f.nodeName(f.server[2])

	cfg := configer.NewMemory(nil)
	if err := f.Engine.SaveConfig(cfg); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	if got, err := cfg.Get(configer.Path{"nodes", routerName, "rules"}); err != nil || got != f.routerRules() {
		t.Fatalf("saved router rules = %q, %v; want %q", got, err, f.routerRules())
	}
	if err := f.Engine.Close(); err != nil {
		t.Fatalf("close saved engine: %v", err)
	}

	restored := newNetworkRouterConfigE2E(t, cfg)
	router := restored.mustNodeByName(routerName)
	client := restored.mustNodeByName(clientName)
	backend1 := restored.mustNodeByName(backend1Name)
	backend2 := restored.mustNodeByName(backend2Name)
	restored.SubscribeMenus(client, restored.mustNodeByName(server1Name), restored.mustNodeByName(server2Name))
	restored.assertNetworkRouterPorts(router, []string{"slot 1", "slot 2", networkRouterDNSPortName})
	restored.mustNetworkLink(router, "left", "Network", client, "right", "Network")
	restored.mustNetworkLink(backend1, "left", "Network", router, "right", "slot 1")
	restored.mustNetworkLink(backend2, "left", "Network", router, "right", "slot 2")
	restored.waitServerListening(restored.mustNodeByName(server1Name), fmt.Sprintf("127.0.0.1:%d", f.port[1]))
	restored.waitServerListening(restored.mustNodeByName(server2Name), fmt.Sprintf("127.0.0.1:%d", f.port[2]))
	restored.assertRouterRequestOnClient(client, f.port[1], networkRouterSlot1Response)
	restored.assertRouterRequestOnClient(client, f.port[2], networkRouterSlot2Response)
}

func TestNetworkRouterE2ERestoreLocalProxyConsumerBeforeSlots(t *testing.T) {
	e, sys := newNetworkRouterLocalProxyE2E(t, configer.NewMemory(nil))
	router := e.AddNode(NodeTypeNetworkRouter, "router")
	backend1 := e.AddNode(NodeTypeMockNetwork, "backend-1")
	backend2 := e.AddNode(NodeTypeMockNetwork, "backend-2")
	socksPort := httpTestPort()
	port1 := httpTestPort()
	port2 := httpTestPort()
	server1 := e.addHTTPServerWithResponse("server-1", port1, networkRouterSlot1Response)
	server2 := e.addHTTPServerWithResponse("server-2", port2, networkRouterSlot2Response)
	proxy := (&localProxyE2E{socksServerE2E: &socksServerE2E{NodeE2E: e.NodeE2E}, sys: sys}).addLocalProxy("aaa-local-proxy", socksPort, nil)
	e.SubscribeMenus(router, server1, server2, proxy)

	e.Engine.SendNodeFormularMsg(E2EWorkspace, router, formular.FormApplyMessage{
		MessageBase: FormMessageBase(formular.MessageFormApply, router),
		BlockID:     "config",
		Values: map[string]any{
			"rules": fmt.Sprintf("PORT %d\nSLOT 1\nPORT %d\nSLOT 2", port1, port2),
		},
	})
	e.assertNetworkRouterPorts(router, []string{"slot 1", "slot 2", networkRouterDNSPortName})
	e.connectByPortName(backend1, "left", "Network", server1, "right", "Network")
	e.connectByPortName(backend2, "left", "Network", server2, "right", "Network")
	e.connectByPortName(router, "left", "Network", proxy, "right", "proxy")
	e.connectByPortName(backend1, "left", "Network", router, "right", "slot 1")
	e.connectByPortName(backend2, "left", "Network", router, "right", "slot 2")
	e.waitServerListening(server1, fmt.Sprintf("127.0.0.1:%d", port1))
	e.waitServerListening(server2, fmt.Sprintf("127.0.0.1:%d", port2))
	(&localProxyE2E{socksServerE2E: &socksServerE2E{NodeE2E: e.NodeE2E}, sys: sys}).waitSocksLog(proxy, fmt.Sprintf("socks listening on 127.0.0.1:%d", socksPort))

	cfg := configer.NewMemory(nil)
	if err := e.Engine.SaveConfig(cfg); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	if err := e.Engine.Close(); err != nil {
		t.Fatalf("close saved engine: %v", err)
	}

	restored, restoredSys := newNetworkRouterLocalProxyE2E(t, cfg)
	restoredRouter := restored.mustNodeByName("router")
	snapshot, ok := restored.Engine.NodeSnapshot(E2EWorkspace, restoredRouter)
	if !ok {
		t.Fatal("restored router snapshot missing")
	}
	if snapshot.Placeholder {
		t.Fatalf("restored router is placeholder: %#v", snapshot.Popups)
	}
	restoredProxy := restored.mustNodeByName("aaa-local-proxy")
	restoredServer1 := restored.mustNodeByName("server-1")
	restoredServer2 := restored.mustNodeByName("server-2")
	restored.SubscribeMenus(restoredRouter, restoredProxy, restoredServer1, restoredServer2)
	restored.waitServerListening(restoredServer1, fmt.Sprintf("127.0.0.1:%d", port1))
	restored.waitServerListening(restoredServer2, fmt.Sprintf("127.0.0.1:%d", port2))
	restoredLocalProxy := &localProxyE2E{socksServerE2E: &socksServerE2E{NodeE2E: restored.NodeE2E}, sys: restoredSys}
	restoredLocalProxy.waitSocksLog(restoredProxy, fmt.Sprintf("socks listening on 127.0.0.1:%d", socksPort))
	if body := restoredLocalProxy.waitLocalProxyHTTPGet(socksPort, port1, nil); body != networkRouterSlot1Response {
		t.Fatalf("restored proxy port %d body = %q, want %q", port1, body, networkRouterSlot1Response)
	}
	if body := restoredLocalProxy.waitLocalProxyHTTPGet(socksPort, port2, nil); body != networkRouterSlot2Response {
		t.Fatalf("restored proxy port %d body = %q, want %q", port2, body, networkRouterSlot2Response)
	}
}

func TestNetworkRouterE2ERoutesByReverseDNSCacheHostname(t *testing.T) {
	manager := newMemoryDNSCacheManager()
	dnsE := newDNSE2EWithDNSCacheManager(t, configer.NewMemory(nil), manager)
	e := &httpE2E{NodeE2E: dnsE.NodeE2E}
	const (
		storageName = "network-router-routing-cache"
		hostName    = "router-dst-cache.test."
	)
	port := httpTestPort()
	warmDNSCacheReverseLookupsForRouting(
		t,
		e.NodeE2E,
		"network-router",
		storageName,
		"router-dst-cache.test A 127.0.0.1",
		dnsCacheWarmQuery{name: hostName, wantA: "127.0.0.1"},
	)

	router := e.AddNode(NodeTypeNetworkRouter, "router")
	backend := e.AddNode(NodeTypeMockNetwork, "backend")
	server := e.addHTTPServerWithResponse("server", port, networkRouterReverseDNSResponse)
	client := e.addHTTPClient("client", port)
	e.SubscribeMenus(router, server, client)
	e.waitClientRequestField(client, "url", fmt.Sprintf("http://127.0.0.1:%d/", port))

	e.Engine.SendNodeFormularMsg(E2EWorkspace, router, formular.FormApplyMessage{
		MessageBase: FormMessageBase(formular.MessageFormApply, router),
		BlockID:     networkRouterConfigBlockID,
		Values: map[string]any{
			networkRouterRulesField: fmt.Sprintf(
				"ADDR_S %s\nSLOT 2\nTRUE\nSLOT 1",
				hostName,
			),
			networkRouterDNSCacheStorageNameField: storageName,
		},
	})
	e.assertNetworkRouterPorts(router, []string{"slot 1", "slot 2", networkRouterDNSPortName})
	e.connectByPortName(backend, "left", "Network", server, "right", "Network")
	e.connectByPortName(backend, "left", "Network", router, "right", "slot 2")
	e.connectByPortName(router, "left", "Network", client, "right", "Network")
	e.waitServerListening(server, fmt.Sprintf("127.0.0.1:%d", port))
	e.assertClientRequestReturns(client, networkRouterReverseDNSResponse, 10*time.Second)
}

func TestNetworkRouterE2ECopyPaste(t *testing.T) {
	f := setupNetworkRouterE2E(t)

	clip := f.Engine.Copy(E2EWorkspace, []uint64{f.router, f.backend[1], f.backend[2]})
	if clip == "" {
		t.Fatal("Copy returned empty clipboard")
	}
	pasted := f.Engine.Paste(E2EWorkspace, clip)
	if len(pasted) != 3 {
		t.Fatalf("Paste returned %d nodes (%v), want 3", len(pasted), pasted)
	}
	router := pasted[0]
	f.assertNetworkRouterPorts(router, []string{"slot 1", "slot 2", networkRouterDNSPortName})
	backend1 := f.routerBackendBySlot(router, 1)
	backend2 := f.routerBackendBySlot(router, 2)
	f.assertNodesLinked(backend1, router)
	f.assertNodesLinked(backend2, router)

	f.Engine.RemoveNode(E2EWorkspace, f.server[1])
	f.Engine.RemoveNode(E2EWorkspace, f.server[2])

	server1 := f.addHTTPServerWithResponse("pasted-server-1", f.port[1], networkRouterSlot1Response)
	server2 := f.addHTTPServerWithResponse("pasted-server-2", f.port[2], networkRouterSlot2Response)
	client := f.addHTTPClient("pasted-client", f.port[1])
	f.SubscribeMenus(server1, server2, client)

	f.connectByPortName(backend1, "left", "Network", server1, "right", "Network")
	f.connectByPortName(backend2, "left", "Network", server2, "right", "Network")
	f.connectByPortName(router, "left", "Network", client, "right", "Network")

	f.waitServerListening(server1, fmt.Sprintf("127.0.0.1:%d", f.port[1]))
	f.waitServerListening(server2, fmt.Sprintf("127.0.0.1:%d", f.port[2]))
	f.assertRouterRequestOnClient(client, f.port[1], networkRouterSlot1Response)
	f.assertRouterRequestOnClient(client, f.port[2], networkRouterSlot2Response)
}

func newNetworkRouterLocalProxyE2E(t *testing.T, cfg configer.Config) (*httpE2E, *sysnetdebug.System) {
	t.Helper()
	pool := newE2ETestDebugPool(t)
	logf := NewDebugLogFactory(16384)
	sys := newE2EDebugSystem()
	engine, err := BuildEngine(
		context.Background(),
		cfg,
		logf,
		sys,
		nil,
		nil, nil,

		nil,
		pool,
		true)

	if err != nil {
		t.Fatalf("BuildEngine error = %v, want nil", err)
	}
	t.Cleanup(func() {
		if err := engine.Close(); err != nil {
			t.Errorf("engine.Close error = %v, want nil", err)
		}
	})
	e := &NodeE2E{T: t, Engine: engine, LogFactory: logf, menus: formular.NewMenuSnapshotState()}
	sub := engine.SubscribeNotifications(func(notification EngineNotification) {
		if notification.Kind != NotificationWorkspace || notification.Workspace == nil {
			return
		}
		if notification.Workspace.Kind == pasta.NotificationNodeMenu {
			e.mu.Lock()
			defer e.mu.Unlock()
			e.menus.Apply(notification.Workspace.Formular)
		}
	})
	if sub == 0 {
		t.Fatal("SubscribeNotifications returned 0")
	}
	t.Cleanup(func() { engine.UnsubscribeNotifications(sub) })
	return &httpE2E{NodeE2E: e}, sys
}

func setupNetworkRouterE2E(t *testing.T) networkRouterE2EFixture {
	t.Helper()
	e := newHTTPE2E(t)
	f := networkRouterE2EFixture{httpE2E: e}
	f.port[1] = httpTestPort()
	f.port[2] = httpTestPort()
	f.router = e.AddNode(NodeTypeNetworkRouter, "router")
	f.backend[1] = e.AddNode(NodeTypeMockNetwork, "backend-1")
	f.backend[2] = e.AddNode(NodeTypeMockNetwork, "backend-2")
	f.server[1] = f.addHTTPServerWithResponse("server-1", f.port[1], networkRouterSlot1Response)
	f.server[2] = f.addHTTPServerWithResponse("server-2", f.port[2], networkRouterSlot2Response)
	f.client = e.addHTTPClient("client", f.port[1])
	e.SubscribeMenus(f.router, f.server[1], f.server[2], f.client)

	f.applyRouterRules()
	f.assertNetworkRouterPorts(f.router, []string{"slot 1", "slot 2", networkRouterDNSPortName})
	e.connectByPortName(f.backend[1], "left", "Network", f.server[1], "right", "Network")
	e.connectByPortName(f.backend[2], "left", "Network", f.server[2], "right", "Network")
	e.connectByPortName(f.backend[1], "left", "Network", f.router, "right", "slot 1")
	e.connectByPortName(f.backend[2], "left", "Network", f.router, "right", "slot 2")
	e.connectByPortName(f.router, "left", "Network", f.client, "right", "Network")

	e.waitServerListening(f.server[1], fmt.Sprintf("127.0.0.1:%d", f.port[1]))
	e.waitServerListening(f.server[2], fmt.Sprintf("127.0.0.1:%d", f.port[2]))
	return f
}

func newNetworkRouterConfigE2E(t *testing.T, cfg configer.Config) *httpE2E {
	t.Helper()
	return newNetworkRouterConfigE2EWithSecrets(t, cfg, newE2ESecretsStorage())
}

func newNetworkRouterConfigE2EWithSecrets(t *testing.T, cfg configer.Config, secrets *e2eSecretsStorage) *httpE2E {
	t.Helper()
	pool := newE2ETestDebugPool(t)
	logf := NewDebugLogFactory(16384)
	if secrets == nil {
		secrets = newE2ESecretsStorage()
	}
	engine, err := BuildEngine(
		context.Background(),
		cfg,
		logf,
		newE2EDebugSystem(),
		nil,
		nil, secrets,

		nil,
		pool,
		true)

	if err != nil {
		t.Fatalf("BuildEngine restored: %v", err)
	}
	t.Cleanup(func() {
		if err := engine.Close(); err != nil {
			t.Errorf("restored engine.Close error = %v, want nil", err)
		}
	})
	e := &NodeE2E{T: t, Engine: engine, LogFactory: logf, Secrets: secrets, menus: formular.NewMenuSnapshotState()}
	sub := engine.SubscribeNotifications(func(notification EngineNotification) {
		if notification.Kind != NotificationWorkspace || notification.Workspace == nil {
			return
		}
		if notification.Workspace.Kind == pasta.NotificationNodeMenu {
			e.mu.Lock()
			defer e.mu.Unlock()
			e.menus.Apply(notification.Workspace.Formular)
		}
	})
	if sub == 0 {
		t.Fatal("SubscribeNotifications returned 0")
	}
	t.Cleanup(func() { engine.UnsubscribeNotifications(sub) })
	return &httpE2E{NodeE2E: e}
}

func (f networkRouterE2EFixture) routerRules() string {
	return fmt.Sprintf("PORT %d\nSLOT 1\nPORT %d\nSLOT 2", f.port[1], f.port[2])
}

func (f networkRouterE2EFixture) applyRouterRules() {
	f.T.Helper()
	f.Engine.SendNodeFormularMsg(E2EWorkspace, f.router, formular.FormApplyMessage{
		MessageBase: FormMessageBase(formular.MessageFormApply, f.router),
		BlockID:     "config",
		Values:      map[string]any{"rules": f.routerRules()},
	})
}

func (e *httpE2E) addHTTPServerWithResponse(name string, port int, response string) uint64 {
	e.T.Helper()
	responseNode := e.addStringConstant(name+"-response", response)
	server := e.addHTTPServer(name, port)
	e.MustAddTypedLinkByPortName(std.TypeString, responseNode, "right", "output", server, "left", "Response")
	return server
}

func (f networkRouterE2EFixture) assertRouterRequest(slot int, response string) {
	f.T.Helper()
	f.assertRouterRequestOnClient(f.client, f.port[slot], response)
}

func (f networkRouterE2EFixture) assertRouterRequestFails(slot int) {
	f.T.Helper()
	f.setHTTPClientURL(f.client, f.port[slot])
	f.sendClientRequest(f.client)
	f.waitClientAnyFailure(f.client)
}

func (e *httpE2E) assertRouterRequestOnClient(client uint64, port int, response string) {
	e.T.Helper()
	e.setHTTPClientURL(client, port)
	e.assertClientRequestReturns(client, response, 10*time.Second)
}

func (e *httpE2E) setHTTPClientURL(client uint64, port int) {
	e.T.Helper()
	url := fmt.Sprintf("http://127.0.0.1:%d/", port)
	e.Engine.SendNodeFormularMsg(E2EWorkspace, client, formular.FieldUpdateMessage{
		MessageBase: FormMessageBase(formular.MessageFieldUpdate, client),
		Field:       formular.FieldRef{BlockID: "request", FieldID: "url"},
		Value:       url,
	})
	e.waitClientRequestField(client, "url", url)
}

func (e *httpE2E) assertNetworkRouterPorts(router uint64, want []string) {
	e.T.Helper()
	Eventually(e.T, func() bool {
		got := e.networkRouterPorts(router)
		if len(got) != len(want) {
			return false
		}
		for i := range got {
			if got[i] != want[i] {
				return false
			}
		}
		return true
	}, "router %d ports %v", router, want)
}

func (e *httpE2E) networkRouterPorts(router uint64) []string {
	e.T.Helper()
	snapshot, ok := e.Engine.NodeSnapshot(E2EWorkspace, router)
	if !ok {
		return nil
	}
	got := make([]string, 0, len(snapshot.RightPorts))
	for _, port := range snapshot.RightPorts {
		ps, ok := e.Engine.PortSnapshot(E2EWorkspace, port)
		if ok {
			got = append(got, ps.Name)
		}
	}
	return got
}

func (e *httpE2E) mustNetworkLink(leftNode uint64, leftDir, leftName string, rightNode uint64, rightDir, rightName string) uint64 {
	e.T.Helper()
	left := e.NodePort(leftNode, leftDir, leftName)
	right := e.NodePort(rightNode, rightDir, rightName)
	link, snapshot, ok := e.Engine.LinkByPorts(E2EWorkspace, left, right)
	if !ok {
		e.T.Fatalf("link missing between node %d %s %q and node %d %s %q", leftNode, leftDir, leftName, rightNode, rightDir, rightName)
	}
	if snapshot.Type != TypeNetwork {
		e.T.Fatalf("link %d type = %q, want %q", link, snapshot.Type, TypeNetwork)
	}
	return link
}

func (e *httpE2E) routerBackendBySlot(router uint64, slot int) uint64 {
	e.T.Helper()
	port := e.RightPort(router, slotPortName(slot))
	snapshot, ok := e.Engine.PortSnapshot(E2EWorkspace, port)
	if !ok {
		e.T.Fatalf("router slot %d port snapshot missing", slot)
	}
	for _, link := range snapshot.Links {
		linkSnapshot, ok := e.Engine.LinkSnapshot(E2EWorkspace, link)
		if !ok || linkSnapshot.Type != TypeNetwork || linkSnapshot.RightPort != port {
			continue
		}
		return linkSnapshot.LeftPortNode
	}
	e.T.Fatalf("router slot %d has no backend link", slot)
	return 0
}

func (e *httpE2E) assertNodesLinked(a, b uint64) {
	e.T.Helper()
	if !e.Engine.NodesConnected(E2EWorkspace, a, b) {
		e.T.Fatalf("nodes %d and %d are not linked", a, b)
	}
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
