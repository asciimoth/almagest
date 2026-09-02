package wireguardnodes_test

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
	"time"

	amnesianodes "github.com/asciimoth/almagest/lib/amnesia"
	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/formular"
	"github.com/asciimoth/pasta/pasta"
	"github.com/asciimoth/pasta/pasta/std"
)

const (
	wireGuardE2EClientAddr = "10.198.10.1"
	wireGuardE2EServerAddr = "10.198.20.1"
)

type wireGuardE2ENames struct {
	mock       string
	clientWG   string
	serverWG   string
	clientVTun string
	serverVTun string
	server     string
	client     string
}

type wireGuardE2EFixture struct {
	*httpE2E

	names wireGuardE2ENames

	mock       uint64
	clientWG   uint64
	serverWG   uint64
	clientVTun uint64
	serverVTun uint64
	server     uint64
	client     uint64

	clientWGTunLink uint64
	clientWGNetLink uint64

	clientWGPort int
	serverWGPort int
	httpPort     int
}

func TestWireGuardE2EVTunHTTPClientServer(t *testing.T) {
	f := setupWireGuardE2E(t, defaultWireGuardE2ENames(""))
	f.assertWireGuardRequestWorks()
	f.assertServerLog(f.server, "GET /")

	f.Engine.RemoveLink(E2EWorkspace, f.clientWGNetLink)
	f.WaitNodeLabel(f.clientWG, "waiting for network")
	f.clientWGNetLink = f.connectByPortName(f.mock, "left", "Network", f.clientWG, "right", "Network")
	f.WaitNodeLabel(f.clientWG, "up")
	f.assertWireGuardRequestWorks()

	f.Engine.RemoveLink(E2EWorkspace, f.clientWGTunLink)
	f.WaitNodeLabel(f.clientWG, "waiting for TUN")
	f.clientWGTunLink = f.connectTunByPortName(f.clientWG, "left", "Tun", f.clientVTun, "right", "Tun")
	f.WaitNodeLabel(f.clientWG, "up")
	f.assertWireGuardRequestWorks()
}

func TestWireGuardE2EWGConfigUsesNamedUDPTransport(t *testing.T) {
	f := setupWireGuardE2E(t, defaultWireGuardE2ENames("wg-config-"))
	f.assertWireGuardRequestWorks()

	// Move the client peer from the WireGuard menu to WGConfig. Waiting for the
	// local removal avoids an intentional duplicate-key ownership conflict.
	applyWireGuardPeers(t, f.Engine, f.clientWG, nil)
	waitWireGuardConfiguredAndRuntimePeerCounts(t, f.NodeE2E, f.clientWG, 0, 0)
	config := f.AddNode(NodeTypeWGConfig, "wireguard-client-peers")
	f.SubscribeMenus(config)
	_, serverPublic := wireGuardTestKeyPair(t, 73)
	applyWireGuardPeers(t, f.Engine, config, []wireGuardPeerConfig{{
		id:                          "server",
		publicKey:                   serverPublic,
		endpoint:                    fmt.Sprintf("127.0.0.1:%d", f.serverWGPort),
		allowedIPs:                  wireGuardE2EServerAddr + "/32",
		persistentKeepaliveInterval: 1,
	}})
	f.MustAddTypedLinkByPortName(TypeWGCtrl, f.clientWG, "left", "WGCtrl", config, "right", "WireGuard")
	f.MustAddTypedLinkByPortName(TypeNetwork, f.mock, "left", "Network", config, "right", "Network")
	assertWGConfigState(t, f.NodeE2E, config, "up")
	waitWireGuardConfiguredAndRuntimePeerCounts(t, f.NodeE2E, f.clientWG, 0, 1)
	f.assertWireGuardRequestWorks()
}

func TestWireGuardE2EObjectPeersMergeUpdateDetachAndReattach(t *testing.T) {
	f := setupWireGuardE2E(t, defaultWireGuardE2ENames("object-peers-"))
	first := f.AddNode(std.NodeTypeObjectConstant, "object-peers-first")
	second := f.AddNode(std.NodeTypeObjectConstant, "object-peers-second")
	_, firstKey := wireGuardTestKeyPair(t, 111)
	_, secondKey := wireGuardTestKeyPair(t, 112)
	_, replacementKeyA := wireGuardTestKeyPair(t, 113)
	_, replacementKeyB := wireGuardTestKeyPair(t, 114)
	_, serverPublic := wireGuardTestKeyPair(t, 73)

	f.ApplyStdConstantValue(first, wireGuardObjectPeersJSON(t, []wireGuardPeerConfig{{
		id: "first", publicKey: firstKey, allowedIPs: "10.211.1.1/32",
	}}))
	f.ApplyStdConstantValue(second, wireGuardObjectPeersJSON(t, []wireGuardPeerConfig{
		{
			// This peer conflicts with the working menu peer. If the object
			// won, its wrong endpoint and route would break the tunnel.
			id: "server-object-conflict", publicKey: serverPublic,
			endpoint: "127.0.0.1:1", allowedIPs: "10.255.255.1/32",
		},
		{id: "second", publicKey: secondKey, allowedIPs: "10.211.2.1/32"},
	}))
	firstLink := f.MustAddTypedLinkByPortName(std.TypeObject, first, "right", "output", f.clientWG, "left", "Peers")
	secondLink := f.MustAddTypedLinkByPortName(std.TypeObject, second, "right", "output", f.clientWG, "left", "Peers")
	waitWireGuardPeerCounts(t, f.httpE2E, f.clientWG, 3)
	f.assertWireGuardRequestWorks()

	// A new value from an existing link replaces only that link's latest
	// contribution and removes the old peer from the live wgo device.
	f.ApplyStdConstantValue(first, wireGuardObjectPeersJSON(t, []wireGuardPeerConfig{
		{id: "replacement-a", publicKey: replacementKeyA, allowedIPs: "10.211.3.1/32"},
		{id: "replacement-b", publicKey: replacementKeyB, allowedIPs: "10.211.4.1/32"},
	}))
	waitWireGuardPeerCounts(t, f.httpE2E, f.clientWG, 4)
	f.assertWireGuardRequestWorks()

	// Detach removes only the second source. Reattach requests its latest
	// object and restores both its unique peer and its shadowed conflict.
	f.Engine.RemoveLink(E2EWorkspace, secondLink)
	waitWireGuardPeerCounts(t, f.httpE2E, f.clientWG, 3)
	f.assertWireGuardRequestWorks()
	secondLink = f.MustAddTypedLinkByPortName(std.TypeObject, second, "right", "output", f.clientWG, "left", "Peers")
	waitWireGuardPeerCounts(t, f.httpE2E, f.clientWG, 4)
	f.assertWireGuardRequestWorks()

	_ = firstLink
	_ = secondLink
}

