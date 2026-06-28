package almagest

import (
	"context"
	"strconv"
	"testing"

	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/gonnect/sysnet/debug"
	"github.com/asciimoth/pasta/pasta/std"
)

func TestDebugLogNodesAvailableOnlyInDebugEngines(t *testing.T) {
	pool := newE2ETestDebugPool(t)

	plain, err := BuildEngine(context.Background(), configer.NewMemory(nil), NewDebugLogFactory(16), &sysnetdebug.System{}, nil, pool, false)
	if err != nil {
		t.Fatalf("BuildEngine plain: %v", err)
	}
	t.Cleanup(func() { _ = plain.Close() })
	if _, ok := plain.NodeClassSnapshot(E2EWorkspace, NodeTypeDebugLogNetwork); ok {
		t.Fatalf("%s is available in non-debug engine", NodeTypeDebugLogNetwork)
	}
	if _, ok := plain.NodeClassSnapshot(E2EWorkspace, NodeTypeDebugLogTun); ok {
		t.Fatalf("%s is available in non-debug engine", NodeTypeDebugLogTun)
	}

	debugEngine, err := BuildEngine(context.Background(), configer.NewMemory(nil), NewDebugLogFactory(16), &sysnetdebug.System{}, nil, pool, true)
	if err != nil {
		t.Fatalf("BuildEngine debug: %v", err)
	}
	t.Cleanup(func() { _ = debugEngine.Close() })
	if _, ok := debugEngine.NodeClassSnapshot(E2EWorkspace, NodeTypeDebugLogNetwork); !ok {
		t.Fatalf("%s is missing from debug engine", NodeTypeDebugLogNetwork)
	}
	if _, ok := debugEngine.NodeClassSnapshot(E2EWorkspace, NodeTypeDebugLogTun); !ok {
		t.Fatalf("%s is missing from debug engine", NodeTypeDebugLogTun)
	}
}

func TestDebugLogNetworkE2EPassthroughDisconnectReconnect(t *testing.T) {
	e := newHTTPE2E(t)
	port := httpTestPort()
	out := e.AddNode(NodeTypeOutNetwork, "out")
	logNetwork := e.AddNode(NodeTypeDebugLogNetwork, "log-network")
	server := e.addHTTPServer("server", port)
	client := e.addHTTPClient("client", port)
	e.SubscribeMenus(server, client)
	e.waitClientRequestField(client, "url", e.clientURLForPort(port))

	upstream := e.connectByPortName(out, "left", "Network", logNetwork, "right", "Network")
	serverLink := e.connectByPortName(logNetwork, "left", "Network", server, "right", "Network")
	clientLink := e.connectByPortName(logNetwork, "left", "Network", client, "right", "Network")

	e.waitServerListening(server, "127.0.0.1:"+itoa(port))
	e.assertClientRequestWorks(client)
	e.AssertDebugLog(NodeTypeDebugLogNetwork, "Dial")
	e.AssertDebugLog(NodeTypeDebugLogNetwork, "GET / HTTP/1.1")
	e.AssertDebugLog(NodeTypeDebugLogNetwork, "Debug http server response")

	e.Engine.RemoveLink(E2EWorkspace, clientLink)
	e.sendClientRequest(client)
	e.waitClientFailure(client, "network is not ready")
	clientLink = e.connectByPortName(logNetwork, "left", "Network", client, "right", "Network")
	e.assertClientRequestWorks(client)

	e.Engine.RemoveLink(E2EWorkspace, upstream)
	e.waitServerLog(server, "waiting for network")
	upstream = e.connectByPortName(out, "left", "Network", logNetwork, "right", "Network")
	e.waitServerListening(server, "127.0.0.1:"+itoa(port))
	e.assertClientRequestWorks(client)

	e.Engine.RemoveLink(E2EWorkspace, serverLink)
	e.waitServerLog(server, "waiting for network")
	serverLink = e.connectByPortName(logNetwork, "left", "Network", server, "right", "Network")
	e.waitServerListening(server, "127.0.0.1:"+itoa(port))
	e.assertClientRequestWorks(client)

	_ = upstream
	_ = serverLink
	_ = clientLink
}

func TestDebugLogTunE2EPassthroughDisconnectReconnect(t *testing.T) {
	e := newHTTPE2E(t)
	port := httpTestPort()
	vtun := e.AddNode(NodeTypeVTun, "vtun")
	logTun := e.AddNode(NodeTypeDebugLogTun, "log-tun")
	spoofer := e.AddNode(NodeTypeSpoofer, "spoofer")
	mock := e.AddNode(NodeTypeMockNetwork, "mock")
	response := e.addStringConstant("server-response", vtunSpooferResponse)
	server := e.addHTTPServerOnHost("server", vtunSpooferServerHost, port)
	client := e.addHTTPClientToURL("client", "http://"+vtunSpooferServerHost+":"+itoa(port)+"/")
	e.SubscribeMenus(vtun, spoofer, response, server, client)
	e.waitClientRequestField(client, "url", "http://"+vtunSpooferServerHost+":"+itoa(port)+"/")

	e.MustAddTypedLinkByPortName(std.TypeString, response, "right", "output", server, "left", "Response")
	logVTun := e.connectTunByPortName(logTun, "left", "in", vtun, "right", "Tun")
	spooferLog := e.connectTunByPortName(spoofer, "left", "Tun", logTun, "right", "Tun")
	e.connectByPortName(mock, "left", "Network", spoofer, "right", "Network")
	e.connectByPortName(mock, "left", "Network", server, "right", "Network")
	e.connectByPortName(vtun, "left", "Network", client, "right", "Network")

	waitDebugLogTunReady(e, vtun, spoofer, server, port)
	e.assertClientRequestWorksWithResponse(client, vtunSpooferResponse)
	e.AssertDebugLog(NodeTypeDebugLogTun, "tun.Read")
	e.AssertDebugLog(NodeTypeDebugLogTun, "tun.Write")

	e.Engine.RemoveLink(E2EWorkspace, spooferLog)
	e.WaitNodeLabel(spoofer, "waiting for tun")
	spooferLog = e.connectTunByPortName(spoofer, "left", "Tun", logTun, "right", "Tun")
	waitDebugLogTunReady(e, vtun, spoofer, server, port)
	e.assertClientRequestWorksWithResponse(client, vtunSpooferResponse)

	e.Engine.RemoveLink(E2EWorkspace, logVTun)
	logVTun = e.connectTunByPortName(logTun, "left", "in", vtun, "right", "Tun")
	waitDebugLogTunReady(e, vtun, spoofer, server, port)
	e.assertClientRequestWorksWithResponse(client, vtunSpooferResponse)

	_ = spooferLog
	_ = logVTun
}

func waitDebugLogTunReady(e *httpE2E, vtun, spoofer, server uint64, port int) {
	e.T.Helper()
	e.WaitNodeLabel(vtun, "vtun")
	e.WaitNodeLabel(spoofer, "forwarding")
	e.waitServerListening(server, vtunSpooferServerHost+":"+itoa(port))
}

func (e *httpE2E) clientURLForPort(port int) string {
	return "http://127.0.0.1:" + itoa(port) + "/"
}

func itoa(v int) string {
	return strconv.Itoa(v)
}
