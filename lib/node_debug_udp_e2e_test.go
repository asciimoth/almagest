package almagest

import (
	"context"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/formular"
	"github.com/asciimoth/gonnect/sysnet/debug"
	"github.com/asciimoth/pasta/pasta"
	"github.com/asciimoth/socksgo/protocol"
)

func TestUDPPingNodesAvailableOnlyInDebugEngines(t *testing.T) {
	pool := newE2ETestDebugPool(t)

	plain, err := BuildEngine(context.Background(), configer.NewMemory(nil), NewDebugLogFactory(16), &sysnetdebug.System{}, nil, pool, false)
	if err != nil {
		t.Fatalf("BuildEngine plain: %v", err)
	}
	t.Cleanup(func() { _ = plain.Close() })
	if _, ok := plain.NodeClassSnapshot(E2EWorkspace, NodeTypeUDPPingServer); ok {
		t.Fatalf("%s is available in non-debug engine", NodeTypeUDPPingServer)
	}
	if _, ok := plain.NodeClassSnapshot(E2EWorkspace, NodeTypeUDPPingClient); ok {
		t.Fatalf("%s is available in non-debug engine", NodeTypeUDPPingClient)
	}

	debugEngine, err := BuildEngine(context.Background(), configer.NewMemory(nil), NewDebugLogFactory(16), &sysnetdebug.System{}, nil, pool, true)
	if err != nil {
		t.Fatalf("BuildEngine debug: %v", err)
	}
	t.Cleanup(func() { _ = debugEngine.Close() })
	if _, ok := debugEngine.NodeClassSnapshot(E2EWorkspace, NodeTypeUDPPingServer); !ok {
		t.Fatalf("%s is missing from debug engine", NodeTypeUDPPingServer)
	}
	if _, ok := debugEngine.NodeClassSnapshot(E2EWorkspace, NodeTypeUDPPingClient); !ok {
		t.Fatalf("%s is missing from debug engine", NodeTypeUDPPingClient)
	}
}

func TestUDPPingE2EIPv4IPv6SocketModes(t *testing.T) {
	for _, tc := range []struct {
		name       string
		network    string
		host       string
		serverMode string
		clientMode string
	}{
		{name: "ipv4_listen_listen", network: "udp4", host: "127.0.0.1", serverMode: udpSocketListen, clientMode: udpSocketListen},
		{name: "ipv4_listen_dial", network: "udp4", host: "127.0.0.1", serverMode: udpSocketListen, clientMode: udpSocketDial},
		{name: "ipv4_dial_listen", network: "udp4", host: "127.0.0.1", serverMode: udpSocketDial, clientMode: udpSocketListen},
		{name: "ipv4_dial_dial", network: "udp4", host: "127.0.0.1", serverMode: udpSocketDial, clientMode: udpSocketDial},
		{name: "ipv6_listen_listen", network: "udp6", host: "::1", serverMode: udpSocketListen, clientMode: udpSocketListen},
		{name: "ipv6_listen_dial", network: "udp6", host: "::1", serverMode: udpSocketListen, clientMode: udpSocketDial},
		{name: "ipv6_dial_listen", network: "udp6", host: "::1", serverMode: udpSocketDial, clientMode: udpSocketListen},
		{name: "ipv6_dial_dial", network: "udp6", host: "::1", serverMode: udpSocketDial, clientMode: udpSocketDial},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := NewNodeE2E(t)
			out := e.AddNode(NodeTypeOutNetwork, "out")
			server := e.AddNode(NodeTypeUDPPingServer, "udp-server")
			client := e.AddNode(NodeTypeUDPPingClient, "udp-client")
			e.SubscribeMenus(server, client)

			serverPort := httpTestPort()
			clientPort := httpTestPort()
			payload := "ping-" + tc.name
			applyUDPPingServerConfig(e, server, tc.network, tc.serverMode, tc.host, serverPort, tc.host, clientPort)
			applyUDPPingClientConfig(e, client, tc.network, tc.clientMode, tc.host, clientPort, tc.host, serverPort, payload)

			e.MustAddTypedLinkByPortName(TypeNetwork, out, "left", "Network", server, "right", "Network")
			e.MustAddTypedLinkByPortName(TypeNetwork, out, "left", "Network", client, "right", "Network")

			waitUDPPingServerLog(e, server, "listening on")
			sendUDPPing(e, client)
			waitUDPPingClientResponse(e, client, "ok", "pong:"+payload)
			e.AssertNodeLabel(client, "ok")
			e.AssertDebugLog(NodeTypeUDPPingClient, "complete: pong:"+payload)
			e.AssertDebugLog(NodeTypeUDPPingServer, "udp pong")
		})
	}
}

