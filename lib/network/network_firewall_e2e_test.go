package networknodes_test

import (
	"fmt"
	"net"
	"strings"
	"testing"

	debugnodes "github.com/asciimoth/almagest/lib/debug"
	networknodes "github.com/asciimoth/almagest/lib/network"
	"github.com/asciimoth/almagest/lib/testkit"
	"github.com/asciimoth/almagest/lib/testkit/debugtest"
	"github.com/asciimoth/almagest/lib/testkit/e2etest"
	"github.com/asciimoth/formular"
	"github.com/asciimoth/pasta/pasta/std"
)

func TestNetworkFirewallE2EHTTPAndUDPOverMockNetwork(t *testing.T) {
	e := newHTTPE2E(t)
	mock := e.AddNode(networknodes.NodeTypeMockNetwork, "firewall-mock")
	firewall := e.AddNode(networknodes.NodeTypeNetworkFirewall, "network-firewall")
	tcpConfig := e.AddNode(std.NodeTypeObjectConstant, "tcp-firewall-config")
	udpConfig := e.AddNode(std.NodeTypeObjectConstant, "udp-firewall-config")
	httpPort := httpTestPort()
	udpPort := httpTestPort()
	httpServer := e.addHTTPServer("firewall-http-server", httpPort)
	httpClient := e.addHTTPClient("firewall-http-client", httpPort)
	udpServer := e.AddNode(debugnodes.NodeTypeUDPPingServer, "firewall-udp-server")
	udpClient := e.AddNode(debugnodes.NodeTypeUDPPingClient, "firewall-udp-client")
	e.SubscribeMenus(firewall, tcpConfig, udpConfig, httpServer, httpClient, udpServer, udpClient)
	e.ApplyStdConstantValue(tcpConfig, `{}`)
	e.ApplyStdConstantValue(udpConfig, `{}`)
	applyNetworkFirewallUDPServer(t, e.NodeE2E, udpServer, udpPort)
	applyNetworkFirewallUDPClient(t, e.NodeE2E, udpClient, udpPort)

	inputLink := e.connectByPortName(mock, "left", "Network", firewall, "right", "Network")
	e.connectByPortName(firewall, "left", "Network", httpClient, "right", "Network")
	e.connectByPortName(firewall, "left", "Network", udpClient, "right", "Network")
	e.connectByPortName(mock, "left", "Network", httpServer, "right", "Network")
	e.connectByPortName(mock, "left", "Network", udpServer, "right", "Network")
	tcpConfigLink := e.MustAddTypedLinkByPortName(std.TypeObject, tcpConfig, "right", "output", firewall, "left", "Config")
	e.MustAddTypedLinkByPortName(std.TypeObject, udpConfig, "right", "output", firewall, "left", "Config")

	e.waitServerListening(httpServer, fmt.Sprintf("127.0.0.1:%d", httpPort))
	waitNetworkFirewallUDPServer(t, e.NodeE2E, udpServer, "listening on")
	e.AssertClientRequestWorks(httpClient)
	assertNetworkFirewallUDPWorks(t, e.NodeE2E, udpClient)

	// A new value from an already attached object node updates the active
	// gonnect.Firewall without replacing the clients' network wrappers.
	e.ApplyStdConstantValue(tcpConfig, fmt.Sprintf(`{"exclude":[{"network":"tcp","ports":[%d]}]}`, httpPort))
	e.sendClientRequest(httpClient)
	e.waitClientFailure(httpClient, "firewall denied")
	assertNetworkFirewallUDPWorks(t, e.NodeE2E, udpClient)

	// Detach removes only that link's contribution. Reattach requests and
	// restores the current object value.
	e.Engine.RemoveLink(e2etest.E2EWorkspace, tcpConfigLink)
	e.AssertClientRequestWorks(httpClient)
	tcpConfigLink = e.MustAddTypedLinkByPortName(std.TypeObject, tcpConfig, "right", "output", firewall, "left", "Config")
	e.sendClientRequest(httpClient)
	e.waitClientFailure(httpClient, "firewall denied")
	e.ApplyStdConstantValue(tcpConfig, `{}`)
	e.AssertClientRequestWorks(httpClient)

	// Menu policy changes use the same live update path as object values.
	applyNetworkFirewallMenuE2E(t, e.NodeE2E, firewall, fmt.Sprintf(`{"exclude":[{"network":"udp","ports":[%d]}]}`, udpPort))
	sendNetworkFirewallUDP(t, e.NodeE2E, udpClient)
	waitNetworkFirewallUDPFailure(t, e.NodeE2E, udpClient, "firewall denied")
	applyNetworkFirewallMenuE2E(t, e.NodeE2E, firewall, `{}`)
	assertNetworkFirewallUDPWorks(t, e.NodeE2E, udpClient)

	// The network input can detach and reattach while both output consumers
	// remain linked.
	e.Engine.RemoveLink(e2etest.E2EWorkspace, inputLink)
	e.sendClientRequest(httpClient)
	e.waitClientAnyFailure(httpClient)
	inputLink = e.connectByPortName(mock, "left", "Network", firewall, "right", "Network")
	e.AssertClientRequestWorks(httpClient)
	assertNetworkFirewallUDPWorks(t, e.NodeE2E, udpClient)
	_ = inputLink
	_ = tcpConfigLink
}

