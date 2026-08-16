package dnsnodes_test

import (
	"context"
	"testing"

	dnsnodes "github.com/asciimoth/almagest/lib/dns"
	"github.com/asciimoth/almagest/lib/testkit"
	"github.com/asciimoth/almagest/lib/testkit/dnse2etest"

	"github.com/asciimoth/configer/configer"
)

func TestStaticDNSMultipleConsumers(t *testing.T) {
	e := newDNSE2E(t)
	static := e.AddNode(dnsnodes.NodeTypeStaticDNS, "static")
	clientA := e.AddNode(dnsnodes.NodeTypeDNSQuery, "client-a")
	clientB := e.AddNode(dnsnodes.NodeTypeDNSQuery, "client-b")
	e.SubscribeMenus(static, clientA, clientB)
	e.applyStaticDNSRecords(static, "example.test A 10.10.0.1")

	e.MustAddTypedLinkByPortName(dnsnodes.TypeDNS, static, "left", "DNS", clientA, "right", "DNS")
	e.MustAddTypedLinkByPortName(dnsnodes.TypeDNS, static, "left", "DNS", clientB, "right", "DNS")

	e.sendDNSRequest(clientA)
	e.waitDNSResponse(clientA, "NOERROR", "10.10.0.1")
	e.sendDNSRequest(clientB)
	e.waitDNSResponse(clientB, "NOERROR", "10.10.0.1")
}

func TestStaticDNSFallbackChain(t *testing.T) {
	e := newDNSE2E(t)
	root := e.AddNode(dnsnodes.NodeTypeStaticDNS, "root")
	middle := e.AddNode(dnsnodes.NodeTypeStaticDNS, "middle")
	tail := e.AddNode(dnsnodes.NodeTypeStaticDNS, "tail")
	client := e.AddNode(dnsnodes.NodeTypeDNSQuery, "client")
	e.SubscribeMenus(root, middle, tail, client)
	e.applyStaticDNSRecords(root, "root.test A 10.20.0.1")
	e.applyStaticDNSRecords(tail, "tail.test A 10.20.0.3")

	e.MustAddTypedLinkByPortName(dnsnodes.TypeDNS, tail, "left", "DNS", middle, "right", "Fallback")
	e.MustAddTypedLinkByPortName(dnsnodes.TypeDNS, middle, "left", "DNS", root, "right", "Fallback")
	e.MustAddTypedLinkByPortName(dnsnodes.TypeDNS, root, "left", "DNS", client, "right", "DNS")

	e.setDNSQueryRequest(client, "tail.test.", "A")
	e.sendDNSRequest(client)
	e.waitDNSResponse(client, "NOERROR", "10.20.0.3")

	e.setDNSQueryRequest(client, "root.test.", "A")
	e.sendDNSRequest(client)
	e.waitDNSResponse(client, "NOERROR", "10.20.0.1")
}

func TestDNSLinksAttachDetachReattach(t *testing.T) {
	e := newDNSE2E(t)
	static := e.AddNode(dnsnodes.NodeTypeStaticDNS, "static")
	client := e.AddNode(dnsnodes.NodeTypeDNSQuery, "client")
	e.SubscribeMenus(static, client)
	e.applyStaticDNSRecords(static, "example.test A 10.30.0.1")

	link := e.MustAddTypedLinkByPortName(dnsnodes.TypeDNS, static, "left", "DNS", client, "right", "DNS")
	e.sendDNSRequest(client)
	e.waitDNSResponse(client, "NOERROR", "10.30.0.1")

	e.Engine.RemoveLink(E2EWorkspace, link)
	e.waitDNSReady(client)
	e.sendDNSRequest(client)
	e.waitDNSFailure(client, "dns is not ready")

	e.MustAddTypedLinkByPortName(dnsnodes.TypeDNS, static, "left", "DNS", client, "right", "DNS")
	e.waitDNSReady(client)
	e.sendDNSRequest(client)
	e.waitDNSResponse(client, "NOERROR", "10.30.0.1")
}

