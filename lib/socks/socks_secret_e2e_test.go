package socksnodes_test

import (
	"fmt"
	"testing"

	httpnodes "github.com/asciimoth/almagest/lib/http"
	networknodes "github.com/asciimoth/almagest/lib/network"
	socksnodes "github.com/asciimoth/almagest/lib/socks"
	"github.com/asciimoth/almagest/lib/storage"
	"github.com/asciimoth/almagest/lib/testkit"
	"github.com/asciimoth/almagest/lib/testkit/httpe2etest"
	"github.com/asciimoth/almagest/lib/testkit/sockstest"
	"github.com/asciimoth/formular"
)

// TestSocksClientAndServerE2ESecretPasswords proves that both nodes resolve
// their own node-scoped secret and use the values in one authenticated SOCKS
// connection. The different inline values make the request fail if either
// secret override is not applied.
func TestSocksClientAndServerE2ESecretPasswords(t *testing.T) {
	e := NewNodeE2E(t)
	httpOps := httpe2etest.NewHTTPOps(e)
	const password = "secret-backed-password"
	e.Secrets.SetSecret("client-password", storage.Secret{Value: password}, "socks-client")
	e.Secrets.SetSecret("server-password", storage.Secret{Value: password}, "socks-server")

	socksPort := httpTestPort()
	httpPort := httpTestPort()
	out := e.AddNode(networknodes.NodeTypeOutNetwork, "out")
	target := httpOps.AddHTTPServer("target", httpPort)
	socksServer := httpOps.AddSocksServer("socks-server", socksPort, []sockstest.User{{
		ID:             "alice",
		Username:       "alice",
		Password:       "wrong-server-fallback",
		PasswordSecret: "server-password",
	}})
	socksClient := e.AddNode(socksnodes.NodeTypeSocksClient, "socks-client")
	httpClient := httpOps.AddHTTPClient("http-client", httpPort)
	e.SubscribeMenus(target, socksServer, socksClient, httpClient)

	proxyURL := fmt.Sprintf("socks5://alice:wrong-client-fallback@127.0.0.1:%d", socksPort)
	e.Engine.SendNodeFormularMsg(E2EWorkspace, socksClient, formular.FieldUpdateMessage{
		MessageBase: testkit.FormMessageBase(formular.MessageFieldUpdate, socksClient),
		Field:       formular.FieldRef{BlockID: "config", FieldID: "proxyURL"},
		Value:       proxyURL,
	})
	e.Engine.SendNodeFormularMsg(E2EWorkspace, socksClient, formular.FieldUpdateMessage{
		MessageBase: testkit.FormMessageBase(formular.MessageFieldUpdate, socksClient),
		Field:       formular.FieldRef{BlockID: "config", FieldID: "passwordSecret"},
		Value:       "client-password",
	})

	httpOps.ConnectByPortName(out, "left", "Network", target, "right", "Network")
	httpOps.ConnectByPortName(out, "left", "Network", socksServer, "right", "listen")
	httpOps.ConnectByPortName(out, "left", "Network", socksServer, "right", "proxy")
	httpOps.ConnectByPortName(out, "left", "Network", socksClient, "right", "out")
	httpOps.ConnectByPortName(socksClient, "left", "Network", httpClient, "right", "Network")

	httpOps.WaitSocksLog(socksServer, fmt.Sprintf("socks listening on 127.0.0.1:%d", socksPort))
	httpOps.WaitServerListening(target, fmt.Sprintf("127.0.0.1:%d", httpPort))
	httpOps.AssertClientRequestWorks(httpClient)
	e.AssertNodeLabel(httpClient, "200 OK")
	e.AssertDebugLog(httpnodes.NodeTypeHTTPServer, "GET /")
}
