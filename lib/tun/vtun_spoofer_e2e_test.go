package tunnodes_test

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	dnsnodes "github.com/asciimoth/almagest/lib/dns"
	httpnodes "github.com/asciimoth/almagest/lib/http"
	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/formular"
	"github.com/asciimoth/gonnect"
	"github.com/asciimoth/pasta/pasta/std"
)

const (
	vtunSpooferServerHost = "10.113.212.1"
	vtunSpooferResponse   = "Response from VTun/Spoofer network"

	vtunSpooferFallbackHost     = "dual-stack-fallback.test"
	vtunSpooferFallbackDNS      = "192.0.2.53"
	vtunSpooferFallbackIPv4     = "192.0.2.80"
	vtunSpooferFallbackIPv6     = "2001:db8::80"
	vtunSpooferFallbackResponse = "Response after IPv6 fallback"
)

type vtunSpooferE2EFixture struct {
	*httpE2E

	vtun     uint64
	spoofer  uint64
	mock     uint64
	server   uint64
	client   uint64
	response uint64
	port     int
}

type vtunSpooferFallbackNetwork struct {
	*gonnect.LoopbackNetwork

	ipv6Started  chan struct{}
	ipv6Canceled chan struct{}
	startOnce    sync.Once
	cancelOnce   sync.Once
	ipv4Dials    atomic.Int64
	ipv6Active   atomic.Int64
}

type vtunSpooferFallbackPacketConn struct {
	gonnect.PacketConn
}

func newVTunSpooferFallbackNetwork() *vtunSpooferFallbackNetwork {
	loopback := gonnect.NewLoopbackNetwork()
	loopback.AllowAnyHost = true
	return &vtunSpooferFallbackNetwork{
		LoopbackNetwork: loopback,
		ipv6Started:     make(chan struct{}),
		ipv6Canceled:    make(chan struct{}),
	}
}

func (n *vtunSpooferFallbackNetwork) Dial(
	ctx context.Context,
	network, address string,
) (net.Conn, error) {
	host, _, err := net.SplitHostPort(address)
	if err == nil {
		if addr, parseErr := netip.ParseAddr(host); parseErr == nil && addr.Is6() {
			n.ipv6Active.Add(1)
			defer n.ipv6Active.Add(-1)
			n.startOnce.Do(func() { close(n.ipv6Started) })
			<-ctx.Done()
			n.cancelOnce.Do(func() { close(n.ipv6Canceled) })
			return nil, ctx.Err()
		}
	}
	n.ipv4Dials.Add(1)
	return n.LoopbackNetwork.Dial(ctx, network, address)
}

func (n *vtunSpooferFallbackNetwork) ListenPacket(
	ctx context.Context,
	network, address string,
) (gonnect.PacketConn, error) {
	conn, err := n.LoopbackNetwork.ListenPacket(ctx, network, address)
	if err != nil {
		return nil, err
	}
	return &vtunSpooferFallbackPacketConn{PacketConn: conn}, nil
}

func (c *vtunSpooferFallbackPacketConn) ReadFrom(p []byte) (int, net.Addr, error) {
	n, source, err := c.PacketConn.ReadFrom(p)
	if err != nil || source == nil {
		return n, source, err
	}
	host, port, splitErr := net.SplitHostPort(source.String())
	if splitErr == nil && host == "127.0.0.1" && port == "53" {
		// LoopbackNetwork maps all allowed IPv4 hosts to 127.0.0.1. Restore
		// the logical DNS source that Spoofer validates for this fixture.
		return n, &net.UDPAddr{IP: net.ParseIP(vtunSpooferFallbackDNS), Port: 53}, nil
	}
	return n, source, nil
}

func newVTunSpooferFallbackE2E(
	t *testing.T,
	output gonnect.Network,
) *httpE2E {
	t.Helper()
	pool := newE2ETestDebugPool(t)
	logFactory := NewDebugLogFactory(16384)
	sys := newE2EDebugSystem()
	sys.OutNetwork = output
	engine, err := BuildEngine(
		context.Background(), configer.NewMemory(nil), logFactory, sys,
		nil, nil, nil, nil, nil, pool, true,
	)
	if err != nil {
		t.Fatalf("BuildEngine: %v", err)
	}
	t.Cleanup(func() {
		if err := engine.Close(); err != nil {
			t.Errorf("engine.Close: %v", err)
		}
	})
	return newHTTPE2EFromNodeE2E(NewNodeE2EFromEngine(t, engine, logFactory, sys, nil))
}