func wireGuardObjectPeersJSON(t *testing.T, peers []wireGuardPeerConfig) string {
	t.Helper()
	values := make([]map[string]any, 0, len(peers))
	for i, peer := range peers {
		peer.normalize(i)
		values = append(values, map[string]any{
			"id":                          peer.id,
			"publicKey":                   peer.publicKey,
			"endpoint":                    peer.endpoint,
			"allowedIPs":                  peer.allowedIPs,
			"persistentKeepaliveInterval": peer.persistentKeepaliveInterval,
		})
	}
	data, err := json.Marshal(map[string]any{"peers": values})
	if err != nil {
		t.Fatalf("marshal WireGuard object peers: %v", err)
	}
	return string(data)
}

func waitWireGuardPeerCounts(t *testing.T, e *httpE2E, node uint64, want int) {
	t.Helper()
	wantText := fmt.Sprint(want)
	Eventually(t, func() bool {
		return e.wireGuardStatusValue(node, "peers") == wantText &&
			e.wireGuardStatusValue(node, "runtimePeers") == wantText
	}, "WireGuard node %d effective and runtime peer count %d", node, want)
}

func TestWireGuardE2EAlternativeBatchSize(t *testing.T) {
	f := setupWireGuardE2EWithBatchSize(t, defaultWireGuardE2ENames("batch-"), 1)
	f.assertWireGuardRequestWorks()
	f.assertServerLog(f.server, "GET /")
}

func TestWireGuardE2EOverSocksClientServer(t *testing.T) {
	for _, tc := range []struct {
		name  string
		query string
	}{
		{name: "udp assoc", query: ""},
		{name: "gost udp tun", query: "?gost"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := setupWireGuardOverSocksE2E(t, tc.query)
			f.assertWireGuardRequestWorks()
			f.assertServerLog(f.server, "GET /")
		})
	}
}

func TestWireGuardE2EOverSocksSymmetricGost(t *testing.T) {
	f := setupWireGuardSymmetricOverSocksE2E(t)
	f.assertWireGuardRequestWorks()
	f.assertServerLog(f.server, "GET /")
}

func TestWireGuardE2ECopyPaste(t *testing.T) {
	f := setupWireGuardE2E(t, defaultWireGuardE2ENames(""))
	f.assertWireGuardRequestWorks()

	clip := f.Engine.Copy(E2EWorkspace, []uint64{
		f.mock,
		f.clientWG,
		f.serverWG,
		f.clientVTun,
		f.serverVTun,
	})
	if clip == "" {
		t.Fatal("Copy returned empty clipboard")
	}
	pasted := f.Engine.Paste(E2EWorkspace, clip)
	if len(pasted) != 5 {
		t.Fatalf("Paste returned %d nodes (%v), want 5", len(pasted), pasted)
	}
	mock := pasted[0]
	clientWG := pasted[1]
	serverWG := pasted[2]
	clientVTun := pasted[3]
	serverVTun := pasted[4]
	f.assertNodesLinked(clientWG, clientVTun)
	f.assertNodesLinked(serverWG, serverVTun)
	f.assertNodesLinked(mock, clientWG)
	f.assertNodesLinked(mock, serverWG)
	f.SubscribeMenus(clientWG, serverWG)
	pastedClientPublic := waitWireGuardPublicKeyValue(t, f.NodeE2E, clientWG)
	pastedServerPublic := waitWireGuardPublicKeyValue(t, f.NodeE2E, serverWG)
	applyWireGuardPeers(t, f.Engine, clientWG, []wireGuardPeerConfig{{
		id:                          "server",
		publicKey:                   pastedServerPublic,
		endpoint:                    fmt.Sprintf("127.0.0.1:%d", f.serverWGPort),
		allowedIPs:                  wireGuardE2EServerAddr + "/32",
		persistentKeepaliveInterval: 1,
	}})
	applyWireGuardPeers(t, f.Engine, serverWG, []wireGuardPeerConfig{{
		id:                          "client",
		publicKey:                   pastedClientPublic,
		endpoint:                    fmt.Sprintf("127.0.0.1:%d", f.clientWGPort),
		allowedIPs:                  wireGuardE2EClientAddr + "/32",
		persistentKeepaliveInterval: 1,
	}})

	port := httpTestPort()
	server := f.addHTTPServerOnHost("pasted-server", wireGuardE2EServerAddr, port)
	client := f.addHTTPClientToURL("pasted-client", fmt.Sprintf("http://%s:%d/", wireGuardE2EServerAddr, port))
	f.SubscribeMenus(clientWG, serverWG, clientVTun, serverVTun, server, client)
	f.waitClientRequestField(client, "url", fmt.Sprintf("http://%s:%d/", wireGuardE2EServerAddr, port))
	f.connectByPortName(clientVTun, "left", "Network", client, "right", "Network")
	f.connectByPortName(serverVTun, "left", "Network", server, "right", "Network")
	f.WaitNodeLabel(clientWG, "up")
	f.WaitNodeLabel(serverWG, "up")
	f.waitServerListening(server, fmt.Sprintf("%s:%d", wireGuardE2EServerAddr, port))
	f.waitClientStableReady(client)
	f.assertWireGuardClientRequestWorks(client)
}

