package dnsnodes_test

import (
	"fmt"
	"net"
	"strconv"
	"testing"

	dnsnodes "github.com/asciimoth/almagest/lib/dns"
	"github.com/asciimoth/almagest/lib/links"
	networknodes "github.com/asciimoth/almagest/lib/network"
	"github.com/asciimoth/almagest/lib/testkit"
	"github.com/asciimoth/almagest/lib/testkit/dnstest"

	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/formular"
)

func TestDNSServerPortsMenuAndSaveConfigE2E(t *testing.T) {
	e := newDNSE2E(t)
	server := e.AddNode(dnsnodes.NodeTypeDNSServer, "server")
	e.SubscribeMenus(server)

	snapshot, ok := e.Engine.NodeSnapshot(E2EWorkspace, server)
	if !ok {
		t.Fatalf("NodeSnapshot(%d) missing", server)
	}
	if len(snapshot.LeftPorts) != 0 || len(snapshot.RightPorts) != 2 {
		t.Fatalf("DnsServer ports = left %v right %v, want no left ports and two right ports", snapshot.LeftPorts, snapshot.RightPorts)
	}
	e.assertDNSServerPort(snapshot.RightPorts[0], "right", "Network", links.TypeNetwork)
	e.assertDNSServerPort(snapshot.RightPorts[1], "right", "DNS", dnsnodes.TypeDNS)
	e.waitDNSServerConfig(server, dnsnodes.DefaultDNSServerNetwork, dnsnodes.DefaultDNSServerHost, dnsnodes.DefaultDNSServerPort)
	e.WaitNodeLabel(server, "waiting for network")

	port := httpTestPort()
	e.applyDNSServerConfig(server, "udp4", "127.0.0.1", port)
	e.WaitNodeLabel(server, "waiting for network")

	cfg := configer.NewMemory(nil)
	if err := e.Engine.SaveConfig(cfg); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	nodeCfg := cfg.View(configer.Path{"nodes", "server"})
	if got := configer.String(nodeCfg, configer.Path{dnsnodes.DNSServerNetworkField}, ""); got != "udp4" {
		t.Fatalf("saved network = %q, want udp4", got)
	}
	if got := configer.String(nodeCfg, configer.Path{dnsnodes.DNSServerHostField}, ""); got != "127.0.0.1" {
		t.Fatalf("saved host = %q, want 127.0.0.1", got)
	}
	if got := configer.Int(nodeCfg, configer.Path{dnsnodes.DNSServerPortField}, 0); got != port {
		t.Fatalf("saved port = %d, want %d", got, port)
	}
}

func TestDNSServerAttachesDNSWithoutReopeningListener(t *testing.T) {
	e := newDNSE2E(t)
	port := httpTestPort()
	nextPort := httpTestPort()
	mock := e.AddNode(networknodes.NodeTypeMockNetwork, "mock")
	static := e.AddNode(dnsnodes.NodeTypeStaticDNS, "static")
	server := e.AddNode(dnsnodes.NodeTypeDNSServer, "server")
	e.SubscribeMenus(static, server)
	e.applyStaticDNSRecords(static, "attach.test A 10.121.0.1")
	e.applyDNSServerConfig(server, "udp", "127.0.0.1", port)

	e.MustAddTypedLinkByPortName(links.TypeNetwork, mock, "left", "Network", server, "right", "Network")
	e.WaitNodeLabel(server, "waiting for DNS")
	firstWorker := dnsServerWorkerID(t, e.Engine, server)

	dnsLink := e.MustAddTypedLinkByPortName(dnsnodes.TypeDNS, static, "left", "DNS", server, "right", "DNS")
	e.waitDNSServerListening(server, "127.0.0.1", port)
	if got := dnsServerWorkerID(t, e.Engine, server); got != firstWorker {
		t.Fatalf("DNS attach replaced worker %d with %d", firstWorker, got)
	}

	e.applyDNSServerConfig(server, "udp", "127.0.0.1", nextPort)
	e.waitDNSServerListening(server, "127.0.0.1", nextPort)
	secondWorker := dnsServerWorkerID(t, e.Engine, server)
	if secondWorker == firstWorker {
		t.Fatalf("DNS listen config kept worker %d, want replacement", firstWorker)
	}

	e.Engine.RemoveLink(E2EWorkspace, dnsLink)
	e.WaitNodeLabel(server, "waiting for DNS")
	if got := dnsServerWorkerID(t, e.Engine, server); got != secondWorker {
		t.Fatalf("DNS detach replaced worker %d with %d", secondWorker, got)
	}
}

