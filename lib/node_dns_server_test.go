package almagest

import (
	"fmt"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/formular"
	"github.com/asciimoth/gonnect"
	"github.com/asciimoth/gonnect/dns"
)

func TestDNSServerPortsMenuAndSaveConfigE2E(t *testing.T) {
	e := newDNSE2E(t)
	server := e.AddNode(NodeTypeDNSServer, "server")
	e.SubscribeMenus(server)

	snapshot, ok := e.Engine.NodeSnapshot(E2EWorkspace, server)
	if !ok {
		t.Fatalf("NodeSnapshot(%d) missing", server)
	}
	if len(snapshot.LeftPorts) != 0 || len(snapshot.RightPorts) != 2 {
		t.Fatalf("DnsServer ports = left %v right %v, want no left ports and two right ports", snapshot.LeftPorts, snapshot.RightPorts)
	}
	e.assertDNSServerPort(snapshot.RightPorts[0], "right", "Network", TypeNetwork)
	e.assertDNSServerPort(snapshot.RightPorts[1], "right", "DNS", TypeDNS)
	e.waitDNSServerConfig(server, DefaultDNSServerNetwork, DefaultDNSServerHost, DefaultDNSServerPort)
	e.WaitNodeLabel(server, "waiting for network")

	port := httpTestPort()
	e.applyDNSServerConfig(server, "udp4", "127.0.0.1", port)
	e.WaitNodeLabel(server, "waiting for network")

	cfg := configer.NewMemory(nil)
	if err := e.Engine.SaveConfig(cfg); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	nodeCfg := cfg.View(configer.Path{"nodes", "server"})
	if got := configer.String(nodeCfg, configer.Path{dnsServerNetworkField}, ""); got != "udp4" {
		t.Fatalf("saved network = %q, want udp4", got)
	}
	if got := configer.String(nodeCfg, configer.Path{dnsServerHostField}, ""); got != "127.0.0.1" {
		t.Fatalf("saved host = %q, want 127.0.0.1", got)
	}
	if got := configer.Int(nodeCfg, configer.Path{dnsServerPortField}, 0); got != port {
		t.Fatalf("saved port = %d, want %d", got, port)
	}
}

