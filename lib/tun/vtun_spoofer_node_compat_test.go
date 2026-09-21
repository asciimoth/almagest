package tunnodes_test

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	dnsnodes "github.com/asciimoth/almagest/lib/dns"
	networknodes "github.com/asciimoth/almagest/lib/network"
	tunnodes "github.com/asciimoth/almagest/lib/tun"
	"github.com/asciimoth/formular"
	"github.com/asciimoth/gonnect"
	"github.com/asciimoth/pasta/pasta/std"
)

const (
	vtunSpooferMiddlewareDNS          = "192.0.2.53"
	vtunSpooferMiddlewareTarget       = "192.0.2.80"
	vtunSpooferMiddlewareHost         = "middleware-stack.test"
	vtunSpooferMiddlewareResponse     = "Response through TUN and Network middleware"
	vtunSpooferMiddlewareReplyPort    = 53053
	vtunSpooferMiddlewareRequestCount = 5
)

type vtunSpooferReplyPortNetwork struct {
	*gonnect.LoopbackNetwork

	translatedReplies atomic.Int64
}

type vtunSpooferReplyPortPacketConn struct {
	gonnect.PacketConn
	network *vtunSpooferReplyPortNetwork
}

type vtunSpooferReplyPortUDPConn struct {
	gonnect.UDPConn
	network *vtunSpooferReplyPortNetwork
}

func newVTunSpooferReplyPortNetwork() *vtunSpooferReplyPortNetwork {
	loopback := gonnect.NewLoopbackNetwork()
	loopback.AllowAnyHost = true
	return &vtunSpooferReplyPortNetwork{LoopbackNetwork: loopback}
}

func (n *vtunSpooferReplyPortNetwork) ListenPacket(
	ctx context.Context,
	network, address string,
) (gonnect.PacketConn, error) {
	conn, err := n.LoopbackNetwork.ListenPacket(ctx, network, address)
	if err != nil {
		return nil, err
	}
	return &vtunSpooferReplyPortPacketConn{PacketConn: conn, network: n}, nil
}

func (n *vtunSpooferReplyPortNetwork) ListenUDP(
	ctx context.Context,
	network, address string,
) (gonnect.UDPConn, error) {
	conn, err := n.LoopbackNetwork.ListenUDP(ctx, network, address)
	if err != nil {
		return nil, err
	}
	return &vtunSpooferReplyPortUDPConn{UDPConn: conn, network: n}, nil
}

func (c *vtunSpooferReplyPortPacketConn) ReadFrom(p []byte) (int, net.Addr, error) {
	n, source, err := c.PacketConn.ReadFrom(p)
	return n, c.network.translateDNSReplySource(source), err
}

func (c *vtunSpooferReplyPortUDPConn) ReadFrom(p []byte) (int, net.Addr, error) {
	n, source, err := c.UDPConn.ReadFrom(p)
	return n, c.network.translateDNSReplySource(source), err
}

func (n *vtunSpooferReplyPortNetwork) translateDNSReplySource(source net.Addr) net.Addr {
	if source == nil {
		return nil
	}
	host, port, err := net.SplitHostPort(source.String())
	if err != nil || host != "127.0.0.1" || port != "53" {
		return source
	}
	n.translatedReplies.Add(1)
	return &net.UDPAddr{
		IP:   net.ParseIP(vtunSpooferMiddlewareDNS),
		Port: vtunSpooferMiddlewareReplyPort,
	}
}

func TestSpooferDoesNotAddRoutesToDefaultTun(t *testing.T) {
	e := newHTTPE2E(t)
	defaultTun := e.AddNode(NodeTypeDefaultTun, "default-tun")
	spoofer := e.AddNode(NodeTypeSpoofer, "spoofer")
	backend := e.AddNode(NodeTypeMockNetwork, "backend")
	e.SubscribeMenus(defaultTun, spoofer)

	e.connectByPortName(backend, "left", "Network", spoofer, "right", "Network")
	e.connectTunByPortName(spoofer, "left", "Tun", defaultTun, "right", "Tun")
	e.WaitNodeLabel(spoofer, "forwarding")

	Eventually(t, func() bool {
		_, ok := e.DebugSystem.GetDefaultTunPeer()
		return ok
	}, "DefaultTun runtime")
	entry, ok := e.DebugSystem.GetDefaultTunPeer()
	if !ok {
		t.Fatal("DefaultTun runtime is missing")
	}
	if len(entry.Config.TunRoutes) != 0 {
		t.Fatalf("DefaultTun routes = %v, want none from Spoofer", entry.Config.TunRoutes)
	}
	if len(entry.Config.SourceRoutes) != 0 {
		t.Fatalf("DefaultTun source routes = %v, want none from Spoofer", entry.Config.SourceRoutes)
	}
}

