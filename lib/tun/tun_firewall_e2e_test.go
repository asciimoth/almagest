package tunnodes_test

import (
	"fmt"
	"net"
	"strings"
	"testing"

	debugnodes "github.com/asciimoth/almagest/lib/debug"
	"github.com/asciimoth/almagest/lib/testkit"
	"github.com/asciimoth/almagest/lib/testkit/debugtest"
	tunnodes "github.com/asciimoth/almagest/lib/tun"
	"github.com/asciimoth/formular"
	"github.com/asciimoth/pasta/pasta/std"
)

func TestTunFirewallE2EVTunCopyHTTPAndUDP(t *testing.T) {
	e := newHTTPE2E(t)
	copyNode := e.AddNode(NodeTypeTunCopy, "firewall-copy")
	firewall := e.AddNode(tunnodes.NodeTypeTunFirewall, "tun-firewall")
	clientVTun := e.AddNode(NodeTypeVTun, "firewall-client-vtun")
	serverVTun := e.AddNode(NodeTypeVTun, "firewall-server-vtun")
	tcpConfig := e.AddNode(std.NodeTypeObjectConstant, "tun-tcp-config")
	udpConfig := e.AddNode(std.NodeTypeObjectConstant, "tun-udp-config")
	httpPort := httpTestPort()
	udpPort := httpTestPort()
	httpServer := e.addHTTPServerOnHost("tun-firewall-http-server", tunCopyE2EServerAddr, httpPort)
	httpClient := e.addHTTPClientToURL("tun-firewall-http-client", fmt.Sprintf("http://%s:%d/", tunCopyE2EServerAddr, httpPort))
	udpServer := e.AddNode(debugnodes.NodeTypeUDPPingServer, "tun-firewall-udp-server")
	udpClient := e.AddNode(debugnodes.NodeTypeUDPPingClient, "tun-firewall-udp-client")
	e.SubscribeMenus(
		copyNode, firewall, clientVTun, serverVTun, tcpConfig, udpConfig,
		httpServer, httpClient, udpServer, udpClient,
	)
	e.ApplyStdConstantValue(tcpConfig, `{}`)
	e.ApplyStdConstantValue(udpConfig, `{}`)
	e.applyTunCopyVTunConfig(clientVTun, "firewall-client-vtun", tunCopyE2EClientAddr+"/24")
	e.applyTunCopyVTunConfig(serverVTun, "firewall-server-vtun", tunCopyE2EServerAddr+"/24")
	applyTunFirewallUDPServerE2E(t, e.NodeE2E, udpServer, udpPort)
	applyTunFirewallUDPClientE2E(t, e.NodeE2E, udpClient, udpPort)

	inputLink := e.MustAddTypedLinkByPortName(TypeTun, firewall, "left", "Tun", clientVTun, "right", "Tun")
	outputLink := e.MustAddTypedLinkByPortName(TypeTun, copyNode, "left", "TunA", firewall, "right", "Tun")
	e.MustAddTypedLinkByPortName(TypeTun, copyNode, "left", "TunB", serverVTun, "right", "Tun")
	e.connectByPortName(clientVTun, "left", "Network", httpClient, "right", "Network")
	e.connectByPortName(clientVTun, "left", "Network", udpClient, "right", "Network")
	e.connectByPortName(serverVTun, "left", "Network", httpServer, "right", "Network")
	e.connectByPortName(serverVTun, "left", "Network", udpServer, "right", "Network")
	e.MustAddTypedLinkByPortName(std.TypeObject, tcpConfig, "right", "output", firewall, "left", "Config")
	udpConfigLink := e.MustAddTypedLinkByPortName(std.TypeObject, udpConfig, "right", "output", firewall, "left", "Config")

	e.WaitNodeLabel(copyNode, "copying")
	e.WaitNodeLabel(firewall, "filtering 0 rules")
	e.waitServerListening(httpServer, fmt.Sprintf("%s:%d", tunCopyE2EServerAddr, httpPort))
	waitTunFirewallUDPServerE2E(t, e.NodeE2E, udpServer, "listening on")
	e.assertClientRequestWorks(httpClient)
	assertTunFirewallUDPWorksE2E(t, e.NodeE2E, udpClient)

	// The attached object producer updates the live tun.Firewall instance. UDP
	// has a short deterministic timeout, while HTTP proves unrelated traffic
	// remains available through the same wrapper.
	e.ApplyStdConstantValue(udpConfig, fmt.Sprintf(`{"exclude":[{"network":"udp","ports":[%d]}]}`, udpPort))
	sendTunFirewallUDPE2E(t, e.NodeE2E, udpClient)
	waitTunFirewallUDPFailureE2E(t, e.NodeE2E, udpClient)
	e.assertClientRequestWorks(httpClient)

	// Object detach removes its rule. Reattach restores the producer's latest
	// value, and another value update clears it again.
	e.Engine.RemoveLink(E2EWorkspace, udpConfigLink)
	assertTunFirewallUDPWorksE2E(t, e.NodeE2E, udpClient)
	udpConfigLink = e.MustAddTypedLinkByPortName(std.TypeObject, udpConfig, "right", "output", firewall, "left", "Config")
	sendTunFirewallUDPE2E(t, e.NodeE2E, udpClient)
	waitTunFirewallUDPFailureE2E(t, e.NodeE2E, udpClient)
	e.ApplyStdConstantValue(udpConfig, `{}`)
	assertTunFirewallUDPWorksE2E(t, e.NodeE2E, udpClient)

	// Menu input follows the same atomic update path and can filter UDP without
	// interrupting the TCP flow.
	applyTunFirewallMenuE2E(t, e.NodeE2E, firewall, fmt.Sprintf(`{"exclude":[{"network":"udp","ports":[%d]}]}`, udpPort))
	sendTunFirewallUDPE2E(t, e.NodeE2E, udpClient)
	waitTunFirewallUDPFailureE2E(t, e.NodeE2E, udpClient)
	e.assertClientRequestWorks(httpClient)
	applyTunFirewallMenuE2E(t, e.NodeE2E, firewall, `{}`)
	assertTunFirewallUDPWorksE2E(t, e.NodeE2E, udpClient)

	// Both TUN sides can detach and reattach without changing the object
	// policy links or the VTun Network consumers.
	e.Engine.RemoveLink(E2EWorkspace, inputLink)
	e.WaitNodeLabel(firewall, "waiting for TUN")
	inputLink = e.MustAddTypedLinkByPortName(TypeTun, firewall, "left", "Tun", clientVTun, "right", "Tun")
	e.WaitNodeLabel(firewall, "filtering 0 rules")
	e.assertClientRequestWorks(httpClient)

	e.Engine.RemoveLink(E2EWorkspace, outputLink)
	e.WaitNodeLabel(copyNode, "waiting for TunA")
	outputLink = e.MustAddTypedLinkByPortName(TypeTun, copyNode, "left", "TunA", firewall, "right", "Tun")
	e.WaitNodeLabel(copyNode, "copying")
	e.assertClientRequestWorks(httpClient)
	assertTunFirewallUDPWorksE2E(t, e.NodeE2E, udpClient)
	_ = inputLink
	_ = outputLink
	_ = udpConfigLink
}