func TestDNSServerAttachesDNSWithoutReopeningListener(t *testing.T) {
	tracker := installTrackedDNSServerFactory(t)
	e := newDNSE2E(t)
	port := httpTestPort()
	nextPort := httpTestPort()
	mock := e.AddNode(NodeTypeMockNetwork, "mock")
	static := e.AddNode(NodeTypeStaticDNS, "static")
	server := e.AddNode(NodeTypeDNSServer, "server")
	e.SubscribeMenus(static, server)
	e.applyStaticDNSRecords(static, "attach.test A 10.121.0.1")
	e.applyDNSServerConfig(server, "udp", "127.0.0.1", port)

	e.MustAddTypedLinkByPortName(TypeNetwork, mock, "left", "Network", server, "right", "Network")
	e.WaitNodeLabel(server, "waiting for DNS")
	firstServer := tracker.server(t, 0)
	if tracker.count() != 1 {
		t.Fatalf("server runtime count = %d, want 1 after initial listen", tracker.count())
	}

	dnsLink := e.MustAddTypedLinkByPortName(TypeDNS, static, "left", "DNS", server, "right", "DNS")
	e.waitDNSServerListening(server, "127.0.0.1", port)
	Eventually(t, func() bool { return firstServer.attachCount() > 0 }, "DNS server upstream attach")
	if tracker.count() != 1 {
		t.Fatalf("server runtime count = %d, want 1 after DNS attach", tracker.count())
	}

	e.applyDNSServerConfig(server, "udp", "127.0.0.1", nextPort)
	e.waitDNSServerListening(server, "127.0.0.1", nextPort)
	Eventually(t, func() bool { return tracker.count() == 2 }, "second DNS server runtime")
	Eventually(t, func() bool { return firstServer.closeCount() > 0 }, "old DNS server runtime close")

	secondServer := tracker.server(t, 1)
	e.Engine.RemoveLink(E2EWorkspace, dnsLink)
	e.WaitNodeLabel(server, "waiting for DNS")
	Eventually(t, func() bool { return secondServer.detachCount() > 0 }, "DNS server upstream detach")
	if tracker.count() != 2 {
		t.Fatalf("server runtime count = %d, want 2 after DNS detach", tracker.count())
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
	mock := e.AddNode(NodeTypeMockNetwork, "cache-mock")
	static := e.AddNode(NodeTypeStaticDNS, "cache-static")
	cache := e.AddNode(NodeTypeDnsCache, "cache")
	server := e.AddNode(NodeTypeDNSServer, "cache-server")
	client := e.AddNode(NodeTypeDNSClient, "cache-client")
	query := e.AddNode(NodeTypeDNSQuery, "cache-query")
	e.SubscribeMenus(static, cache, server, client, query)

	e.applyStaticDNSRecords(static, "server-cache.test A 10.123.0.1")
	e.applyDNSCacheConfig(cache, "server-cache-storage", false, false)
	e.applyDNSServerConfig(server, "udp", "127.0.0.1", port)
	e.applyDNSClientServers(client, fmt.Sprintf("udp://127.0.0.1:%d", port))
	e.setDNSQueryRequest(query, "server-cache.test.", "A")

	e.MustAddTypedLinkByPortName(TypeNetwork, mock, "left", "Network", server, "right", "Network")
	e.MustAddTypedLinkByPortName(TypeDNS, static, "left", "DNS", cache, "right", "DNS")
	e.MustAddTypedLinkByPortName(TypeDNS, cache, "left", "DNS", server, "right", "DNS")
	e.MustAddTypedLinkByPortName(TypeNetwork, mock, "left", "Network", client, "right", "Network")
	e.MustAddTypedLinkByPortName(TypeDNS, client, "left", "DNS", query, "right", "DNS")
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
	pastedServer := pastedNodeByClass(t, e.Engine, pasted, NodeTypeDNSServer)
	pastedQuery := pastedNodeByClass(t, e.Engine, pasted, NodeTypeDNSQuery)
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
	server := nodeIDByName(t, restored.Engine, graph.serverName)
	query := nodeIDByName(t, restored.Engine, graph.queryName)
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
		mock:       e.AddNode(NodeTypeMockNetwork, prefix+"-mock"),
		staticDNS:  e.AddNode(NodeTypeStaticDNS, prefix+"-static"),
		serverName: prefix + "-server",
		client:     e.AddNode(NodeTypeDNSClient, prefix+"-client"),
		queryName:  prefix + "-query",
		port:       port,
	}
	graph.server = e.AddNode(NodeTypeDNSServer, graph.serverName)
	graph.query = e.AddNode(NodeTypeDNSQuery, graph.queryName)
	e.SubscribeMenus(graph.staticDNS, graph.server, graph.client, graph.query)

	e.applyStaticDNSRecords(graph.staticDNS, fmt.Sprintf("%s A %s", host, ip))
	e.applyDNSServerConfig(graph.server, "udp", "127.0.0.1", port)
	e.applyDNSClientServers(graph.client, fmt.Sprintf("udp://127.0.0.1:%d", port))
	e.setDNSQueryRequest(graph.query, host+".", "A")

	e.MustAddTypedLinkByPortName(TypeNetwork, graph.mock, "left", "Network", graph.server, "right", "Network")
	e.MustAddTypedLinkByPortName(TypeDNS, graph.staticDNS, "left", "DNS", graph.server, "right", "DNS")
	e.MustAddTypedLinkByPortName(TypeNetwork, graph.mock, "left", "Network", graph.client, "right", "Network")
	e.MustAddTypedLinkByPortName(TypeDNS, graph.client, "left", "DNS", graph.query, "right", "DNS")
	e.waitDNSServerListening(graph.server, "127.0.0.1", port)
	e.WaitNodeLabel(graph.client, "1 DNS server")
	return graph
}

