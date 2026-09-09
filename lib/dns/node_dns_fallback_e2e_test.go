package dnsnodes_test

import (
	"testing"

	dnsnodes "github.com/asciimoth/almagest/lib/dns"
	"github.com/asciimoth/almagest/lib/testkit"
	"github.com/asciimoth/configer/configer"
)

func TestDnsFallbackNestedGraphFailoverAndLiveRewireE2E(t *testing.T) {
	e := newDNSE2E(t)
	outerMiss := e.AddNode(dnsnodes.NodeTypeStaticDNS, "outer-miss")
	innerMiss := e.AddNode(dnsnodes.NodeTypeStaticDNS, "inner-miss")
	innerAnswer := e.AddNode(dnsnodes.NodeTypeStaticDNS, "inner-answer")
	lastAnswer := e.AddNode(dnsnodes.NodeTypeStaticDNS, "last-answer")
	inner := e.AddNode(dnsnodes.NodeTypeDnsFallback, "inner-fallback")
	outer := e.AddNode(dnsnodes.NodeTypeDnsFallback, "outer-fallback")
	query := e.AddNode(dnsnodes.NodeTypeDNSQuery, "query")
	e.SubscribeMenus(outerMiss, innerMiss, innerAnswer, lastAnswer, query)

	e.applyStaticDNSRecords(innerAnswer, "nested.fallback.test A 10.140.0.2")
	e.applyStaticDNSRecords(lastAnswer, "nested.fallback.test A 10.140.0.3")
	e.setDNSQueryRequest(query, "nested.fallback.test.", "A")

	e.MustAddTypedLinkByPortName(dnsnodes.TypeDNS, outerMiss, "left", "DNS", outer, "right", "slot 1")
	e.MustAddTypedLinkByPortName(dnsnodes.TypeDNS, innerMiss, "left", "DNS", inner, "right", "slot 1")
	innerAnswerLink := e.MustAddTypedLinkByPortName(dnsnodes.TypeDNS, innerAnswer, "left", "DNS", inner, "right", "slot 2")
	e.MustAddTypedLinkByPortName(dnsnodes.TypeDNS, inner, "left", "DNS", outer, "right", "slot 2")
	lastAnswerLink := e.MustAddTypedLinkByPortName(dnsnodes.TypeDNS, lastAnswer, "left", "DNS", outer, "right", "slot 3")
	e.MustAddTypedLinkByPortName(dnsnodes.TypeDNS, outer, "left", "DNS", query, "right", "DNS")
	e.WaitNodeLabel(inner, "fallback across 2 DNS backends")
	e.WaitNodeLabel(outer, "fallback across 3 DNS backends")

	e.sendDNSRequest(query)
	e.waitDNSResponse(query, "NOERROR", "10.140.0.2")

	e.Engine.RemoveLink(E2EWorkspace, innerAnswerLink)
	e.WaitNodeLabel(inner, "fallback DNS")
	e.sendDNSRequest(query)
	e.waitDNSResponse(query, "NOERROR", "10.140.0.3")

	e.Engine.RemoveLink(E2EWorkspace, lastAnswerLink)
	e.WaitNodeLabel(outer, "fallback across 2 DNS backends")
	e.sendDNSRequest(query)
	e.waitDNSResponse(query, "NXDOMAIN")

	e.MustAddTypedLinkByPortName(dnsnodes.TypeDNS, innerAnswer, "left", "DNS", inner, "right", "slot 2")
	e.sendDNSRequest(query)
	e.waitDNSResponse(query, "NOERROR", "10.140.0.2")
}

func TestDnsFallbackSaveRestorePreservesSparseSlotsE2E(t *testing.T) {
	e := newDNSE2E(t)
	unused := e.AddNode(dnsnodes.NodeTypeStaticDNS, "unused")
	answer := e.AddNode(dnsnodes.NodeTypeStaticDNS, "answer")
	fallback := e.AddNode(dnsnodes.NodeTypeDnsFallback, "fallback")
	query := e.AddNode(dnsnodes.NodeTypeDNSQuery, "query")
	e.SubscribeMenus(unused, answer, query)

	e.applyStaticDNSRecords(answer, "restore.fallback.test A 10.141.0.2")
	e.setDNSQueryRequest(query, "restore.fallback.test.", "A")
	unusedLink := e.MustAddTypedLinkByPortName(dnsnodes.TypeDNS, unused, "left", "DNS", fallback, "right", "slot 1")
	e.MustAddTypedLinkByPortName(dnsnodes.TypeDNS, answer, "left", "DNS", fallback, "right", "slot 2")
	e.MustAddTypedLinkByPortName(dnsnodes.TypeDNS, fallback, "left", "DNS", query, "right", "DNS")
	e.Engine.RemoveLink(E2EWorkspace, unusedLink)
	e.WaitNodeLabel(fallback, "fallback DNS")
	assertDNSFallbackE2EPorts(t, e, fallback, []string{"slot 1", "slot 2"})

	e.sendDNSRequest(query)
	e.waitDNSResponse(query, "NOERROR", "10.141.0.2")

	cfg := configer.NewMemory(nil)
	if err := e.Engine.SaveConfig(cfg); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}

	restored := newDNSE2EFromConfig(t, cfg)
	restoredFallback := testkit.NodeIDByName(t, restored.Engine, E2EWorkspace, "fallback")
	restoredQuery := testkit.NodeIDByName(t, restored.Engine, E2EWorkspace, "query")
	restored.SubscribeMenus(restoredQuery)
	restored.WaitNodeLabel(restoredFallback, "fallback DNS")
	assertDNSFallbackE2EPorts(t, restored, restoredFallback, []string{"slot 1", "slot 2"})
	restored.sendDNSRequest(restoredQuery)
	restored.waitDNSResponse(restoredQuery, "NOERROR", "10.141.0.2")
}

func assertDNSFallbackE2EPorts(t *testing.T, e *dnsE2E, node uint64, want []string) {
	t.Helper()
	Eventually(t, func() bool {
		snapshot, ok := e.Engine.NodeSnapshot(E2EWorkspace, node)
		if !ok || len(snapshot.RightPorts) != len(want) {
			return false
		}
		for i, port := range snapshot.RightPorts {
			portSnapshot, ok := e.Engine.PortSnapshot(E2EWorkspace, port)
			if !ok || portSnapshot.Direction != "right" || portSnapshot.Name != want[i] || len(portSnapshot.Types) != 1 || portSnapshot.Types[0] != dnsnodes.TypeDNS {
				return false
			}
		}
		return true
	}, "DnsFallback ports %v", want)
}
