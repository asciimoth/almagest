package yggdrasilnodes_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/asciimoth/almagest/lib/links"
	"github.com/asciimoth/almagest/lib/storage"
	"github.com/asciimoth/almagest/lib/testkit"
	"github.com/asciimoth/almagest/lib/testkit/e2etest"
	"github.com/asciimoth/almagest/lib/testkit/httpe2etest"
	ygg "github.com/asciimoth/almagest/lib/yggdrasil"
	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/formular"
)

const workspace = "main"

func apply(e *e2etest.NodeE2E, id uint64, block string, values map[string]any) {
	e.Engine.SendNodeFormularMsg(workspace, id, formular.FormApplyMessage{MessageBase: testkit.FormMessageBase(formular.MessageFormApply, id), BlockID: block, Values: values})
}
func field(e *e2etest.NodeE2E, id uint64, block, key string) string {
	item, ok := e.MenuItem(id, block, key)
	if !ok {
		return ""
	}
	v, _ := formular.StringValue(item.Value)
	return v
}
func address(e *e2etest.NodeE2E, id uint64) string {
	e.T.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if v := field(e, id, "status", "address"); v != "" {
			return v
		}
		time.Sleep(10 * time.Millisecond)
	}
	e.T.Fatal("Yggdrasil did not publish its address")
	return ""
}

type pair struct {
	e                                               *e2etest.NodeE2E
	h                                               *httpe2etest.HTTPOps
	a, b, va, vb, carrier, netLink, tunLink, client uint64
	addrA, addrB                                    string
}