func applyTunFirewallMenuE2E(t *testing.T, e *NodeE2E, node uint64, rules string) {
	t.Helper()
	e.Engine.SendNodeFormularMsg(E2EWorkspace, node, formular.FormApplyMessage{
		MessageBase: FormMessageBase(formular.MessageFormApply, node),
		BlockID:     tunnodes.TunFirewallConfigBlockID,
		Values:      map[string]any{tunnodes.TunFirewallRulesField: rules},
	})
}

func applyTunFirewallUDPServerE2E(t *testing.T, e *NodeE2E, node uint64, port int) {
	t.Helper()
	e.Engine.SendNodeFormularMsg(E2EWorkspace, node, formular.FormApplyMessage{
		MessageBase: FormMessageBase(formular.MessageFormApply, node),
		BlockID:     "config",
		Values: map[string]any{
			"network": "udp4", "socket_mode": debugtest.UDPSocketListen,
			"local_host": tunCopyE2EServerAddr, "local_port": port,
			"remote_host": tunCopyE2EClientAddr, "remote_port": 0,
		},
	})
	e.WaitNodeLabel(node, fmt.Sprintf("udp4/listen %s", net.JoinHostPort(tunCopyE2EServerAddr, fmt.Sprint(port))))
}

func applyTunFirewallUDPClientE2E(t *testing.T, e *NodeE2E, node uint64, port int) {
	t.Helper()
	e.Engine.SendNodeFormularMsg(E2EWorkspace, node, formular.FormApplyMessage{
		MessageBase: FormMessageBase(formular.MessageFormApply, node),
		BlockID:     "config",
		Values: map[string]any{
			"network": "udp4", "socket_mode": debugtest.UDPSocketDial,
			"local_host": tunCopyE2EClientAddr, "local_port": 0,
			"remote_host": tunCopyE2EServerAddr, "remote_port": port,
			"payload": "tun-firewall-ping", "timeout_ms": 1000,
		},
	})
	e.WaitNodeLabel(node, debugtest.UDPPingStatusReady)
}

func assertTunFirewallUDPWorksE2E(t *testing.T, e *NodeE2E, client uint64) {
	t.Helper()
	sendTunFirewallUDPE2E(t, e, client)
	testkit.Eventually(t, func() bool {
		return tunFirewallUDPStatusE2E(e, client) == "Status: ok" &&
			tunFirewallUDPResultE2E(e, client, "error") == "" &&
			tunFirewallUDPResultE2E(e, client, "response") == "pong:tun-firewall-ping"
	}, "TUN firewall UDP response")
}

func sendTunFirewallUDPE2E(t *testing.T, e *NodeE2E, client uint64) {
	t.Helper()
	e.Engine.SendNodeFormularMsg(E2EWorkspace, client, formular.ButtonPressMessage{
		MessageBase: FormMessageBase(formular.MessageButtonPress, client),
		BlockID:     "config",
		ButtonID:    "send",
	})
}

func waitTunFirewallUDPFailureE2E(t *testing.T, e *NodeE2E, client uint64) {
	t.Helper()
	testkit.Eventually(t, func() bool {
		return tunFirewallUDPStatusE2E(e, client) == "Status: fail" && tunFirewallUDPResultE2E(e, client, "error") != ""
	}, "TUN firewall UDP timeout")
}

func waitTunFirewallUDPServerE2E(t *testing.T, e *NodeE2E, server uint64, want string) {
	t.Helper()
	testkit.Eventually(t, func() bool {
		item, ok := e.MenuItem(server, "logs", "worker")
		if !ok {
			return false
		}
		for _, line := range item.Logs {
			if strings.Contains(line.Text, want) {
				return true
			}
		}
		return false
	}, "TUN firewall UDP server log containing %q", want)
}

func tunFirewallUDPStatusE2E(e *NodeE2E, client uint64) string {
	block, ok := e.MenuBlock(client, "result")
	if !ok {
		return ""
	}
	item, ok := testkit.FindItem(block, "status")
	if !ok {
		return ""
	}
	return item.Text
}

func tunFirewallUDPResultE2E(e *NodeE2E, client uint64, field string) string {
	item, ok := e.MenuItem(client, "result", field)
	if !ok || item.Field == nil {
		return ""
	}
	value, _ := item.Value.(string)
	return value
}
