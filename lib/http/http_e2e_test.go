package httpnodes_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/asciimoth/almagest/lib/testkit/httpe2etest"
	"github.com/asciimoth/almagest/lib/testkit/sockstest"
	"github.com/asciimoth/pasta/pasta/std"
	"github.com/asciimoth/socksgo/protocol"
)

type httpE2E struct {
	*NodeE2E
	*httpe2etest.HTTPOps
}

func TestHTTPClientServerE2EOutNetwork(t *testing.T) {
	e := newHTTPE2E(t)
	port := httpTestPort()
	out := e.AddNode(NodeTypeOutNetwork, "out")
	server := e.addHTTPServer("server", port)
	client := e.addHTTPClient("client", port)
	e.SubscribeMenus(server, client)
	e.waitClientRequestField(client, "url", fmt.Sprintf("http://127.0.0.1:%d/", port))

	serverLink := e.connectByPortName(out, "left", "Network", server, "right", "Network")
	clientLink := e.connectByPortName(out, "left", "Network", client, "right", "Network")

	e.waitServerListening(server, fmt.Sprintf("127.0.0.1:%d", port))
	e.sendClientRequest(client)
	e.waitClientResponse(client, "200 OK", "Debug http server response")
	e.AssertNodeLabel(client, "200 OK")
	e.AssertDebugLog(NodeTypeHTTPClient, "complete: 200 OK")
	e.AssertDebugLog(NodeTypeHTTPServer, "GET /")
	e.assertServerLog(server, "GET /")

	e.Engine.RemoveLink(E2EWorkspace, clientLink)
	e.waitClientReady(client)
	e.sendClientRequest(client)
	e.waitClientFailure(client, "network is not ready")
	e.AssertDebugLog(NodeTypeHTTPClient, "failed: network is not ready")

	e.mustAddLinkByPorts(e.LeftPort(out, "Network"), e.RightPort(client, "Network"))
	e.waitClientReady(client)
	e.sendClientRequest(client)
	e.waitClientResponse(client, "200 OK", "Debug http server response")

	e.Engine.RemoveLink(E2EWorkspace, serverLink)
	e.waitServerLog(server, "waiting for network")
	e.sendClientRequest(client)
	e.waitClientFailure(client, "connect")
}

func TestHTTPClientServerE2ESeparateOutNetworkNodesShareNetwork(t *testing.T) {
	e := newHTTPE2E(t)
	port := httpTestPort()
	serverOut := e.AddNode(NodeTypeOutNetwork, "server-out")
	clientOut := e.AddNode(NodeTypeOutNetwork, "client-out")
	server := e.addHTTPServer("server", port)
	client := e.addHTTPClient("client", port)
	e.SubscribeMenus(server, client)
	e.waitClientRequestField(client, "url", fmt.Sprintf("http://127.0.0.1:%d/", port))

	e.connectByPortName(serverOut, "left", "Network", server, "right", "Network")
	e.connectByPortName(clientOut, "left", "Network", client, "right", "Network")

	e.waitServerListening(server, fmt.Sprintf("127.0.0.1:%d", port))
	e.sendClientRequest(client)
	e.waitClientResponse(client, "200 OK", "Debug http server response")
	e.AssertNodeLabel(server, fmt.Sprintf("127.0.0.1:%d", port))
	e.AssertNodeLabel(client, "200 OK")
}