func makePair(e *e2etest.NodeE2E, prefix string, carrierA, carrierB uint64, host string, tunFirst bool, schemes ...string) pair {
	e.T.Helper()
	p := pair{e: e, h: httpe2etest.NewHTTPOps(e), carrier: carrierA}
	p.a = e.AddNode(ygg.NodeTypeYggdrasil, prefix+"a")
	p.b = e.AddNode(ygg.NodeTypeYggdrasil, prefix+"b")
	p.va = e.AddNode("almagest/VTun", prefix+"va")
	p.vb = e.AddNode("almagest/VTun", prefix+"vb")
	e.SubscribeMenus(p.a, p.b, p.va, p.vb)
	p.addrA = address(e, p.a)
	p.addrB = address(e, p.b)
	scheme := "tcp"
	if len(schemes) > 0 {
		scheme = schemes[0]
	}
	endpoint := fmt.Sprintf("%s://%s:%d", scheme, host, e2etest.TestPort())
	apply(e, p.b, "config", map[string]any{"listen": endpoint})
	apply(e, p.a, "config", map[string]any{"peers": endpoint})
	tunLinks := func() {
		p.tunLink = e.MustAddTypedLinkByPortName(links.TypeTun, p.a, "left", "Tun", p.va, "right", "Tun")
		e.MustAddTypedLinkByPortName(links.TypeTun, p.b, "left", "Tun", p.vb, "right", "Tun")
	}
	netLinks := func() {
		p.netLink = e.MustAddTypedLinkByPortName(links.TypeNetwork, carrierA, "left", "Network", p.a, "right", "default")
		e.MustAddTypedLinkByPortName(links.TypeNetwork, carrierB, "left", "Network", p.b, "right", "default")
	}
	if tunFirst {
		tunLinks()
		netLinks()
	} else {
		netLinks()
		tunLinks()
	}
	e.WaitNodeLabel(p.a, "up")
	e.WaitNodeLabel(p.b, "up")
	return p
}
func (p *pair) http() {
	e := p.e
	server := e.AddNode("almagest/HttpServer", fmt.Sprintf("server-%d", p.b))
	port := e2etest.TestPort()
	apply(e, server, "listen", map[string]any{"host": p.addrB, "port": port})
	p.client = e.AddNode("almagest/HttpClient", fmt.Sprintf("client-%d", p.b))
	target := fmt.Sprintf("http://[%s]:%d/", p.addrB, port)
	e.Engine.SendNodeFormularMsg(workspace, p.client, formular.FieldUpdateMessage{MessageBase: testkit.FormMessageBase(formular.MessageFieldUpdate, p.client), Field: formular.FieldRef{BlockID: "request", FieldID: "url"}, Value: target})
	e.SubscribeMenus(server, p.client)
	e.MustAddTypedLinkByPortName(links.TypeNetwork, p.va, "left", "Network", p.client, "right", "Network")
	e.MustAddTypedLinkByPortName(links.TypeNetwork, p.vb, "left", "Network", server, "right", "Network")
	p.h.WaitServerListening(server, fmt.Sprintf("[%s]:%d", p.addrB, port))
	p.check()
}
func (p *pair) check() {
	p.e.T.Helper()
	p.h.AssertClientRequestReturns(p.client, "Debug http server response", 90*time.Second)
}
func TestYggdrasilMockNetworkLifecycle(t *testing.T) {
	for _, tunFirst := range []bool{true, false} {
		t.Run(fmt.Sprintf("tunFirst=%t", tunFirst), func(t *testing.T) {
			e := e2etest.NewNodeE2E(t)
			carrier := e.AddNode("almagest/MockNetwork", "carrier")
			p := makePair(e, "", carrier, carrier, "127.0.0.1", tunFirst)
			p.http()
			secret := field(e, p.a, "config", "privateKeySecret")
			for _, order := range [][]string{{"network"}, {"tun"}, {"network", "tun"}, {"tun", "network"}} {
				for _, what := range order {
					if what == "network" {
						e.Engine.RemoveLink(workspace, p.netLink)
					} else {
						e.Engine.RemoveLink(workspace, p.tunLink)
					}
				}
				for i := len(order) - 1; i >= 0; i-- {
					if order[i] == "network" {
						p.netLink = e.MustAddTypedLinkByPortName(links.TypeNetwork, carrier, "left", "Network", p.a, "right", "default")
					} else {
						p.tunLink = e.MustAddTypedLinkByPortName(links.TypeTun, p.a, "left", "Tun", p.va, "right", "Tun")
					}
				}
				e.WaitNodeLabel(p.a, "up")
				p.check()
				if address(e, p.a) != p.addrA {
					t.Fatal("rewiring changed identity")
				}
			}
			apply(e, p.a, "config", map[string]any{"enabled": false})
			e.WaitNodeLabel(p.a, "disabled")
			apply(e, p.a, "config", map[string]any{"enabled": true})
			e.WaitNodeLabel(p.a, "up")
			p.check()
			apply(e, p.a, "config", map[string]any{"peers": "invalid"})
			if field(e, p.a, "config", "peers") == "invalid" {
				t.Fatal("invalid menu edit was accepted")
			}
			p.check()
			saved := configer.NewMemory(nil)
			if err := e.Engine.SaveConfig(saved); err != nil {
				t.Fatal(err)
			}
			if got := configer.String(saved, configer.Path{"nodes", "a", "privateKeySecret"}, ""); got != secret {
				t.Fatalf("saved secret = %q, want %q", got, secret)
			}
			if _, err := saved.Get(configer.Path{"nodes", "a", "privateKey"}); err == nil {
				t.Fatal("raw identity was saved")
			}
			e.Engine.RemoveNode(workspace, p.a)
			if _, ok := e.Engine.NodeSnapshot(workspace, p.a); ok {
				t.Fatal("removed node remains")
			}
			fresh := e.AddNode(ygg.NodeTypeYggdrasil, "a")
			e.SubscribeMenus(fresh)
			if address(e, fresh) == p.addrA {
				t.Fatal("new node reused removed node identity")
			}
		})
	}
}
func TestYggdrasilNested(t *testing.T) {
	e := e2etest.NewNodeE2E(t)
	carrier := e.AddNode("almagest/MockNetwork", "carrier")
	outer := makePair(e, "outer-", carrier, carrier, "127.0.0.1", true)
	outer.http()
	inner := makePair(e, "inner-", outer.va, outer.vb, "["+outer.addrB+"]", false)
	inner.http()
	e.Engine.RemoveLink(workspace, outer.netLink)
	e.WaitNodeLabel(outer.a, "waiting for network")
	outer.netLink = e.MustAddTypedLinkByPortName(links.TypeNetwork, carrier, "left", "Network", outer.a, "right", "default")
	e.WaitNodeLabel(outer.a, "up")
	inner.check()
}
func TestYggdrasilMultipleUpstreamNetworks(t *testing.T) {
	e := e2etest.NewNodeE2E(t)
	first := e.AddNode("almagest/MockNetwork", "first")
	second := e.AddNode("almagest/MockNetwork", "second")
	p := makePair(e, "", first, first, "127.0.0.1", false)
	p.http()
	// A second independent carrier reaches another mesh node. A host mapping
	// must use that carrier while the existing default connection stays usable.
	c := e.AddNode(ygg.NodeTypeYggdrasil, "c")
	vc := e.AddNode("almagest/VTun", "vc")
	e.SubscribeMenus(c)
	addrC := address(e, c)
	endpoint := fmt.Sprintf("tcp://127.0.0.2:%d", e2etest.TestPort())
	apply(e, c, "config", map[string]any{"listen": endpoint})
	e.MustAddTypedLinkByPortName(links.TypeTun, c, "left", "Tun", vc, "right", "Tun")
	e.MustAddTypedLinkByPortName(links.TypeNetwork, second, "left", "Network", c, "right", "default")
	original := field(e, p.a, "config", "peers")
	apply(e, p.a, "config", map[string]any{"networks": "second=127.0.0.2", "peers": original + "\n" + endpoint})
	deadline := time.Now().Add(5 * time.Second)
	for field(e, p.a, "config", "networks") != "second=127.0.0.2" && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	mapping := e.MustAddTypedLinkByPortName(links.TypeNetwork, second, "left", "Network", p.a, "right", "second")
	other := pair{e: e, h: p.h, a: p.a, b: c, va: p.va, vb: vc, addrB: addrC}
	other.http()
	p.check()
	e.Engine.RemoveLink(workspace, mapping)
	p.check()
	e.MustAddTypedLinkByPortName(links.TypeNetwork, second, "left", "Network", p.a, "right", "second")
	other.check()
	// Edit a mapping without detaching its graph link, then restore it. The
	// same network wrapper must move with the port to the new host pattern.
	apply(e, p.a, "config", map[string]any{"networks": "second=127.0.0.3"})
	deadline = time.Now().Add(5 * time.Second)
	for field(e, p.a, "config", "networks") != "second=127.0.0.3" && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	apply(e, p.a, "config", map[string]any{"networks": "second=127.0.0.2"})
	other.check()
	apply(e, p.a, "config", map[string]any{"networks": "", "peers": original})
	p.check()
	snap, _ := e.Engine.NodeSnapshot(workspace, p.a)
	if len(snap.RightPorts) != 1 {
		t.Fatal("removed mapping port remains")
	}
	if strings.Contains(field(e, p.a, "config", "privateKeySecret"), "\n") {
		t.Fatal("invalid secret selector")
	}
}

