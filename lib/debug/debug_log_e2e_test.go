package debugnodes_test

import (
	"context"
	"strconv"
	"testing"

	almagest "github.com/asciimoth/almagest/lib"
	debugnodes "github.com/asciimoth/almagest/lib/debug"
	dnsnodes "github.com/asciimoth/almagest/lib/dns"
	"github.com/asciimoth/almagest/lib/logging"
	networknodes "github.com/asciimoth/almagest/lib/network"
	"github.com/asciimoth/almagest/lib/testkit/e2etest"
	"github.com/asciimoth/almagest/lib/testkit/httpe2etest"
	tunnodes "github.com/asciimoth/almagest/lib/tun"
	"github.com/asciimoth/configer/configer"
	sysnetdebug "github.com/asciimoth/gonnect/sysnet/debug"
)

type httpE2E struct {
	*e2etest.NodeE2E
	*httpe2etest.HTTPOps
}

func newHTTPE2E(t *testing.T) *httpE2E {
	t.Helper()
	e := e2etest.NewNodeE2E(t)
	return &httpE2E{NodeE2E: e, HTTPOps: httpe2etest.NewHTTPOps(e)}
}

func httpTestPort() int { return e2etest.TestPort() }
func itoa(v int) string { return strconv.Itoa(v) }

func (e *httpE2E) addHTTPServer(name string, port int) uint64 { return e.AddHTTPServer(name, port) }
func (e *httpE2E) addHTTPClient(name string, port int) uint64 { return e.AddHTTPClient(name, port) }
func (e *httpE2E) connectByPortName(leftNode uint64, leftDir, leftName string, rightNode uint64, rightDir, rightName string) uint64 {
	return e.ConnectByPortName(leftNode, leftDir, leftName, rightNode, rightDir, rightName)
}
func (e *httpE2E) sendClientRequest(client uint64) { e.SendClientRequest(client) }
func (e *httpE2E) waitServerListening(server uint64, address string) {
	e.WaitServerListening(server, address)
}
func (e *httpE2E) assertClientRequestWorks(client uint64) { e.AssertClientRequestWorks(client) }
func (e *httpE2E) waitClientFailure(client uint64, wantErrPart string) {
	e.WaitClientFailure(client, wantErrPart)
}
func (e *httpE2E) waitClientRequestField(client uint64, field, want string) {
	e.WaitClientRequestField(client, field, want)
}
func (e *httpE2E) waitServerLog(server uint64, wantPart string) {
	e.WaitServerLog(server, wantPart)
}
func (e *httpE2E) clientURLForPort(port int) string {
	return "http://127.0.0.1:" + itoa(port) + "/"
}