func TestVTunSpooferE2EBlackholedIPv6FallsBackOnFirstRequest(t *testing.T) {
	output := newVTunSpooferFallbackNetwork()
	t.Cleanup(func() { _ = output.Close() })
	e := newVTunSpooferFallbackE2E(t, output)

	out := e.AddNode(NodeTypeOutNetwork, "controlled-out")
	vtun := e.AddNode(NodeTypeVTun, "dual-stack-vtun")
	joiner := e.AddNode(NodeTypeTunJoiner, "joiner")
	spoofer := e.AddNode(NodeTypeSpoofer, "spoofer")
	staticDNS := e.AddNode(dnsnodes.NodeTypeStaticDNS, "dual-stack-dns")
	dnsServer := e.AddNode(dnsnodes.NodeTypeDNSServer, "dns-server")
	response := e.addStringConstant("fallback-response", vtunSpooferFallbackResponse)
	serverPort := httpTestPort()
	server := e.addHTTPServerOnHost("ipv4-server", vtunSpooferFallbackIPv4, serverPort)
	clientURL := fmt.Sprintf("http://%s:%d/", vtunSpooferFallbackHost, serverPort)
	client := e.addHTTPClientToURL("fallback-client", clientURL)
	e.SubscribeMenus(vtun, spoofer, staticDNS, dnsServer, response, server, client)
	e.waitClientRequestField(client, "url", clientURL)

	newDNSE2EFromNodeE2E(e.NodeE2E).applyStaticDNSRecords(staticDNS, fmt.Sprintf(
		"%s A %s\n%s AAAA %s",
		vtunSpooferFallbackHost, vtunSpooferFallbackIPv4,
		vtunSpooferFallbackHost, vtunSpooferFallbackIPv6,
	))
	e.Engine.SendNodeFormularMsg(E2EWorkspace, dnsServer, formular.FormApplyMessage{
		MessageBase: FormMessageBase(formular.MessageFormApply, dnsServer),
		BlockID:     dnsnodes.DNSServerConfigBlockID,
		Values: map[string]any{
			dnsnodes.DNSServerNetworkField: "udp4",
			dnsnodes.DNSServerHostField:    vtunSpooferFallbackDNS,
			dnsnodes.DNSServerPortField:    53,
		},
	})
	e.Engine.SendNodeFormularMsg(E2EWorkspace, vtun, formular.FormApplyMessage{
		MessageBase: FormMessageBase(formular.MessageFormApply, vtun),
		BlockID:     "config",
		Values: map[string]any{
			"name":           "dual-stack-vtun",
			"localAddrs":     "192.0.2.2\n2001:db8::2",
			"dnsServers":     vtunSpooferFallbackDNS,
			"mtu":            1500,
			"endpointCh":     64,
			"eventCh":        8,
			"noLoopbackAddr": true,
			"disableSACK":    false,
		},
	})

	e.MustAddTypedLinkByPortName(
		std.TypeString, response, "right", "output", server, "left", "Response",
	)
	e.MustAddTypedLinkByPortName(
		dnsnodes.TypeDNS, staticDNS, "left", "DNS", dnsServer, "right", "DNS",
	)
	e.connectByPortName(out, "left", "Network", dnsServer, "right", "Network")
	e.connectByPortName(out, "left", "Network", server, "right", "Network")
	spooferOutLink := e.connectByPortName(out, "left", "Network", spoofer, "right", "Network")
	e.connectTunByPortName(joiner, "left", "Default", vtun, "right", "Tun")
	e.connectTunByPortName(spoofer, "left", "Tun", joiner, "right", "Tun")
	clientNetworkLink := e.connectByPortName(vtun, "left", "Network", client, "right", "Network")

	e.WaitNodeLabel(joiner, "joined default")
	e.WaitNodeLabel(spoofer, "forwarding")
	e.WaitNodeLabel(vtun, "dual-stack-vtun")
	e.WaitNodeLabel(dnsServer, "listening "+net.JoinHostPort(vtunSpooferFallbackDNS, "53"))
	e.waitServerListening(server, net.JoinHostPort(vtunSpooferFallbackIPv4, fmt.Sprint(serverPort)))
	// Make the client input transition observable. Its initial ready state does
	// not prove that the queued VTun payload has arrived.
	e.Engine.RemoveLink(E2EWorkspace, clientNetworkLink)
	e.LogFactory.Drain()
	e.connectByPortName(vtun, "left", "Network", client, "right", "Network")
	workerStarts := 0
	Eventually(t, func() bool {
		for _, entry := range e.LogFactory.Drain() {
			if entry.Source.NodeID == client &&
				entry.Source.NodeClass == httpnodes.NodeTypeHTTPClient &&
				entry.Message == "worker started" {
				workerStarts++
			}
		}
		return workerStarts >= 2
	}, "HTTP client receives the initial and requested VTun payloads")
	e.WaitClientReady(client)

	started := time.Now()
	e.SendClientRequest(client)
	e.WaitClientResponse(client, "200 OK", vtunSpooferFallbackResponse)
	elapsed := time.Since(started)
	if elapsed < 100*time.Millisecond {
		t.Fatalf("first request completed in %v, before the IPv6 fallback delay", elapsed)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("first request completed in %v, want fallback before 5s", elapsed)
	}
	select {
	case <-output.ipv6Started:
	default:
		t.Fatal("first request did not start the controlled IPv6 attempt")
	}
	if got := output.ipv4Dials.Load(); got != 1 {
		t.Fatalf("IPv4 dial count = %d, want one first-request fallback", got)
	}
	if got := output.ipv6Active.Load(); got != 1 {
		t.Fatalf("active IPv6 preparations = %d, want one before Spoofer stop", got)
	}

	e.Engine.RemoveLink(E2EWorkspace, spooferOutLink)
	e.WaitNodeLabel(spoofer, "waiting for network")
	select {
	case <-output.ipv6Canceled:
	case <-time.After(time.Second):
		t.Fatal("pending IPv6 preparation was not canceled after Spoofer stopped")
	}
	Eventually(t, func() bool { return output.ipv6Active.Load() == 0 }, "IPv6 preparation cleanup")
}