func TestStaticDNSCopyPastePreservesConfigAndLinks(t *testing.T) {
	e := newDNSE2E(t)
	static := e.AddNode(dnsnodes.NodeTypeStaticDNS, "static")
	client := e.AddNode(dnsnodes.NodeTypeDNSQuery, "client")
	e.SubscribeMenus(static, client)
	e.applyStaticDNSRecords(static, "copy.test A 10.40.0.1")
	e.setDNSQueryRequest(client, "copy.test.", "A")
	e.MustAddTypedLinkByPortName(dnsnodes.TypeDNS, static, "left", "DNS", client, "right", "DNS")
	e.sendDNSRequest(client)
	e.waitDNSResponse(client, "NOERROR", "10.40.0.1")

	clip := e.Engine.Copy(E2EWorkspace, []uint64{static, client})
	if clip == "" {
		t.Fatal("Copy returned empty clipboard")
	}
	pasted := e.Engine.Paste(E2EWorkspace, clip)
	if len(pasted) != 2 {
		t.Fatalf("Paste returned %d nodes (%v), want 2", len(pasted), pasted)
	}
	pastedClient := testkit.PastedNodeByClass(t, e.Engine, E2EWorkspace, pasted, dnsnodes.NodeTypeDNSQuery)
	e.SubscribeMenus(pastedClient)
	e.sendDNSRequest(pastedClient)
	e.waitDNSResponse(pastedClient, "NOERROR", "10.40.0.1")
}

func TestStaticDNSSaveRestorePreservesConfigAndLinks(t *testing.T) {
	e := newDNSE2E(t)
	static := e.AddNode(dnsnodes.NodeTypeStaticDNS, "static")
	client := e.AddNode(dnsnodes.NodeTypeDNSQuery, "client")
	e.SubscribeMenus(static, client)
	e.applyStaticDNSRecords(static, "restore.test A 10.50.0.1")
	e.setDNSQueryRequest(client, "restore.test.", "A")
	e.MustAddTypedLinkByPortName(dnsnodes.TypeDNS, static, "left", "DNS", client, "right", "DNS")

	cfg := configer.NewMemory(nil)
	if err := e.Engine.SaveConfig(cfg); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}

	restored := newDNSE2EFromConfig(t, cfg)
	restoredClient := testkit.NodeIDByName(t, restored.Engine, E2EWorkspace, "client")
	restored.SubscribeMenus(restoredClient)
	restored.sendDNSRequest(restoredClient)
	restored.waitDNSResponse(restoredClient, "NOERROR", "10.50.0.1")
}

type dnsE2E struct {
	*NodeE2E
	*dnse2etest.DNSOps
}

func newDNSE2E(t *testing.T) *dnsE2E {
	t.Helper()
	return newDNSE2EFromNodeE2E(NewNodeE2E(t))
}

func newDNSE2EFromNodeE2E(e *NodeE2E) *dnsE2E {
	return &dnsE2E{NodeE2E: e, DNSOps: dnse2etest.NewDNSOps(e)}
}

func newDNSE2EFromConfig(t *testing.T, cfg configer.Config) *dnsE2E {
	t.Helper()
	pool := newE2ETestDebugPool(t)
	logf := NewDebugLogFactory(16384)
	sys := newE2EDebugSystem()
	engine, err := BuildEngine(context.Background(), cfg, logf, sys, nil, nil, nil, nil, nil, pool, true)
	if err != nil {
		t.Fatalf("BuildEngine restore: %v", err)
	}
	t.Cleanup(func() {
		if err := engine.Close(); err != nil {
			t.Errorf("engine.Close error = %v, want nil", err)
		}
	})

	e := NewNodeE2EFromEngine(t, engine, logf, sys)
	return newDNSE2EFromNodeE2E(e)
}

func (e *dnsE2E) applyStaticDNSRecords(node uint64, records string) {
	e.ApplyStaticRecords(node, records)
}

func (e *dnsE2E) setDNSQueryRequest(client uint64, name, qtype string) {
	e.SetQueryRequest(client, name, qtype)
}

func (e *dnsE2E) sendDNSRequest(client uint64) {
	e.SendRequest(client)
}

func (e *dnsE2E) waitDNSResponse(client uint64, status string, wantParts ...string) {
	e.WaitResponse(client, status, wantParts...)
}

func (e *dnsE2E) waitDNSFailure(client uint64, wantErrPart string) {
	e.WaitFailure(client, wantErrPart)
}

func (e *dnsE2E) waitDNSReady(client uint64) {
	e.WaitReady(client)
}
