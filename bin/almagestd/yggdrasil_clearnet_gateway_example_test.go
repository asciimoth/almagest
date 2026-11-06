package main

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/asciimoth/almagest/lib/testkit/e2etest"
	"github.com/asciimoth/almagest/lib/testkit/httpe2etest"
	"github.com/asciimoth/configer/configer"
)

func TestYggdrasilClearnetGatewayExample(t *testing.T) {
	const example = "044-yggdrasil-clearnet-gateway"
	const gatewayIP = "202:b29a:28f5:f219:a87a:6e8f:2b59:6597"
	const gatewayAddress = "[" + gatewayIP + "]:1080"
	const peerURL = "tcp://127.0.0.1:25440"

	store, err := openExampleStore("../../examples")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.readManifest(example); err != nil {
		t.Fatalf("read example manifest: %v", err)
	}
	directory := filepath.Join("../../examples", example)
	data, err := os.ReadFile(filepath.Join(directory, "config.hujson"))
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := parseEditableConfig(data)
	if err != nil {
		t.Fatal(err)
	}
	nodeValue := func(name, key string) string {
		t.Helper()
		return configer.String(cfg, configer.Path{"nodes", name, key}, "")
	}
	links := func(name string) []string {
		t.Helper()
		raw, err := cfg.Get(configer.Path{"nodes", name, "Links"})
		if err != nil {
			t.Fatal(err)
		}
		values, ok := raw.([]any)
		if !ok {
			t.Fatalf("%s links have type %T", name, raw)
		}
		out := make([]string, 0, len(values))
		for _, value := range values {
			link, ok := value.(string)
			if !ok {
				t.Fatalf("%s link has type %T", name, value)
			}
			out = append(out, link)
		}
		return out
	}
	for _, want := range []struct{ name, link string }{
		{"HTTP Client", "Network -> [SOCKS Client] Network"},
		{"SOCKS Client", "out -> [Client IP Stack] Network"},
		{"Client IP Stack", "Tun -> [Client Yggdrasil] Tun"},
		{"Client Yggdrasil", "default -> [Carrier Network] Network"},
		{"Server Yggdrasil", "default -> [Carrier Network] Network"},
		{"Server IP Stack", "Tun -> [Server Yggdrasil] Tun"},
		{"SOCKS Gateway", "listen -> [Server IP Stack] Network"},
		{"SOCKS Gateway", "proxy -> [Clearnet] Network"},
	} {
		if !slices.Contains(links(want.name), want.link) {
			t.Fatalf("%s is missing link %q", want.name, want.link)
		}
	}
	if nodeValue("Carrier Network", "Class") != "almagest/MockNetwork" ||
		nodeValue("Clearnet", "Class") != "almagest/OutNetwork" {
		t.Fatal("carrier or clearnet network has the wrong class")
	}
	if nodeValue("HTTP Client", "url") != "http://example.com/" {
		t.Fatalf("HTTP target = %q", nodeValue("HTTP Client", "url"))
	}
	if nodeValue("SOCKS Client", "proxyURL") != "socks5://"+gatewayAddress ||
		nodeValue("SOCKS Gateway", "socksHost") != gatewayIP ||
		configer.Int(cfg, configer.Path{"nodes", "SOCKS Gateway", "socksPort"}, 0) != 1080 {
		t.Fatal("SOCKS client and gateway addresses differ")
	}
	if nodeValue("Client Yggdrasil", "peers") != peerURL || nodeValue("Server Yggdrasil", "listen") != peerURL {
		t.Fatal("Yggdrasil peer and listener addresses differ")
	}
	rawNodes, err := cfg.Get(configer.Path{"nodes"})
	if err != nil {
		t.Fatal(err)
	}
	nodes, ok := rawNodes.(map[string]any)
	if !ok {
		t.Fatalf("nodes have type %T", rawNodes)
	}
	yggCount := 0
	for name := range nodes {
		if nodeValue(name, "Class") == "almagest/Yggdrasil" {
			yggCount++
		}
	}
	if yggCount != 2 {
		t.Fatalf("Yggdrasil node count = %d, want 2", yggCount)
	}
	if slices.Contains(links("SOCKS Client"), "out -> [Clearnet] Network") ||
		slices.Contains(links("Client Yggdrasil"), "default -> [Clearnet] Network") {
		t.Fatal("client has a direct clearnet link")
	}

	// Copy the database because opening it can write to the file.
	database, err := os.ReadFile(filepath.Join(directory, "files", "kv.db"))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "kv.db")
	if err := os.WriteFile(path, database, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := validateBboltDatabase(path); err != nil {
		t.Fatalf("validate example database: %v", err)
	}
	manager, err := openKVManager(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := manager.Close(); err != nil {
			t.Error(err)
		}
	})
	secrets := e2etest.NewSecretsStorage()
	for name, key := range map[string]string{
		"Client Yggdrasil": "ygg-example-client-key",
		"Server Yggdrasil": "ygg-example-server-key",
	} {
		if got := nodeValue(name, "privateKeySecret"); got != key {
			t.Fatalf("%s identity selector = %q", name, got)
		}
		secret, err := manager.GetSecret(key, name)
		if err != nil {
			t.Fatalf("read %s identity: %v", name, err)
		}
		secrets.SetSecret(key, secret, name)
	}

	// The debug OutNetwork is a loopback network. Put a test HTTP server on
	// example.com there, so the request keeps its saved URL and the test does
	// not depend on an external site.
	if err := cfg.Set(configer.Path{"nodes", "Test Clearnet Server"}, map[string]any{
		"Class":    "almagest/HttpServer",
		"Links":    []any{"Network -> [Clearnet] Network"},
		"host":     "example.com",
		"port":     80,
		"response": "Response through the Yggdrasil SOCKS gateway",
	}); err != nil {
		t.Fatal(err)
	}
	e := e2etest.NewNodeE2EWithConfigAndSecrets(t, cfg, secrets)
	nodeID := func(name string) uint64 {
		t.Helper()
		id, ok := e.Engine.NodeByName("main", name)
		if !ok {
			t.Fatalf("missing node %q", name)
		}
		return id
	}
	client := nodeID("HTTP Client")
	gateway := nodeID("SOCKS Gateway")
	server := nodeID("Test Clearnet Server")
	clientYgg := nodeID("Client Yggdrasil")
	serverYgg := nodeID("Server Yggdrasil")
	e.SubscribeMenus(client, gateway, server, clientYgg, serverYgg)
	e.WaitNodeLabel(clientYgg, "up")
	e.WaitNodeLabel(serverYgg, "up")
	http := httpe2etest.NewHTTPOps(e)
	http.WaitSocksLog(gateway, "socks listening on "+gatewayAddress)
	http.WaitServerListening(server, "example.com:80")
	http.AssertClientRequestReturns(client, "Response through the Yggdrasil SOCKS gateway", 90*time.Second)
}