func applyNetworkFirewallMenuE2E(t *testing.T, e *e2etest.NodeE2E, node uint64, rules string) {
	t.Helper()
	e.Engine.SendNodeFormularMsg(e2etest.E2EWorkspace, node, formular.FormApplyMessage{
		MessageBase: testkit.FormMessageBase(formular.MessageFormApply, node),
		BlockID:     networknodes.NetworkFirewallConfigBlockID,
		Values:      map[string]any{networknodes.NetworkFirewallRulesField: rules},
	})
}

func applyNetworkFirewallUDPServer(t *testing.T, e *e2etest.NodeE2E, node uint64, port int) {
	t.Helper()
	e.Engine.SendNodeFormularMsg(e2etest.E2EWorkspace, node, formular.FormApplyMessage{
		MessageBase: testkit.FormMessageBase(formular.MessageFormApply, node),
		BlockID:     "config",
		Values: map[string]any{
			"network": "udp4", "socket_mode": debugtest.UDPSocketListen,
			"local_host": "127.0.0.1", "local_port": port,
			"remote_host": "127.0.0.1", "remote_port": 0,
		},
	})
	e.WaitNodeLabel(node, fmt.Sprintf("udp4/listen %s", net.JoinHostPort("127.0.0.1", fmt.Sprint(port))))
}

func applyNetworkFirewallUDPClient(t *testing.T, e *e2etest.NodeE2E, node uint64, port int) {
	t.Helper()
	e.Engine.SendNodeFormularMsg(e2etest.E2EWorkspace, node, formular.FormApplyMessage{
		MessageBase: testkit.FormMessageBase(formular.MessageFormApply, node),
		BlockID:     "config",
		Values: map[string]any{
			"network": "udp4", "socket_mode": debugtest.UDPSocketDial,
			"local_host": "127.0.0.1", "local_port": 0,
			"remote_host": "127.0.0.1", "remote_port": port,
			"payload": "network-firewall-ping", "timeout_ms": 1000,
		},
	})
	e.WaitNodeLabel(node, debugtest.UDPPingStatusReady)
}

func assertNetworkFirewallUDPWorks(t *testing.T, e *e2etest.NodeE2E, client uint64) {
	t.Helper()
	sendNetworkFirewallUDP(t, e, client)
	testkit.Eventually(t, func() bool {
		return networkFirewallUDPStatus(e, client) == "Status: ok" &&
			networkFirewallUDPResult(e, client, "error") == "" &&
			networkFirewallUDPResult(e, client, "response") == "pong:network-firewall-ping"
	}, "network firewall UDP response")
}

func sendNetworkFirewallUDP(t *testing.T, e *e2etest.NodeE2E, client uint64) {
	t.Helper()
	e.Engine.SendNodeFormularMsg(e2etest.E2EWorkspace, client, formular.ButtonPressMessage{
		MessageBase: testkit.FormMessageBase(formular.MessageButtonPress, client),
		BlockID:     "config",
		ButtonID:    "send",
	})
}

func waitNetworkFirewallUDPFailure(t *testing.T, e *e2etest.NodeE2E, client uint64, want string) {
	t.Helper()
	testkit.Eventually(t, func() bool {
		return networkFirewallUDPStatus(e, client) == "Status: fail" && strings.Contains(networkFirewallUDPResult(e, client, "error"), want)
	}, "network firewall UDP failure containing %q", want)
}

func waitNetworkFirewallUDPServer(t *testing.T, e *e2etest.NodeE2E, server uint64, want string) {
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
	}, "network firewall UDP server log containing %q", want)
}

func networkFirewallUDPStatus(e *e2etest.NodeE2E, client uint64) string {
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

func networkFirewallUDPResult(e *e2etest.NodeE2E, client uint64, field string) string {
	item, ok := e.MenuItem(client, "result", field)
	if !ok || item.Field == nil {
		return ""
	}
	value, _ := item.Value.(string)
	return value
}
