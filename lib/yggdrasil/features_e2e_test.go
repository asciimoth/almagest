package yggdrasilnodes_test

import (
	"fmt"
	"strconv"
	"testing"
	"time"

	"github.com/asciimoth/almagest/lib/links"
	"github.com/asciimoth/almagest/lib/testkit"
	"github.com/asciimoth/almagest/lib/testkit/e2etest"
	"github.com/asciimoth/almagest/lib/testkit/httpe2etest"
	ygg "github.com/asciimoth/almagest/lib/yggdrasil"
	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/formular"
	"github.com/asciimoth/pasta/pasta/std"
)

func waitPeerCount(t *testing.T, e *e2etest.NodeE2E, id uint64, minimum int) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		got, _ := strconv.Atoi(field(e, id, "status", "connectedPeers"))
		if got >= minimum {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("node %d did not reach %d connected peers", id, minimum)
}

func TestYggdrasilPublicAutopeerAndJumper(t *testing.T) {
	e := e2etest.NewNodeE2E(t)
	carrier := e.AddNode("almagest/MockNetwork", "carrier")
	public := e.AddNode(ygg.NodeTypeYggdrasil, "public")
	client := e.AddNode(ygg.NodeTypeYggdrasil, "client")
	server := e.AddNode(ygg.NodeTypeYggdrasil, "server")
	clientTun := e.AddNode("almagest/VTun", "client-tun")
	serverTun := e.AddNode("almagest/VTun", "server-tun")
	e.SubscribeMenus(public, client, server)
	serverAddress := address(e, server)
	publicURL := fmt.Sprintf("tcp://127.0.0.1:%d", e2etest.TestPort())
	serverURL := fmt.Sprintf("tcp://127.0.0.1:%d", e2etest.TestPort())
	apply(e, public, "config", map[string]any{"listen": publicURL})
	apply(e, server, "config", map[string]any{"listen": serverURL, "peers": publicURL, "jumperEnabled": true, "jumperAddresses": serverURL, "nodeInfoPrivacy": true, "nodeInfo": `{"service":"http"}`})
	for _, id := range []uint64{public, client, server} {
		e.MustAddTypedLinkByPortName(links.TypeNetwork, carrier, "left", "Network", id, "right", "default")
	}
	e.MustAddTypedLinkByPortName(links.TypeTun, client, "left", "Tun", clientTun, "right", "Tun")
	e.MustAddTypedLinkByPortName(links.TypeTun, server, "left", "Tun", serverTun, "right", "Tun")

	// This HTTP server supplies a local public-peers document through the same
	// carrier. No host network access is needed to discover the public node.
	sourcePort := e2etest.TestPort()
	source := e.AddNode("almagest/HttpServer", "peer-list")
	e.SubscribeMenus(source)
	apply(e, source, "listen", map[string]any{"host": "127.0.0.1", "port": sourcePort})
	e.MustAddTypedLinkByPortName(links.TypeNetwork, carrier, "left", "Network", source, "right", "Network")
	document := fmt.Sprintf(`{"schema_version":"1","generated_at":"2026-01-01T00:00:00Z","sources":{"public_peers_repo":"test","public_peers_branch":"main","uptime_page":"test"},"peers":[{"continent":"Asia","country":"Georgia","source_file":"test","endpoints":[{"url":%q,"protocol":"tcp","host":"127.0.0.1","port":%d}]}]}`, publicURL, mustPort(t, publicURL))
	h := httpe2etest.NewHTTPOps(e)
	constant := h.AddStringConstant("peer-list-document", document)
	e.MustAddTypedLinkByPortName(std.TypeString, constant, "right", "output", source, "left", "Response")
	peerSourceURL := fmt.Sprintf("http://127.0.0.1:%d/", sourcePort)
	h.WaitServerListening(source, fmt.Sprintf("127.0.0.1:%d", sourcePort))
	apply(e, client, "config", map[string]any{
		"autoPeerEnabled": true, "autoPeerSources": peerSourceURL,
		"autoPeerCountries": "georgia", "autoPeerSchemes": "tcp",
		"autoPeerFetchInterval": "1s", "autoPeerCheckInterval": "100ms",
		"jumperEnabled": true, "jumperCheckInterval": "100ms", "jumperLinkTimeout": "5s",
	})
	e.WaitNodeLabel(client, "up")
	e.WaitNodeLabel(server, "up")
	waitPeerCount(t, e, client, 1)
	if field(e, client, "status", "autoPeerCandidates") != "1" {
		t.Fatal("public-peer document was not loaded")
	}
	apply(e, client, "config", map[string]any{"autoPeerFetchInterval": "invalid"})
	if got := field(e, client, "config", "autoPeerFetchInterval"); got != "1s" {
		t.Fatalf("invalid menu interval was accepted: %q", got)
	}
	saved := configer.NewMemory(nil)
	if err := e.Engine.SaveConfig(saved); err != nil {
		t.Fatal(err)
	}
	if got := configer.String(saved, configer.Path{"nodes", "client", "autoPeerSources"}, ""); got != peerSourceURL {
		t.Fatalf("saved public-peer source = %q", got)
	}

	servicePort := e2etest.TestPort()
	service := e.AddNode("almagest/HttpServer", "service")
	e.SubscribeMenus(service)
	apply(e, service, "listen", map[string]any{"host": serverAddress, "port": servicePort})
	e.MustAddTypedLinkByPortName(links.TypeNetwork, serverTun, "left", "Network", service, "right", "Network")
	app := e.AddNode("almagest/HttpClient", "app")
	e.Engine.SendNodeFormularMsg(workspace, app, formular.FieldUpdateMessage{MessageBase: testkit.FormMessageBase(formular.MessageFieldUpdate, app), Field: formular.FieldRef{BlockID: "request", FieldID: "url"}, Value: fmt.Sprintf("http://[%s]:%d/", serverAddress, servicePort)})
	e.SubscribeMenus(app, service)
	e.MustAddTypedLinkByPortName(links.TypeNetwork, clientTun, "left", "Network", app, "right", "Network")
	h.WaitServerListening(service, fmt.Sprintf("[%s]:%d", serverAddress, servicePort))
	h.AssertClientRequestReturns(app, "Debug http server response", 90*time.Second)
	waitPeerCount(t, e, client, 2) // Jumper added a direct link to the server.
	if got := field(e, client, "config", "peers"); got != "" {
		t.Fatalf("manual peers = %q, want none", got)
	}
	apply(e, client, "config", map[string]any{"jumperEnabled": false})
	e.WaitNodeLabel(client, "up")
	if got := field(e, client, "status", "jumper"); got != "off" {
		t.Fatalf("jumper status = %q", got)
	}
	h.AssertClientRequestReturns(app, "Debug http server response", 90*time.Second)
}

func mustPort(t *testing.T, raw string) int {
	t.Helper()
	var port int
	if _, err := fmt.Sscanf(raw, "tcp://127.0.0.1:%d", &port); err != nil {
		t.Fatal(err)
	}
	return port
}