func TestHTTPClientSocksClientServerE2E(t *testing.T) {
	e := newHTTPE2E(t)
	socksPort := httpTestPort()
	httpPort := httpTestPort()
	proxyURL := fmt.Sprintf("socks5://127.0.0.1:%d", socksPort)

	clientOut := e.AddNode(NodeTypeOutNetwork, "client-out")
	listenOut := e.AddNode(NodeTypeOutNetwork, "listen-out")
	mock := e.AddNode(NodeTypeMockNetwork, "mock")
	url := e.addStringConstant("socks-url", proxyURL)
	socksClient := e.AddNode(NodeTypeSocksClient, "socks-client")
	httpClient := e.addHTTPClient("http-client", httpPort)
	socksServer := e.addSocksServer("socks-server", socksPort, nil)
	httpServer := e.addHTTPServer("http-server", httpPort)
	e.SubscribeMenus(url, socksClient, httpClient, socksServer, httpServer)
	e.waitClientRequestField(httpClient, "url", fmt.Sprintf("http://127.0.0.1:%d/", httpPort))

	links := []struct {
		name      string
		add       func() uint64
		afterAdd  func()
		failPart  string
		currentID uint64
	}{
		{
			name: "HTTP client to SOCKS client",
			add: func() uint64 {
				return e.connectByPortName(socksClient, "left", "Network", httpClient, "right", "Network")
			},
			failPart: "network is not ready",
		},
		{
			name: "SOCKS client to OutNetwork",
			add: func() uint64 {
				return e.connectByPortName(clientOut, "left", "Network", socksClient, "right", "out")
			},
		},
		{
			name: "SOCKS server listen to OutNetwork",
			add: func() uint64 {
				return e.connectByPortName(listenOut, "left", "Network", socksServer, "right", "listen")
			},
			afterAdd: func() {
				e.waitSocksLog(socksServer, fmt.Sprintf("socks listening on 127.0.0.1:%d", socksPort))
			},
		},
		{
			name: "SOCKS server proxy to MockNetwork",
			add: func() uint64 {
				return e.connectByPortName(mock, "left", "Network", socksServer, "right", "proxy")
			},
		},
		{
			name: "HTTP server to MockNetwork",
			add: func() uint64 {
				return e.connectByPortName(mock, "left", "Network", httpServer, "right", "Network")
			},
			afterAdd: func() {
				e.waitServerListening(httpServer, fmt.Sprintf("127.0.0.1:%d", httpPort))
			},
		},
	}

	e.MustAddTypedLinkByPortName(std.TypeString, url, "right", "output", socksClient, "left", "Proxy URL")
	for i := range links {
		links[i].currentID = links[i].add()
		if links[i].afterAdd != nil {
			links[i].afterAdd()
		}
	}

	e.WaitNodeLabel(socksClient, proxyURL)
	e.waitServerListening(httpServer, fmt.Sprintf("127.0.0.1:%d", httpPort))
	e.waitSocksLog(socksServer, fmt.Sprintf("socks listening on 127.0.0.1:%d", socksPort))
	e.assertClientRequestWorks(httpClient)
	e.assertServerLog(httpServer, "GET /")

	for i := range links {
		t.Logf("checking removal and restore of %s link", links[i].name)
		e.Engine.RemoveLink(E2EWorkspace, links[i].currentID)
		e.sendClientRequest(httpClient)
		if links[i].failPart != "" {
			e.waitClientFailure(httpClient, links[i].failPart)
		} else {
			e.waitClientAnyFailure(httpClient)
		}

		links[i].currentID = links[i].add()
		if links[i].afterAdd != nil {
			links[i].afterAdd()
		}
		e.assertClientRequestWorks(httpClient)
	}
}