func TestWireGuardE2ESaveRestoreLoadingOrders(t *testing.T) {
	for _, tc := range []struct {
		name  string
		names wireGuardE2ENames
	}{
		{
			name: "wireguard names before attached nodes",
			names: wireGuardE2ENames{
				mock:       "mid-mock",
				clientWG:   "aaa-client-wg",
				serverWG:   "aab-server-wg",
				clientVTun: "client-vtun",
				serverVTun: "server-vtun",
				server:     "server",
				client:     "client",
			},
		},
		{
			name: "wireguard names after attached nodes",
			names: wireGuardE2ENames{
				mock:       "aaa-mock",
				clientWG:   "zzy-client-wg",
				serverWG:   "zzz-server-wg",
				clientVTun: "client-vtun",
				serverVTun: "server-vtun",
				server:     "server",
				client:     "client",
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := setupWireGuardE2E(t, tc.names)
			f.assertWireGuardRequestWorks()
			f.Engine.RemoveNode(E2EWorkspace, f.client)
			f.Engine.RemoveNode(E2EWorkspace, f.server)

			cfg := configer.NewMemory(nil)
			if err := f.Engine.SaveConfig(cfg); err != nil {
				t.Fatalf("SaveConfig: %v", err)
			}
			if err := f.Engine.Close(); err != nil {
				t.Fatalf("close saved engine: %v", err)
			}

			restored := newNetworkRouterConfigE2EWithSecrets(t, cfg, f.Secrets)
			mock := restored.mustNodeByName(tc.names.mock)
			clientWG := restored.mustNodeByName(tc.names.clientWG)
			serverWG := restored.mustNodeByName(tc.names.serverWG)
			clientVTun := restored.mustNodeByName(tc.names.clientVTun)
			serverVTun := restored.mustNodeByName(tc.names.serverVTun)
			restored.SubscribeMenus(clientWG, serverWG, clientVTun, serverVTun)

			restored.mustTypedLink(TypeTun, clientWG, "left", "Tun", clientVTun, "right", "Tun")
			restored.mustTypedLink(TypeTun, serverWG, "left", "Tun", serverVTun, "right", "Tun")
			restored.mustNetworkLink(mock, "left", "Network", clientWG, "right", "Network")
			restored.mustNetworkLink(mock, "left", "Network", serverWG, "right", "Network")
			assertWireGuardPeerField(t, restored.NodeE2E, clientWG, "allowedIPs", wireGuardE2EServerAddr+"/32")
			assertWireGuardPeerField(t, restored.NodeE2E, serverWG, "allowedIPs", wireGuardE2EClientAddr+"/32")

			port := httpTestPort()
			server := restored.addHTTPServerOnHost("restored-"+tc.names.server, wireGuardE2EServerAddr, port)
			client := restored.addHTTPClientToURL("restored-"+tc.names.client, fmt.Sprintf("http://%s:%d/", wireGuardE2EServerAddr, port))
			restored.SubscribeMenus(server, client)
			restored.connectByPortName(clientVTun, "left", "Network", client, "right", "Network")
			restored.connectByPortName(serverVTun, "left", "Network", server, "right", "Network")
			restored.WaitNodeLabel(clientWG, "up")
			restored.WaitNodeLabel(serverWG, "up")
			restored.waitServerListening(server, fmt.Sprintf("%s:%d", wireGuardE2EServerAddr, port))
			restored.waitClientRequestField(client, "url", fmt.Sprintf("http://%s:%d/", wireGuardE2EServerAddr, port))
			restored.waitClientStableReady(client)
			restored.assertWireGuardClientRequestWorks(client)
		})
	}
}

func TestWireGuardE2ESendsTunConfigToAttachedTun(t *testing.T) {
	e, recorders := newWireGuardTunConfigE2E(t)
	wg := e.AddNode(NodeTypeWireGuard, "wg")
	recorder := e.AddNode(testTunConfigRecorderNodeType, "recorder")
	privateKey, _ := wireGuardTestKeyPair(t, 91)
	_, peerPublicKey := wireGuardTestKeyPair(t, 92)
	applyWireGuardConfigWithOwnedIPs(t, e.Engine, wg, privateKey, 0, "10.251.0.2/32\nfd00:251::2/128", []wireGuardPeerConfig{{
		publicKey:  peerPublicKey,
		allowedIPs: "10.252.0.0/24",
	}})

	e.MustAddTypedLinkByPortName(TypeTun, wg, "left", "Tun", recorder, "right", "Tun")
	recorders.assertLastConfig(t, recorder, TunConfig{
		Addrs:  []string{"10.251.0.2/32", "fd00:251::2/128"},
		Routes: []string{"10.252.0.0/24"},
	})

	applyWireGuardConfigWithOwnedIPs(t, e.Engine, wg, privateKey, 0, "10.251.0.3", []wireGuardPeerConfig{{
		publicKey:  peerPublicKey,
		allowedIPs: "10.253.0.1",
	}})
	recorders.assertLastConfig(t, recorder, TunConfig{
		Addrs:  []string{"10.251.0.3/32"},
		Routes: []string{"10.253.0.1/32"},
	})

	applyWireGuardConfigWithOwnedIPs(t, e.Engine, wg, privateKey, 0, "", nil)
	recorders.assertLastConfig(t, recorder, TunConfig{})
}