func TestUDPPingE2EMockNetwork(t *testing.T) {
	e := NewNodeE2E(t)
	mock := e.AddNode(NodeTypeMockNetwork, "mock")
	server := e.AddNode(NodeTypeUDPPingServer, "udp-server")
	client := e.AddNode(NodeTypeUDPPingClient, "udp-client")
	e.SubscribeMenus(server, client)

	port := httpTestPort()
	payload := "mock-ping"
	applyUDPPingServerConfig(e, server, "udp4", udpSocketListen, "127.0.0.1", port, "127.0.0.1", 0)
	applyUDPPingClientConfig(e, client, "udp4", udpSocketDial, "127.0.0.1", 0, "127.0.0.1", port, payload)
	e.MustAddTypedLinkByPortName(TypeNetwork, mock, "left", "Network", server, "right", "Network")
	e.MustAddTypedLinkByPortName(TypeNetwork, mock, "left", "Network", client, "right", "Network")

	waitUDPPingServerLog(e, server, "listening on")
	sendUDPPing(e, client)
	waitUDPPingClientResponse(e, client, "ok", "pong:"+payload)
}

func TestUDPPingE2EOverWireGuard(t *testing.T) {
	f := setupWireGuardE2E(t, defaultWireGuardE2ENames("udp-"))
	server := f.AddNode(NodeTypeUDPPingServer, "udp-ping-server")
	client := f.AddNode(NodeTypeUDPPingClient, "udp-ping-client")
	f.SubscribeMenus(server, client)

	port := httpTestPort()
	payload := "wireguard-udp-ping"
	applyUDPPingServerConfig(f.NodeE2E, server, "udp4", udpSocketListen, wireGuardE2EServerAddr, port, wireGuardE2EClientAddr, 0)
	applyUDPPingClientConfig(f.NodeE2E, client, "udp4", udpSocketDial, wireGuardE2EClientAddr, 0, wireGuardE2EServerAddr, port, payload)
	f.connectByPortName(f.serverVTun, "left", "Network", server, "right", "Network")
	f.connectByPortName(f.clientVTun, "left", "Network", client, "right", "Network")

	waitUDPPingServerLog(f.NodeE2E, server, "listening on")
	assertUDPPingClientResponseEventually(f.NodeE2E, client, "pong:"+payload)
	f.AssertNodeLabel(client, "ok")
	f.AssertDebugLog(NodeTypeUDPPingServer, "udp pong")
}

func TestUDPPingE2ESocksClientBehindProxyUDPAssoc(t *testing.T) {
	f := newUDPPingSocksFixture(t, "")
	serverPort := httpTestPort()
	payload := "socks-client-udp-assoc"

	server := f.e.AddNode(NodeTypeUDPPingServer, "udp-server")
	client := f.e.AddNode(NodeTypeUDPPingClient, "udp-client")
	f.e.SubscribeMenus(server, client)
	applyUDPPingServerConfig(f.e, server, "udp4", udpSocketListen, "127.0.0.1", serverPort, "127.0.0.1", 0)
	applyUDPPingClientConfig(f.e, client, "udp4", udpSocketDial, "127.0.0.1", 0, "127.0.0.1", serverPort, payload)
	f.e.MustAddTypedLinkByPortName(TypeNetwork, f.target, "left", "Network", server, "right", "Network")
	f.e.MustAddTypedLinkByPortName(TypeNetwork, f.clientProxy, "left", "Network", client, "right", "Network")

	waitUDPPingServerLog(f.e, server, "listening on")
	sendUDPPing(f.e, client)
	waitUDPPingClientResponse(f.e, client, "ok", "pong:"+payload)
}