func TestVTunSpooferE2EDisconnectReconnectLinks(t *testing.T) {
	f := setupVTunSpooferE2E(t)
	f.assertVTunSpooferRequestWorks(f.client)

	links := []struct {
		name        string
		add         func() uint64
		afterRemove func()
		failPart    string
		current     uint64
	}{
		{
			name: "client to VTun network",
			add: func() uint64 {
				return f.connectByPortName(f.vtun, "left", "Network", f.client, "right", "Network")
			},
			failPart: "network is not ready",
			current:  f.mustTypedLink(TypeNetwork, f.vtun, "left", "Network", f.client, "right", "Network"),
		},
		{
			name: "VTun to Spoofer TUN",
			add: func() uint64 {
				return f.connectTunByPortName(f.spoofer, "left", "Tun", f.vtun, "right", "Tun")
			},
			afterRemove: func() {
				f.WaitNodeLabel(f.spoofer, "waiting for tun")
			},
			current: f.mustTypedLink(TypeTun, f.spoofer, "left", "Tun", f.vtun, "right", "Tun"),
		},
		{
			name: "Spoofer to MockNetwork",
			add: func() uint64 {
				return f.connectByPortName(f.mock, "left", "Network", f.spoofer, "right", "Network")
			},
			afterRemove: func() {
				f.WaitNodeLabel(f.spoofer, "waiting for network")
			},
			current: f.mustTypedLink(TypeNetwork, f.mock, "left", "Network", f.spoofer, "right", "Network"),
		},
		{
			name: "HTTP server to MockNetwork",
			add: func() uint64 {
				return f.connectByPortName(f.mock, "left", "Network", f.server, "right", "Network")
			},
			afterRemove: func() {
				f.waitServerLog(f.server, "waiting for network")
			},
			current: f.mustTypedLink(TypeNetwork, f.mock, "left", "Network", f.server, "right", "Network"),
		},
	}

	for i := range links {
		t.Logf("checking removal and restore of %s link", links[i].name)
		f.Engine.RemoveLink(E2EWorkspace, links[i].current)
		if links[i].failPart != "" {
			f.sendClientRequest(f.client)
			f.waitClientFailure(f.client, links[i].failPart)
		}
		if links[i].afterRemove != nil {
			links[i].afterRemove()
		}
		links[i].current = links[i].add()
		f.waitVTunSpooferReady()
		f.assertVTunSpooferRequestWorks(f.client)
	}
}

