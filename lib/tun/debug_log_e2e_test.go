package tunnodes_test

import (
	"testing"

	"github.com/asciimoth/almagest/lib/testkit/e2etest"
	tunnodes "github.com/asciimoth/almagest/lib/tun"
	"github.com/asciimoth/pasta/pasta/std"
)

func TestDebugLogTunE2EPassthroughDisconnectReconnect(t *testing.T) {
	e := newHTTPE2E(t)
	port := httpTestPort()
	vtun := e.AddNode(NodeTypeVTun, "vtun")
	logTun := e.AddNode(tunnodes.NodeTypeDebugLogTun, "log-tun")
	spoofer := e.AddNode(NodeTypeSpoofer, "spoofer")
	mock := e.AddNode(NodeTypeMockNetwork, "mock")
	response := e.addStringConstant("server-response", vtunSpooferResponse)
	server := e.addHTTPServerOnHost("server", vtunSpooferServerHost, port)
	client := e.addHTTPClientToURL("client", "http://"+vtunSpooferServerHost+":"+itoa(port)+"/")
	e.SubscribeMenus(vtun, spoofer, response, server, client)
	e.waitClientRequestField(client, "url", "http://"+vtunSpooferServerHost+":"+itoa(port)+"/")

	e.MustAddTypedLinkByPortName(std.TypeString, response, "right", "output", server, "left", "Response")
	logVTun := e.connectTunByPortName(logTun, "left", "in", vtun, "right", "Tun")
	spooferLog := e.connectTunByPortName(spoofer, "left", "Tun", logTun, "right", "Tun")
	e.connectByPortName(mock, "left", "Network", spoofer, "right", "Network")
	e.connectByPortName(mock, "left", "Network", server, "right", "Network")
	e.connectByPortName(vtun, "left", "Network", client, "right", "Network")

	waitDebugLogTunReady(e, vtun, spoofer, server, port)
	e.assertClientRequestWorksWithResponse(client, vtunSpooferResponse)
	e.AssertDebugLog(tunnodes.NodeTypeDebugLogTun, "tun.Read")
	e.AssertDebugLog(tunnodes.NodeTypeDebugLogTun, "tun.Write")

	e.Engine.RemoveLink(e2etest.E2EWorkspace, spooferLog)
	e.WaitNodeLabel(spoofer, "waiting for tun")
	spooferLog = e.connectTunByPortName(spoofer, "left", "Tun", logTun, "right", "Tun")
	waitDebugLogTunReady(e, vtun, spoofer, server, port)
	e.assertClientRequestWorksWithResponse(client, vtunSpooferResponse)

	e.Engine.RemoveLink(e2etest.E2EWorkspace, logVTun)
	logVTun = e.connectTunByPortName(logTun, "left", "in", vtun, "right", "Tun")
	waitDebugLogTunReady(e, vtun, spoofer, server, port)
	e.assertClientRequestWorksWithResponse(client, vtunSpooferResponse)

	_ = spooferLog
	_ = logVTun
}

func waitDebugLogTunReady(e *httpE2E, vtun, spoofer, server uint64, port int) {
	e.T.Helper()
	e.WaitNodeLabel(vtun, "vtun")
	e.WaitNodeLabel(spoofer, "forwarding")
	e.waitServerListening(server, vtunSpooferServerHost+":"+itoa(port))
}
