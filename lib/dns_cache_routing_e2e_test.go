package almagest

import (
	"testing"

	dnsnodes "github.com/asciimoth/almagest/lib/dns"
)

type dnsCacheWarmQuery struct {
	name  string
	wantA string
}

func warmDNSCacheReverseLookupsForRouting(
	t *testing.T,
	e *NodeE2E,
	prefix string,
	storageName string,
	records string,
	queries ...dnsCacheWarmQuery,
) {
	t.Helper()
	dnsE := &dnsE2E{NodeE2E: e}
	static := e.AddNode(dnsnodes.NodeTypeStaticDNS, prefix+"-static-dns")
	cache := e.AddNode(dnsnodes.NodeTypeDnsCache, prefix+"-dns-cache")
	query := e.AddNode(dnsnodes.NodeTypeDNSQuery, prefix+"-dns-query")
	e.SubscribeMenus(static, cache, query)

	dnsE.applyStaticDNSRecords(static, records)
	dnsE.applyDNSCacheConfig(cache, storageName, true, false)
	e.MustAddTypedLinkByPortName(dnsnodes.TypeDNS, static, "left", "DNS", cache, "right", "DNS")
	e.MustAddTypedLinkByPortName(dnsnodes.TypeDNS, cache, "left", "DNS", query, "right", "DNS")
	e.WaitNodeLabel(cache, "caching DNS")

	for _, q := range queries {
		dnsE.setDNSQueryRequest(query, q.name, "A")
		dnsE.sendDNSRequest(query)
		dnsE.waitDNSResponse(query, "NOERROR", q.wantA)
	}
}
