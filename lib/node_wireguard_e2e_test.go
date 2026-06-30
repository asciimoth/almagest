package almagest

import (
	"context"
	"fmt"
	"reflect"
	"testing"
	"time"

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

			restored := newNetworkRouterConfigE2E(t, cfg)
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

func setupWireGuardE2E(t *testing.T, names wireGuardE2ENames) wireGuardE2EFixture {
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
	logf := NewDebugLogFactory(4096)
	sys := newE2EDebugSystem()
	recorders := newTunConfigRecorderRegistry()
	engine, err := BuildEngine(
		context.Background(),
		configer.NewMemory(nil),
		logf,
		sys,
		[]pasta.NodeClass{tunConfigRecorderClass{registry: recorders}},
		pool,
		true,
	)
	if err != nil {
		t.Fatalf("BuildEngine error = %v, want nil", err)
	}
	t.Cleanup(func() {
		if err := engine.Close(); err != nil {
			t.Errorf("engine.Close error = %v, want nil", err)
		}
	})

	e := &NodeE2E{T: t, Engine: engine, LogFactory: logf, DebugSystem: sys, menus: formular.NewMenuSnapshotState()}
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
	return e, recorders
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
	deadline := time.Now().Add(35 * time.Second)
	for {
		if e.clientStatus(client) != "Status: running..." {
			e.sendClientRequest(client)
		}

		requestDeadline := time.Now().Add(20 * time.Second)
		for {
			if e.clientStatus(client) == "Status: 200 OK" &&
				e.clientResultField(client, "error") == "" &&
				e.clientResultField(client, "response") == "Debug http server response" {
				e.AssertNodeLabel(client, "200 OK")
				return
			}
			if e.clientStatus(client) != "Status: running..." {
				break
			}
			if time.Now().After(requestDeadline) {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		if time.Now().After(deadline) {
			e.T.Fatalf(
				"timed out waiting for wireguard client response; got status=%q error=%q response=%q",
				e.clientStatus(client),
				e.clientResultField(client, "error"),
				e.clientResultField(client, "response"),
			)
		}
		time.Sleep(10 * time.Millisecond)
	}
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
