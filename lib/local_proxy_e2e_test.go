package almagest

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"testing"

	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/formular"
	sysnetdebug "github.com/asciimoth/gonnect/sysnet/debug"
	"github.com/asciimoth/pasta/pasta"
	"github.com/asciimoth/socksgo"
	"github.com/asciimoth/socksgo/protocol"
)

type localProxyE2E struct {
	*socksServerE2E
	sys *sysnetdebug.System
}

func TestLocalProxyE2EConnectThroughLocalNet(t *testing.T) {
	e := newLocalProxyE2E(t)
	native := e.AddNode(testNativeNetworkNodeType, "native")
	socksPort := httpTestPort()
	httpPort := httpTestPort()
	server := e.addHTTPServer("target", httpPort)
	proxy := e.addLocalProxy("local-proxy", socksPort, nil)
	e.SubscribeMenus(server, proxy)

	e.MustAddTypedLinkByPortName(TypeNetwork, native, "left", "Network", server, "right", "Network")
	e.MustAddTypedLinkByPortName(TypeNetwork, native, "left", "Network", proxy, "right", "proxy")

	e.waitSocksLog(proxy, fmt.Sprintf("socks listening on 127.0.0.1:%d", socksPort))
	body := e.waitLocalProxyHTTPGet(socksPort, httpPort, nil)
	if body != "Debug http server response" {
		t.Fatalf("response body = %q, want debug response", body)
	}
	e.AssertDebugLog(NodeTypeLocalProxy, "connect")
}

func TestLocalProxyE2EUserPassAuth(t *testing.T) {
	e := newLocalProxyE2E(t)
	native := e.AddNode(testNativeNetworkNodeType, "native")
	socksPort := httpTestPort()
	httpPort := httpTestPort()
	server := e.addHTTPServer("target", httpPort)
	proxy := e.addLocalProxy("local-proxy", socksPort, []socksServerUser{
		{id: "user-1", username: "alice", password: "secret"},
	})
	e.SubscribeMenus(server, proxy)

	e.MustAddTypedLinkByPortName(TypeNetwork, native, "left", "Network", server, "right", "Network")
	e.MustAddTypedLinkByPortName(TypeNetwork, native, "left", "Network", proxy, "right", "proxy")
	e.waitSocksLog(proxy, fmt.Sprintf("socks listening on 127.0.0.1:%d", socksPort))

	e.waitLocalProxyHTTPGetFailure(socksPort, httpPort, (&protocol.AuthMethods{}).Add(&protocol.PassAuthMethod{User: "alice", Pass: "wrong"}))
	body := e.waitLocalProxyHTTPGet(socksPort, httpPort, (&protocol.AuthMethods{}).Add(&protocol.PassAuthMethod{User: "alice", Pass: "secret"}))
	if body != "Debug http server response" {
		t.Fatalf("response body = %q, want debug response", body)
	}
}

func newLocalProxyE2E(t *testing.T) *localProxyE2E {
	t.Helper()
	pool := newE2ETestDebugPool(t)
	logf := NewDebugLogFactory(16384)
	sys := newE2EDebugSystem()
	engine, err := BuildEngine(
		context.Background(),
		configer.NewMemory(nil),
		logf,
		sys,
		[]pasta.NodeClass{testNativeNetworkClass{}},
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
	return &localProxyE2E{socksServerE2E: &socksServerE2E{NodeE2E: e}, sys: sys}
}

func (e *localProxyE2E) addLocalProxy(name string, port int, users []socksServerUser) uint64 {
	e.T.Helper()
	id := e.AddNode(NodeTypeLocalProxy, name)
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
	return id
}

func (e *localProxyE2E) waitLocalProxyHTTPGet(socksPort, httpPort int, auth *protocol.AuthMethods) string {
	e.T.Helper()
	var body string
	var err error
	Eventually(e.T, func() bool {
		body, err = localProxyHTTPGet(e.sys, socksPort, httpPort, auth)
		return err == nil
	}, "local proxy HTTP GET via %d to %d, last error: %v", socksPort, httpPort, err)
	return body
}

func (e *localProxyE2E) waitLocalProxyHTTPGetFailure(socksPort, httpPort int, auth *protocol.AuthMethods) {
	e.T.Helper()
	Eventually(e.T, func() bool {
		_, err := localProxyHTTPGet(e.sys, socksPort, httpPort, auth)
		return err != nil
	}, "local proxy HTTP GET failure via %d to %d", socksPort, httpPort)
}

func localProxyHTTPGet(sys *sysnetdebug.System, socksPort, httpPort int, auth *protocol.AuthMethods) (string, error) {
	client := &socksgo.Client{
		SocksVersion: "5",
		ProxyAddr:    fmt.Sprintf("127.0.0.1:%d", socksPort),
		Auth:         auth,
		Filter:       func(_, _ string) bool { return false },
		Dialer:       sys.LocalNet().Dial,
	}
	conn, err := client.Dial(context.Background(), "tcp", fmt.Sprintf("127.0.0.1:%d", httpPort))
	if err != nil {
		return "", err
	}
	defer conn.Close() //nolint:errcheck
	if _, err := fmt.Fprintf(conn, "GET / HTTP/1.1\r\nHost: 127.0.0.1:%d\r\nConnection: close\r\n\r\n", httpPort); err != nil {
		return "", err
	}
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close() //nolint:errcheck
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	return string(data), nil
}
