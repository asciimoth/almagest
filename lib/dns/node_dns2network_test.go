package dnsnodes_test

import (
	"fmt"
	dnsnodes "github.com/asciimoth/almagest/lib/dns"
	"github.com/asciimoth/almagest/lib/links"
	networknodes "github.com/asciimoth/almagest/lib/network"
	"github.com/asciimoth/almagest/lib/testkit"
	"testing"

	"github.com/asciimoth/configer/configer"
)

func TestDns2NetworkCopyPastePreservesLinks(t *testing.T) {
	e := newDNSE2E(t)
	graph := e.setupDns2NetworkInjectGraph("copy.test", "10.60.0.1", "copy")
	e.sendDNSRequest(graph.client)
	e.waitDNSResponse(graph.client, "NOERROR", "10.60.0.1")

	clip := e.Engine.Copy(E2EWorkspace, []uint64{graph.mock, graph.staticDNS, graph.inject, graph.adapter, graph.client})
	if clip == "" {
		t.Fatal("Copy returned empty clipboard")
	}
	pasted := e.Engine.Paste(E2EWorkspace, clip)
	if len(pasted) != 5 {
		t.Fatalf("Paste returned %d nodes (%v), want 5", len(pasted), pasted)
	}
	pastedClient := testkit.PastedNodeByClass(t, e.Engine, E2EWorkspace, pasted, dnsnodes.NodeTypeDNSQuery)
	e.SubscribeMenus(pastedClient)
	e.sendDNSRequest(pastedClient)
	e.waitDNSResponse(pastedClient, "NOERROR", "10.60.0.1")
}

func TestDns2NetworkSaveRestorePreservesLinks(t *testing.T) {
	e := newDNSE2E(t)
	graph := e.setupDns2NetworkInjectGraph("restore.test", "10.70.0.1", "restore")
	e.sendDNSRequest(graph.client)
	e.waitDNSResponse(graph.client, "NOERROR", "10.70.0.1")

	cfg := configer.NewMemory(nil)
	if err := e.Engine.SaveConfig(cfg); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	restored := newDNSE2EFromConfig(t, cfg)
	client := testkit.NodeIDByName(t, restored.Engine, E2EWorkspace, graph.clientName)
	restored.SubscribeMenus(client)
	restored.sendDNSRequest(client)
	restored.waitDNSResponse(client, "NOERROR", "10.70.0.1")
}

func TestDns2NetworkWorksWithDnsInject(t *testing.T) {
	e := newDNSE2E(t)
	graph := e.setupDns2NetworkInjectGraph("inject.test", "10.80.0.1", "inject")
	e.sendDNSRequest(graph.client)
	e.waitDNSResponse(graph.client, "NOERROR", "10.80.0.1")
	e.WaitNodeLabel(graph.inject, "injecting DNS")
	e.WaitNodeLabel(graph.adapter, "resolving DNS")
}

type dns2NetworkInjectGraph struct {
	mock       uint64
	staticDNS  uint64
	inject     uint64
	adapter    uint64
	client     uint64
	clientName string
}

func (e *dnsE2E) setupDns2NetworkInjectGraph(host, ip, prefix string) dns2NetworkInjectGraph {
	e.T.Helper()
	graph := dns2NetworkInjectGraph{
		mock:       e.AddNode(networknodes.NodeTypeMockNetwork, prefix+"-mock"),
		staticDNS:  e.AddNode(dnsnodes.NodeTypeStaticDNS, prefix+"-dns"),
		inject:     e.AddNode(dnsnodes.NodeTypeDnsInject, prefix+"-inject"),
		adapter:    e.AddNode(dnsnodes.NodeTypeDns2Network, prefix+"-adapter"),
		clientName: prefix + "-client",
	}
	graph.client = e.AddNode(dnsnodes.NodeTypeDNSQuery, graph.clientName)
	e.SubscribeMenus(graph.staticDNS, graph.client)
	e.applyStaticDNSRecords(graph.staticDNS, fmt.Sprintf("%s A %s", host, ip))
	e.setDNSQueryRequest(graph.client, host+".", "A")
	e.MustAddTypedLinkByPortName(links.TypeNetwork, graph.mock, "left", "Network", graph.inject, "right", "Network")
	e.MustAddTypedLinkByPortName(dnsnodes.TypeDNS, graph.staticDNS, "left", "DNS", graph.inject, "right", "DNS")
	e.MustAddTypedLinkByPortName(links.TypeNetwork, graph.inject, "left", "Network", graph.adapter, "right", "Network")
	e.MustAddTypedLinkByPortName(dnsnodes.TypeDNS, graph.adapter, "left", "DNS", graph.client, "right", "DNS")
	e.WaitNodeLabel(graph.inject, "injecting DNS")
	e.WaitNodeLabel(graph.adapter, "resolving DNS")
	return graph
}