func TestDNSServerServesStaticDNSOverNetworkE2E(t *testing.T) {
	e := newDNSE2E(t)
	graph := e.setupDNSServerGraph("serve", "served.test", "10.122.0.1", httpTestPort())

	e.sendDNSRequest(graph.query)
	e.waitDNSResponse(graph.query, "NOERROR", "10.122.0.1")
	e.waitDNSServerListening(graph.server, "127.0.0.1", graph.port)
}

func TestDNSServerWorksThroughDnsCacheE2E(t *testing.T) {
	e := newDNSE2EWithDNSCacheManager(t, configer.NewMemory(nil), newMemoryDNSCacheManager())
	port := httpTestPort()
	mock := e.AddNode(networknodes.NodeTypeMockNetwork, "cache-mock")
	static := e.AddNode(dnsnodes.NodeTypeStaticDNS, "cache-static")
	cache := e.AddNode(dnsnodes.NodeTypeDnsCache, "cache")
	server := e.AddNode(dnsnodes.NodeTypeDNSServer, "cache-server")
	client := e.AddNode(dnsnodes.NodeTypeDNSClient, "cache-client")
	query := e.AddNode(dnsnodes.NodeTypeDNSQuery, "cache-query")
	e.SubscribeMenus(static, cache, server, client, query)

	e.applyStaticDNSRecords(static, "server-cache.test A 10.123.0.1")
	e.applyDNSCacheConfig(cache, "server-cache-storage", false, false)
	e.applyDNSServerConfig(server, "udp", "127.0.0.1", port)
	e.applyDNSClientServers(client, fmt.Sprintf("udp://127.0.0.1:%d", port))
	e.setDNSQueryRequest(query, "server-cache.test.", "A")

	e.MustAddTypedLinkByPortName(links.TypeNetwork, mock, "left", "Network", server, "right", "Network")
	e.MustAddTypedLinkByPortName(dnsnodes.TypeDNS, static, "left", "DNS", cache, "right", "DNS")
	e.MustAddTypedLinkByPortName(dnsnodes.TypeDNS, cache, "left", "DNS", server, "right", "DNS")
	e.MustAddTypedLinkByPortName(links.TypeNetwork, mock, "left", "Network", client, "right", "Network")
	e.MustAddTypedLinkByPortName(dnsnodes.TypeDNS, client, "left", "DNS", query, "right", "DNS")
	e.WaitNodeLabel(cache, "caching DNS")
	e.waitDNSServerListening(server, "127.0.0.1", port)

	e.sendDNSRequest(query)
	e.waitDNSResponse(query, "NOERROR", "10.123.0.1")
}

func TestDNSServerCopyPastePreservesLinksE2E(t *testing.T) {
	e := newDNSE2E(t)
	graph := e.setupDNSServerGraph("copy", "copy-server.test", "10.124.0.1", httpTestPort())
	e.sendDNSRequest(graph.query)
	e.waitDNSResponse(graph.query, "NOERROR", "10.124.0.1")

	clip := e.Engine.Copy(E2EWorkspace, []uint64{graph.mock, graph.staticDNS, graph.server, graph.client, graph.query})
	if clip == "" {
		t.Fatal("Copy returned empty clipboard")
	}
	pasted := e.Engine.Paste(E2EWorkspace, clip)
	if len(pasted) != 5 {
		t.Fatalf("Paste returned %d nodes (%v), want 5", len(pasted), pasted)
	}
	pastedServer := testkit.PastedNodeByClass(t, e.Engine, E2EWorkspace, pasted, dnsnodes.NodeTypeDNSServer)
	pastedQuery := testkit.PastedNodeByClass(t, e.Engine, E2EWorkspace, pasted, dnsnodes.NodeTypeDNSQuery)
	e.SubscribeMenus(pastedServer, pastedQuery)
	e.waitDNSServerListening(pastedServer, "127.0.0.1", graph.port)
	e.sendDNSRequest(pastedQuery)
	e.waitDNSResponse(pastedQuery, "NOERROR", "10.124.0.1")
}