func TestHTTPServerThroughSocksClientE2E(t *testing.T) {
	e := newHTTPE2E(t)
	socksPort := httpTestPort()
	httpPort := httpTestPort()
	proxyURL := fmt.Sprintf("socks5://127.0.0.1:%d", socksPort)

	mock := e.AddNode(NodeTypeMockNetwork, "mock")
	serverOut := e.AddNode(NodeTypeOutNetwork, "server-out")
	listenOut := e.AddNode(NodeTypeOutNetwork, "listen-out")
	url := e.addStringConstant("socks-url", proxyURL)
	socksClient := e.AddNode(NodeTypeSocksClient, "socks-client")
	httpServer := e.addHTTPServer("http-server", httpPort)
	httpClient := e.addHTTPClient("http-client", httpPort)
	socksServer := e.addSocksServer("socks-server", socksPort, nil)
	e.SubscribeMenus(url, socksClient, httpClient, socksServer, httpServer)
	e.waitClientRequestField(httpClient, "url", fmt.Sprintf("http://127.0.0.1:%d/", httpPort))

	e.MustAddTypedLinkByPortName(std.TypeString, url, "right", "output", socksClient, "left", "Proxy URL")
	httpServerSocksLink := e.connectByPortName(socksClient, "left", "Network", httpServer, "right", "Network")
	e.connectByPortName(serverOut, "left", "Network", socksClient, "right", "out")
	e.connectByPortName(listenOut, "left", "Network", socksServer, "right", "listen")
	e.connectByPortName(mock, "left", "Network", socksServer, "right", "proxy")
	e.connectByPortName(mock, "left", "Network", httpClient, "right", "Network")

	e.WaitNodeLabel(socksClient, proxyURL)
	e.waitSocksLog(socksServer, fmt.Sprintf("socks listening on 127.0.0.1:%d", socksPort))
	e.waitServerListening(httpServer, fmt.Sprintf("127.0.0.1:%d", httpPort))
	e.assertClientRequestWorks(httpClient)

	e.Engine.RemoveLink(E2EWorkspace, httpServerSocksLink)
	e.waitServerLog(httpServer, "waiting for network")

	httpServerSocksLink = e.connectByPortName(socksClient, "left", "Network", httpServer, "right", "Network")
	e.waitServerListening(httpServer, fmt.Sprintf("127.0.0.1:%d", httpPort))
	e.assertClientRequestWorks(httpClient)

	e.applySocksServerCommands(socksServer, socksPort, map[protocol.Cmd]bool{
		protocol.CmdBind: false,
	})
	e.waitSocksLog(socksServer, "worker restarting")
	e.applySocksServerCommands(socksServer, socksPort, map[protocol.Cmd]bool{
		protocol.CmdBind: true,
	})
	e.waitSocksLog(socksServer, fmt.Sprintf("socks listening on 127.0.0.1:%d", socksPort))
	e.waitServerListening(httpServer, fmt.Sprintf("127.0.0.1:%d", httpPort))
	e.assertClientRequestWorks(httpClient)

	e.Engine.RemoveLink(E2EWorkspace, httpServerSocksLink)
	httpServerSocksLink = e.connectByPortName(socksClient, "left", "Network", httpServer, "right", "Network")
	e.waitServerListening(httpServer, fmt.Sprintf("127.0.0.1:%d", httpPort))
	e.assertClientRequestWorks(httpClient)
	_ = httpServerSocksLink
}

func TestHTTPServerThroughSocksClientRecoversFromRepeatedSocksListenerRestarts(t *testing.T) {
	e := newHTTPE2E(t)
	socksPort := httpTestPort()
	httpPort := httpTestPort()
	_, httpClient, socksServer := e.setupHTTPServerThroughSocksClient(
		fmt.Sprintf("socks5://127.0.0.1:%d", socksPort),
		httpPort,
	)
	e.assertClientRequestWorks(httpClient)

	const rounds = 10
	listenLog := fmt.Sprintf("socks listening on 127.0.0.1:%d", socksPort)
	for round := range rounds {
		t.Logf("SOCKS listener restart round %d/%d", round+1, rounds)
		restartsBefore := e.SocksLogCount(socksServer, "worker restarting")
		e.applySocksServerCommands(socksServer, socksPort, map[protocol.Cmd]bool{
			protocol.CmdBind: false,
		})
		e.waitSocksLogCountAbove(socksServer, "worker restarting", restartsBefore)

		listensBefore := e.SocksLogCount(socksServer, listenLog)
		e.applySocksServerCommands(socksServer, socksPort, map[protocol.Cmd]bool{
			protocol.CmdBind: true,
		})
		e.waitSocksLogCountAbove(socksServer, listenLog, listensBefore)
		// The server log is capped. Counting identical "listening" entries
		// stops detecting restarts when a new entry evicts an old match. A
		// successful fresh request proves that the BIND listener recovered.
		e.assertClientRequestWorks(httpClient)
	}
}