func TestVTunSpooferE2ESaveRestore(t *testing.T) {
	f := setupVTunSpooferE2E(t)
	f.applyVTunConfig(f.vtun, vtunConfigFormValues("restored-vtun"))
	f.applySpooferConfig(f.spoofer, spooferConfigFormValues())
	f.waitVTunSpooferReady()
	f.assertVTunSpooferRequestWorks(f.client)

	vtunName := f.nodeName(f.vtun)
	spooferName := f.nodeName(f.spoofer)
	mockName := f.nodeName(f.mock)
	serverName := f.nodeName(f.server)
	clientName := f.nodeName(f.client)
	responseName := f.nodeName(f.response)

	cfg := configer.NewMemory(nil)
	if err := f.Engine.SaveConfig(cfg); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	f.assertSavedConfig(cfg, vtunName, spooferName)
	if err := f.Engine.Close(); err != nil {
		t.Fatalf("close saved engine: %v", err)
	}

	restored := newVTunSpooferConfigE2E(t, cfg)
	vtun := restored.mustNodeByName(vtunName)
	spoofer := restored.mustNodeByName(spooferName)
	mock := restored.mustNodeByName(mockName)
	server := restored.mustNodeByName(serverName)
	client := restored.mustNodeByName(clientName)
	response := restored.mustNodeByName(responseName)
	restored.SubscribeMenus(vtun, spoofer, server, client, response)
	restored.mustTypedLink(TypeTun, spoofer, "left", "Tun", vtun, "right", "Tun")
	restored.mustNetworkLink(mock, "left", "Network", spoofer, "right", "Network")
	restored.mustNetworkLink(mock, "left", "Network", server, "right", "Network")
	restored.mustNetworkLink(vtun, "left", "Network", client, "right", "Network")
	restored.mustTypedLink(std.TypeString, response, "right", "output", server, "left", "Response")
	restored.waitServerListening(server, fmt.Sprintf("%s:%d", vtunSpooferServerHost, f.port))
	restored.waitClientRequestField(client, "url", f.clientURL())
	restored.assertClientRequestWorksWithResponse(client, vtunSpooferResponse)
	restored.WaitNodeLabel(vtun, "restored-vtun")
	restored.WaitNodeLabel(spoofer, "forwarding")
}

func TestVTunSpooferE2ECopyPaste(t *testing.T) {
	f := setupVTunSpooferE2E(t)
	f.assertVTunSpooferRequestWorks(f.client)

	clip := f.Engine.Copy(E2EWorkspace, []uint64{f.vtun, f.spoofer, f.mock})
	if clip == "" {
		t.Fatal("Copy returned empty clipboard")
	}
	pasted := f.Engine.Paste(E2EWorkspace, clip)
	if len(pasted) != 3 {
		t.Fatalf("Paste returned %d nodes (%v), want 3", len(pasted), pasted)
	}
	vtun := pasted[0]
	spoofer := pasted[1]
	mock := pasted[2]
	f.assertNodesLinked(vtun, spoofer)
	f.assertNodesLinked(spoofer, mock)

	port := httpTestPort()
	server := f.addHTTPServerOnHost("pasted-server", vtunSpooferServerHost, port)
	client := f.addHTTPClientToURL("pasted-client", fmt.Sprintf("http://%s:%d/", vtunSpooferServerHost, port))
	response := f.addStringConstant("pasted-response", vtunSpooferResponse)
	f.SubscribeMenus(vtun, spoofer, server, client, response)
	f.waitClientRequestField(client, "url", fmt.Sprintf("http://%s:%d/", vtunSpooferServerHost, port))
	f.MustAddTypedLinkByPortName(std.TypeString, response, "right", "output", server, "left", "Response")
	f.connectByPortName(mock, "left", "Network", server, "right", "Network")
	f.connectByPortName(vtun, "left", "Network", client, "right", "Network")

	f.waitServerListening(server, fmt.Sprintf("%s:%d", vtunSpooferServerHost, port))
	f.assertClientRequestWorksWithResponse(client, vtunSpooferResponse)
}