func TestAmnesiaOwnedIPsFlowThroughWireGuardToTun(t *testing.T) {
	e, _ := newWireGuardTunConfigE2E(t)
	wg := e.AddNode(NodeTypeWireGuard, "wg")
	amnesia := e.AddNode(amnesianodes.NodeTypeAmnesia, "amnesia")
	vtun := e.AddNode(NodeTypeVTun, "vtun")
	network := e.AddNode(NodeTypeMockNetwork, "network")
	e.SubscribeMenus(amnesia, vtun)
	privateKey, _ := wireGuardTestKeyPair(t, 93)
	_, peerPublicKey := wireGuardTestKeyPair(t, 94)
	applyWireGuardConfigWithOwnedIPs(t, e.Engine, wg, privateKey, 0, "10.93.0.2/32", nil)

	const connectionSecret = "amnesia-native-profile"
	e.Secrets.SetSecret(connectionSecret, Secret{Value: fmt.Sprintf(`[Interface]
PrivateKey = %s
Address = 10.94.0.2/32
DNS = 8.8.8.8

[Peer]
PublicKey = %s
Endpoint = 127.0.0.1:51820
AllowedIPs = 0.0.0.0/0
`, privateKey, peerPublicKey)}, "amnesia")
	e.Engine.SendNodeFormularMsg(E2EWorkspace, amnesia, formular.FormApplyMessage{
		MessageBase: FormMessageBase(formular.MessageFormApply, amnesia),
		BlockID:     "config",
		Values: map[string]any{
			"enabled":          true,
			"connectionSecret": connectionSecret,
		},
	})

	e.MustAddTypedLinkByPortName(TypeTun, wg, "left", "Tun", vtun, "right", "Tun")
	e.MustAddTypedLinkByPortName(TypeNetwork, network, "left", "Network", wg, "right", "Network")
	ctrlLink := e.MustAddTypedLinkByPortName(TypeWGCtrl, wg, "left", "WGCtrl", amnesia, "right", "WireGuard")
	waitAmnesiaUp(t, e, amnesia)
	waitVTunEffectiveLocalAddrs(t, e, vtun, "10.93.0.2\n10.94.0.2")

	e.Engine.SendNodeFormularMsg(E2EWorkspace, amnesia, formular.FormApplyMessage{
		MessageBase: FormMessageBase(formular.MessageFormApply, amnesia),
		BlockID:     "config",
		Values:      map[string]any{"enabled": false},
	})
	waitVTunEffectiveLocalAddrs(t, e, vtun, "10.93.0.2")

	e.Engine.SendNodeFormularMsg(E2EWorkspace, amnesia, formular.FormApplyMessage{
		MessageBase: FormMessageBase(formular.MessageFormApply, amnesia),
		BlockID:     "config",
		Values:      map[string]any{"enabled": true},
	})
	waitVTunEffectiveLocalAddrs(t, e, vtun, "10.93.0.2\n10.94.0.2")

	e.Engine.RemoveLink(E2EWorkspace, ctrlLink)
	waitVTunEffectiveLocalAddrs(t, e, vtun, "10.93.0.2")
}

