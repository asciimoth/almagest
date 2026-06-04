package lib

import (
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/asciimoth/formular"
	"github.com/asciimoth/pasta/pasta/std"
	"github.com/asciimoth/socksgo/protocol"
)

var nextHTTPTestPort atomic.Uint32

func httpTestPort() int {
	base := uint32(20000 + os.Getpid()%20000)
	return int(base + nextHTTPTestPort.Add(1))
}

type httpE2E struct {
	*NodeE2E
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
	e.sendClientRequest(client)
	e.waitClientResponse(client, "200 OK", "Debug http server response")
	e.assertServerLog(server, "GET /")
}

func newHTTPE2E(t *testing.T) *httpE2E {
	t.Helper()
	return &httpE2E{NodeE2E: NewNodeE2E(t)}
}

func (e *httpE2E) addHTTPServer(name string, port int) uint64 {
	e.T.Helper()
	id := e.AddNode(NodeTypeHTTPServer, name)
	e.Engine.SendNodeFormularMsg(E2EWorkspace, id, formular.FormApplyMessage{
		MessageBase: FormMessageBase(formular.MessageFormApply, id),
		BlockID:     "listen",
		Values: map[string]any{
			"host": "127.0.0.1",
			"port": port,
		},
	})
	e.WaitNodeLabel(id, fmt.Sprintf("127.0.0.1:%d", port))
	return id
}

func (e *httpE2E) addHTTPClient(name string, port int) uint64 {
	e.T.Helper()
	id := e.AddNode(NodeTypeHTTPClient, name)
	e.Engine.SendNodeFormularMsg(E2EWorkspace, id, formular.FieldUpdateMessage{
		MessageBase: FormMessageBase(formular.MessageFieldUpdate, id),
		Field:       formular.FieldRef{BlockID: "request", FieldID: "url"},
		Value:       fmt.Sprintf("http://127.0.0.1:%d/", port),
	})
	return id
}

func (e *httpE2E) addSocksServer(name string, port int, users []socksServerUser) uint64 {
	e.T.Helper()
	id := e.AddNode(NodeTypeSocksServer, name)
	values := map[string]any{
		"socksHost": "127.0.0.1",
		"socksPort": port,
		"wsHost":    "",
		"wsPort":    0,
		"wsPath":    "/socks",
	}
	if users != nil {
		values["users"] = socksServerUserValues(users)
	}
	e.Engine.SendNodeFormularMsg(E2EWorkspace, id, formular.FormApplyMessage{
		MessageBase: FormMessageBase(formular.MessageFormApply, id),
		BlockID:     "config",
		Values:      values,
	})
	e.WaitNodeLabel(id, fmt.Sprintf("127.0.0.1:%d", port))
	return id
}

func (e *httpE2E) addStringConstant(name, value string) uint64 {
	e.T.Helper()
	id := e.AddNode(std.NodeTypeStringConstant, name)
	e.Engine.SendNodeFormularMsg(E2EWorkspace, id, formular.FieldUpdateMessage{
		MessageBase: FormMessageBase(formular.MessageFieldUpdate, id),
		Field:       formular.FieldRef{BlockID: "state", FieldID: "value"},
		Value:       value,
	})
	e.WaitNodeLabel(id, value)
	return id
}

func (e *httpE2E) applySocksServerCommands(socks uint64, port int, overrides map[protocol.Cmd]bool) {
	e.T.Helper()
	commands := defaultSocksServerCommands()
	for cmd, value := range overrides {
		commands[cmd] = value
	}
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
	e.Engine.SendNodeFormularMsg(E2EWorkspace, socks, formular.FormApplyMessage{
		MessageBase: FormMessageBase(formular.MessageFormApply, socks),
		BlockID:     "config",
		Values:      values,
	})
}

func (e *httpE2E) connectByPortName(leftNode uint64, leftDir, leftName string, rightNode uint64, rightDir, rightName string) uint64 {
	e.T.Helper()
	return e.MustAddTypedLinkByPortName(TypeNetwork, leftNode, leftDir, leftName, rightNode, rightDir, rightName)
}

func (e *httpE2E) mustAddLinkByPorts(a, b uint64) uint64 {
	e.T.Helper()
	return e.MustAddTypedLinkByPorts(TypeNetwork, a, b)
}

func (e *httpE2E) sendClientRequest(client uint64) {
	e.T.Helper()
	e.Engine.SendNodeFormularMsg(E2EWorkspace, client, formular.ButtonPressMessage{
		MessageBase: FormMessageBase(formular.MessageButtonPress, client),
		BlockID:     "request",
		ButtonID:    "send",
	})
}