func TestVTunSpooferE2EMenuConfigUpdates(t *testing.T) {
	f := setupVTunSpooferE2E(t)
	f.assertVTunSpooferRequestWorks(f.client)

	vtunValues := vtunConfigFormValues("menu-vtun")
	spooferValues := spooferConfigFormValues()
	f.applyVTunConfig(f.vtun, vtunValues)
	f.applySpooferConfig(f.spoofer, spooferValues)
	f.waitVTunSpooferReady()
	f.assertConfigMenuValues(f.vtun, vtunValues)
	f.assertConfigMenuValues(f.spoofer, spooferValues)
	f.WaitNodeLabel(f.vtun, "menu-vtun")
	f.WaitNodeLabel(f.spoofer, "forwarding")
	f.assertVTunSpooferRequestWorks(f.client)
}

func setupVTunSpooferE2E(t *testing.T) vtunSpooferE2EFixture {
	t.Helper()
	e := newHTTPE2E(t)
	f := vtunSpooferE2EFixture{httpE2E: e, port: httpTestPort()}
	f.vtun = e.AddNode(NodeTypeVTun, "vtun")
	f.spoofer = e.AddNode(NodeTypeSpoofer, "spoofer")
	f.mock = e.AddNode(NodeTypeMockNetwork, "mock")
	f.response = e.addStringConstant("server-response", vtunSpooferResponse)
	f.server = e.addHTTPServerOnHost("server", vtunSpooferServerHost, f.port)
	f.client = e.addHTTPClientToURL("client", f.clientURL())
	e.SubscribeMenus(f.vtun, f.spoofer, f.response, f.server, f.client)
	e.waitClientRequestField(f.client, "url", f.clientURL())

	e.MustAddTypedLinkByPortName(std.TypeString, f.response, "right", "output", f.server, "left", "Response")
	e.connectTunByPortName(f.spoofer, "left", "Tun", f.vtun, "right", "Tun")
	e.connectByPortName(f.mock, "left", "Network", f.spoofer, "right", "Network")
	e.connectByPortName(f.mock, "left", "Network", f.server, "right", "Network")
	e.connectByPortName(f.vtun, "left", "Network", f.client, "right", "Network")
	f.waitVTunSpooferReady()
	e.waitServerListening(f.server, fmt.Sprintf("%s:%d", vtunSpooferServerHost, f.port))
	return f
}

func newVTunSpooferConfigE2E(t *testing.T, cfg configer.Config) *httpE2E {
	t.Helper()
	pool := newE2ETestDebugPool(t)
	logf := NewDebugLogFactory(16384)
	engine, err := BuildEngine(
		context.Background(),
		cfg,
		logf,
		newE2EDebugSystem(),
		nil,
		nil, nil,
		nil,

		nil,
		pool,
		true)

	if err != nil {
		t.Fatalf("BuildEngine restored: %v", err)
	}
	t.Cleanup(func() {
		if err := engine.Close(); err != nil {
			t.Errorf("restored engine.Close error = %v, want nil", err)
		}
	})
	return newHTTPE2EFromNodeE2E(NewNodeE2EFromEngine(t, engine, logf, nil, nil))
}

func (f vtunSpooferE2EFixture) clientURL() string {
	return fmt.Sprintf("http://%s:%d/", vtunSpooferServerHost, f.port)
}

