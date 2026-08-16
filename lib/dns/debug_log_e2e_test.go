package dnsnodes_test

import (
	"testing"

	dnsnodes "github.com/asciimoth/almagest/lib/dns"
	"github.com/asciimoth/almagest/lib/testkit/e2etest"
)

func TestDebugLogDNSE2EPassthroughDisconnectReconnect(t *testing.T) {
	e := newDNSE2E(t)
	static := e.AddNode(dnsnodes.NodeTypeStaticDNS, "static")
	logDNS := e.AddNode(dnsnodes.NodeTypeDebugLogDNS, "log-dns")
	client := e.AddNode(dnsnodes.NodeTypeDNSQuery, "client")
	e.SubscribeMenus(static, client)
	e.applyStaticDNSRecords(static, "logged.e2e.test A 10.60.0.1")
	e.setDNSQueryRequest(client, "logged.e2e.test.", "A")

	upstream := e.MustAddTypedLinkByPortName(dnsnodes.TypeDNS, static, "left", "DNS", logDNS, "right", "DNS")
	clientLink := e.MustAddTypedLinkByPortName(dnsnodes.TypeDNS, logDNS, "left", "DNS", client, "right", "DNS")
	e.sendDNSRequest(client)
	e.waitDNSResponse(client, "NOERROR", "10.60.0.1")
	e.AssertDebugLog(dnsnodes.NodeTypeDebugLogDNS, "dns.Request")
	e.AssertDebugLog(dnsnodes.NodeTypeDebugLogDNS, "logged.e2e.test. A")
	e.AssertDebugLog(dnsnodes.NodeTypeDebugLogDNS, "dns.Response")

	e.Engine.RemoveLink(e2etest.E2EWorkspace, clientLink)
	e.waitDNSReady(client)
	e.sendDNSRequest(client)
	e.waitDNSFailure(client, "dns is not ready")
	clientLink = e.MustAddTypedLinkByPortName(dnsnodes.TypeDNS, logDNS, "left", "DNS", client, "right", "DNS")
	e.waitDNSReady(client)
	e.sendDNSRequest(client)
	e.waitDNSResponse(client, "NOERROR", "10.60.0.1")

	e.Engine.RemoveLink(e2etest.E2EWorkspace, upstream)
	upstream = e.MustAddTypedLinkByPortName(dnsnodes.TypeDNS, static, "left", "DNS", logDNS, "right", "DNS")
	e.waitDNSReady(client)
	e.sendDNSRequest(client)
	e.waitDNSResponse(client, "NOERROR", "10.60.0.1")

	_ = upstream
	_ = clientLink
}
