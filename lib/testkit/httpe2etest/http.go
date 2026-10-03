package httpe2etest

import (
	"fmt"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	httpnodes "github.com/asciimoth/almagest/lib/http"
	"github.com/asciimoth/almagest/lib/links"
	networknodes "github.com/asciimoth/almagest/lib/network"
	socksnodes "github.com/asciimoth/almagest/lib/socks"
	"github.com/asciimoth/almagest/lib/testkit"
	"github.com/asciimoth/almagest/lib/testkit/sockstest"
	"github.com/asciimoth/formular"
	"github.com/asciimoth/pasta/pasta"
	"github.com/asciimoth/pasta/pasta/std"
	"github.com/asciimoth/socksgo/protocol"
)

// E2EWorkspace is the default workspace used by root and moved e2e tests.
const E2EWorkspace = "main"

// HTTPBase is the NodeE2E surface required by HTTP helpers.
type HTTPBase interface {
	TestingT() *testing.T
	AddNode(class, name string) uint64
	SubscribeMenus(nodes ...uint64)
	MustAddTypedLinkByPortName(wantType string, leftNode uint64, leftDir, leftName string, rightNode uint64, rightDir, rightName string) uint64
	MustAddTypedLinkByPorts(wantType string, a, b uint64) uint64
	SendNodeFormularMsg(workspace string, node uint64, msg any)
	NodeSnapshot(workspace string, id uint64) (pasta.NodeSnapshot, bool)
	WaitNodeLabel(node uint64, want string)
	AssertNodeLabel(node uint64, want string)
	AssertDebugLog(nodeClass, wantPart string)
	MenuItem(node uint64, blockID, itemID string) (formular.Item, bool)
	ApplyStdConstantValue(node uint64, value any)
}

// HTTPOps provides HTTP, SOCKS, and network-link e2e helpers.
type HTTPOps struct {
	HTTPBase
}

func (e *HTTPOps) t() *testing.T {
	return e.TestingT()
}

// NewHTTPOps returns HTTP helper methods for base.
func NewHTTPOps(base HTTPBase) *HTTPOps {
	return &HTTPOps{HTTPBase: base}
}

// SetupHTTPServerThroughSocksClient builds an HTTP server behind a SOCKS client.
func (e *HTTPOps) SetupHTTPServerThroughSocksClient(proxyURL string, httpPort int) (uint64, uint64, uint64) {
	e.t().Helper()
	mock := e.AddNode(networknodes.NodeTypeMockNetwork, "mock")
	serverOut := e.AddNode(networknodes.NodeTypeOutNetwork, "server-out")
	listenOut := e.AddNode(networknodes.NodeTypeOutNetwork, "listen-out")
	url := e.AddStringConstant("socks-url", proxyURL)
	socksClient := e.AddNode(socksnodes.NodeTypeSocksClient, "socks-client")
	httpServer := e.AddHTTPServer("http-server", httpPort)
	httpClient := e.AddHTTPClient("http-client", httpPort)
	proxyPort := ProxyPortFromURL(e.t(), proxyURL)
	socksServer := e.AddSocksServer("socks-server", proxyPort, nil)
	e.SubscribeMenus(url, socksClient, httpClient, socksServer, httpServer)
	e.WaitClientRequestField(httpClient, "url", fmt.Sprintf("http://127.0.0.1:%d/", httpPort))

	e.MustAddTypedLinkByPortName(std.TypeString, url, "right", "output", socksClient, "left", "Proxy URL")
	e.ConnectByPortName(socksClient, "left", "Network", httpServer, "right", "Network")
	e.ConnectByPortName(serverOut, "left", "Network", socksClient, "right", "out")
	e.ConnectByPortName(listenOut, "left", "Network", socksServer, "right", "listen")
	e.ConnectByPortName(mock, "left", "Network", socksServer, "right", "proxy")
	e.ConnectByPortName(mock, "left", "Network", httpClient, "right", "Network")

	e.WaitNodeLabel(socksClient, proxyURL)
	e.WaitSocksLog(socksServer, fmt.Sprintf("socks listening on 127.0.0.1:%d", proxyPort))
	e.WaitServerListening(httpServer, fmt.Sprintf("127.0.0.1:%d", httpPort))
	return httpServer, httpClient, socksServer
}