func (f vtunSpooferE2EFixture) waitVTunSpooferReady() {
	f.T.Helper()
	f.WaitNodeLabel(f.spoofer, "forwarding")
	Eventually(f.T, func() bool {
		snapshot, ok := f.Engine.NodeSnapshot(E2EWorkspace, f.vtun)
		return ok && snapshot.Label != "idle" && snapshot.Label != "invalid config"
	}, "VTun ready")
}

func (f vtunSpooferE2EFixture) assertVTunSpooferRequestWorks(client uint64) {
	f.T.Helper()
	f.assertClientRequestWorksWithResponse(client, vtunSpooferResponse)
}

func (e *httpE2E) assertClientRequestWorksWithResponse(client uint64, response string) {
	e.T.Helper()
	e.assertClientRequestReturns(client, response, 10*time.Second)
}

func (e *httpE2E) addHTTPServerOnHost(name, host string, port int) uint64 {
	e.T.Helper()
	id := e.AddNode(NodeTypeHTTPServer, name)
	e.Engine.SendNodeFormularMsg(E2EWorkspace, id, formular.FormApplyMessage{
		MessageBase: FormMessageBase(formular.MessageFormApply, id),
		BlockID:     "listen",
		Values: map[string]any{
			"host": host,
			"port": port,
		},
	})
	e.WaitNodeLabel(id, fmt.Sprintf("%s:%d", host, port))
	return id
}

func (e *httpE2E) addHTTPClientToURL(name, url string) uint64 {
	e.T.Helper()
	id := e.AddNode(NodeTypeHTTPClient, name)
	e.Engine.SendNodeFormularMsg(E2EWorkspace, id, formular.FieldUpdateMessage{
		MessageBase: FormMessageBase(formular.MessageFieldUpdate, id),
		Field:       formular.FieldRef{BlockID: "request", FieldID: "url"},
		Value:       url,
	})
	return id
}

func (e *httpE2E) connectTunByPortName(leftNode uint64, leftDir, leftName string, rightNode uint64, rightDir, rightName string) uint64 {
	e.T.Helper()
	return e.MustAddTypedLinkByPortName(TypeTun, leftNode, leftDir, leftName, rightNode, rightDir, rightName)
}

func (e *httpE2E) mustTypedLink(linkType string, leftNode uint64, leftDir, leftName string, rightNode uint64, rightDir, rightName string) uint64 {
	e.T.Helper()
	left := e.NodePort(leftNode, leftDir, leftName)
	right := e.NodePort(rightNode, rightDir, rightName)
	link, snapshot, ok := e.Engine.LinkByPorts(E2EWorkspace, left, right)
	if !ok {
		e.T.Fatalf("link missing between node %d %s %q and node %d %s %q", leftNode, leftDir, leftName, rightNode, rightDir, rightName)
	}
	if snapshot.Type != linkType {
		e.T.Fatalf("link %d type = %q, want %q", link, snapshot.Type, linkType)
	}
	return link
}

func (f vtunSpooferE2EFixture) applyVTunConfig(node uint64, values map[string]any) {
	f.T.Helper()
	f.applyConfig(node, values)
}

func (f vtunSpooferE2EFixture) applySpooferConfig(node uint64, values map[string]any) {
	f.T.Helper()
	f.applyConfig(node, values)
}

func (f vtunSpooferE2EFixture) applyConfig(node uint64, values map[string]any) {
	f.T.Helper()
	f.Engine.SendNodeFormularMsg(E2EWorkspace, node, formular.FormApplyMessage{
		MessageBase: FormMessageBase(formular.MessageFormApply, node),
		BlockID:     "config",
		Values:      values,
	})
	f.assertConfigMenuValues(node, values)
}

func vtunConfigFormValues(name string) map[string]any {
	return map[string]any{
		"name":           name,
		"localAddrs":     "10.250.0.2",
		"dnsServers":     "1.1.1.1",
		"mtu":            1400,
		"endpointCh":     64,
		"eventCh":        8,
		"noLoopbackAddr": true,
		"disableSACK":    true,
	}
}