func TestYggdrasilTLS(t *testing.T) {
	e := e2etest.NewNodeE2E(t)
	carrier := e.AddNode("almagest/MockNetwork", "carrier")
	p := makePair(e, "tls-", carrier, carrier, "127.0.0.1", true, "tls")
	p.http()
}

func TestYggdrasilSaveRestoreIdentityAndMappings(t *testing.T) {
	e := e2etest.NewNodeE2E(t)
	id := e.AddNode(ygg.NodeTypeYggdrasil, "saved")
	e.SubscribeMenus(id)
	original := address(e, id)
	apply(e, id, "config", map[string]any{"networks": "onion=*.onion\ni2p=*.i2p", "peers": "tls://example.test:443"})
	deadline := time.Now().Add(5 * time.Second)
	for field(e, id, "config", "networks") != "onion=*.onion\ni2p=*.i2p" && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	saved := configer.NewMemory(nil)
	if err := e.Engine.SaveConfig(saved); err != nil {
		t.Fatal(err)
	}
	restored := e2etest.NewNodeE2EWithConfigAndSecrets(t, saved, e.Secrets)
	node, ok := restored.Engine.NodeByName(workspace, "saved")
	if !ok {
		t.Fatal("restored node missing")
	}
	restored.SubscribeMenus(node)
	if got := address(restored, node); got != original {
		t.Fatalf("restored address %s, want %s", got, original)
	}
	for _, name := range []string{"default", "onion", "i2p"} {
		_ = restored.NodePort(node, "right", name)
	}
	if got := field(restored, node, "config", "peers"); got != "tls://example.test:443" {
		t.Fatalf("restored peers %q", got)
	}
}