func TestUDPPingE2ESocksClientBehindProxyGostUDPTun(t *testing.T) {
	f := newUDPPingSocksFixture(t, "?gost")
	serverPort := httpTestPort()
	payload := "socks-client-gost"

	server := f.e.AddNode(NodeTypeUDPPingServer, "udp-server")
	client := f.e.AddNode(NodeTypeUDPPingClient, "udp-client")
	f.e.SubscribeMenus(server, client)
	applyUDPPingServerConfig(f.e, server, "udp4", udpSocketListen, "127.0.0.1", serverPort, "127.0.0.1", 0)
	applyUDPPingClientConfig(f.e, client, "udp4", udpSocketDial, "127.0.0.1", 0, "127.0.0.1", serverPort, payload)
	f.e.MustAddTypedLinkByPortName(TypeNetwork, f.target, "left", "Network", server, "right", "Network")
	f.e.MustAddTypedLinkByPortName(TypeNetwork, f.clientProxy, "left", "Network", client, "right", "Network")

	waitUDPPingServerLog(f.e, server, "listening on")
	sendUDPPing(f.e, client)
	waitUDPPingClientResponse(f.e, client, "ok", "pong:"+payload)
}

func TestUDPPingE2ESocksServerBehindProxyGostUDPTun(t *testing.T) {
	f := newUDPPingSocksFixture(t, "?gost")
	serverPort := httpTestPort()
	payload := "socks-server-gost"

	server := f.e.AddNode(NodeTypeUDPPingServer, "udp-server")
	client := f.e.AddNode(NodeTypeUDPPingClient, "udp-client")
	f.e.SubscribeMenus(server, client)
	applyUDPPingServerConfig(f.e, server, "udp4", udpSocketListen, "0.0.0.0", serverPort, "127.0.0.1", 0)
	applyUDPPingClientConfig(f.e, client, "udp4", udpSocketDial, "127.0.0.1", 0, "127.0.0.1", serverPort, payload)
	f.e.MustAddTypedLinkByPortName(TypeNetwork, f.clientProxy, "left", "Network", server, "right", "Network")
	f.e.MustAddTypedLinkByPortName(TypeNetwork, f.target, "left", "Network", client, "right", "Network")

	waitUDPPingServerLog(f.e, server, "listening on")
	sendUDPPing(f.e, client)
	waitUDPPingClientResponse(f.e, client, "ok", "pong:"+payload)
}

func TestUDPPingE2ESocksBothBehindProxyGostUDPTun(t *testing.T) {
	f := newUDPPingSocksFixture(t, "?gost")
	serverPort := httpTestPort()
	payload := "socks-both-gost"

	serverProxy := f.clientProxy
	clientProxy := f.addSocksClient("socks-client-2")
	server := f.e.AddNode(NodeTypeUDPPingServer, "udp-server")
	client := f.e.AddNode(NodeTypeUDPPingClient, "udp-client")
	f.e.SubscribeMenus(server, client)
	applyUDPPingServerConfig(f.e, server, "udp4", udpSocketListen, "0.0.0.0", serverPort, "127.0.0.1", 0)
	applyUDPPingClientConfig(f.e, client, "udp4", udpSocketDial, "127.0.0.1", 0, "127.0.0.1", serverPort, payload)
	f.e.MustAddTypedLinkByPortName(TypeNetwork, serverProxy, "left", "Network", server, "right", "Network")
	f.e.MustAddTypedLinkByPortName(TypeNetwork, clientProxy, "left", "Network", client, "right", "Network")

	waitUDPPingServerLog(f.e, server, "listening on")
	sendUDPPing(f.e, client)
	waitUDPPingClientResponse(f.e, client, "ok", "pong:"+payload)
}

func TestUDPPingE2EClientReportsMissingNetwork(t *testing.T) {
	e := NewNodeE2E(t)
	client := e.AddNode(NodeTypeUDPPingClient, "udp-client")
	e.SubscribeMenus(client)
	applyUDPPingClientConfig(e, client, "udp4", udpSocketDial, "127.0.0.1", 0, "127.0.0.1", httpTestPort(), "ping")

	sendUDPPing(e, client)
	waitUDPPingClientFailure(e, client, "network is not ready")
	e.AssertDebugLog(NodeTypeUDPPingClient, "network is not ready")
}

type udpPingSocksFixture struct {
	e           *NodeE2E
	listen      uint64
	target      uint64
	socksServer uint64
	clientProxy uint64
	socksURL    string
}