func spooferConfigFormValues() map[string]any {
	return map[string]any{
		"mtu":                  1400,
		"queueLength":          128,
		"disableSACK":          true,
		"ttl":                  64,
		"icmpBurst":            8,
		"icmpLimit":            1.5,
		"tcpSendBufferSize":    4096,
		"tcpReceiveBufferSize": 4096,
		"congestionControlAlg": "",
		"disableNagle":         true,
		"disableTCPModRecBuff": true,
		"tcpKeepAlive":         true,
		"tcpKeepAliveIdle":     10,
		"tcpKeepAliveInterval": 5,
		"tcpKeepAliveCount":    3,
		"tcpForwardWnd":        1024,
		"tcpForwardAttempts":   16,
	}
}

func (f vtunSpooferE2EFixture) assertConfigMenuValues(node uint64, values map[string]any) {
	f.T.Helper()
	Eventually(f.T, func() bool {
		for key, want := range values {
			item, ok := f.MenuItem(node, "config", key)
			if !ok || item.Field == nil || !reflect.DeepEqual(item.Value, want) {
				return false
			}
		}
		return true
	}, "node %d config menu values %v", node, values)
}

func (f vtunSpooferE2EFixture) assertSavedConfig(cfg configer.Config, vtunName, spooferName string) {
	f.T.Helper()
	want := []struct {
		path  configer.Path
		value any
	}{
		{configer.Path{"nodes", vtunName, "name"}, "restored-vtun"},
		{configer.Path{"nodes", vtunName, "localAddrs"}, "10.250.0.2"},
		{configer.Path{"nodes", vtunName, "dnsServers"}, "1.1.1.1"},
		{configer.Path{"nodes", vtunName, "mtu"}, 1400},
		{configer.Path{"nodes", vtunName, "endpointCh"}, 64},
		{configer.Path{"nodes", vtunName, "eventCh"}, 8},
		{configer.Path{"nodes", vtunName, "noLoopbackAddr"}, true},
		{configer.Path{"nodes", vtunName, "disableSACK"}, true},
		{configer.Path{"nodes", spooferName, "mtu"}, 1400},
		{configer.Path{"nodes", spooferName, "queueLength"}, 128},
		{configer.Path{"nodes", spooferName, "disableSACK"}, true},
		{configer.Path{"nodes", spooferName, "ttl"}, 64},
		{configer.Path{"nodes", spooferName, "icmpBurst"}, 8},
		{configer.Path{"nodes", spooferName, "icmpLimit"}, 1.5},
		{configer.Path{"nodes", spooferName, "tcpSendBufferSize"}, 4096},
		{configer.Path{"nodes", spooferName, "tcpReceiveBufferSize"}, 4096},
		{configer.Path{"nodes", spooferName, "congestionControlAlg"}, ""},
		{configer.Path{"nodes", spooferName, "disableNagle"}, true},
		{configer.Path{"nodes", spooferName, "disableTCPModRecBuff"}, true},
		{configer.Path{"nodes", spooferName, "tcpKeepAlive"}, true},
		{configer.Path{"nodes", spooferName, "tcpKeepAliveIdle"}, 10},
		{configer.Path{"nodes", spooferName, "tcpKeepAliveInterval"}, 5},
		{configer.Path{"nodes", spooferName, "tcpKeepAliveCount"}, 3},
		{configer.Path{"nodes", spooferName, "tcpForwardWnd"}, 1024},
		{configer.Path{"nodes", spooferName, "tcpForwardAttempts"}, 16},
	}
	for _, entry := range want {
		got, err := cfg.Get(entry.path)
		if err != nil {
			f.T.Fatalf("saved config %v missing: %v", entry.path, err)
		}
		if !sameConfigValue(got, entry.value) {
			f.T.Fatalf("saved config %v = %#v (%T), want %#v (%T)", entry.path, got, got, entry.value, entry.value)
		}
	}
}

func sameConfigValue(got, want any) bool {
	if reflect.DeepEqual(got, want) {
		return true
	}
	switch want := want.(type) {
	case int:
		switch got := got.(type) {
		case int:
			return got == want
		case int64:
			return got == int64(want)
		case float64:
			return got == float64(want)
		}
	case float64:
		switch got := got.(type) {
		case int:
			return float64(got) == want
		case int64:
			return float64(got) == want
		case float64:
			return got == want
		}
	}
	return false
}