func TestHTTPServerThroughPlainSocksBindDoesNotStopBeforeFirstAccept(t *testing.T) {
	e := newHTTPE2E(t)
	httpServer, _, socksServer := e.setupHTTPServerThroughSocksClient(
		fmt.Sprintf("socks5://127.0.0.1:%d", httpTestPort()),
		httpTestPort(),
	)

	// Regression coverage for the daemon loop:
	// HttpServer.Listen goes through SocksClient.Listen, which uses plain
	// SOCKS BIND without "?gost". Plain BIND is one-shot after an inbound
	// connection, but it must stay open while idle. The broken behavior was an
	// immediate "server stopped: EOF" before any HTTP client connected, causing
	// the daemon to reconnect to the SOCKS server forever.
	e.assertServerNoNewLogFor(httpServer, "server stopped:", 750*time.Millisecond)
	e.assertSocksNoNewLogFor(socksServer, "closed: accept", 750*time.Millisecond)
}

func TestHTTPServerThroughPlainSocksBindStopsOnlyAfterFirstAcceptedConnection(t *testing.T) {
	e := newHTTPE2E(t)
	httpServer, httpClient, _ := e.setupHTTPServerThroughSocksClient(
		fmt.Sprintf("socks5://127.0.0.1:%d", httpTestPort()),
		httpTestPort(),
	)
	e.assertServerNoNewLogFor(httpServer, "server stopped:", 250*time.Millisecond)
	stoppedBefore := e.serverLogCount(httpServer, "server stopped:")
	listeningBefore := e.serverLogCount(httpServer, "listening on ")

	// Standard SOCKS BIND is expected to accept only one incoming connection.
	// After that first request is served, the HTTP server should observe EOF
	// and restart its listener. This documents the expected failure point so
	// the idle EOF regression above cannot be hidden by the restart loop.
	e.assertClientRequestWorks(httpClient)
	Eventually(t, func() bool {
		return e.serverLogCount(httpServer, "server stopped:") > stoppedBefore
	}, "plain BIND listener stops after first accepted request")
	Eventually(t, func() bool {
		return e.serverLogCount(httpServer, "listening on ") > listeningBefore
	}, "plain BIND HTTP server listener restarts after first accepted request")

	stoppedAfterRestart := e.serverLogCount(httpServer, "server stopped:")
	e.assertServerNoNewLogFor(httpServer, "server stopped:", 750*time.Millisecond)
	if got := e.serverLogCount(httpServer, "server stopped:"); got != stoppedAfterRestart {
		t.Fatalf("server stopped log count = %d, want %d after idle restart", got, stoppedAfterRestart)
	}
}

func TestHTTPClientServerE2EMockNetwork(t *testing.T) {
	e := newHTTPE2E(t)
	port := httpTestPort()
	mock := e.AddNode(NodeTypeMockNetwork, "mock")
	server := e.addHTTPServer("server", port)
	client := e.addHTTPClient("client", port)
	e.SubscribeMenus(server, client)
	e.waitClientRequestField(client, "url", fmt.Sprintf("http://127.0.0.1:%d/", port))

	e.connectByPortName(mock, "left", "Network", server, "right", "Network")
	e.connectByPortName(mock, "left", "Network", client, "right", "Network")

	e.waitServerListening(server, fmt.Sprintf("127.0.0.1:%d", port))
	// Link creation can return while another goroutine drains workspace work.
	// Retry until the mock network payload has reached the client instead of
	// letting the first button press overtake that payload.
	e.assertClientRequestWorks(client)
	e.assertServerLog(server, "GET /")
}