func newUDPPingSocksFixture(t *testing.T, query string) *udpPingSocksFixture {
	t.Helper()
	e := newUDPPingSocksE2E(t)
	listen := e.AddNode(NodeTypeOutNetwork, "socks-listen-network")
	target := e.AddNode(NodeTypeMockNetwork, "target-network")
	socksServer := e.AddNode(NodeTypeSocksServer, "socks-server")
	socksPort := httpTestPort()
	socksURL := fmt.Sprintf("socks5://127.0.0.1:%d%s", socksPort, query)
	e.SubscribeMenus(socksServer)
	applyUDPPingSocksServerConfig(e, socksServer, socksPort, query == "?gost")
	e.MustAddTypedLinkByPortName(TypeNetwork, listen, "left", "Network", socksServer, "right", "listen")
	e.MustAddTypedLinkByPortName(TypeNetwork, target, "left", "Network", socksServer, "right", "proxy")
	waitSocksServerLog(e, socksServer, fmt.Sprintf("socks listening on 127.0.0.1:%d", socksPort))

	f := &udpPingSocksFixture{
		e:           e,
		listen:      listen,
		target:      target,
		socksServer: socksServer,
		socksURL:    socksURL,
	}
	f.clientProxy = f.addSocksClient("socks-client")
	return f
}

