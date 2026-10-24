package socksnodes_test

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/asciimoth/almagest/lib/storage"
	"github.com/asciimoth/almagest/lib/testkit/sockstest"
	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/formular"
	"github.com/asciimoth/gonnect"
	"github.com/asciimoth/gonnect/sockowner"
	"github.com/asciimoth/gonnect/sysnet"
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
	proxy := e.addLocalProxy("local-proxy", socksPort, []sockstest.User{
		{ID: "user-1", Username: "alice", Password: "secret"},
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

func TestLocalProxyE2ESecretPasswordLifecycle(t *testing.T) {
	e := newLocalProxyE2E(t)
	native := e.AddNode(testNativeNetworkNodeType, "native")
	socksPort := httpTestPort()
	httpPort := httpTestPort()
	server := e.addHTTPServer("target", httpPort)
	e.Secrets.SetSecret("local-password", storage.Secret{Value: "secret-one"}, "local-proxy")
	proxy := e.addLocalProxy("local-proxy", socksPort, []sockstest.User{{
		ID:             "user-1",
		Username:       "alice",
		Password:       "inline-fallback",
		PasswordSecret: "local-password",
	}})
	e.SubscribeMenus(server, proxy)

	e.MustAddTypedLinkByPortName(TypeNetwork, native, "left", "Network", server, "right", "Network")
	e.MustAddTypedLinkByPortName(TypeNetwork, native, "left", "Network", proxy, "right", "proxy")
	e.waitSocksLog(proxy, fmt.Sprintf("socks listening on 127.0.0.1:%d", socksPort))

	e.assertLocalProxyPasswordWorks(socksPort, httpPort, "secret-one")
	e.waitLocalProxyHTTPGetFailure(socksPort, httpPort, localProxyPasswordAuth("inline-fallback"))

	// A value update replaces the running worker password.
	e.Secrets.SetSecret("local-password", storage.Secret{Value: "secret-two"}, "local-proxy")
	e.sendLocalProxySecretChanged(proxy, "local-password", "local-password")
	e.assertLocalProxyPasswordWorks(socksPort, httpPort, "secret-two")
	e.waitLocalProxyHTTPGetFailure(socksPort, httpPort, localProxyPasswordAuth("secret-one"))

	// A rename updates the persisted selector and resolves the new key.
	e.Secrets.SetSecret("renamed-password", storage.Secret{Value: "secret-three"}, "local-proxy")
	e.Secrets.DeleteSecret("local-password")
	e.sendLocalProxySecretChanged(proxy, "local-password", "renamed-password")
	e.assertLocalProxyPasswordWorks(socksPort, httpPort, "secret-three")
	e.assertLocalProxyPasswordSecret(proxy, "renamed-password")

	// Deletion or loss of node access activates the inline fallback. Recreating
	// the selected secret activates its value again.
	e.Secrets.DeleteSecret("renamed-password")
	e.sendLocalProxySecretChanged(proxy, "renamed-password", "")
	e.assertLocalProxyPasswordWorks(socksPort, httpPort, "inline-fallback")
	e.waitLocalProxyHTTPGetFailure(socksPort, httpPort, localProxyPasswordAuth("secret-three"))

	e.Secrets.SetSecret("renamed-password", storage.Secret{Value: "secret-four"}, "other-node")
	e.sendLocalProxySecretChanged(proxy, "renamed-password", "renamed-password")
	e.assertLocalProxyPasswordWorks(socksPort, httpPort, "inline-fallback")

	e.Secrets.SetSecret("renamed-password", storage.Secret{Value: "secret-five"}, "local-proxy")
	e.sendLocalProxySecretChanged(proxy, "", "renamed-password")
	e.assertLocalProxyPasswordWorks(socksPort, httpPort, "secret-five")
}

func TestLocalProxyE2EClientIncludeExcludeAndBackwardCompatibility(t *testing.T) {
	e := newLocalProxyE2EWithSystem(t, func(system *sysnetdebug.System) {
		system.LocalNetwork = gonnect.NativeConfig{}.Build()
		system.RuleMatcher = func(rule sysnet.Rule, _ sockowner.FlowTuple) (bool, error) {
			return rule.Type == "comm" && rule.Rule == "allowed-client", nil
		}
	})
	native := e.AddNode(testNativeNetworkNodeType, "native")
	socksPort := httpTestPort()
	httpPort := httpTestPort()
	server := e.addHTTPServer("target", httpPort)
	proxy := e.addLocalProxy("local-proxy", socksPort, nil)
	e.SubscribeMenus(server, proxy)
	e.MustAddTypedLinkByPortName(TypeNetwork, native, "left", "Network", server, "right", "Network")
	e.MustAddTypedLinkByPortName(TypeNetwork, native, "left", "Network", proxy, "right", "proxy")
	e.waitSocksLog(proxy, fmt.Sprintf("socks listening on 127.0.0.1:%d", socksPort))

	// No rule keeps the legacy allow-all behavior.
	e.waitLocalProxyHTTPGet(socksPort, httpPort, nil)
	e.applyLocalProxyRules(proxy, "include", []sysnet.Rule{{Type: "comm", Rule: "allowed-client"}})
	e.waitLocalProxyHTTPGet(socksPort, httpPort, nil)

	// The same matching client is denied in exclude mode.
	e.applyLocalProxyRules(proxy, "include", nil)
	e.applyLocalProxyRules(proxy, "exclude", []sysnet.Rule{{Type: "comm", Rule: "allowed-client"}})
	e.waitLocalProxyHTTPGetFailure(socksPort, httpPort, nil)

	// Clearing the policy restores the previous behavior.
	e.applyLocalProxyRules(proxy, "exclude", nil)
	e.waitLocalProxyHTTPGet(socksPort, httpPort, nil)
}

func newLocalProxyE2E(t *testing.T) *localProxyE2E {
	return newLocalProxyE2EWithSystem(t, nil)
}

func newLocalProxyE2EWithSystem(t *testing.T, configure func(*sysnetdebug.System)) *localProxyE2E {
	t.Helper()
	pool := newE2ETestDebugPool(t)
	logf := NewDebugLogFactory(16384)
	sys := newE2EDebugSystem()
	secrets := NewSecretsStorage()
	if configure != nil {
		configure(sys)
	}
	engine, err := BuildEngine(
		context.Background(),
		configer.NewMemory(nil),
		logf,
		sys,
		nil,
		nil, secrets,
		nil,

		[]pasta.NodeClass{testNativeNetworkClass{}},
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
	e := NewNodeE2EFromEngine(t, engine, logf, sys, secrets)
	return &localProxyE2E{socksServerE2E: &socksServerE2E{NodeE2E: e}, sys: sys}
}

func localProxyPasswordAuth(password string) *protocol.AuthMethods {
	return (&protocol.AuthMethods{}).Add(&protocol.PassAuthMethod{User: "alice", Pass: password})
}

func (e *localProxyE2E) assertLocalProxyPasswordWorks(socksPort, httpPort int, password string) {
	e.T.Helper()
	body := e.waitLocalProxyHTTPGet(socksPort, httpPort, localProxyPasswordAuth(password))
	if body != "Debug http server response" {
		e.T.Fatalf("response body = %q, want debug response", body)
	}
}

func (e *localProxyE2E) sendLocalProxySecretChanged(node uint64, prev, next string) {
	e.T.Helper()
	if !e.Engine.SendInboxesAndWait(E2EWorkspace, []pasta.InboxMessage{{
		ReceiverNode: node,
		Payload: storage.SecretValueChanged{
			SecretNamePrev: prev,
			SecretNameNew:  next,
		},
	}}) {
		e.T.Fatal("secret change inbox was not delivered")
	}
}

func (e *localProxyE2E) assertLocalProxyPasswordSecret(node uint64, want string) {
	e.T.Helper()
	Eventually(e.T, func() bool {
		item, ok := e.MenuItem(node, "config", "users")
		if !ok || item.Field == nil {
			return false
		}
		for _, element := range item.Elements {
			if element.ID != "user-1" {
				continue
			}
			for _, child := range element.Items {
				if child.ID == "passwordSecret" {
					return child.Value == want
				}
			}
		}
		return false
	}, "local proxy password secret selector %q", want)
}

func (e *localProxyE2E) applyLocalProxyRules(node uint64, field string, rules []sysnet.Rule) {
	e.T.Helper()
	values := make([]formular.ArrayElementValue, 0, len(rules))
	for i, rule := range rules {
		values = append(values, formular.ArrayElementValue{
			ID:       fmt.Sprintf("rule-%d", i),
			Template: "rule",
			Values:   map[string]any{"type": rule.Type, "rule": rule.Rule},
		})
	}
	e.Engine.SendNodeFormularMsg(E2EWorkspace, node, formular.FormApplyMessage{
		MessageBase: FormMessageBase(formular.MessageFormApply, node),
		BlockID:     "config",
		Values:      map[string]any{field: values},
	})
}

func (e *localProxyE2E) addLocalProxy(name string, port int, users []sockstest.User) uint64 {
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
		values["users"] = sockstest.ServerUserValues(users)
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
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	client := &socksgo.Client{
		SocksVersion: "5",
		ProxyAddr:    fmt.Sprintf("127.0.0.1:%d", socksPort),
		Auth:         auth,
		Filter:       func(_, _ string) bool { return false },
		Dialer:       sys.LocalNet().Dial,
	}
	conn, err := client.Dial(ctx, "tcp", fmt.Sprintf("127.0.0.1:%d", httpPort))
	if err != nil {
		return "", err
	}
	defer conn.Close() //nolint:errcheck
	if err := conn.SetDeadline(time.Now().Add(500 * time.Millisecond)); err != nil {
		return "", err
	}
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
