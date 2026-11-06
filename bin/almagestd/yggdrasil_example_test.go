package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/asciimoth/almagest/lib/testkit/e2etest"
	"github.com/asciimoth/almagest/lib/testkit/httpe2etest"
	"github.com/asciimoth/configer/configer"
)

func TestYggdrasilPublicPeerExample(t *testing.T) {
	const directory = "../../examples/042-yggdrasil-public-peer"
	store, err := openExampleStore("../../examples")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.readManifest("042-yggdrasil-public-peer"); err != nil {
		t.Fatalf("read example manifest: %v", err)
	}
	configData, err := os.ReadFile(filepath.Join(directory, "config.hujson"))
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := parseEditableConfig(configData)
	if err != nil {
		t.Fatal(err)
	}
	rawNodes, err := cfg.Get(configer.Path{"nodes"})
	if err != nil {
		t.Fatal(err)
	}
	nodes, ok := rawNodes.(map[string]any)
	if !ok {
		t.Fatalf("example nodes have type %T", rawNodes)
	}
	yggCount := 0
	for name := range nodes {
		if configer.String(cfg, configer.Path{"nodes", name, "Class"}, "") == "almagest/Yggdrasil" {
			yggCount++
		}
	}
	if yggCount != 3 {
		t.Fatalf("Yggdrasil node count = %d, want 3", yggCount)
	}

	// Copy the database because the daemon store can write to it when opened.
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
	for key, node := range map[string]string{
		"ygg-example-client-key": "Client Yggdrasil",
		"ygg-example-server-key": "Server Yggdrasil",
		"ygg-example-public-key": "Public Peer",
	} {
		if got := configer.String(cfg, configer.Path{"nodes", node, "privateKeySecret"}, ""); got != key {
			t.Fatalf("%s identity selector = %q, want %q", node, got, key)
		}
		secret, err := manager.GetSecret(key, node)
		if err != nil {
			t.Fatalf("read %s identity: %v", node, err)
		}
		secrets.SetSecret(key, secret, node)
	}
	peerURL := configer.String(cfg, configer.Path{"nodes", "Public Peer", "listen"}, "")
	for _, node := range []string{"Client Yggdrasil", "Server Yggdrasil"} {
		if got := configer.String(cfg, configer.Path{"nodes", node, "peers"}, ""); got != peerURL {
			t.Fatalf("%s peers = %q, want only %q", node, got, peerURL)
		}
	}
	if got := configer.String(cfg, configer.Path{"nodes", "Public Peer", "peers"}, ""); got != "" {
		t.Fatalf("public peer has outbound peer %q", got)
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
	server := nodeID("HTTP Server")
	clientYgg := nodeID("Client Yggdrasil")
	serverYgg := nodeID("Server Yggdrasil")
	public := nodeID("Public Peer")
	e.SubscribeMenus(client, server, clientYgg, serverYgg, public)
	e.WaitNodeLabel(clientYgg, "up")
	e.WaitNodeLabel(serverYgg, "up")
	e.WaitNodeLabel(public, "waiting for TUN")
	if got := configer.String(cfg, configer.Path{"nodes", "HTTP Server", "host"}, ""); got != "202:b29a:28f5:f219:a87a:6e8f:2b59:6597" {
		t.Fatalf("HTTP server host = %q", got)
	}
	http := httpe2etest.NewHTTPOps(e)
	http.WaitServerListening(server, "[202:b29a:28f5:f219:a87a:6e8f:2b59:6597]:8080")
	http.AssertClientRequestReturns(client, "Response through the Yggdrasil public peer", 90*time.Second)
}
