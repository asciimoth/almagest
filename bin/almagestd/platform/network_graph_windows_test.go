//go:build windows

package platform

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	almagest "github.com/asciimoth/almagest/lib"
	debugnodes "github.com/asciimoth/almagest/lib/debug"
	dnsnodes "github.com/asciimoth/almagest/lib/dns"
	httpnodes "github.com/asciimoth/almagest/lib/http"
	"github.com/asciimoth/almagest/lib/links"
	"github.com/asciimoth/almagest/lib/logging"
	networknodes "github.com/asciimoth/almagest/lib/network"
	"github.com/asciimoth/almagest/lib/nodekit"
	socksnodes "github.com/asciimoth/almagest/lib/socks"
	"github.com/asciimoth/almagest/lib/storage"
	tunnodes "github.com/asciimoth/almagest/lib/tun"
	wireguardnodes "github.com/asciimoth/almagest/lib/wireguard"
	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/formular"
	"github.com/asciimoth/gonnect"
	"github.com/asciimoth/gonnect/dns"
	"github.com/asciimoth/gonnect/sysnet"
	"github.com/asciimoth/pasta/pasta"
	"github.com/asciimoth/socksgo/protocol"
	wgdevice "github.com/asciimoth/wgo/device"
	"golang.org/x/sys/windows"
)

const windowsGraphDNSAddress = "198.18.232.1"

