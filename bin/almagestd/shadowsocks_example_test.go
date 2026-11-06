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

func TestShadowsocksClientAndServerExample(t *testing.T) {
	const example = "045-shadowsocks-client-and-server"
	store, err := openExampleStore("../../examples")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.readManifest(example); err != nil {
		t.Fatalf("read example manifest: %v", err)
	}
	data, err := os.ReadFile(filepath.Join("../../examples", example, "config.hujson"))
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := parseEditableConfig(data)
	if err != nil {
		t.Fatal(err)
	}
	get := func(node, key string) string {
		t.Helper()
		return configer.String(cfg, configer.Path{"nodes", node, key}, "")
	}
	if get("Shadowsocks Client", "address") != get("Shadowsocks Server", "address") ||
		get("Shadowsocks Client", "method") != get("Shadowsocks Server", "method") ||
		get("Shadowsocks Client", "password") != get("Shadowsocks Server", "password") {
		t.Fatal("Shadowsocks client and server settings differ")
	}
	if get("HTTP Client", "url") != "http://service.shadowsocks.test:8080/" {
		t.Fatal("example HTTP target changed")
	}

	e := e2etest.NewNodeE2EWithConfig(t, cfg)
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
	ssClient := nodeID("Shadowsocks Client")
	ssServer := nodeID("Shadowsocks Server")
	e.SubscribeMenus(client, server, ssClient, ssServer)
	e.WaitNodeLabel(ssServer, "127.0.0.1:8388")
	e.WaitNodeLabel(ssClient, "127.0.0.1:8388")
	http := httpe2etest.NewHTTPOps(e)
	http.WaitServerListening(server, "127.0.0.1:8080")
	http.AssertClientRequestReturns(client, "Response through the Shadowsocks client and server", 10*time.Second)
}