func TestAmnesiaOwnedIPEnablesVTunHTTPRequest(t *testing.T) {
	e := newHTTPE2E(t)
	clientWGPort := httpTestPort()
	serverWGPort := httpTestPort()
	httpPort := httpTestPort()
	clientPrivate, clientPublic := wireGuardTestKeyPair(t, 95)
	serverPrivate, serverPublic := wireGuardTestKeyPair(t, 96)

	mock := e.AddNode(NodeTypeMockNetwork, "amnesia-http-network")
	clientWG := e.AddNode(NodeTypeWireGuard, "amnesia-http-client-wg")
	serverWG := e.AddNode(NodeTypeWireGuard, "amnesia-http-server-wg")
	amnesia := e.AddNode(amnesianodes.NodeTypeAmnesia, "amnesia-http-controller")
	clientVTun := e.AddNode(NodeTypeVTun, "amnesia-http-client-vtun")
	serverVTun := e.AddNode(NodeTypeVTun, "amnesia-http-server-vtun")
	server := e.addHTTPServerOnHost("amnesia-http-server", wireGuardE2EServerAddr, httpPort)
	client := e.addHTTPClientToURL("amnesia-http-client", fmt.Sprintf("http://%s:%d/", wireGuardE2EServerAddr, httpPort))
	e.SubscribeMenus(clientWG, serverWG, amnesia, clientVTun, serverVTun, server, client)

	e.applyTunCopyVTunConfig(clientVTun, "amnesia-http-client-vtun", "")
	e.applyTunCopyVTunConfig(serverVTun, "amnesia-http-server-vtun", wireGuardE2EServerAddr+"/24")
	applyWireGuardConfig(t, e.Engine, clientWG, clientPrivate, clientWGPort, nil)
	applyWireGuardConfig(t, e.Engine, serverWG, serverPrivate, serverWGPort, []wireGuardPeerConfig{{
		id:                          "client",
		publicKey:                   clientPublic,
		endpoint:                    fmt.Sprintf("127.0.0.1:%d", clientWGPort),
		allowedIPs:                  wireGuardE2EClientAddr + "/32",
		persistentKeepaliveInterval: 1,
	}})

	const connectionSecret = "amnesia-http-native-profile"
	e.Secrets.SetSecret(connectionSecret, Secret{Value: fmt.Sprintf(`[Interface]
PrivateKey = %s
Address = %s/32

[Peer]
PublicKey = %s
Endpoint = 127.0.0.1:%d
AllowedIPs = %s/32
PersistentKeepalive = 1
`, clientPrivate, wireGuardE2EClientAddr, serverPublic, serverWGPort, wireGuardE2EServerAddr)}, "amnesia-http-controller")
	e.Engine.SendNodeFormularMsg(E2EWorkspace, amnesia, formular.FormApplyMessage{
		MessageBase: FormMessageBase(formular.MessageFormApply, amnesia),
		BlockID:     "config",
		Values: map[string]any{
			"enabled":          true,
			"connectionSecret": connectionSecret,
		},
	})

	e.connectTunByPortName(clientWG, "left", "Tun", clientVTun, "right", "Tun")
	e.connectTunByPortName(serverWG, "left", "Tun", serverVTun, "right", "Tun")
	e.connectByPortName(mock, "left", "Network", clientWG, "right", "Network")
	e.connectByPortName(mock, "left", "Network", serverWG, "right", "Network")
	e.MustAddTypedLinkByPortName(TypeWGCtrl, clientWG, "left", "WGCtrl", amnesia, "right", "WireGuard")
	e.connectByPortName(clientVTun, "left", "Network", client, "right", "Network")
	e.connectByPortName(serverVTun, "left", "Network", server, "right", "Network")

	waitAmnesiaUp(t, e.NodeE2E, amnesia)
	waitVTunEffectiveLocalAddrs(t, e.NodeE2E, clientVTun, wireGuardE2EClientAddr)
	e.waitWireGuardUp(clientWG)
	e.waitWireGuardUp(serverWG)
	e.waitServerListening(server, fmt.Sprintf("%s:%d", wireGuardE2EServerAddr, httpPort))
	e.waitClientStableReady(client)
	e.assertWireGuardClientRequestWorks(client)
}

func waitAmnesiaUp(t *testing.T, e *NodeE2E, node uint64) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		snapshot, ok := e.Engine.NodeSnapshot(E2EWorkspace, node)
		if ok && snapshot.Label == "up" {
			return
		}
		time.Sleep(time.Millisecond)
	}
	snapshot, _ := e.Engine.NodeSnapshot(E2EWorkspace, node)
	state, _ := e.MenuItem(node, "status", "state")
	errItem, _ := e.MenuItem(node, "status", "error")
	wg, _ := e.MenuItem(node, "status", "wireGuard")
	t.Fatalf("Amnesia %d label=%q state=%#v WireGuard=%#v error=%#v", node, snapshot.Label, state.Value, wg.Value, errItem.Value)
}

func waitVTunEffectiveLocalAddrs(t *testing.T, e *NodeE2E, node uint64, want string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		item, ok := e.MenuItem(node, "effective", "localAddrs")
		if ok && item.Value == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	item, ok := e.MenuItem(node, "effective", "localAddrs")
	t.Fatalf("VTun %d effective local addresses = %#v (present=%t), want %q", node, item.Value, ok, want)
}

func setupWireGuardE2E(t *testing.T, names wireGuardE2ENames) wireGuardE2EFixture {
	t.Helper()
	return setupWireGuardE2EWithBatchSize(t, names, 0)
}

func setupWireGuardE2EWithBatchSize(t *testing.T, names wireGuardE2ENames, batchSize int) wireGuardE2EFixture {
	t.Helper()
	names = completeWireGuardE2ENames(names)
	e := newHTTPE2E(t)
	f := wireGuardE2EFixture{
		httpE2E:      e,
		names:        names,
		clientWGPort: httpTestPort(),
		serverWGPort: httpTestPort(),
		httpPort:     httpTestPort(),
	}
	clientPrivate, clientPublic := wireGuardTestKeyPair(t, 41)
	serverPrivate, serverPublic := wireGuardTestKeyPair(t, 73)

	f.mock = e.AddNode(NodeTypeMockNetwork, names.mock)
	f.clientWG = e.AddNode(NodeTypeWireGuard, names.clientWG)
	f.serverWG = e.AddNode(NodeTypeWireGuard, names.serverWG)
	f.clientVTun = e.AddNode(NodeTypeVTun, names.clientVTun)
	f.serverVTun = e.AddNode(NodeTypeVTun, names.serverVTun)
	f.server = e.addHTTPServerOnHost(names.server, wireGuardE2EServerAddr, f.httpPort)
	f.client = e.addHTTPClientToURL(names.client, fmt.Sprintf("http://%s:%d/", wireGuardE2EServerAddr, f.httpPort))
	e.SubscribeMenus(f.clientWG, f.serverWG, f.clientVTun, f.serverVTun, f.server, f.client)
	e.waitClientRequestField(f.client, "url", fmt.Sprintf("http://%s:%d/", wireGuardE2EServerAddr, f.httpPort))

	e.applyTunCopyVTunConfig(f.clientVTun, "wireguard-client-vtun", wireGuardE2EClientAddr+"/24")
	e.applyTunCopyVTunConfig(f.serverVTun, "wireguard-server-vtun", wireGuardE2EServerAddr+"/24")
	applyWireGuardConfigWithBatchAndOwnedIPs(t, e.Engine, f.clientWG, clientPrivate, f.clientWGPort, batchSize, "", []wireGuardPeerConfig{{
		id:                          "server",
		publicKey:                   serverPublic,
		endpoint:                    fmt.Sprintf("127.0.0.1:%d", f.serverWGPort),
		allowedIPs:                  wireGuardE2EServerAddr + "/32",
		persistentKeepaliveInterval: 1,
	}})
	applyWireGuardConfigWithBatchAndOwnedIPs(t, e.Engine, f.serverWG, serverPrivate, f.serverWGPort, batchSize, "", []wireGuardPeerConfig{{
		id:                          "client",
		publicKey:                   clientPublic,
		endpoint:                    fmt.Sprintf("127.0.0.1:%d", f.clientWGPort),
		allowedIPs:                  wireGuardE2EClientAddr + "/32",
		persistentKeepaliveInterval: 1,
	}})

	f.clientWGTunLink = e.connectTunByPortName(f.clientWG, "left", "Tun", f.clientVTun, "right", "Tun")
	e.connectTunByPortName(f.serverWG, "left", "Tun", f.serverVTun, "right", "Tun")
	f.clientWGNetLink = e.connectByPortName(f.mock, "left", "Network", f.clientWG, "right", "Network")
	e.connectByPortName(f.mock, "left", "Network", f.serverWG, "right", "Network")
	e.connectByPortName(f.clientVTun, "left", "Network", f.client, "right", "Network")
	e.connectByPortName(f.serverVTun, "left", "Network", f.server, "right", "Network")

	e.waitWireGuardUp(f.clientWG)
	e.waitWireGuardUp(f.serverWG)
	e.WaitNodeLabel(f.clientVTun, "wireguard-client-vtun")
	e.WaitNodeLabel(f.serverVTun, "wireguard-server-vtun")
	e.waitServerListening(f.server, fmt.Sprintf("%s:%d", wireGuardE2EServerAddr, f.httpPort))
	return f
}