func TestDNSServerSaveRestorePreservesConfigAndLinksE2E(t *testing.T) {
	e := newDNSE2E(t)
	graph := e.setupDNSServerGraph("restore", "restore-server.test", "10.125.0.1", httpTestPort())
	e.sendDNSRequest(graph.query)
	e.waitDNSResponse(graph.query, "NOERROR", "10.125.0.1")

	cfg := configer.NewMemory(nil)
	if err := e.Engine.SaveConfig(cfg); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}

	restored := newDNSE2EFromConfig(t, cfg)
	server := testkit.NodeIDByName(t, restored.Engine, E2EWorkspace, graph.serverName)
	query := testkit.NodeIDByName(t, restored.Engine, E2EWorkspace, graph.queryName)
	restored.SubscribeMenus(server, query)
	restored.waitDNSServerConfig(server, "udp", "127.0.0.1", graph.port)
	restored.waitDNSServerListening(server, "127.0.0.1", graph.port)
	restored.sendDNSRequest(query)
	restored.waitDNSResponse(query, "NOERROR", "10.125.0.1")
}

type dnsServerE2EGraph struct {
	mock       uint64
	staticDNS  uint64
	server     uint64
	client     uint64
	query      uint64
	serverName string
	queryName  string
	port       int
}

func (e *dnsE2E) setupDNSServerGraph(prefix, host, ip string, port int) dnsServerE2EGraph {
	e.T.Helper()
	graph := dnsServerE2EGraph{
		mock:       e.AddNode(networknodes.NodeTypeMockNetwork, prefix+"-mock"),
		staticDNS:  e.AddNode(dnsnodes.NodeTypeStaticDNS, prefix+"-static"),
		serverName: prefix + "-server",
		client:     e.AddNode(dnsnodes.NodeTypeDNSClient, prefix+"-client"),
		queryName:  prefix + "-query",
		port:       port,
	}
	graph.server = e.AddNode(dnsnodes.NodeTypeDNSServer, graph.serverName)
	graph.query = e.AddNode(dnsnodes.NodeTypeDNSQuery, graph.queryName)
	e.SubscribeMenus(graph.staticDNS, graph.server, graph.client, graph.query)

	e.applyStaticDNSRecords(graph.staticDNS, fmt.Sprintf("%s A %s", host, ip))
	e.applyDNSServerConfig(graph.server, "udp", "127.0.0.1", port)
	e.applyDNSClientServers(graph.client, fmt.Sprintf("udp://127.0.0.1:%d", port))
	e.setDNSQueryRequest(graph.query, host+".", "A")

	e.MustAddTypedLinkByPortName(links.TypeNetwork, graph.mock, "left", "Network", graph.server, "right", "Network")
	e.MustAddTypedLinkByPortName(dnsnodes.TypeDNS, graph.staticDNS, "left", "DNS", graph.server, "right", "DNS")
	e.MustAddTypedLinkByPortName(links.TypeNetwork, graph.mock, "left", "Network", graph.client, "right", "Network")
	e.MustAddTypedLinkByPortName(dnsnodes.TypeDNS, graph.client, "left", "DNS", graph.query, "right", "DNS")
	e.waitDNSServerListening(graph.server, "127.0.0.1", port)
	e.WaitNodeLabel(graph.client, "1 DNS server")
	return graph
}