// ProxyPortFromURL returns the port in a socks5://host:port URL.
func ProxyPortFromURL(t *testing.T, proxyURL string) int {
	t.Helper()
	hostPort := strings.TrimPrefix(proxyURL, "socks5://")
	if i := strings.IndexByte(hostPort, '?'); i >= 0 {
		hostPort = hostPort[:i]
	}
	_, portString, err := net.SplitHostPort(hostPort)
	if err != nil {
		t.Fatalf("proxy URL %q is not a socks5 host:port URL: %v", proxyURL, err)
	}
	port, err := strconv.Atoi(portString)
	if err != nil {
		t.Fatalf("proxy URL %q port is not an integer: %v", proxyURL, err)
	}
	return port
}

// AddHTTPServer adds and configures an HTTP server node.
func (e *HTTPOps) AddHTTPServer(name string, port int) uint64 {
	e.t().Helper()
	id := e.AddNode(httpnodes.NodeTypeHTTPServer, name)
	e.SendNodeFormularMsg(E2EWorkspace, id, formular.FormApplyMessage{
		MessageBase: testkit.FormMessageBase(formular.MessageFormApply, id),
		BlockID:     "listen",
		Values: map[string]any{
			"host": "127.0.0.1",
			"port": port,
		},
	})
	e.WaitNodeLabel(id, fmt.Sprintf("127.0.0.1:%d", port))
	return id
}

// AddHTTPClient adds and configures an HTTP client node for localhost:port.
func (e *HTTPOps) AddHTTPClient(name string, port int) uint64 {
	e.t().Helper()
	id := e.AddNode(httpnodes.NodeTypeHTTPClient, name)
	e.SendNodeFormularMsg(E2EWorkspace, id, formular.FieldUpdateMessage{
		MessageBase: testkit.FormMessageBase(formular.MessageFieldUpdate, id),
		Field:       formular.FieldRef{BlockID: "request", FieldID: "url"},
		Value:       fmt.Sprintf("http://127.0.0.1:%d/", port),
	})
	return id
}

// AddSocksServer adds and configures a SOCKS server node.
func (e *HTTPOps) AddSocksServer(name string, port int, users []sockstest.User) uint64 {
	e.t().Helper()
	id := e.AddNode(socksnodes.NodeTypeSocksServer, name)
	values := map[string]any{
		"socksHost": "127.0.0.1",
		"socksPort": port,
		"wsHost":    "",
		"wsPort":    0,
		"wsPath":    "/socks",
	}
	if users != nil {
		values["users"] = sockstest.ServerUserValues(users)
	}
	e.SendNodeFormularMsg(E2EWorkspace, id, formular.FormApplyMessage{
		MessageBase: testkit.FormMessageBase(formular.MessageFormApply, id),
		BlockID:     "config",
		Values:      values,
	})
	e.WaitNodeLabel(id, fmt.Sprintf("127.0.0.1:%d", port))
	return id
}

// AddStringConstant adds a Pasta string constant node.
func (e *HTTPOps) AddStringConstant(name, value string) uint64 {
	e.t().Helper()
	id := e.AddNode(std.NodeTypeStringConstant, name)
	e.ApplyStdConstantValue(id, value)
	e.WaitNodeLabel(id, value)
	return id
}

// ApplySocksServerCommands applies SOCKS command capability overrides.
func (e *HTTPOps) ApplySocksServerCommands(socks uint64, port int, overrides map[protocol.Cmd]bool) {
	e.t().Helper()
	commands := sockstest.DefaultServerCommands()
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
	for cmd, field := range sockstest.ServerCommandFields() {
		values[field] = commands[cmd]
	}
	e.SendNodeFormularMsg(E2EWorkspace, socks, formular.FormApplyMessage{
		MessageBase: testkit.FormMessageBase(formular.MessageFormApply, socks),
		BlockID:     "config",
		Values:      values,
	})
}

// ConnectByPortName connects two named Network ports.
func (e *HTTPOps) ConnectByPortName(leftNode uint64, leftDir, leftName string, rightNode uint64, rightDir, rightName string) uint64 {
	e.t().Helper()
	return e.MustAddTypedLinkByPortName(links.TypeNetwork, leftNode, leftDir, leftName, rightNode, rightDir, rightName)
}

// MustAddNetworkLinkByPorts connects two ports and requires a Network link.
func (e *HTTPOps) MustAddNetworkLinkByPorts(a, b uint64) uint64 {
	e.t().Helper()
	return e.MustAddTypedLinkByPorts(links.TypeNetwork, a, b)
}

// SendClientRequest presses the HTTP client send button.
func (e *HTTPOps) SendClientRequest(client uint64) {
	e.t().Helper()
	e.SendNodeFormularMsg(E2EWorkspace, client, formular.ButtonPressMessage{
		MessageBase: testkit.FormMessageBase(formular.MessageButtonPress, client),
		BlockID:     "request",
		ButtonID:    "send",
	})
}