func setupWireGuardOverSocksE2E(t *testing.T, socksQuery string) wireGuardE2EFixture {
	t.Helper()
	names := defaultWireGuardE2ENames("socks-")
	e := newHTTPE2E(t)
	f := wireGuardE2EFixture{
		httpE2E:      e,
		names:        names,
		clientWGPort: httpTestPort(),
		serverWGPort: httpTestPort(),
		httpPort:     httpTestPort(),
	}
	socksPort := httpTestPort()
	clientPrivate, clientPublic := wireGuardTestKeyPair(t, 41)
	serverPrivate, serverPublic := wireGuardTestKeyPair(t, 73)

	f.mock = e.AddNode(NodeTypeMockNetwork, names.mock)
	clientOut := e.AddNode(NodeTypeOutNetwork, "socks-client-out")
	listenOut := e.AddNode(NodeTypeOutNetwork, "socks-listen-out")
	socksURL := fmt.Sprintf("socks5://127.0.0.1:%d%s", socksPort, socksQuery)
	url := e.addStringConstant("socks-url", socksURL)
	socksClient := e.AddNode(NodeTypeSocksClient, "wireguard-socks-client")
	socksServer := e.addSocksServer("wireguard-socks-server", socksPort, nil)
	f.clientWG = e.AddNode(NodeTypeWireGuard, names.clientWG)
	f.serverWG = e.AddNode(NodeTypeWireGuard, names.serverWG)
	f.clientVTun = e.AddNode(NodeTypeVTun, names.clientVTun)
	f.serverVTun = e.AddNode(NodeTypeVTun, names.serverVTun)
	f.server = e.addHTTPServerOnHost(names.server, wireGuardE2EServerAddr, f.httpPort)
	f.client = e.addHTTPClientToURL(names.client, fmt.Sprintf("http://%s:%d/", wireGuardE2EServerAddr, f.httpPort))
	e.SubscribeMenus(url, socksClient, socksServer, f.clientWG, f.serverWG, f.clientVTun, f.serverVTun, f.server, f.client)
	e.waitClientRequestField(f.client, "url", fmt.Sprintf("http://%s:%d/", wireGuardE2EServerAddr, f.httpPort))

	e.applyTunCopyVTunConfig(f.clientVTun, "wireguard-client-vtun", wireGuardE2EClientAddr+"/24")
	e.applyTunCopyVTunConfig(f.serverVTun, "wireguard-server-vtun", wireGuardE2EServerAddr+"/24")
	applyWireGuardConfig(t, e.Engine, f.clientWG, clientPrivate, f.clientWGPort, []wireGuardPeerConfig{{
		id:                          "server",
		publicKey:                   serverPublic,
		endpoint:                    fmt.Sprintf("127.0.0.1:%d", f.serverWGPort),
		allowedIPs:                  wireGuardE2EServerAddr + "/32",
		persistentKeepaliveInterval: 1,
	}})
	applyWireGuardConfig(t, e.Engine, f.serverWG, serverPrivate, f.serverWGPort, []wireGuardPeerConfig{{
		id:                          "client",
		publicKey:                   clientPublic,
		allowedIPs:                  wireGuardE2EClientAddr + "/32",
		persistentKeepaliveInterval: 1,
	}})

	e.MustAddTypedLinkByPortName(std.TypeString, url, "right", "output", socksClient, "left", "Proxy URL")
	f.clientWGTunLink = e.connectTunByPortName(f.clientWG, "left", "Tun", f.clientVTun, "right", "Tun")
	e.connectTunByPortName(f.serverWG, "left", "Tun", f.serverVTun, "right", "Tun")
	e.connectByPortName(f.mock, "left", "Network", socksServer, "right", "proxy")
	e.connectByPortName(f.mock, "left", "Network", f.serverWG, "right", "Network")
	e.connectByPortName(listenOut, "left", "Network", socksServer, "right", "listen")
	e.waitSocksLog(socksServer, fmt.Sprintf("socks listening on 127.0.0.1:%d", socksPort))
	e.connectByPortName(clientOut, "left", "Network", socksClient, "right", "out")
	e.WaitNodeLabel(socksClient, socksURL)
	f.clientWGNetLink = e.connectByPortName(socksClient, "left", "Network", f.clientWG, "right", "Network")
	e.connectByPortName(f.clientVTun, "left", "Network", f.client, "right", "Network")
	e.connectByPortName(f.serverVTun, "left", "Network", f.server, "right", "Network")

	e.waitWireGuardUp(f.clientWG)
	e.waitWireGuardUp(f.serverWG)
	e.WaitNodeLabel(f.clientVTun, "wireguard-client-vtun")
	e.WaitNodeLabel(f.serverVTun, "wireguard-server-vtun")
	e.waitServerListening(f.server, fmt.Sprintf("%s:%d", wireGuardE2EServerAddr, f.httpPort))
	return f
}