func newHTTPE2E(t *testing.T) *httpE2E {
	t.Helper()
	return newHTTPE2EFromNodeE2E(NewNodeE2E(t))
}

func newHTTPE2EFromNodeE2E(e *NodeE2E) *httpE2E {
	return &httpE2E{NodeE2E: e, HTTPOps: httpe2etest.NewHTTPOps(e)}
}

func (e *httpE2E) setupHTTPServerThroughSocksClient(proxyURL string, httpPort int) (uint64, uint64, uint64) {
	return e.SetupHTTPServerThroughSocksClient(proxyURL, httpPort)
}

func (e *httpE2E) addHTTPServer(name string, port int) uint64 {
	return e.AddHTTPServer(name, port)
}

func (e *httpE2E) addHTTPClient(name string, port int) uint64 {
	return e.AddHTTPClient(name, port)
}

func (e *httpE2E) addSocksServer(name string, port int, users []sockstest.User) uint64 {
	return e.AddSocksServer(name, port, users)
}

func (e *httpE2E) addStringConstant(name, value string) uint64 {
	return e.AddStringConstant(name, value)
}

func (e *httpE2E) applySocksServerCommands(socks uint64, port int, overrides map[protocol.Cmd]bool) {
	e.ApplySocksServerCommands(socks, port, overrides)
}

func (e *httpE2E) connectByPortName(leftNode uint64, leftDir, leftName string, rightNode uint64, rightDir, rightName string) uint64 {
	return e.ConnectByPortName(leftNode, leftDir, leftName, rightNode, rightDir, rightName)
}

func (e *httpE2E) mustAddLinkByPorts(a, b uint64) uint64 {
	return e.MustAddNetworkLinkByPorts(a, b)
}

func (e *httpE2E) sendClientRequest(client uint64) {
	e.SendClientRequest(client)
}

func (e *httpE2E) waitServerListening(server uint64, address string) {
	e.WaitServerListening(server, address)
}

func (e *httpE2E) waitClientResponse(client uint64, status, response string) {
	e.WaitClientResponse(client, status, response)
}

func (e *httpE2E) assertClientRequestWorks(client uint64) {
	e.AssertClientRequestWorks(client)
}

func (e *httpE2E) waitClientFailure(client uint64, wantErrPart string) {
	e.WaitClientFailure(client, wantErrPart)
}

func (e *httpE2E) waitClientAnyFailure(client uint64) {
	e.WaitClientAnyFailure(client)
}

func (e *httpE2E) waitClientReady(client uint64) {
	e.WaitClientReady(client)
}

func (e *httpE2E) waitClientRequestField(client uint64, field, want string) {
	e.WaitClientRequestField(client, field, want)
}

func (e *httpE2E) waitServerLog(server uint64, wantPart string) {
	e.WaitServerLog(server, wantPart)
}

func (e *httpE2E) waitSocksLog(socks uint64, wantPart string) {
	e.WaitSocksLog(socks, wantPart)
}

func (e *httpE2E) waitSocksLogCountAbove(socks uint64, wantPart string, previous int) {
	e.WaitSocksLogCountAbove(socks, wantPart, previous)
}

func (e *httpE2E) assertServerLog(server uint64, wantPart string) {
	e.AssertServerLog(server, wantPart)
}

func (e *httpE2E) assertServerNoNewLogFor(server uint64, wantPart string, duration time.Duration) {
	e.AssertServerNoNewLogFor(server, wantPart, duration)
}

func (e *httpE2E) serverLogCount(server uint64, wantPart string) int {
	return e.ServerLogCount(server, wantPart)
}

func (e *httpE2E) assertSocksNoNewLogFor(socks uint64, wantPart string, duration time.Duration) {
	e.AssertSocksNoNewLogFor(socks, wantPart, duration)
}