func (e *dnsE2E) applyDNSServerConfig(node uint64, network, host string, port int) {
	e.T.Helper()
	e.Engine.SendNodeFormularMsg(E2EWorkspace, node, formular.FormApplyMessage{
		MessageBase: FormMessageBase(formular.MessageFormApply, node),
		BlockID:     dnsnodes.DNSServerConfigBlockID,
		Values: map[string]any{
			dnsnodes.DNSServerNetworkField: network,
			dnsnodes.DNSServerHostField:    host,
			dnsnodes.DNSServerPortField:    port,
		},
	})
	e.waitDNSServerConfig(node, dnsnodes.NormalizeDNSServerNetwork(network), host, port)
}

func (e *dnsE2E) waitDNSServerConfig(node uint64, network, host string, port int) {
	e.T.Helper()
	Eventually(e.T, func() bool {
		return e.dnsServerFieldString(node, dnsnodes.DNSServerNetworkField) == network &&
			e.dnsServerFieldString(node, dnsnodes.DNSServerHostField) == host &&
			e.dnsServerFieldInt(node, dnsnodes.DNSServerPortField) == port
	}, "DnsServer config %s %s:%d", network, host, port)
}

func (e *dnsE2E) waitDNSServerListening(node uint64, host string, port int) {
	e.T.Helper()
	e.WaitNodeLabel(node, "listening "+net.JoinHostPort(host, strconv.Itoa(port)))
}

func (e *dnsE2E) dnsServerFieldString(node uint64, field string) string {
	item, ok := e.MenuItem(node, dnsnodes.DNSServerConfigBlockID, field)
	if !ok || item.Field == nil {
		return ""
	}
	value, _ := formular.StringValue(item.Value)
	return value
}

func (e *dnsE2E) dnsServerFieldInt(node uint64, field string) int {
	item, ok := e.MenuItem(node, dnsnodes.DNSServerConfigBlockID, field)
	if !ok || item.Field == nil {
		return 0
	}
	value, _ := formular.IntValue(item.Value)
	return value
}

func (e *dnsE2E) assertDNSServerPort(port uint64, direction, name, typ string) {
	e.T.Helper()
	snapshot, ok := e.Engine.PortSnapshot(E2EWorkspace, port)
	if !ok {
		e.T.Fatalf("PortSnapshot(%d) missing", port)
	}
	if snapshot.Direction != direction || snapshot.Name != name || len(snapshot.Types) != 1 || snapshot.Types[0] != typ {
		e.T.Fatalf("port %d = %#v, want %s %q %s", port, snapshot, direction, name, typ)
	}
}

func (e *dnsE2E) applyDNSClientServers(node uint64, servers ...string) {
	e.T.Helper()
	e.Engine.SendNodeFormularMsg(E2EWorkspace, node, formular.FormApplyMessage{
		MessageBase: FormMessageBase(formular.MessageFormApply, node),
		BlockID:     dnsnodes.DNSClientConfigBlockID,
		Values: map[string]any{
			dnsnodes.DNSClientServersField: dnstest.DNSClientServerValues(servers...),
		},
	})
	e.waitDNSClientServers(node, dnsnodes.NormalizeDNSClientServers(servers))
}

func (e *dnsE2E) waitDNSClientServers(node uint64, want []string) {
	e.T.Helper()
	Eventually(e.T, func() bool {
		item, ok := e.MenuItem(node, dnsnodes.DNSClientConfigBlockID, dnsnodes.DNSClientServersField)
		if !ok || item.Field == nil {
			return false
		}
		got := dnstest.DNSClientServerElementValues(e.T, item.Elements)
		if len(got) != len(want) {
			return false
		}
		for i := range got {
			if got[i] != want[i] {
				return false
			}
		}
		return true
	}, "DnsClient servers %v", want)
}

func dnsServerWorkerID(t *testing.T, engine Engine, node uint64) uint64 {
	t.Helper()
	var id uint64
	Eventually(t, func() bool {
		workers, ok := engine.WorkersSnapshot(E2EWorkspace)
		if !ok {
			return false
		}
		id = 0
		for workerID, worker := range workers {
			if worker.Node == node && worker.WorkerName == "dns server" {
				if id != 0 {
					return false
				}
				id = workerID
			}
		}
		return id != 0
	}, "DNS server worker for node %d", node)
	return id
}