func TestDebugNodesAvailableOnlyInDebugEngines(t *testing.T) {
	pool := e2etest.NewTestDebugPool(t)

	plain, err := almagest.BuildEngine(context.Background(), configer.NewMemory(nil), logging.NewDebugLogFactory(16), &sysnetdebug.System{}, nil, nil, nil, nil, nil, pool, false)
	if err != nil {
		t.Fatalf("BuildEngine plain: %v", err)
	}
	t.Cleanup(func() { _ = plain.Close() })
	if _, ok := plain.NodeClassSnapshot(e2etest.E2EWorkspace, debugnodes.NodeTypeDebugLogNetwork); ok {
		t.Fatalf("%s is available in non-debug engine", debugnodes.NodeTypeDebugLogNetwork)
	}
	if _, ok := plain.NodeClassSnapshot(e2etest.E2EWorkspace, debugnodes.NodeTypeDebugKV); ok {
		t.Fatalf("%s is available in non-debug engine", debugnodes.NodeTypeDebugKV)
	}
	if _, ok := plain.NodeClassSnapshot(e2etest.E2EWorkspace, dnsnodes.NodeTypeDebugLogDNS); ok {
		t.Fatalf("%s is available in non-debug engine", dnsnodes.NodeTypeDebugLogDNS)
	}
	if _, ok := plain.NodeClassSnapshot(e2etest.E2EWorkspace, tunnodes.NodeTypeDebugLogTun); ok {
		t.Fatalf("%s is available in non-debug engine", tunnodes.NodeTypeDebugLogTun)
	}

	debugEngine, err := almagest.BuildEngine(context.Background(), configer.NewMemory(nil), logging.NewDebugLogFactory(16), &sysnetdebug.System{}, nil, nil, nil, nil, nil, pool, true)
	if err != nil {
		t.Fatalf("BuildEngine debug: %v", err)
	}
	t.Cleanup(func() { _ = debugEngine.Close() })
	if _, ok := debugEngine.NodeClassSnapshot(e2etest.E2EWorkspace, debugnodes.NodeTypeDebugLogNetwork); !ok {
		t.Fatalf("%s is missing from debug engine", debugnodes.NodeTypeDebugLogNetwork)
	}
	if _, ok := debugEngine.NodeClassSnapshot(e2etest.E2EWorkspace, debugnodes.NodeTypeDebugKV); !ok {
		t.Fatalf("%s is missing from debug engine", debugnodes.NodeTypeDebugKV)
	}
	if _, ok := debugEngine.NodeClassSnapshot(e2etest.E2EWorkspace, dnsnodes.NodeTypeDebugLogDNS); !ok {
		t.Fatalf("%s is missing from debug engine", dnsnodes.NodeTypeDebugLogDNS)
	}
	if _, ok := debugEngine.NodeClassSnapshot(e2etest.E2EWorkspace, tunnodes.NodeTypeDebugLogTun); !ok {
		t.Fatalf("%s is missing from debug engine", tunnodes.NodeTypeDebugLogTun)
	}
}

func TestDebugLogNetworkE2EPassthroughDisconnectReconnect(t *testing.T) {
	e := newHTTPE2E(t)
	port := httpTestPort()
	out := e.AddNode(networknodes.NodeTypeOutNetwork, "out")
	logNetwork := e.AddNode(debugnodes.NodeTypeDebugLogNetwork, "log-network")
	server := e.addHTTPServer("server", port)
	client := e.addHTTPClient("client", port)
	e.SubscribeMenus(server, client)
	e.waitClientRequestField(client, "url", e.clientURLForPort(port))

	upstream := e.connectByPortName(out, "left", "Network", logNetwork, "right", "Network")
	serverLink := e.connectByPortName(logNetwork, "left", "Network", server, "right", "Network")
	clientLink := e.connectByPortName(logNetwork, "left", "Network", client, "right", "Network")

	e.waitServerListening(server, "127.0.0.1:"+itoa(port))
	e.assertClientRequestWorks(client)
	e.AssertDebugLog(debugnodes.NodeTypeDebugLogNetwork, "Dial")
	e.AssertDebugLog(debugnodes.NodeTypeDebugLogNetwork, "GET / HTTP/1.1")
	e.AssertDebugLog(debugnodes.NodeTypeDebugLogNetwork, "Debug http server response")

	e.Engine.RemoveLink(e2etest.E2EWorkspace, clientLink)
	e.sendClientRequest(client)
	e.waitClientFailure(client, "network is not ready")
	clientLink = e.connectByPortName(logNetwork, "left", "Network", client, "right", "Network")
	e.assertClientRequestWorks(client)

	e.Engine.RemoveLink(e2etest.E2EWorkspace, upstream)
	e.waitServerLog(server, "waiting for network")
	upstream = e.connectByPortName(out, "left", "Network", logNetwork, "right", "Network")
	e.waitServerListening(server, "127.0.0.1:"+itoa(port))
	e.assertClientRequestWorks(client)

	e.Engine.RemoveLink(e2etest.E2EWorkspace, serverLink)
	e.waitServerLog(server, "waiting for network")
	serverLink = e.connectByPortName(logNetwork, "left", "Network", server, "right", "Network")
	e.waitServerListening(server, "127.0.0.1:"+itoa(port))
	e.assertClientRequestWorks(client)

	_ = upstream
	_ = serverLink
	_ = clientLink
}
