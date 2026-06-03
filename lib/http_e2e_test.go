package lib

import (
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/asciimoth/formular"
	"github.com/asciimoth/pasta/pasta/std"
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
	Eventually(e.T, func() bool {
		return e.clientStatus(client) == "Status: "+status &&
			e.clientResultField(client, "error") == "" &&
			e.clientResultField(client, "response") == response
	}, "client response %q %q", status, response)
}

func (e *httpE2E) waitClientFailure(client uint64, wantErrPart string) {
	e.T.Helper()
	Eventually(e.T, func() bool {
		return e.clientStatus(client) == "Status: fail" &&
			strings.Contains(e.clientResultField(client, "error"), wantErrPart)
	}, "client failure containing %q", wantErrPart)
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