func TestWindowsGraphOutNetLocalNetAndWireGuardWithDefaultTun(t *testing.T) {
	engine, system, networkReceiver, _ := newWindowsNetworkGraphEngine(t)
	defaultTun := addWindowsGraphNode(t, engine, tunnodes.NodeTypeDefaultTun, "default tun")
	// The native network uses Go's resolver. Windows does not supply localhost
	// entries in its default hosts file, and an unlinked DefaultTun drops DNS.
	// Supply both loopback answers through the graph's managed DNS provider.
	applyWindowsGraphForm(engine, defaultTun, "config", map[string]any{
		"tunAddrs": windowsGraphDNSAddress + "/32",
		"dnsIP":    windowsGraphDNSAddress,
	})
	static := addWindowsGraphNode(t, engine, dnsnodes.NodeTypeStaticDNS, "local dns")
	applyWindowsGraphForm(engine, static, "config", map[string]any{
		"records": "localhost A 127.0.0.1\nlocalhost AAAA ::1", "ttl": 60,
	})
	linkWindowsGraphPorts(t, engine, static, "left", "DNS", defaultTun, "right", "DNS")
	waitWindowsGraphDNSAnswer(t, "udp", "localhost.", netip.MustParseAddr("127.0.0.1"))
	out := addWindowsGraphNode(t, engine, "almagest/OutNetwork", "out")
	receiver := addWindowsGraphNode(t, engine, networkReceiver.ClassName(), "network receiver")
	linkWindowsGraphPorts(t, engine, out, "left", "Network", receiver, "right", "Network")
	network := receiveWindowsNetworkPayload(t, networkReceiver.events).Network

	for _, family := range []struct {
		tcp      string
		udp      string
		wildcard string
	}{
		{tcp: "tcp4", udp: "udp4", wildcard: "0.0.0.0:0"},
		{tcp: "tcp6", udp: "udp6", wildcard: "[::]:0"},
	} {
		testWindowsGraphTCP(t, "OutNet", network, family.tcp, family.wildcard)
		testWindowsGraphUDP(t, "OutNet", network, family.udp, family.wildcard)
		testWindowsGraphWildcardUDPLoopback(t, network, family.udp, family.wildcard)
		testWindowsGraphTCP(t, "LocalNet", system.LocalNet(), family.tcp, family.wildcard)
		testWindowsGraphUDP(t, "LocalNet", system.LocalNet(), family.udp, family.wildcard)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if listener, err := network.ListenTCP(ctx, "tcp", ":0"); err == nil {
		_ = listener.Close()
		t.Fatal("OutNet accepted an ambiguous generic wildcard listener")
	}

	proxyPort := reserveWindowsLoopbackPort(t)
	proxy := addWindowsGraphNode(t, engine, socksnodes.NodeTypeLocalProxy, "local proxy")
	applyWindowsGraphForm(engine, proxy, "config", map[string]any{
		"socksHost": "0.0.0.0", "socksPort": proxyPort,
		"wsHost": "", "wsPort": 0, "wsPath": "/socks",
	})
	linkWindowsGraphPorts(t, engine, out, "left", "Network", proxy, "right", "proxy")
	waitWindowsGraphTCPConnect(t, system.LocalNet(), "tcp4", fmt.Sprintf("127.0.0.1:%d", proxyPort))
	lookupCtx, lookupCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer lookupCancel()
	if ips, err := system.LocalNet().LookupIP(lookupCtx, "ip", "localhost"); err != nil {
		t.Fatalf("LocalNet localhost lookup: %v", err)
	} else {
		var hasIPv4, hasIPv6 bool
		for _, ip := range ips {
			if !ip.IsLoopback() {
				t.Fatalf("LocalNet localhost answer %s is not loopback", ip)
			}
			hasIPv4 = hasIPv4 || ip.To4() != nil
			hasIPv6 = hasIPv6 || ip.To4() == nil
		}
		if !hasIPv4 || !hasIPv6 {
			t.Fatalf("LocalNet localhost answers = %v, want mixed IPv4 and IPv6 loopback answers", ips)
		}
	}

	vtun := addWindowsGraphNode(t, engine, tunnodes.NodeTypeVTun, "wireguard vtun")
	wg := addWindowsGraphNode(t, engine, wireguardnodes.NodeTypeWireGuard, "wireguard")
	applyWindowsGraphForm(engine, wg, "config", map[string]any{
		"privateKey": "CAkKCwwNDg8QERITFBUWFxgZGhscHR4fICEiIyQlJkc=",
		"listenPort": 0,
		"ownedIPs":   "10.232.0.2/32\nfd00:232::2/128",
	})
	linkWindowsGraphPorts(t, engine, vtun, "right", "Tun", wg, "left", "Tun")
	linkWindowsGraphPorts(t, engine, out, "left", "Network", wg, "right", "Network")
	waitWindowsGraphNodeLabel(t, engine, wg, "up")
	if snapshot, ok := engine.NodeSnapshot(almagest.MainWorkspace, defaultTun); !ok || snapshot.Label != "ready" {
		t.Fatalf("DefaultTun state while WireGuard uses OutNet = %#v, want ready", snapshot)
	}
}

func TestWindowsGraphWireGuardSymmetricGostOverOutNetLoopback(t *testing.T) {
	engine, _, _, logf := newWindowsDebugNetworkGraphEngine(t)
	menus := newWindowsGraphMenuRecorder(t, engine)
	const (
		clientAddress = "10.198.10.1"
		serverAddress = "10.198.20.1"
		httpPort      = 28080
		udpPort       = 26120
		udpPayload    = "nested-wireguard-udp-ping"
	)
	socksPort := reserveWindowsLoopbackPort(t)
	clientWireGuardPort := reserveWindowsLoopbackUDPPort(t)
	serverWireGuardPort := reserveWindowsLoopbackUDPPort(t)
	for serverWireGuardPort == clientWireGuardPort {
		serverWireGuardPort = reserveWindowsLoopbackUDPPort(t)
	}

	mock := addWindowsGraphNode(t, engine, networknodes.NodeTypeMockNetwork, "SOCKS transport")
	out := addWindowsGraphNode(t, engine, networknodes.NodeTypeOutNetwork, "SOCKS proxy OutNet")
	socksServer := addWindowsGraphNode(t, engine, socksnodes.NodeTypeSocksServer, "SOCKS server")
	clientSocks := addWindowsGraphNode(t, engine, socksnodes.NodeTypeSocksClient, "client SOCKS")
	serverSocks := addWindowsGraphNode(t, engine, socksnodes.NodeTypeSocksClient, "server SOCKS")
	clientWireGuard := addWindowsGraphNode(t, engine, wireguardnodes.NodeTypeWireGuard, "client WireGuard")
	serverWireGuard := addWindowsGraphNode(t, engine, wireguardnodes.NodeTypeWireGuard, "server WireGuard")
	clientVTun := addWindowsGraphNode(t, engine, tunnodes.NodeTypeVTun, "client VTun")
	serverVTun := addWindowsGraphNode(t, engine, tunnodes.NodeTypeVTun, "server VTun")
	httpServer := addWindowsGraphNode(t, engine, httpnodes.NodeTypeHTTPServer, "HTTP server")
	httpClient := addWindowsGraphNode(t, engine, httpnodes.NodeTypeHTTPClient, "HTTP client")
	udpServer := addWindowsGraphNode(t, engine, debugnodes.NodeTypeUDPPingServer, "UDP ping server")
	udpClient := addWindowsGraphNode(t, engine, debugnodes.NodeTypeUDPPingClient, "UDP ping client")
	for _, node := range []uint64{socksServer, clientWireGuard, serverWireGuard, httpServer, httpClient, udpServer, udpClient} {
		engine.SubscribeNodeMenu(almagest.MainWorkspace, node, menus.subscription)
	}

	applyWindowsGraphForm(engine, socksServer, "config", map[string]any{
		"socksHost": "127.0.0.1", "socksPort": socksPort,
		"wsHost": "", "wsPort": 0, "wsPath": "/socks", "cmdGostUDPTun": true,
	})
	proxyURL := fmt.Sprintf("socks5://127.0.0.1:%d?gost", socksPort)
	for _, node := range []uint64{clientSocks, serverSocks} {
		engine.SendNodeFormularMsg(almagest.MainWorkspace, node, formular.FieldUpdateMessage{
			MessageBase: formular.MessageBase{Type: formular.MessageFieldUpdate, MenuID: pasta.NodeMenuID(node)},
			Field:       formular.FieldRef{BlockID: "config", FieldID: "proxyURL"},
			Value:       proxyURL,
		})
	}
	applyWindowsGraphVTun(engine, clientVTun, "windows-socks-client-vtun", "")
	applyWindowsGraphVTun(engine, serverVTun, "windows-socks-server-vtun", "")
	clientSecret, clientPublic := windowsGraphWireGuardKey(t, engine, clientWireGuard)
	serverSecret, serverPublic := windowsGraphWireGuardKey(t, engine, serverWireGuard)
	applyWindowsGraphWireGuard(engine, clientWireGuard, clientSecret, clientWireGuardPort, clientAddress+"/24", []formular.ArrayElementValue{{
		ID: "server", Template: "peer", Values: map[string]any{
			"publicKey": serverPublic, "endpoint": fmt.Sprintf("127.0.0.1:%d", serverWireGuardPort),
			"allowedIPs": serverAddress + "/32", "persistentKeepaliveInterval": 1,
		},
	}})
	applyWindowsGraphWireGuard(engine, serverWireGuard, serverSecret, serverWireGuardPort, serverAddress+"/24", []formular.ArrayElementValue{{
		ID: "client", Template: "peer", Values: map[string]any{
			"publicKey": clientPublic, "endpoint": fmt.Sprintf("127.0.0.1:%d", clientWireGuardPort),
			"allowedIPs": clientAddress + "/32", "persistentKeepaliveInterval": 1,
		},
	}})
	applyWindowsGraphForm(engine, httpServer, "listen", map[string]any{"host": serverAddress, "port": httpPort})
	engine.SendNodeFormularMsg(almagest.MainWorkspace, httpClient, formular.FieldUpdateMessage{
		MessageBase: formular.MessageBase{Type: formular.MessageFieldUpdate, MenuID: pasta.NodeMenuID(httpClient)},
		Field:       formular.FieldRef{BlockID: "request", FieldID: "url"},
		Value:       fmt.Sprintf("http://%s:%d/", serverAddress, httpPort),
	})
	applyWindowsGraphForm(engine, udpServer, "config", map[string]any{
		"network": "udp4", "socket_mode": "listen",
		"local_host": serverAddress, "local_port": udpPort,
		"remote_host": clientAddress, "remote_port": 0,
	})
	applyWindowsGraphForm(engine, udpClient, "config", map[string]any{
		"network": "udp4", "socket_mode": "dial",
		"local_host": clientAddress, "local_port": 0,
		"remote_host": serverAddress, "remote_port": udpPort,
		"payload": udpPayload, "timeout_ms": 1000,
	})

	// Keep the SOCKS control path on the mock network. Only proxy operations
	// use OutNet. This is the route that exercises wildcard UDP writes to the
	// loopback WireGuard endpoints.
	linkWindowsGraphPorts(t, engine, mock, "left", "Network", clientSocks, "right", "out")
	linkWindowsGraphPorts(t, engine, mock, "left", "Network", serverSocks, "right", "out")
	linkWindowsGraphPorts(t, engine, mock, "left", "Network", socksServer, "right", "listen")
	linkWindowsGraphPorts(t, engine, out, "left", "Network", socksServer, "right", "proxy")
	linkWindowsGraphPorts(t, engine, clientSocks, "left", "Network", clientWireGuard, "right", "Network")
	linkWindowsGraphPorts(t, engine, serverSocks, "left", "Network", serverWireGuard, "right", "Network")
	linkWindowsGraphPorts(t, engine, clientVTun, "right", "Tun", clientWireGuard, "left", "Tun")
	linkWindowsGraphPorts(t, engine, serverVTun, "right", "Tun", serverWireGuard, "left", "Tun")
	linkWindowsGraphPorts(t, engine, clientVTun, "left", "Network", httpClient, "right", "Network")
	linkWindowsGraphPorts(t, engine, serverVTun, "left", "Network", httpServer, "right", "Network")
	linkWindowsGraphPorts(t, engine, clientVTun, "left", "Network", udpClient, "right", "Network")
	linkWindowsGraphPorts(t, engine, serverVTun, "left", "Network", udpServer, "right", "Network")

	waitWindowsGraphMenuLog(t, menus, socksServer, "socks listening on "+net.JoinHostPort("127.0.0.1", fmt.Sprint(socksPort)))
	waitWindowsGraphMenuLog(t, menus, httpServer, "listening on "+net.JoinHostPort(serverAddress, fmt.Sprint(httpPort)))
	waitWindowsGraphNodeLabel(t, engine, clientWireGuard, "up")
	waitWindowsGraphNodeLabel(t, engine, serverWireGuard, "up")
	waitWindowsGraphNodeLabel(t, engine, clientVTun, "windows-socks-client-vtun")
	waitWindowsGraphNodeLabel(t, engine, serverVTun, "windows-socks-server-vtun")
	waitWindowsGraphHTTPResponse(t, engine, menus, logf, httpClient, clientWireGuard, serverWireGuard, "Debug http server response")
	waitWindowsGraphMenuLog(t, menus, udpServer, "listening on "+net.JoinHostPort(serverAddress, fmt.Sprint(udpPort)))
	waitWindowsGraphUDPPingResponse(t, engine, menus, udpClient, "pong:"+udpPayload)

	// A successful request proves the tunnel transported data. Keep it active
	// briefly and reject the former steady retry signatures as well.
	time.Sleep(500 * time.Millisecond)
	for _, entry := range logf.Drain() {
		message := strings.ToLower(entry.Message)
		fromTunnel := entry.Source.NodeClass == wireguardnodes.NodeTypeWireGuard ||
			entry.Source.NodeClass == socksnodes.NodeTypeSocksServer
		if fromTunnel && (strings.Contains(message, "invalid argument") ||
			strings.Contains(message, "only one usage") || strings.Contains(message, "eof")) {
			t.Fatalf("unexpected WireGuard-over-SOCKS retry log from %s: %s", entry.Source.NodeClass, entry.Message)
		}
	}
}

func TestWindowsOutNetFixedWildcardUDPThroughSocksRelayAPI(t *testing.T) {
	_, system, _, _ := newWindowsNetworkGraphEngine(t)
	t.Run("raw OutNet", func(t *testing.T) {
		testWindowsOutNetFixedWildcardUDPPair(t, system.OutNet(), system.OutNet())
	})
	t.Run("detached OutNet", func(t *testing.T) {
		leftNetwork := gonnect.DetachNetwork(system.OutNet(), nil, nil)
		rightNetwork := gonnect.DetachNetwork(system.OutNet(), nil, nil)
		t.Cleanup(func() {
			_ = leftNetwork.Close()
			_ = rightNetwork.Close()
		})
		testWindowsOutNetFixedWildcardUDPPair(t, leftNetwork, rightNetwork)
	})
}

func testWindowsOutNetFixedWildcardUDPPair(t *testing.T, leftNetwork, rightNetwork gonnect.Network) {
	t.Helper()
	leftPort := reserveWindowsWildcardUDPPort(t, "udp4", "0.0.0.0:0")
	rightPort := reserveWindowsWildcardUDPPort(t, "udp4", "0.0.0.0:0")
	for rightPort == leftPort {
		rightPort = reserveWindowsWildcardUDPPort(t, "udp4", "0.0.0.0:0")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	left, err := leftNetwork.ListenPacket(ctx, "udp4", net.JoinHostPort("0.0.0.0", fmt.Sprint(leftPort)))
	if err != nil {
		t.Fatalf("left fixed wildcard ListenPacket: %v", err)
	}
	defer left.Close() //nolint:errcheck
	right, err := rightNetwork.ListenPacket(ctx, "udp4", net.JoinHostPort("0.0.0.0", fmt.Sprint(rightPort)))
	if err != nil {
		t.Fatalf("right fixed wildcard ListenPacket: %v", err)
	}
	defer right.Close() //nolint:errcheck

	for _, exchange := range []struct {
		name        string
		sender      gonnect.PacketConn
		receiver    gonnect.PacketConn
		destination int
	}{
		{name: "left to right", sender: left, receiver: right, destination: rightPort},
		{name: "right to left", sender: right, receiver: left, destination: leftPort},
	} {
		t.Run(exchange.name, func(t *testing.T) {
			payload := []byte(exchange.name)
			destination := protocol.AddrFromHostPort(
				net.JoinHostPort("127.0.0.1", fmt.Sprint(exchange.destination)), "udp4",
			)
			if err := protocol.WriteToAddrUDP(exchange.sender, destination, payload); err != nil {
				t.Fatalf("SOCKS relay WriteToAddrUDP: %v", err)
			}
			if err := exchange.receiver.SetReadDeadline(time.Now().Add(10 * time.Second)); err != nil {
				t.Fatal(err)
			}
			buffer := make([]byte, 64)
			n, source, err := exchange.receiver.ReadFrom(buffer)
			if err != nil || string(buffer[:n]) != string(payload) {
				t.Fatalf("fixed wildcard read = %q from %v, %v", buffer[:n], source, err)
			}
		})
	}
}

func TestWindowsGraphDefaultTunDNSProviderLifecycle(t *testing.T) {
	engine, _, _, _ := newWindowsNetworkGraphEngine(t)
	defaultTun := addWindowsGraphNode(t, engine, tunnodes.NodeTypeDefaultTun, "default tun")
	applyWindowsGraphForm(engine, defaultTun, "config", map[string]any{
		"tunAddrs": windowsGraphDNSAddress + "/32",
		"dnsIP":    windowsGraphDNSAddress,
	})
	static := addWindowsGraphNode(t, engine, dnsnodes.NodeTypeStaticDNS, "static dns")
	applyWindowsGraphForm(engine, static, "config", map[string]any{
		"records": "graph.test A 192.0.2.18", "ttl": 60,
	})
	link := linkWindowsGraphPorts(t, engine, static, "left", "DNS", defaultTun, "right", "DNS")
	waitWindowsGraphDNSAnswer(t, "udp", "graph.test.", netip.MustParseAddr("192.0.2.18"))
	waitWindowsGraphDNSAnswer(t, "tcp", "graph.test.", netip.MustParseAddr("192.0.2.18"))

	applyWindowsGraphForm(engine, static, "config", map[string]any{
		"records": "graph.test A 192.0.2.19", "ttl": 60,
	})
	waitWindowsGraphDNSAnswer(t, "udp", "graph.test.", netip.MustParseAddr("192.0.2.19"))
	engine.RemoveLink(almagest.MainWorkspace, link)
	if err := queryWindowsGraphDNS("udp", "graph.test.", netip.MustParseAddr("192.0.2.19")); err == nil {
		t.Fatal("DefaultTun DNS answered after provider detach; want SetDNS(nil) drop")
	}

	linkWindowsGraphPorts(t, engine, static, "left", "DNS", defaultTun, "right", "DNS")
	waitWindowsGraphDNSAnswer(t, "tcp", "graph.test.", netip.MustParseAddr("192.0.2.19"))
	snapshot, _ := engine.NodeSnapshot(almagest.MainWorkspace, defaultTun)
	foundWarning := false
	for _, popup := range snapshot.Popups {
		foundWarning = foundWarning || popup.Text == string(sysnet.WarningDefaultTunDNSRouteNotExclusive)
	}
	if !foundWarning {
		t.Fatalf("DefaultTun popups = %#v, want DNS route warning", snapshot.Popups)
	}
}

func TestWindowsGraphRejectsUnsupportedDefaultTunSourceRoutes(t *testing.T) {
	sender := &windowsTunConfigSenderClass{config: links.TunConfig{
		Addrs:  []string{"10.232.0.2/32"},
		Routes: []string{"192.0.2.0/24"},
		SourceRoutes: []links.TunSourceRoute{{
			Destination: netip.MustParsePrefix("192.0.2.0/24"),
			Source:      netip.MustParseAddr("10.232.0.2"),
		}},
	}}
	engine, _, _, logf := newWindowsNetworkGraphEngine(t, sender)
	defaultTun := addWindowsGraphNode(t, engine, tunnodes.NodeTypeDefaultTun, "default tun")
	provider := addWindowsGraphNode(t, engine, sender.ClassName(), "source routes")
	linkWindowsGraphPorts(t, engine, provider, "left", "Tun", defaultTun, "right", "Tun")
	waitWindowsGraphNodeLabel(t, engine, defaultTun, "invalid config")
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		for _, entry := range logf.Drain() {
			if strings.Contains(entry.Message, "DefaultTun.SourceRoutes") && strings.Contains(entry.Message, "not supported") {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("missing clear unsupported DefaultTun.SourceRoutes graph error")
}

func windowsGraphListenerAddress(t *testing.T, network gonnect.Network, address string) string {
	t.Helper()
	endpoint, err := netip.ParseAddrPort(address)
	if err != nil {
		t.Fatal(err)
	}
	if endpoint.Addr().IsUnspecified() {
		// OutNet sockets are bound to the selected underlay interface. A
		// loopback destination cannot be reached through that binding. LocalNet
		// exposes only loopback addresses through this same API.
		addrs, addrErr := network.InterfaceAddrs()
		if addrErr != nil {
			t.Fatal(addrErr)
		}
		for _, addr := range addrs {
			prefix, parseErr := netip.ParsePrefix(addr.String())
			if parseErr == nil && prefix.Addr().Is4() == endpoint.Addr().Is4() && !prefix.Addr().IsUnspecified() {
				return netip.AddrPortFrom(prefix.Addr(), endpoint.Port()).String()
			}
		}
		t.Fatalf("no network interface address for listener %s: %v", address, addrs)
	}
	return endpoint.String()
}

func testWindowsGraphTCP(t *testing.T, name string, network gonnect.Network, family, wildcard string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	listener, err := network.ListenTCP(ctx, family, wildcard)
	if err != nil {
		t.Fatalf("%s ListenTCP(%s): %v", name, family, err)
	}
	defer listener.Close() //nolint:errcheck
	accepted := make(chan error, 1)
	go func() {
		conn, acceptErr := listener.AcceptTCP()
		if acceptErr == nil {
			defer conn.Close() //nolint:errcheck
			buf := make([]byte, 4)
			_, acceptErr = io.ReadFull(conn, buf)
			if acceptErr == nil && string(buf) != "ping" {
				acceptErr = fmt.Errorf("TCP payload = %q", buf)
			}
		}
		accepted <- acceptErr
	}()
	conn, err := network.DialTCP(ctx, family, wildcard, windowsGraphListenerAddress(t, network, listener.Addr().String()))
	if err != nil {
		t.Fatalf("%s DialTCP(%s): %v", name, family, err)
	}
	if _, err = conn.Write([]byte("ping")); err != nil {
		t.Fatalf("%s TCP write(%s): %v", name, family, err)
	}
	_ = conn.Close()
	if err = <-accepted; err != nil {
		t.Fatalf("%s TCP accept(%s): %v", name, family, err)
	}
}

func testWindowsGraphUDP(t *testing.T, name string, network gonnect.Network, family, wildcard string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	server, err := network.ListenUDP(ctx, family, wildcard)
	if err != nil {
		t.Fatalf("%s ListenUDP(%s): %v", name, family, err)
	}
	defer server.Close() //nolint:errcheck
	client, err := network.DialUDP(ctx, family, wildcard, windowsGraphListenerAddress(t, network, server.LocalAddr().String()))
	if err != nil {
		t.Fatalf("%s DialUDP(%s): %v", name, family, err)
	}
	defer client.Close() //nolint:errcheck
	_ = server.SetReadDeadline(time.Now().Add(10 * time.Second))
	if _, err = client.Write([]byte("ping")); err != nil {
		t.Fatalf("%s UDP write(%s): %v", name, family, err)
	}
	buf := make([]byte, 4)
	if n, _, readErr := server.ReadFromUDP(buf); readErr != nil || n != len(buf) || string(buf) != "ping" {
		t.Fatalf("%s UDP read(%s) = %d, %q, %v", name, family, n, buf[:n], readErr)
	}
}

func testWindowsGraphWildcardUDPLoopback(t *testing.T, network gonnect.Network, family, wildcard string) {
	t.Helper()
	loopback := netip.MustParseAddr("127.0.0.1")
	if family == "udp6" {
		loopback = netip.IPv6Loopback()
	}
	requestedAddress, err := net.ResolveUDPAddr(family, wildcard)
	if err != nil {
		t.Fatalf("resolve wildcard UDP address %q: %v", wildcard, err)
	}
	reservation, err := net.ListenUDP(family, requestedAddress)
	if err != nil {
		t.Fatalf("reserve wildcard UDP port(%s): %v", family, err)
	}
	requestedPort := reservation.LocalAddr().(*net.UDPAddr).Port
	if err = reservation.Close(); err != nil {
		t.Fatalf("release wildcard UDP port(%s): %v", family, err)
	}
	requestedAddress.Port = requestedPort
	receiver, err := net.ListenUDP(family, net.UDPAddrFromAddrPort(netip.AddrPortFrom(loopback, 0)))
	if err != nil {
		t.Fatalf("native loopback ListenUDP(%s): %v", family, err)
	}
	defer receiver.Close() //nolint:errcheck
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	connection, err := network.ListenUDP(ctx, family, requestedAddress.String())
	if err != nil {
		t.Fatalf("OutNet wildcard ListenUDP(%s, %s): %v", family, requestedAddress, err)
	}
	defer connection.Close() //nolint:errcheck
	logicalPort := connection.LocalAddr().(*net.UDPAddr).Port
	if logicalPort != requestedPort {
		t.Fatalf("OutNet wildcard listen port(%s) = %d, want requested port %d", family, logicalPort, requestedPort)
	}
	destination := receiver.LocalAddr().(*net.UDPAddr).AddrPort()
	if _, err = connection.WriteToUDPAddrPort([]byte("loop"), destination); err != nil {
		t.Fatalf("OutNet wildcard loopback write(%s): %v", family, err)
	}
	_ = receiver.SetReadDeadline(time.Now().Add(10 * time.Second))
	buffer := make([]byte, 8)
	n, source, err := receiver.ReadFromUDPAddrPort(buffer)
	if err != nil || string(buffer[:n]) != "loop" {
		t.Fatalf("native loopback read(%s) = %q from %s, %v", family, buffer[:n], source, err)
	}
	if int(source.Port()) != logicalPort {
		t.Fatalf("OutNet loopback source port(%s) = %d, want logical port %d", family, source.Port(), logicalPort)
	}
	if _, err = receiver.WriteToUDPAddrPort([]byte("reply"), source); err != nil {
		t.Fatalf("native loopback reply(%s): %v", family, err)
	}
	_ = connection.SetReadDeadline(time.Now().Add(10 * time.Second))
	n, replySource, err := connection.ReadFromUDPAddrPort(buffer)
	if err != nil || string(buffer[:n]) != "reply" || replySource != destination {
		t.Fatalf("OutNet loopback reply read(%s) = %q from %s, %v; want source %s", family, buffer[:n], replySource, err, destination)
	}
}

func applyWindowsGraphVTun(engine almagest.Engine, node uint64, name, localAddress string) {
	applyWindowsGraphForm(engine, node, "config", map[string]any{
		"name": name, "localAddrs": localAddress, "dnsServers": "", "mtu": 1500,
		"endpointCh": 1024, "eventCh": 10, "noLoopbackAddr": false, "disableSACK": false,
	})
}

func windowsGraphWireGuardKey(t *testing.T, engine almagest.Engine, node uint64) (string, string) {
	t.Helper()
	privateKey, err := wgdevice.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate WireGuard key: %v", err)
	}
	snapshot, ok := engine.NodeSnapshot(almagest.MainWorkspace, node)
	if !ok {
		t.Fatalf("WireGuard node %d snapshot missing", node)
	}
	secret, err := engine.SecretsStorage().NewSecret(storage.Secret{Value: privateKey.Base64()}, snapshot.Name)
	if err != nil {
		t.Fatalf("store WireGuard private key: %v", err)
	}
	return secret, privateKey.PublicKey().Base64()
}

func applyWindowsGraphWireGuard(engine almagest.Engine, node uint64, privateKeySecret string, listenPort int, ownedIPs string, peers []formular.ArrayElementValue) {
	applyWindowsGraphForm(engine, node, "config", map[string]any{
		"privateKeySecret": privateKeySecret, "listenPort": listenPort, "ownedIPs": ownedIPs,
	})
	applyWindowsGraphForm(engine, node, "peers", map[string]any{"peers": peers})
}

func waitWindowsGraphHTTPResponse(t *testing.T, engine almagest.Engine, menus *windowsGraphMenuRecorder, logf *logging.DebugLogFactory, client, clientWireGuard, serverWireGuard uint64, want string) {
	t.Helper()
	deadline := time.Now().Add(45 * time.Second)
	var status, response, requestError string
	for time.Now().Before(deadline) {
		status, response, requestError, _ = menus.httpClientResult(client)
		snapshot, _ := engine.NodeSnapshot(almagest.MainWorkspace, client)
		if status == "Status: 200 OK" && response == want && requestError == "" && snapshot.Label == "200 OK" {
			return
		}
		if status != "Status: running..." {
			engine.SendNodeFormularMsg(almagest.MainWorkspace, client, formular.ButtonPressMessage{
				MessageBase: formular.MessageBase{Type: formular.MessageButtonPress, MenuID: pasta.NodeMenuID(client)},
				BlockID:     "request", ButtonID: "send",
			})
		}
		time.Sleep(100 * time.Millisecond)
	}
	for _, entry := range logf.Drain() {
		if entry.Source.NodeClass == wireguardnodes.NodeTypeWireGuard || entry.Source.NodeClass == socksnodes.NodeTypeSocksServer {
			t.Logf("%s[%d] %s: %s", entry.Source.NodeClass, entry.Source.NodeID, entry.Level, entry.Message)
		}
	}
	t.Fatalf(
		"HTTP request through WireGuard over SOCKS failed: status=%q response=%q error=%q clientWG=%v serverWG=%v",
		status, response, requestError, menus.blockValues(clientWireGuard, "status"), menus.blockValues(serverWireGuard, "status"),
	)
}

func waitWindowsGraphUDPPingResponse(t *testing.T, engine almagest.Engine, menus *windowsGraphMenuRecorder, client uint64, want string) {
	t.Helper()
	deadline := time.Now().Add(45 * time.Second)
	var status, response, requestError string
	for time.Now().Before(deadline) {
		result := menus.blockValues(client, "result")
		status, _ = result["statusText"].(string)
		response, _ = result["response"].(string)
		requestError, _ = result["error"].(string)
		snapshot, _ := engine.NodeSnapshot(almagest.MainWorkspace, client)
		if status == "Status: ok" && response == want && requestError == "" && snapshot.Label == "ok" {
			return
		}
		if status != "Status: running..." {
			engine.SendNodeFormularMsg(almagest.MainWorkspace, client, formular.ButtonPressMessage{
				MessageBase: formular.MessageBase{Type: formular.MessageButtonPress, MenuID: pasta.NodeMenuID(client)},
				BlockID:     "config", ButtonID: "send",
			})
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("UDP ping through WireGuard over SOCKS failed: status=%q response=%q error=%q", status, response, requestError)
}

func waitWindowsGraphMenuLog(t *testing.T, menus *windowsGraphMenuRecorder, node uint64, want string) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if menus.logContains(node, want) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("node %d log did not contain %q", node, want)
}

func (r *windowsGraphMenuRecorder) httpClientResult(node uint64) (string, string, string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	menu, ok := r.state.Snapshot(pasta.NodeMenuID(node))
	if !ok {
		return "", "", "", false
	}
	var status, response, requestError string
	for _, block := range menu.Blocks {
		if block.ID != "result" {
			continue
		}
		for _, item := range block.Items {
			switch item.ID {
			case "status":
				status = item.Text
			case "response":
				response, _ = formular.StringValue(item.Value)
			case "error":
				requestError, _ = formular.StringValue(item.Value)
			}
		}
		return status, response, requestError, true
	}
	return "", "", "", false
}

func (r *windowsGraphMenuRecorder) logContains(node uint64, want string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	menu, ok := r.state.Snapshot(pasta.NodeMenuID(node))
	if !ok {
		return false
	}
	for _, block := range menu.Blocks {
		for _, item := range block.Items {
			for _, line := range item.Logs {
				if strings.Contains(line.Text, want) {
					return true
				}
			}
		}
	}
	return false
}

func (r *windowsGraphMenuRecorder) blockValues(node uint64, blockID string) map[string]any {
	r.mu.Lock()
	defer r.mu.Unlock()
	values := map[string]any{}
	menu, ok := r.state.Snapshot(pasta.NodeMenuID(node))
	if !ok {
		return values
	}
	for _, block := range menu.Blocks {
		if block.ID != blockID {
			continue
		}
		for _, item := range block.Items {
			if item.Field != nil {
				values[item.ID] = item.Value
			}
			if item.Text != "" {
				values[item.ID+"Text"] = item.Text
			}
		}
	}
	return values
}

func waitWindowsGraphTCPConnect(t *testing.T, network gonnect.Network, family, address string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		conn, err := network.DialTCP(ctx, family, "", address)
		cancel()
		if err == nil {
			_ = conn.Close()
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("LocalNet did not connect to local proxy at %s", address)
}

func reserveWindowsLoopbackPort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve loopback port: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	return port
}

func reserveWindowsLoopbackUDPPort(t *testing.T) int {
	t.Helper()
	listener, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatalf("reserve loopback UDP port: %v", err)
	}
	port := listener.LocalAddr().(*net.UDPAddr).Port
	_ = listener.Close()
	return port
}

func reserveWindowsWildcardUDPPort(t *testing.T, network, address string) int {
	t.Helper()
	resolved, err := net.ResolveUDPAddr(network, address)
	if err != nil {
		t.Fatalf("resolve wildcard UDP address: %v", err)
	}
	listener, err := net.ListenUDP(network, resolved)
	if err != nil {
		t.Fatalf("reserve wildcard UDP port: %v", err)
	}
	port := listener.LocalAddr().(*net.UDPAddr).Port
	if err = listener.Close(); err != nil {
		t.Fatalf("release wildcard UDP port: %v", err)
	}
	return port
}

func waitWindowsGraphDNSAnswer(t *testing.T, network, name string, want netip.Addr) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	var err error
	for time.Now().Before(deadline) {
		err = queryWindowsGraphDNS(network, name, want)
		if err == nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("%s DNS query for %s: %v", network, name, err)
}

func queryWindowsGraphDNS(network, name string, want netip.Addr) error {
	request := &dns.Message{ID: dns.NextID(), RecursionDesired: true, Questions: []dns.Question{{
		Name: name, Type: dns.TypeA, Class: dns.ClassIN,
	}}}
	wire, err := dns.Pack(request)
	if err != nil {
		return err
	}
	conn, err := net.DialTimeout(network, net.JoinHostPort(windowsGraphDNSAddress, "53"), 500*time.Millisecond)
	if err != nil {
		return err
	}
	defer conn.Close() //nolint:errcheck
	_ = conn.SetDeadline(time.Now().Add(500 * time.Millisecond))
	if network == "tcp" {
		framed := make([]byte, 2+len(wire))
		binary.BigEndian.PutUint16(framed, uint16(len(wire))) //nolint:gosec // DNS TCP frames are 16-bit.
		copy(framed[2:], wire)
		if _, err = conn.Write(framed); err != nil {
			return err
		}
		if _, err = io.ReadFull(conn, framed[:2]); err != nil {
			return err
		}
		wire = make([]byte, binary.BigEndian.Uint16(framed[:2]))
		if _, err = io.ReadFull(conn, wire); err != nil {
			return err
		}
	} else {
		if _, err = conn.Write(wire); err != nil {
			return err
		}
		wire = make([]byte, 4096)
		n, readErr := conn.Read(wire)
		if readErr != nil {
			return readErr
		}
		wire = wire[:n]
	}
	response, err := dns.Unpack(wire)
	if err != nil {
		return err
	}
	for _, answer := range response.Answers {
		if answer.Type == dns.TypeA && len(answer.Data) == net.IPv4len && netip.AddrFrom4([4]byte(answer.Data)) == want {
			return nil
		}
	}
	return fmt.Errorf("DNS answers = %#v, want %s", response.Answers, want)
}

type windowsNetworkReceiverClass struct {
	events chan links.NetworkPayload
}

func (*windowsNetworkReceiverClass) ClassName() string        { return "test/WindowsNetworkReceiver" }
func (*windowsNetworkReceiverClass) ShortDescription() string { return "Windows network receiver" }
func (*windowsNetworkReceiverClass) LongDescription() string {
	return "Receives native network graph values."
}
func (*windowsNetworkReceiverClass) DefaultNodeParams() pasta.NodeClassParams {
	return pasta.NodeClassParams{InitialPorts: []pasta.Port{nodekit.NetworkPort("right", "Network")}}
}
func (c *windowsNetworkReceiverClass) NewNode(configer.Config, ...*pasta.NodeClassState) (pasta.Node, error) {
	return &windowsNetworkReceiverNode{events: c.events}, nil
}

type windowsNetworkReceiverNode struct {
	pasta.BasicNode
	in     uint64
	events chan links.NetworkPayload
}

func (n *windowsNetworkReceiverNode) OnInit(_ *pasta.Workspace, _ pasta.Logger, _ uint64, _ string, restored *pasta.NodeInitData, _, _, _, _ bool) error {
	n.in = restored.RightPorts[0]
	return nil
}
func (n *windowsNetworkReceiverNode) PreLinkAdd(port uint64, linkType, direction string) error {
	if port != n.in || direction != "right" || linkType != links.TypeNetwork {
		return pasta.LinkTypeErr(linkType)
	}
	return nil
}
func (n *windowsNetworkReceiverNode) OnEvent(event pasta.Event, linkType string, _ []string, direction string) error {
	if event.ReceiverPort == n.in && direction == "right" && linkType == links.TypeNetwork {
		if payload, ok := event.Payload.(links.NetworkPayload); ok {
			n.events <- payload
		}
	}
	return nil
}

type windowsTunConfigSenderClass struct {
	config links.TunConfig
}

func (*windowsTunConfigSenderClass) ClassName() string        { return "test/WindowsTunConfigSender" }
func (*windowsTunConfigSenderClass) ShortDescription() string { return "Windows TUN config sender" }
func (*windowsTunConfigSenderClass) LongDescription() string {
	return "Sends one TUN config for policy tests."
}
func (*windowsTunConfigSenderClass) DefaultNodeParams() pasta.NodeClassParams {
	return pasta.NodeClassParams{InitialPorts: []pasta.Port{nodekit.TunPort("left", "Tun")}}
}
func (c *windowsTunConfigSenderClass) NewNode(configer.Config, ...*pasta.NodeClassState) (pasta.Node, error) {
	return &windowsTunConfigSenderNode{config: c.config}, nil
}

type windowsTunConfigSenderNode struct {
	pasta.BasicNode
	w      *pasta.Workspace
	id     uint64
	out    uint64
	config links.TunConfig
}

func (n *windowsTunConfigSenderNode) OnInit(w *pasta.Workspace, _ pasta.Logger, id uint64, _ string, restored *pasta.NodeInitData, _, _, _, _ bool) error {
	n.w = w
	n.id = id
	n.out = restored.LeftPorts[0]
	return nil
}
func (n *windowsTunConfigSenderNode) PreLinkAdd(port uint64, linkType, direction string) error {
	if port != n.out || direction != "left" || linkType != links.TypeTun {
		return pasta.LinkTypeErr(linkType)
	}
	return nil
}
func (n *windowsTunConfigSenderNode) OnLinkAdd(link, port uint64, _, _ string) error {
	if port == n.out {
		n.w.EmitEventLocked(n.id, link, n.config)
	}
	return nil
}

func newWindowsNetworkGraphEngine(t *testing.T, extra ...pasta.NodeClass) (almagest.Engine, sysnet.System, *windowsNetworkReceiverClass, *logging.DebugLogFactory) {
	return newWindowsNetworkGraphEngineWithDebug(t, false, extra...)
}

func newWindowsDebugNetworkGraphEngine(t *testing.T, extra ...pasta.NodeClass) (almagest.Engine, sysnet.System, *windowsNetworkReceiverClass, *logging.DebugLogFactory) {
	return newWindowsNetworkGraphEngineWithDebug(t, true, extra...)
}

func newWindowsNetworkGraphEngineWithDebug(t *testing.T, debug bool, extra ...pasta.NodeClass) (almagest.Engine, sysnet.System, *windowsNetworkReceiverClass, *logging.DebugLogFactory) {
	t.Helper()
	if !windows.GetCurrentProcessToken().IsElevated() {
		t.Skip("native Windows network graph tests require an elevated identity")
	}
	system, err := SystemIntegration(configer.NewMemory(nil), nil)
	if err != nil {
		t.Fatalf("SystemIntegration: %v", err)
	}
	receiver := &windowsNetworkReceiverClass{events: make(chan links.NetworkPayload, 8)}
	classes := append([]pasta.NodeClass{receiver}, extra...)
	logf := logging.NewDebugLogFactory(4096)
	engine, err := almagest.BuildEngine(
		context.Background(), configer.NewMemory(nil), logf, system,
		nil, nil, &windowsGraphSecrets{values: map[string]storage.Secret{}}, nil,
		classes, nil, debug,
	)
	if err != nil {
		_ = system.Close()
		t.Fatalf("BuildEngine: %v", err)
	}
	t.Cleanup(func() {
		if err := engine.Close(); err != nil {
			t.Errorf("engine.Close: %v", err)
		}
	})
	return engine, system, receiver, logf
}

type windowsGraphSecrets struct {
	mu     sync.Mutex
	values map[string]storage.Secret
	next   int
}

func (s *windowsGraphSecrets) GetSecret(key, _ string) (storage.Secret, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	secret, ok := s.values[key]
	if !ok {
		return storage.Secret{}, storage.ErrSecretAccessDenied
	}
	return secret, nil
}
func (s *windowsGraphSecrets) NewSecret(secret storage.Secret, _ string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.next++
	key := fmt.Sprintf("windows-graph-secret-%d", s.next)
	s.values[key] = secret
	return key, nil
}

func addWindowsGraphNode(t *testing.T, engine almagest.Engine, class, name string) uint64 {
	t.Helper()
	id, err := engine.AddNode(almagest.MainWorkspace, class, name)
	if err != nil {
		t.Fatalf("AddNode %s: %v", class, err)
	}
	return id
}

func linkWindowsGraphPorts(t *testing.T, engine almagest.Engine, from uint64, fromDirection, fromName string, to uint64, toDirection, toName string) uint64 {
	t.Helper()
	fromPort := windowsGraphPort(t, engine, from, fromDirection, fromName)
	toPort := windowsGraphPort(t, engine, to, toDirection, toName)
	link, _, err := engine.AddLink(almagest.MainWorkspace, fromPort, toPort)
	if err != nil {
		t.Fatalf("AddLink %d/%s to %d/%s: %v", from, fromName, to, toName, err)
	}
	return link
}

func windowsGraphPort(t *testing.T, engine almagest.Engine, node uint64, direction, name string) uint64 {
	t.Helper()
	snapshot, ok := engine.NodeSnapshot(almagest.MainWorkspace, node)
	if !ok {
		t.Fatalf("node %d snapshot missing", node)
	}
	ports := snapshot.LeftPorts
	if direction == "right" {
		ports = snapshot.RightPorts
	}
	for _, id := range ports {
		port, ok := engine.PortSnapshot(almagest.MainWorkspace, id)
		if ok && port.Name == name {
			return id
		}
	}
	t.Fatalf("node %d has no %s port %q", node, direction, name)
	return 0
}

func receiveWindowsNetworkPayload(t *testing.T, events <-chan links.NetworkPayload) links.NetworkPayload {
	t.Helper()
	select {
	case payload := <-events:
		if payload.Network == nil {
			t.Fatal("received nil Windows network payload")
		}
		return payload
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for Windows network payload")
		return links.NetworkPayload{}
	}
}

func applyWindowsGraphForm(engine almagest.Engine, node uint64, block string, values map[string]any) {
	engine.SendNodeFormularMsg(almagest.MainWorkspace, node, formular.FormApplyMessage{
		MessageBase: formular.MessageBase{Type: formular.MessageFormApply, MenuID: pasta.NodeMenuID(node)},
		BlockID:     block, Values: values,
	})
}

func waitWindowsGraphNodeLabel(t *testing.T, engine almagest.Engine, node uint64, want string) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if snapshot, ok := engine.NodeSnapshot(almagest.MainWorkspace, node); ok && snapshot.Label == want {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	snapshot, _ := engine.NodeSnapshot(almagest.MainWorkspace, node)
	t.Fatalf("node %d label = %q, want %q", node, snapshot.Label, want)
}