func (e *httpE2E) waitServerListening(server uint64, address string) {
	e.T.Helper()
	e.waitServerLog(server, "listening on "+address)
	e.AssertDebugLog(NodeTypeHTTPServer, "listening on "+address)
}

func (e *httpE2E) waitClientResponse(client uint64, status, response string) {
	e.T.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		gotStatus := e.clientStatus(client)
		gotErr := e.clientResultField(client, "error")
		gotResp := e.clientResultField(client, "response")
		if gotStatus == "Status: "+status && gotErr == "" && gotResp == response {
			return
		}
		if time.Now().After(deadline) {
			e.T.Fatalf(
				"timed out waiting for client response %q %q; got status=%q error=%q response=%q",
				status,
				response,
				gotStatus,
				gotErr,
				gotResp,
			)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (e *httpE2E) assertClientRequestWorks(client uint64) {
	e.T.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if e.clientStatus(client) != "Status: running..." {
			e.sendClientRequest(client)
		}
		Eventually(e.T, func() bool {
			return e.clientStatus(client) != "Status: running..."
		}, "client request finished")
		if e.clientStatus(client) == "Status: 200 OK" &&
			e.clientResultField(client, "error") == "" &&
			e.clientResultField(client, "response") == "Debug http server response" {
			e.AssertNodeLabel(client, "200 OK")
			return
		}
		if time.Now().After(deadline) {
			e.T.Fatalf(
				"timed out waiting for retried client response; got status=%q error=%q response=%q",
				e.clientStatus(client),
				e.clientResultField(client, "error"),
				e.clientResultField(client, "response"),
			)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (e *httpE2E) waitClientFailure(client uint64, wantErrPart string) {
	e.T.Helper()
	Eventually(e.T, func() bool {
		return e.clientStatus(client) == "Status: fail" &&
			strings.Contains(e.clientResultField(client, "error"), wantErrPart)
	}, "client failure containing %q", wantErrPart)
	e.AssertNodeLabel(client, "fail")
}

func (e *httpE2E) waitClientAnyFailure(client uint64) {
	e.T.Helper()
	Eventually(e.T, func() bool {
		return e.clientStatus(client) == "Status: fail" &&
			e.clientResultField(client, "error") != ""
	}, "client failure with a non-empty error")
	e.AssertNodeLabel(client, "fail")
}

func (e *httpE2E) waitClientReady(client uint64) {
	e.T.Helper()
	Eventually(e.T, func() bool {
		return e.clientStatus(client) == "Status: ready" &&
			e.clientResultField(client, "error") == "" &&
			e.clientResultField(client, "response") == ""
	}, "client ready")
}

func (e *httpE2E) waitClientRequestField(client uint64, field, want string) {
	e.T.Helper()
	Eventually(e.T, func() bool {
		item, ok := e.MenuItem(client, "request", field)
		return ok && item.Field != nil && item.Value == want
	}, "client request field %s = %q", field, want)
}

func (e *httpE2E) waitServerLog(server uint64, wantPart string) {
	e.T.Helper()
	Eventually(e.T, func() bool {
		return e.serverLogsContain(server, wantPart)
	}, "server log containing %q", wantPart)
}

func (e *httpE2E) waitSocksLog(socks uint64, wantPart string) {
	e.T.Helper()
	Eventually(e.T, func() bool {
		item, ok := e.MenuItem(socks, "logs", "worker")
		if !ok {
			return false
		}
		for _, line := range item.Logs {
			if strings.Contains(line.Text, wantPart) {
				return true
			}
		}
		return false
	}, "socks log containing %q", wantPart)
}

func (e *httpE2E) assertServerLog(server uint64, wantPart string) {
	e.T.Helper()
	if !e.serverLogsContain(server, wantPart) {
		e.T.Fatalf("server logs do not contain %q", wantPart)
	}
}

func (e *httpE2E) serverLogsContain(server uint64, wantPart string) bool {
	item, ok := e.MenuItem(server, "logs", "worker")
	if !ok {
		return false
	}
	for _, line := range item.Logs {
		if strings.Contains(line.Text, wantPart) {
			return true
		}
	}
	return false
}

func (e *httpE2E) clientStatus(client uint64) string {
	item, ok := e.MenuItem(client, "result", "status")
	if !ok {
		return ""
	}
	return item.Text
}

func (e *httpE2E) clientResultField(client uint64, field string) string {
	item, ok := e.MenuItem(client, "result", field)
	if !ok || item.Field == nil {
		return ""
	}
	value, _ := std.StringFromPayload(item.Value)
	return value
}