// WaitServerListening waits until the HTTP server reports a listening address.
func (e *HTTPOps) WaitServerListening(server uint64, address string) {
	e.t().Helper()
	e.WaitServerLog(server, "listening on "+address)
	e.AssertDebugLog(httpnodes.NodeTypeHTTPServer, "listening on "+address)
}

// WaitClientResponse waits for the HTTP client result fields.
func (e *HTTPOps) WaitClientResponse(client uint64, status, response string) {
	e.t().Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		gotStatus := e.ClientStatus(client)
		gotErr := e.ClientResultField(client, "error")
		gotResp := e.ClientResultField(client, "response")
		if gotStatus == "Status: "+status && gotErr == "" && gotResp == response {
			return
		}
		if time.Now().After(deadline) {
			e.t().Fatalf(
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

// AssertClientRequestWorks retries until the HTTP client gets the debug response.
func (e *HTTPOps) AssertClientRequestWorks(client uint64) {
	e.t().Helper()
	e.AssertClientRequestReturns(client, "Debug http server response", 10*time.Second)
}

// AssertClientRequestReturns retries until the HTTP client gets response.
func (e *HTTPOps) AssertClientRequestReturns(client uint64, response string, timeout time.Duration) {
	e.t().Helper()
	deadline := time.Now().Add(timeout)
	observedCurrentRequest := false
	for {
		gotStatus, gotErr, gotResp, gotLabel := e.ClientResultSnapshot(client)
		running := gotStatus == "Status: "+httpnodes.HTTPClientStatusRunning || gotLabel == httpnodes.HTTPClientStatusRunning

		if gotStatus == "Status: 200 OK" &&
			gotErr == "" &&
			gotResp == response &&
			observedCurrentRequest &&
			gotLabel == "200 OK" {
			return
		}

		// Menu notifications are asynchronous with engine snapshots. A stale
		// success from the previous request must not satisfy this helper after a
		// fresh button press has already moved the node label to "running...".
		if !observedCurrentRequest {
			observedCurrentRequest = true
			if !running {
				e.SendClientRequest(client)
			}
		} else if !running {
			e.SendClientRequest(client)
		}
		if time.Now().After(deadline) {
			e.t().Fatalf(
				"timed out waiting for retried client response %q; got status=%q error=%q response=%q label=%q",
				response,
				gotStatus,
				gotErr,
				gotResp,
				gotLabel,
			)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// ClientResultSnapshot returns current HTTP client status fields plus node label.
func (e *HTTPOps) ClientResultSnapshot(client uint64) (status, errText, response, label string) {
	e.t().Helper()
	return e.ClientStatus(client),
		e.ClientResultField(client, "error"),
		e.ClientResultField(client, "response"),
		e.ClientLabel(client)
}

// ClientLabel returns the current client node label.
func (e *HTTPOps) ClientLabel(client uint64) string {
	e.t().Helper()
	snapshot, ok := e.NodeSnapshot(E2EWorkspace, client)
	if !ok {
		e.t().Fatalf("NodeSnapshot(%d) missing", client)
	}
	return snapshot.Label
}

// WaitClientFailure waits until the HTTP client fails with text.
func (e *HTTPOps) WaitClientFailure(client uint64, wantErrPart string) {
	e.t().Helper()
	testkit.Eventually(e.t(), func() bool {
		return e.ClientStatus(client) == "Status: fail" &&
			strings.Contains(e.ClientResultField(client, "error"), wantErrPart)
	}, "client failure containing %q", wantErrPart)
	e.AssertNodeLabel(client, "fail")
}

// WaitClientAnyFailure waits until the HTTP client has any non-empty failure.
func (e *HTTPOps) WaitClientAnyFailure(client uint64) {
	e.t().Helper()
	testkit.Eventually(e.t(), func() bool {
		return e.ClientStatus(client) == "Status: fail" &&
			e.ClientResultField(client, "error") != ""
	}, "client failure with a non-empty error")
	e.AssertNodeLabel(client, "fail")
}

// WaitClientReady waits until the HTTP client is ready and has no result.
func (e *HTTPOps) WaitClientReady(client uint64) {
	e.t().Helper()
	testkit.Eventually(e.t(), func() bool {
		return e.ClientStatus(client) == "Status: ready" &&
			e.ClientResultField(client, "error") == "" &&
			e.ClientResultField(client, "response") == ""
	}, "client ready")
}

// WaitClientRequestField waits until a request config field has want.
func (e *HTTPOps) WaitClientRequestField(client uint64, field, want string) {
	e.t().Helper()
	testkit.Eventually(e.t(), func() bool {
		item, ok := e.MenuItem(client, "request", field)
		return ok && item.Field != nil && item.Value == want
	}, "client request field %s = %q", field, want)
}

// WaitServerLog waits until server logs contain text.
func (e *HTTPOps) WaitServerLog(server uint64, wantPart string) {
	e.t().Helper()
	testkit.Eventually(e.t(), func() bool {
		return e.ServerLogsContain(server, wantPart)
	}, "server log containing %q", wantPart)
}

// WaitServerLogCountAbove waits until a new matching server log is present.
func (e *HTTPOps) WaitServerLogCountAbove(server uint64, wantPart string, previous int) {
	e.t().Helper()
	testkit.Eventually(e.t(), func() bool {
		return e.ServerLogCount(server, wantPart) > previous
	}, "server log count for %q above %d", wantPart, previous)
}

// WaitSocksLog waits until SOCKS logs contain text.
func (e *HTTPOps) WaitSocksLog(socks uint64, wantPart string) {
	e.t().Helper()
	testkit.Eventually(e.t(), func() bool {
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

// WaitSocksLogCountAbove waits until a new matching SOCKS log is present.
func (e *HTTPOps) WaitSocksLogCountAbove(socks uint64, wantPart string, previous int) {
	e.t().Helper()
	testkit.Eventually(e.t(), func() bool {
		return e.SocksLogCount(socks, wantPart) > previous
	}, "socks log count for %q above %d", wantPart, previous)
}

// AssertServerLog fails unless server logs contain text.
func (e *HTTPOps) AssertServerLog(server uint64, wantPart string) {
	e.t().Helper()
	if !e.ServerLogsContain(server, wantPart) {
		e.t().Fatalf("server logs do not contain %q", wantPart)
	}
}

// AssertServerNoNewLogFor fails if a server log count changes within duration.
func (e *HTTPOps) AssertServerNoNewLogFor(server uint64, wantPart string, duration time.Duration) {
	e.t().Helper()
	before := e.ServerLogCount(server, wantPart)
	deadline := time.Now().Add(duration)
	for time.Now().Before(deadline) {
		if got := e.ServerLogCount(server, wantPart); got != before {
			e.t().Fatalf("server log count for %q changed from %d to %d during %s", wantPart, before, got, duration)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// ServerLogsContain reports whether server logs contain text.
func (e *HTTPOps) ServerLogsContain(server uint64, wantPart string) bool {
	return e.ServerLogCount(server, wantPart) > 0
}

// ServerLogCount returns the number of server log lines that contain text.
func (e *HTTPOps) ServerLogCount(server uint64, wantPart string) int {
	item, ok := e.MenuItem(server, "logs", "worker")
	if !ok {
		return 0
	}
	count := 0
	for _, line := range item.Logs {
		if strings.Contains(line.Text, wantPart) {
			count++
		}
	}
	return count
}

// AssertSocksNoNewLogFor fails if a SOCKS log count changes within duration.
func (e *HTTPOps) AssertSocksNoNewLogFor(socks uint64, wantPart string, duration time.Duration) {
	e.t().Helper()
	before := e.SocksLogCount(socks, wantPart)
	deadline := time.Now().Add(duration)
	for time.Now().Before(deadline) {
		if got := e.SocksLogCount(socks, wantPart); got != before {
			e.t().Fatalf("socks log count for %q changed from %d to %d during %s", wantPart, before, got, duration)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// SocksLogCount returns the number of SOCKS log lines that contain text.
func (e *HTTPOps) SocksLogCount(socks uint64, wantPart string) int {
	item, ok := e.MenuItem(socks, "logs", "worker")
	if !ok {
		return 0
	}
	count := 0
	for _, line := range item.Logs {
		if strings.Contains(line.Text, wantPart) {
			count++
		}
	}
	return count
}

// ClientStatus returns the HTTP client status text.
func (e *HTTPOps) ClientStatus(client uint64) string {
	item, ok := e.MenuItem(client, "result", "status")
	if !ok {
		return ""
	}
	return item.Text
}

// ClientResultField returns a string HTTP client result field.
func (e *HTTPOps) ClientResultField(client uint64, field string) string {
	item, ok := e.MenuItem(client, "result", field)
	if !ok || item.Field == nil {
		return ""
	}
	value, _ := std.StringFromPayload(item.Value)
	return value
}