func newUDPPingSocksE2E(t *testing.T) *NodeE2E {
	t.Helper()
	logf := NewDebugLogFactory(16384)
	sys := newE2EDebugSystem()
	engine, err := BuildEngine(
		context.Background(),
		configer.NewMemory(nil),
		logf,
		sys,
		nil,
		nil,
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
	return e
}

func (f *udpPingSocksFixture) addSocksClient(name string) uint64 {
	f.e.T.Helper()
	client := f.e.AddNode(NodeTypeSocksClient, name)
	f.e.SubscribeMenus(client)
	applyUDPPingSocksClientURL(f.e, client, f.socksURL)
	f.e.MustAddTypedLinkByPortName(TypeNetwork, f.listen, "left", "Network", client, "right", "out")
	f.e.WaitNodeLabel(client, f.socksURL)
	return client
}

func applyUDPPingSocksServerConfig(e *NodeE2E, node uint64, port int, gost bool) {
	e.T.Helper()
	commands := defaultSocksServerCommands()
	commands[protocol.CmdUDPAssoc] = !gost
	commands[protocol.CmdGostUDPTun] = gost
	values := map[string]any{
		"socksHost": "127.0.0.1",
		"socksPort": port,
		"wsHost":    "",
		"wsPort":    0,
		"wsPath":    "/socks",
		"users":     []formular.ArrayElementValue{},
	}
	for cmd, field := range socksServerCommandFields() {
		values[field] = commands[cmd]
	}
	e.Engine.SendNodeFormularMsg(E2EWorkspace, node, formular.FormApplyMessage{
		MessageBase: FormMessageBase(formular.MessageFormApply, node),
		BlockID:     "config",
		Values:      values,
	})
	e.WaitNodeLabel(node, fmt.Sprintf("127.0.0.1:%d", port))
}

func applyUDPPingSocksClientURL(e *NodeE2E, node uint64, url string) {
	e.T.Helper()
	e.Engine.SendNodeFormularMsg(E2EWorkspace, node, formular.FieldUpdateMessage{
		MessageBase: FormMessageBase(formular.MessageFieldUpdate, node),
		Field:       formular.FieldRef{BlockID: "config", FieldID: "proxyURL"},
		Value:       url,
	})
}

func applyUDPPingServerConfig(e *NodeE2E, node uint64, network, mode, localHost string, localPort int, remoteHost string, remotePort int) {
	e.T.Helper()
	e.Engine.SendNodeFormularMsg(E2EWorkspace, node, formular.FormApplyMessage{
		MessageBase: FormMessageBase(formular.MessageFormApply, node),
		BlockID:     "config",
		Values: map[string]any{
			"network":     network,
			"socket_mode": mode,
			"local_host":  localHost,
			"local_port":  localPort,
			"remote_host": remoteHost,
			"remote_port": remotePort,
		},
	})
	e.WaitNodeLabel(node, fmt.Sprintf("%s/%s %s", network, mode, netJoinHostPort(localHost, localPort)))
}

func applyUDPPingClientConfig(e *NodeE2E, node uint64, network, mode, localHost string, localPort int, remoteHost string, remotePort int, payload string) {
	e.T.Helper()
	e.Engine.SendNodeFormularMsg(E2EWorkspace, node, formular.FormApplyMessage{
		MessageBase: FormMessageBase(formular.MessageFormApply, node),
		BlockID:     "config",
		Values: map[string]any{
			"network":     network,
			"socket_mode": mode,
			"local_host":  localHost,
			"local_port":  localPort,
			"remote_host": remoteHost,
			"remote_port": remotePort,
			"payload":     payload,
			"timeout_ms":  1000,
		},
	})
	e.WaitNodeLabel(node, udpPingStatusReady)
}

func sendUDPPing(e *NodeE2E, client uint64) {
	e.T.Helper()
	e.Engine.SendNodeFormularMsg(E2EWorkspace, client, formular.ButtonPressMessage{
		MessageBase: FormMessageBase(formular.MessageButtonPress, client),
		BlockID:     "config",
		ButtonID:    "send",
	})
}

func waitUDPPingServerLog(e *NodeE2E, server uint64, wantPart string) {
	e.T.Helper()
	Eventually(e.T, func() bool {
		for _, line := range udpPingLogs(e, server) {
			if strings.Contains(line.Text, wantPart) {
				return true
			}
		}
		return false
	}, "udp server log containing %q", wantPart)
}

func waitSocksServerLog(e *NodeE2E, server uint64, wantPart string) {
	e.T.Helper()
	Eventually(e.T, func() bool {
		for _, line := range udpPingLogs(e, server) {
			if strings.Contains(line.Text, wantPart) {
				return true
			}
		}
		return false
	}, "socks server log containing %q", wantPart)
}

func waitUDPPingClientResponse(e *NodeE2E, client uint64, status, response string) {
	e.T.Helper()
	Eventually(e.T, func() bool {
		return udpPingClientStatus(e, client) == "Status: "+status &&
			udpPingResultField(e, client, "error") == "" &&
			udpPingResultField(e, client, "response") == response
	}, "udp client response %q", response)
}

func assertUDPPingClientResponseEventually(e *NodeE2E, client uint64, response string) {
	e.T.Helper()
	deadline := time.Now().Add(35 * time.Second)
	for {
		if udpPingClientStatus(e, client) != "Status: "+udpPingStatusRunning {
			sendUDPPing(e, client)
		}

		requestDeadline := time.Now().Add(2 * time.Second)
		for {
			if udpPingClientStatus(e, client) == "Status: ok" &&
				udpPingResultField(e, client, "error") == "" &&
				udpPingResultField(e, client, "response") == response {
				e.AssertNodeLabel(client, "ok")
				return
			}
			if udpPingClientStatus(e, client) != "Status: "+udpPingStatusRunning {
				break
			}
			if time.Now().After(requestDeadline) {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		if time.Now().After(deadline) {
			e.T.Fatalf(
				"timed out waiting for udp client response; got status=%q error=%q response=%q",
				udpPingClientStatus(e, client),
				udpPingResultField(e, client, "error"),
				udpPingResultField(e, client, "response"),
			)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func waitUDPPingClientFailure(e *NodeE2E, client uint64, wantErrPart string) {
	e.T.Helper()
	Eventually(e.T, func() bool {
		return udpPingClientStatus(e, client) == "Status: fail" &&
			strings.Contains(udpPingResultField(e, client, "error"), wantErrPart)
	}, "udp client failure containing %q", wantErrPart)
}

func udpPingClientStatus(e *NodeE2E, client uint64) string {
	e.T.Helper()
	block, ok := e.MenuBlock(client, "result")
	if !ok {
		return ""
	}
	item, ok := FindItem(block, "status")
	if !ok {
		return ""
	}
	return item.Text
}

func udpPingResultField(e *NodeE2E, client uint64, field string) string {
	e.T.Helper()
	item, ok := e.MenuItem(client, "result", field)
	if !ok || item.Field == nil {
		return ""
	}
	value, _ := item.Value.(string)
	return value
}

func udpPingLogs(e *NodeE2E, node uint64) []formular.LogLine {
	e.T.Helper()
	item, ok := e.MenuItem(node, "logs", "worker")
	if !ok {
		return nil
	}
	return item.Logs
}

func netJoinHostPort(host string, port int) string {
	return net.JoinHostPort(host, fmt.Sprint(port))
}