func (e *dnsE2E) applyDNSServerConfig(node uint64, network, host string, port int) {
	e.T.Helper()
	e.Engine.SendNodeFormularMsg(E2EWorkspace, node, formular.FormApplyMessage{
		MessageBase: FormMessageBase(formular.MessageFormApply, node),
		BlockID:     dnsServerConfigBlockID,
		Values: map[string]any{
			dnsServerNetworkField: network,
			dnsServerHostField:    host,
			dnsServerPortField:    port,
		},
	})
	e.waitDNSServerConfig(node, normalizeDNSServerNetwork(network), host, port)
}

func (e *dnsE2E) waitDNSServerConfig(node uint64, network, host string, port int) {
	e.T.Helper()
	Eventually(e.T, func() bool {
		return e.dnsServerFieldString(node, dnsServerNetworkField) == network &&
			e.dnsServerFieldString(node, dnsServerHostField) == host &&
			e.dnsServerFieldInt(node, dnsServerPortField) == port
	}, "DnsServer config %s %s:%d", network, host, port)
}

func (e *dnsE2E) waitDNSServerListening(node uint64, host string, port int) {
	e.T.Helper()
	e.WaitNodeLabel(node, "listening "+net.JoinHostPort(host, strconv.Itoa(port)))
}

func (e *dnsE2E) dnsServerFieldString(node uint64, field string) string {
	item, ok := e.MenuItem(node, dnsServerConfigBlockID, field)
	if !ok || item.Field == nil {
		return ""
	}
	value, _ := formular.StringValue(item.Value)
	return value
}

func (e *dnsE2E) dnsServerFieldInt(node uint64, field string) int {
	item, ok := e.MenuItem(node, dnsServerConfigBlockID, field)
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
		BlockID:     dnsClientConfigBlockID,
		Values: map[string]any{
			dnsClientServersField: dnsClientServerValues(servers...),
		},
	})
	e.waitDNSClientServers(node, normalizeDNSClientServers(servers))
}

func (e *dnsE2E) waitDNSClientServers(node uint64, want []string) {
	e.T.Helper()
	Eventually(e.T, func() bool {
		item, ok := e.MenuItem(node, dnsClientConfigBlockID, dnsClientServersField)
		if !ok || item.Field == nil {
			return false
		}
		got := dnsClientServerElementValues(e.T, item.Elements)
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

type trackedDNSServerFactory struct {
	mu      sync.Mutex
	servers []*trackedDNSServer
}

type trackedDNSServer struct {
	dnsServerRuntime
	attaches atomic.Int32
	detaches atomic.Int32
	closes   atomic.Int32
}

func installTrackedDNSServerFactory(t *testing.T) *trackedDNSServerFactory {
	t.Helper()
	tracker := &trackedDNSServerFactory{}
	previous := dnsServerNewServer
	dnsServerNewServer = func(conn net.PacketConn, upstream dns.Interface, spawner gonnect.Spawner) dnsServerRuntime {
		server := &trackedDNSServer{dnsServerRuntime: previous(conn, upstream, spawner)}
		tracker.mu.Lock()
		tracker.servers = append(tracker.servers, server)
		tracker.mu.Unlock()
		return server
	}
	t.Cleanup(func() {
		dnsServerNewServer = previous
	})
	return tracker
}

func (f *trackedDNSServerFactory) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.servers)
}

func (f *trackedDNSServerFactory) server(t *testing.T, index int) *trackedDNSServer {
	t.Helper()
	Eventually(t, func() bool {
		return f.count() > index
	}, fmt.Sprintf("tracked DNS server %d", index))
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.servers[index]
}

func (s *trackedDNSServer) Attach(upstream dns.Interface) {
	s.attaches.Add(1)
	s.dnsServerRuntime.Attach(upstream)
}

func (s *trackedDNSServer) Detach() {
	s.detaches.Add(1)
	s.dnsServerRuntime.Detach()
}

func (s *trackedDNSServer) Close() error {
	s.closes.Add(1)
	return s.dnsServerRuntime.Close()
}

func (s *trackedDNSServer) attachCount() int {
	return int(s.attaches.Load())
}

func (s *trackedDNSServer) detachCount() int {
	return int(s.detaches.Load())
}

func (s *trackedDNSServer) closeCount() int {
	return int(s.closes.Load())
}