func TestVTunSpooferUDPReplyPortChangeThroughMiddlewareNodes(t *testing.T) {
	output := newVTunSpooferReplyPortNetwork()
	t.Cleanup(func() { _ = output.Close() })
	e := newVTunSpooferFallbackE2E(t, output)

	out := e.AddNode(NodeTypeOutNetwork, "controlled-out")
	remapper := e.AddNode(networknodes.NodeTypeRemapper, "remapper")
	router := e.AddNode(networknodes.NodeTypeNetworkRouter, "router")
	networkFirewall := e.AddNode(networknodes.NodeTypeNetworkFirewall, "network-firewall")
	spoofer := e.AddNode(NodeTypeSpoofer, "spoofer")
	joiner := e.AddNode(NodeTypeTunJoiner, "joiner")
	logTun := e.AddNode(tunnodes.NodeTypeDebugLogTun, "log-tun")
	tunFirewall := e.AddNode(tunnodes.NodeTypeTunFirewall, "tun-firewall")
	vtun := e.AddNode(NodeTypeVTun, "vtun")
	staticDNS := e.AddNode(dnsnodes.NodeTypeStaticDNS, "static-dns")
	dnsServer := e.AddNode(dnsnodes.NodeTypeDNSServer, "dns-server")
	response := e.addStringConstant("response", vtunSpooferMiddlewareResponse)
	serverPort := httpTestPort()
	server := e.addHTTPServerOnHost("server", vtunSpooferMiddlewareTarget, serverPort)
	clientURL := fmt.Sprintf("http://%s:%d/", vtunSpooferMiddlewareHost, serverPort)
	client := e.addHTTPClientToURL("client", clientURL)
	e.SubscribeMenus(
		remapper, router, networkFirewall, spoofer, joiner, logTun, tunFirewall,
		vtun, staticDNS, dnsServer, response, server, client,
	)
	e.waitClientRequestField(client, "url", clientURL)

	newDNSE2EFromNodeE2E(e.NodeE2E).applyStaticDNSRecords(
		staticDNS,
		fmt.Sprintf("%s A %s", vtunSpooferMiddlewareHost, vtunSpooferMiddlewareTarget),
	)
	e.Engine.SendNodeFormularMsg(E2EWorkspace, dnsServer, formular.FormApplyMessage{
		MessageBase: FormMessageBase(formular.MessageFormApply, dnsServer),
		BlockID:     dnsnodes.DNSServerConfigBlockID,
		Values: map[string]any{
			dnsnodes.DNSServerNetworkField: "udp4",
			dnsnodes.DNSServerHostField:    vtunSpooferMiddlewareDNS,
			dnsnodes.DNSServerPortField:    53,
		},
	})
	e.Engine.SendNodeFormularMsg(E2EWorkspace, router, formular.FormApplyMessage{
		MessageBase: FormMessageBase(formular.MessageFormApply, router),
		BlockID:     networknodes.NetworkRouterConfigBlockID,
		Values: map[string]any{
			networknodes.NetworkRouterRulesField: "TRUE\nSLOT 1",
		},
	})
	// The firewall tracks UDP responses by the complete remote endpoint. A
	// reply from another source port is unsolicited by design, so permit the
	// controlled DNS host explicitly. This also verifies that Spoofer's broader
	// reply handling does not bypass NetworkFirewall policy.
	e.Engine.SendNodeFormularMsg(E2EWorkspace, networkFirewall, formular.FormApplyMessage{
		MessageBase: FormMessageBase(formular.MessageFormApply, networkFirewall),
		BlockID:     networknodes.NetworkFirewallConfigBlockID,
		Values: map[string]any{
			networknodes.NetworkFirewallRulesField: fmt.Sprintf(
				`{"include":[{"network":"udp","hosts":[%q]}]}`,
				vtunSpooferMiddlewareDNS,
			),
		},
	})
	e.Engine.SendNodeFormularMsg(E2EWorkspace, vtun, formular.FormApplyMessage{
		MessageBase: FormMessageBase(formular.MessageFormApply, vtun),
		BlockID:     "config",
		Values: map[string]any{
			"name":           "middleware-vtun",
			"localAddrs":     "192.0.2.2",
			"dnsServers":     vtunSpooferMiddlewareDNS,
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
	e.connectByPortName(out, "left", "Network", remapper, "right", "Network")
	e.connectByPortName(remapper, "left", "Network", router, "right", "slot 1")
	e.connectByPortName(router, "left", "Network", networkFirewall, "right", "Network")
	e.connectByPortName(networkFirewall, "left", "Network", spoofer, "right", "Network")
	e.connectTunByPortName(tunFirewall, "left", "Tun", vtun, "right", "Tun")
	e.connectTunByPortName(logTun, "left", "in", tunFirewall, "right", "Tun")
	e.connectTunByPortName(joiner, "left", "Default", logTun, "right", "Tun")
	e.connectTunByPortName(spoofer, "left", "Tun", joiner, "right", "Tun")
	e.connectByPortName(vtun, "left", "Network", client, "right", "Network")

	e.WaitNodeLabel(remapper, "remapping network")
	e.WaitNodeLabel(router, "router")
	e.WaitNodeLabel(networkFirewall, "filtering 1 rules")
	e.WaitNodeLabel(tunFirewall, "filtering 0 rules")
	e.WaitNodeLabel(logTun, "tun log")
	e.WaitNodeLabel(joiner, "joined default")
	e.WaitNodeLabel(spoofer, "forwarding")
	e.WaitNodeLabel(vtun, "middleware-vtun")
	e.WaitNodeLabel(
		dnsServer,
		"listening "+net.JoinHostPort(vtunSpooferMiddlewareDNS, strconv.Itoa(53)),
	)
	e.waitServerListening(server, net.JoinHostPort(vtunSpooferMiddlewareTarget, strconv.Itoa(serverPort)))
	e.WaitClientReady(client)

	for range vtunSpooferMiddlewareRequestCount {
		e.assertClientRequestReturns(client, vtunSpooferMiddlewareResponse, 10*time.Second)
	}
	if got := output.translatedReplies.Load(); got == 0 {
		t.Fatal("controlled network did not produce a changed-port DNS reply")
	}
}

var _ gonnect.Network = (*vtunSpooferReplyPortNetwork)(nil)