func setupWireGuardSymmetricOverSocksE2E(t *testing.T) wireGuardE2EFixture {
	t.Helper()
	names := defaultWireGuardE2ENames("symmetric-socks-")
	e := newHTTPE2E(t)
	f := wireGuardE2EFixture{
		httpE2E:      e,
		names:        names,
		clientWGPort: httpTestPort(),
		serverWGPort: httpTestPort(),
		httpPort:     httpTestPort(),
	}
	socksPort := httpTestPort()
	clientPrivate, clientPublic := wireGuardTestKeyPair(t, 41)
	serverPrivate, serverPublic := wireGuardTestKeyPair(t, 73)

	f.mock = e.AddNode(NodeTypeMockNetwork, names.mock)
	clientOut := e.AddNode(NodeTypeOutNetwork, "symmetric-socks-client-out")
	listenOut := e.AddNode(NodeTypeOutNetwork, "symmetric-socks-listen-out")
	socksURL := fmt.Sprintf("socks5://127.0.0.1:%d?gost", socksPort)
	url := e.addStringConstant("symmetric-socks-url", socksURL)
	clientSocks := e.AddNode(NodeTypeSocksClient, "wireguard-client-socks")
	serverSocks := e.AddNode(NodeTypeSocksClient, "wireguard-server-socks")
	socksServer := e.addSocksServer("wireguard-shared-socks-server", socksPort, nil)
	f.clientWG = e.AddNode(NodeTypeWireGuard, names.clientWG)
	f.serverWG = e.AddNode(NodeTypeWireGuard, names.serverWG)
	f.clientVTun = e.AddNode(NodeTypeVTun, names.clientVTun)
	f.serverVTun = e.AddNode(NodeTypeVTun, names.serverVTun)
	f.server = e.addHTTPServerOnHost(names.server, wireGuardE2EServerAddr, f.httpPort)
	f.client = e.addHTTPClientToURL(names.client, fmt.Sprintf("http://%s:%d/", wireGuardE2EServerAddr, f.httpPort))
	e.SubscribeMenus(url, clientSocks, serverSocks, socksServer, f.clientWG, f.serverWG, f.clientVTun, f.serverVTun, f.server, f.client)
	e.waitClientRequestField(f.client, "url", fmt.Sprintf("http://%s:%d/", wireGuardE2EServerAddr, f.httpPort))

	e.applyTunCopyVTunConfig(f.clientVTun, "wireguard-client-vtun", wireGuardE2EClientAddr+"/24")
	e.applyTunCopyVTunConfig(f.serverVTun, "wireguard-server-vtun", wireGuardE2EServerAddr+"/24")
	applyWireGuardConfig(t, e.Engine, f.clientWG, clientPrivate, f.clientWGPort, []wireGuardPeerConfig{{
		id:                          "server",
		publicKey:                   serverPublic,
		endpoint:                    fmt.Sprintf("127.0.0.1:%d", f.serverWGPort),
		allowedIPs:                  wireGuardE2EServerAddr + "/32",
		persistentKeepaliveInterval: 1,
	}})
	applyWireGuardConfig(t, e.Engine, f.serverWG, serverPrivate, f.serverWGPort, []wireGuardPeerConfig{{
		id:                          "client",
		publicKey:                   clientPublic,
		endpoint:                    fmt.Sprintf("127.0.0.1:%d", f.clientWGPort),
		allowedIPs:                  wireGuardE2EClientAddr + "/32",
		persistentKeepaliveInterval: 1,
	}})

	e.MustAddTypedLinkByPortName(std.TypeString, url, "right", "output", clientSocks, "left", "Proxy URL")
	e.MustAddTypedLinkByPortName(std.TypeString, url, "right", "output", serverSocks, "left", "Proxy URL")
	f.clientWGTunLink = e.connectTunByPortName(f.clientWG, "left", "Tun", f.clientVTun, "right", "Tun")
	e.connectTunByPortName(f.serverWG, "left", "Tun", f.serverVTun, "right", "Tun")
	e.connectByPortName(clientOut, "left", "Network", clientSocks, "right", "out")
	e.connectByPortName(clientOut, "left", "Network", serverSocks, "right", "out")
	e.WaitNodeLabel(clientSocks, socksURL)
	e.WaitNodeLabel(serverSocks, socksURL)
	f.clientWGNetLink = e.connectByPortName(clientSocks, "left", "Network", f.clientWG, "right", "Network")
	e.connectByPortName(serverSocks, "left", "Network", f.serverWG, "right", "Network")
	e.connectByPortName(f.clientVTun, "left", "Network", f.client, "right", "Network")
	e.connectByPortName(f.serverVTun, "left", "Network", f.server, "right", "Network")

	e.connectByPortName(f.mock, "left", "Network", socksServer, "right", "proxy")
	e.connectByPortName(listenOut, "left", "Network", socksServer, "right", "listen")
	e.waitSocksLog(socksServer, fmt.Sprintf("socks listening on 127.0.0.1:%d", socksPort))

	e.waitWireGuardUp(f.clientWG)
	e.waitWireGuardUp(f.serverWG)
	e.WaitNodeLabel(f.clientVTun, "wireguard-client-vtun")
	e.WaitNodeLabel(f.serverVTun, "wireguard-server-vtun")
	e.waitServerListening(f.server, fmt.Sprintf("%s:%d", wireGuardE2EServerAddr, f.httpPort))
	return f
}

