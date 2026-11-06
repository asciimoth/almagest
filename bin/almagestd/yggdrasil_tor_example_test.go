package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/asciimoth/almagest/lib/testkit/e2etest"
	"github.com/asciimoth/configer/configer"
)

func TestYggdrasilTorPeersExample(t *testing.T) {
	const example = "043-yggdrasil-tor-peers"
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
	node := func(name, key string) string {
		t.Helper()
		return configer.String(cfg, configer.Path{"nodes", name, key}, "")
	}
	hasLink := func(name, want string) bool {
		t.Helper()
		raw, err := cfg.Get(configer.Path{"nodes", name, "Links"})
		if err != nil {
			t.Fatal(err)
		}
		links, ok := raw.([]any)
		if !ok {
			t.Fatalf("%s links have type %T", name, raw)
		}
		values := make([]string, 0, len(links))
		for _, link := range links {
			value, ok := link.(string)
			if !ok {
				t.Fatalf("%s link has type %T", name, link)
			}
			values = append(values, value)
		}
		return slices.Contains(values, want)
	}
	for _, link := range []struct{ node, target string }{
		{"Yggdrasil Address", "Network -> [Yggdrasil IP Stack] Network"},
		{"Yggdrasil IP Stack", "Tun -> [Yggdrasil] Tun"},
		{"Yggdrasil", "default -> [Direct Network] Network"},
		{"Yggdrasil", "onion -> [Tor Client] Network default"},
		{"Tor Client", "out -> [Direct Network] Network"},
	} {
		if !hasLink(link.node, link.target) {
			t.Fatalf("%s is missing link %q", link.node, link.target)
		}
	}
	if got := node("Yggdrasil Address", "provider"); got != "yggdrasil" {
		t.Fatalf("FetchAddr provider = %q", got)
	}
	if got := node("Yggdrasil", "networks"); got != "onion=*.onion" {
		t.Fatalf("Yggdrasil upstream mapping = %q", got)
	}
	if got := node("Yggdrasil", "autoPeerSources"); !strings.Contains(got, "BUILTIN") || !strings.Contains(got, "https://asciimoth.github.io/yggpeers/peers.json") {
		t.Fatalf("public-peer sources = %q", got)
	}
	if got := node("Yggdrasil", "autoPeerCountries"); !strings.Contains(got, "tor") || !strings.Contains(got, "germany") {
		t.Fatalf("public-peer countries = %q", got)
	}
	if got := node("Yggdrasil", "autoPeerSchemes"); got != "tcp\ntls" {
		t.Fatalf("public-peer schemes = %q", got)
	}
	if got := node("Yggdrasil", "peers"); !strings.Contains(got, ".onion:") {
		t.Fatalf("onion peer = %q", got)
	}
	if got := configer.Bool(cfg, configer.Path{"nodes", "Yggdrasil", "autoPeerEnabled"}, false); !got {
		t.Fatal("autopeering is disabled")
	}

	// Start the saved graph without external peer connections. The separate
	// autopeering integration tests cover peer selection and dialing.
	if err := cfg.Set(configer.Path{"nodes", "Yggdrasil", "autoPeerEnabled"}, false); err != nil {
		t.Fatal(err)
	}
	if err := cfg.Set(configer.Path{"nodes", "Yggdrasil", "peers"}, ""); err != nil {
		t.Fatal(err)
	}
	e := e2etest.NewNodeE2EWithConfig(t, cfg)
	id, ok := e.Engine.NodeByName("main", "Yggdrasil")
	if !ok {
		t.Fatal("Yggdrasil node missing")
	}
	e.SubscribeMenus(id)
	e.WaitNodeLabel(id, "up")
	if item, ok := e.MenuItem(id, "config", "networks"); !ok || item.Value != "onion=*.onion" {
		t.Fatalf("runtime upstream mapping = %#v", item)
	}
	addressID, ok := e.Engine.NodeByName("main", "Yggdrasil Address")
	if !ok {
		t.Fatal("FetchAddr node missing")
	}
	e.SubscribeMenus(addressID)
	if item, ok := e.MenuItem(addressID, "check", "provider"); !ok || item.Value != "yggdrasil" {
		t.Fatalf("runtime FetchAddr provider = %#v", item)
	}
}