func TestYggdrasilTunProviderUnavailable(t *testing.T) {
	e := e2etest.NewNodeE2E(t)
	carrier := e.AddNode("almagest/MockNetwork", "carrier")
	p := makePair(e, "", carrier, carrier, "127.0.0.1", true)
	p.http()
	e.Engine.RemoveLink(workspace, p.tunLink)
	middleware := e.AddNode("almagest/LogTun", "tun-log")
	upstream := e.MustAddTypedLinkByPortName(links.TypeTun, middleware, "left", "in", p.va, "right", "Tun")
	e.MustAddTypedLinkByPortName(links.TypeTun, p.a, "left", "Tun", middleware, "right", "Tun")
	e.WaitNodeLabel(p.a, "up")
	p.check()
	e.Engine.RemoveLink(workspace, upstream)
	e.WaitNodeLabel(p.a, "waiting for TUN")
	e.MustAddTypedLinkByPortName(links.TypeTun, middleware, "left", "in", p.va, "right", "Tun")
	e.WaitNodeLabel(p.a, "up")
	p.check()
}

func TestYggdrasilMenuIdentityValidation(t *testing.T) {
	e := e2etest.NewNodeE2E(t)
	id := e.AddNode(ygg.NodeTypeYggdrasil, "identity")
	e.SubscribeMenus(id)
	original := address(e, id)
	selector := field(e, id, "config", "privateKeySecret")
	count := e.Secrets.SecretCount()
	e.Secrets.SetSecret("invalid", storage.Secret{Value: "bad-key"}, "identity")
	for _, secret := range []string{"missing", "invalid"} {
		apply(e, id, "config", map[string]any{"privateKeySecret": secret})
		deadline := time.Now().Add(time.Second)
		for time.Now().Before(deadline) {
			item, _ := e.MenuItem(id, "config", "privateKeySecret")
			if item.Field != nil && item.StatusText != "" {
				break
			}
			time.Sleep(time.Millisecond)
		}
		if got := field(e, id, "config", "privateKeySecret"); got != selector {
			t.Fatalf("invalid secret changed selector to %q", got)
		}
		if address(e, id) != original {
			t.Fatal("invalid secret changed identity")
		}
	}
	if got := e.Secrets.SecretCount(); got != count+1 {
		t.Fatalf("invalid edits generated secrets: %d", got)
	}
	e.Secrets.SetSecret("chosen", storage.Secret{Value: strings.Repeat("01", 32)}, "identity")
	apply(e, id, "config", map[string]any{"privateKeySecret": "chosen"})
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && field(e, id, "status", "address") == original {
		time.Sleep(time.Millisecond)
	}
	if got := field(e, id, "status", "address"); got == "" || got == original {
		t.Fatal("valid identity edit was not applied")
	}
	if got := field(e, id, "config", "privateKeySecret"); got != "chosen" {
		t.Fatal("valid selector was not retained", got)
	}
}