func newWireGuardTunConfigE2E(t *testing.T) (*NodeE2E, *tunConfigRecorderRegistry) {
	t.Helper()

	pool := newE2ETestDebugPool(t)
	logf := NewDebugLogFactory(16384)
	sys := newE2EDebugSystem()
	secrets := newE2ESecretsStorage()
	recorders := newTunConfigRecorderRegistry()
	engine, err := BuildEngine(
		context.Background(),
		configer.NewMemory(nil),
		logf,
		sys,
		nil,
		nil, secrets,
		nil,

		[]pasta.NodeClass{tunConfigRecorderClass{registry: recorders}},
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

	return NewNodeE2EFromEngine(t, engine, logf, sys, secrets), recorders
}

func (r *tunConfigRecorderRegistry) assertLastConfig(t *testing.T, id uint64, want TunConfig) {
	t.Helper()
	var got TunConfig
	Eventually(t, func() bool {
		r.mu.Lock()
		node := r.nodes[id]
		r.mu.Unlock()
		if node == nil {
			return false
		}
		node.mu.Lock()
		defer node.mu.Unlock()
		if len(node.configs) == 0 {
			return false
		}
		got = node.configs[len(node.configs)-1]
		return reflect.DeepEqual(got, want)
	}, "WireGuard TunConfig for recorder %d = %#v", id, want)
}

func (e *httpE2E) waitWireGuardUp(node uint64) {
	e.T.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		snapshot, ok := e.Engine.NodeSnapshot(E2EWorkspace, node)
		if ok && snapshot.Label == "up" {
			return
		}
		if time.Now().After(deadline) {
			e.T.Fatalf(
				"timed out waiting for WireGuard node %d up; label=%q state=%q error=%q tun=%q network=%q listenPort=%q",
				node,
				snapshot.Label,
				e.wireGuardStatusValue(node, "state"),
				e.wireGuardStatusValue(node, "error"),
				e.wireGuardStatusValue(node, "tun"),
				e.wireGuardStatusValue(node, "network"),
				e.wireGuardStatusValue(node, "listenPort"),
			)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (e *httpE2E) wireGuardStatusValue(node uint64, field string) string {
	item, ok := e.MenuItem(node, "status", field)
	if !ok {
		return ""
	}
	if value, ok := formular.StringValue(item.Value); ok {
		return value
	}
	return fmt.Sprint(item.Value)
}

func (f wireGuardE2EFixture) assertWireGuardRequestWorks() {
	f.T.Helper()
	f.assertWireGuardClientRequestWorks(f.client)
}

func (e *httpE2E) waitClientStableReady(client uint64) {
	e.T.Helper()
	deadline := time.Now().Add(2 * time.Second)
	var stableSince time.Time
	for {
		ready := e.clientStatus(client) == "Status: ready" &&
			e.clientResultField(client, "error") == "" &&
			e.clientResultField(client, "response") == ""
		if ready {
			if stableSince.IsZero() {
				stableSince = time.Now()
			}
			if time.Since(stableSince) >= 100*time.Millisecond {
				return
			}
		} else {
			stableSince = time.Time{}
		}
		if time.Now().After(deadline) {
			e.T.Fatalf(
				"timed out waiting for stable client ready; got status=%q error=%q response=%q",
				e.clientStatus(client),
				e.clientResultField(client, "error"),
				e.clientResultField(client, "response"),
			)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (e *httpE2E) assertWireGuardClientRequestWorks(client uint64) {
	e.T.Helper()
	e.assertClientRequestReturns(client, "Debug http server response", 90*time.Second)
}

func defaultWireGuardE2ENames(prefix string) wireGuardE2ENames {
	return completeWireGuardE2ENames(wireGuardE2ENames{
		mock:       prefix + "mock",
		clientWG:   prefix + "client-wg",
		serverWG:   prefix + "server-wg",
		clientVTun: prefix + "client-vtun",
		serverVTun: prefix + "server-vtun",
		server:     prefix + "server",
		client:     prefix + "client",
	})
}

func completeWireGuardE2ENames(names wireGuardE2ENames) wireGuardE2ENames {
	if names.mock == "" {
		names.mock = "mock"
	}
	if names.clientWG == "" {
		names.clientWG = "client-wg"
	}
	if names.serverWG == "" {
		names.serverWG = "server-wg"
	}
	if names.clientVTun == "" {
		names.clientVTun = "client-vtun"
	}
	if names.serverVTun == "" {
		names.serverVTun = "server-vtun"
	}
	if names.server == "" {
		names.server = "server"
	}
	if names.client == "" {
		names.client = "client"
	}
	return names
}
