package dnsnodes_test

import (
	dnsnodes "github.com/asciimoth/almagest/lib/dns"
	"testing"

	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/formular"
)

func TestDnsRouterRoutesBetweenStaticDNSBackendsE2E(t *testing.T) {
	e := newDNSE2E(t)
	east := e.AddNode(dnsnodes.NodeTypeStaticDNS, "east")
	west := e.AddNode(dnsnodes.NodeTypeStaticDNS, "west")
	router := e.AddNode(dnsnodes.NodeTypeDnsRouter, "router")
	client := e.AddNode(dnsnodes.NodeTypeDNSQuery, "client")
	e.SubscribeMenus(east, west, router, client)

	e.applyStaticDNSRecords(east, "svc.east.router.test A 10.101.0.1")
	e.applyStaticDNSRecords(west, "svc.west.router.test A 10.101.0.2")
	e.applyDNSRouterRules(router, `ADDR_RE (^|.*\.)east\.router\.test\.$
BACKEND slot 1

ADDR_RE (^|.*\.)west\.router\.test\.$
BACKEND slot 2`)
	e.assertDNSRouterPorts(router, []string{"slot 1", "slot 2"})

	e.MustAddTypedLinkByPortName(dnsnodes.TypeDNS, east, "left", "DNS", router, "right", "slot 1")
	westLink := e.MustAddTypedLinkByPortName(dnsnodes.TypeDNS, west, "left", "DNS", router, "right", "slot 2")
	e.MustAddTypedLinkByPortName(dnsnodes.TypeDNS, router, "left", "DNS", client, "right", "DNS")
	e.WaitNodeLabel(router, "routing 2 DNS backends")

	e.setDNSQueryRequest(client, "svc.east.router.test.", "A")
	e.sendDNSRequest(client)
	e.waitDNSResponse(client, "NOERROR", "10.101.0.1")

	e.setDNSQueryRequest(client, "svc.west.router.test.", "A")
	e.sendDNSRequest(client)
	e.waitDNSResponse(client, "NOERROR", "10.101.0.2")

	e.Engine.RemoveLink(E2EWorkspace, westLink)
	e.WaitNodeLabel(router, "routing DNS")
	e.sendDNSRequest(client)
	e.waitDNSFailure(client, "dns: no upstream attached")

	e.MustAddTypedLinkByPortName(dnsnodes.TypeDNS, west, "left", "DNS", router, "right", "slot 2")
	e.WaitNodeLabel(router, "routing 2 DNS backends")
	e.sendDNSRequest(client)
	e.waitDNSResponse(client, "NOERROR", "10.101.0.2")
}

func TestDnsRouterWorksWithDnsCacheE2E(t *testing.T) {
	e := newDNSE2EWithDNSCacheManager(t, configer.NewMemory(nil), newMemoryDNSCacheManager())
	static := e.AddNode(dnsnodes.NodeTypeStaticDNS, "static")
	cache := e.AddNode(dnsnodes.NodeTypeDnsCache, "cache")
	router := e.AddNode(dnsnodes.NodeTypeDnsRouter, "router")
	client := e.AddNode(dnsnodes.NodeTypeDNSQuery, "client")
	e.SubscribeMenus(static, cache, router, client)

	e.applyStaticDNSRecords(static, "cached.router.test A 10.102.0.1")
	e.applyDNSCacheConfig(cache, "router-cache", false, false)
	e.setDNSQueryRequest(client, "cached.router.test.", "A")

	cacheInput := e.MustAddTypedLinkByPortName(dnsnodes.TypeDNS, static, "left", "DNS", cache, "right", "DNS")
	e.MustAddTypedLinkByPortName(dnsnodes.TypeDNS, cache, "left", "DNS", router, "right", "slot 1")
	e.MustAddTypedLinkByPortName(dnsnodes.TypeDNS, router, "left", "DNS", client, "right", "DNS")
	e.WaitNodeLabel(cache, "caching DNS")
	e.WaitNodeLabel(router, "routing DNS")

	e.sendDNSRequest(client)
	e.waitDNSResponse(client, "NOERROR", "10.102.0.1")

	e.Engine.RemoveLink(E2EWorkspace, cacheInput)
	e.WaitNodeLabel(cache, "cache-only DNS")
	e.sendDNSRequest(client)
	e.waitDNSResponse(client, "NOERROR", "10.102.0.1")
}

func (e *dnsE2E) applyDNSRouterRules(node uint64, rules string) {
	e.T.Helper()
	e.Engine.SendNodeFormularMsg(E2EWorkspace, node, formular.FormApplyMessage{
		MessageBase: FormMessageBase(formular.MessageFormApply, node),
		BlockID:     dnsnodes.DNSRouterConfigBlockID,
		Values: map[string]any{
			dnsnodes.DNSRouterRulesField: rules,
		},
	})
	e.waitDNSRouterRules(node, rules)
}

func (e *dnsE2E) waitDNSRouterRules(node uint64, want string) {
	e.T.Helper()
	Eventually(e.T, func() bool {
		item, ok := e.MenuItem(node, dnsnodes.DNSRouterConfigBlockID, dnsnodes.DNSRouterRulesField)
		return ok && item.Field != nil && item.Value == want
	}, "DnsRouter rules = %q", want)
}

func (e *dnsE2E) assertDNSRouterPorts(router uint64, want []string) {
	e.T.Helper()
	Eventually(e.T, func() bool {
		snapshot, ok := e.Engine.NodeSnapshot(E2EWorkspace, router)
		if !ok {
			return false
		}
		got := make([]string, 0, len(snapshot.RightPorts))
		for _, port := range snapshot.RightPorts {
			ps, ok := e.Engine.PortSnapshot(E2EWorkspace, port)
			if !ok {
				return false
			}
			got = append(got, ps.Name)
		}
		if len(got) != len(want) {
			return false
		}
		for i := range got {
			if got[i] != want[i] {
				return false
			}
		}
		return true
	}, "DnsRouter ports %v", want)
}
