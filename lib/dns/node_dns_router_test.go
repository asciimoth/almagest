package dnsnodes

import (
	"testing"

	"github.com/asciimoth/almagest/lib/logging"
	"github.com/asciimoth/almagest/lib/nodekit"
	"github.com/asciimoth/almagest/lib/testkit"
	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/formular"
	"github.com/asciimoth/gonnect/dns"
	"github.com/asciimoth/gonnect/routing"
	"github.com/asciimoth/pasta/pasta"
)

func TestDNSRouterBytecodeRulesRouteByRegexpNameTypeAndDefault(t *testing.T) {
	_, route := compileDNSRouterRulesForTest(t, `ADDR_RE (^|.*\.)east\.test\.$
QTYPE A
AND
BACKEND slot 2

ADDR_S host.west.test.
QTYPE AAAA
AND
BACKEND slot 3

ADDR_RE (^|.*\.)west\.test\.$
BACKEND slot 4

TRUE
BACKEND slot 1`)

	for _, tc := range []struct {
		name  string
		qname string
		qtype uint16
		want  string
	}{
		{name: "suffix and type", qname: "svc.east.test.", qtype: dns.TypeA, want: "slot 2"},
		{name: "type mismatch falls through", qname: "svc.east.test.", qtype: dns.TypeAAAA, want: "slot 1"},
		{name: "exact name", qname: "host.west.test.", qtype: dns.TypeAAAA, want: "slot 3"},
		{name: "suffix fallback", qname: "api.west.test.", qtype: dns.TypeA, want: "slot 4"},
		{name: "default", qname: "unknown.test.", qtype: dns.TypeA, want: "slot 1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := route(dnsTestQuery(tc.qname, tc.qtype)); got != tc.want {
				t.Fatalf("Route(%s %s) = %q, want %q", tc.qname, TypeName(tc.qtype), got, tc.want)
			}
		})
	}
}

func TestDNSRouterRulesEmptyRoutesDefaultSlot(t *testing.T) {
	rules, route := compileDNSRouterRulesForTest(t, "# comments only\n\n")
	if got := route(dnsTestQuery("example.test.", dns.TypeA)); got != "slot 1" {
		t.Fatalf("empty rules Route() = %q, want slot 1", got)
	}
	if got := RouterRuleSlots(rules); len(got) != 1 || got[0] != 1 {
		t.Fatalf("empty rules slots = %v, want [1]", got)
	}
}

func TestDNSRouterRulesRejectInvalidInput(t *testing.T) {
	for _, text := range []string{
		"ADDR_RE [\nBACKEND slot 1",
		"TRUE\nBACKEND slot 17",
		"QTYPE BADTYPE\nBACKEND slot 1",
		"NOPE example.test\nBACKEND slot 1",
		"TRUE\nBACKEND default",
	} {
		t.Run(text, func(t *testing.T) {
			if _, _, err := CompileRouterRules(text); err == nil {
				t.Fatal("compileRules succeeded, want error")
			}
		})
	}
}

func TestDNSRouterNodeEmitsStableRouterAndReviewsSlotPorts(t *testing.T) {
	w := newDNSRouterTestWorkspace(t)
	routerID, err := w.AddNodeByClass(NodeTypeDnsRouter, "router")
	if err != nil {
		t.Fatalf("AddNodeByClass DnsRouter: %v", err)
	}
	assertDNSRouterRightPorts(t, w, routerID, []string{"slot 1"})

	receiver := &dnsReceiverNode{events: make(chan DNSPayload, 3)}
	if _, err := w.AddNode(receiver, "test/DNSReceiver", "receiver"); err != nil {
		t.Fatalf("AddNode receiver: %v", err)
	}
	routerSnapshot := testkit.MustNodeSnapshot(t, w, routerID)
	if _, _, err := w.AddLink(routerSnapshot.LeftPorts[0], receiver.in); err != nil {
		t.Fatalf("AddLink router->receiver: %v", err)
	}

	first := receiveDNSRouterRuntime(t, receiver.events)
	rulesText := "ADDR_RE (^|.*\\.)west\\.test\\.$\nBACKEND slot 2\n\nTRUE\nBACKEND slot 1"
	applyDNSRouterRulesToWorkspace(t, w, routerID, rulesText)
	assertDNSRouterRightPorts(t, w, routerID, []string{"slot 1", "slot 2"})
	second := receiveDNSRouterRuntime(t, receiver.events)
	if first != second {
		t.Fatalf("DNS router runtime changed from %p to %p", first, second)
	}

	cfg := configer.NewMemory(nil)
	if err := w.SaveConfig(cfg); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	if got, err := cfg.Get(configer.Path{"router", DNSRouterRulesField}); err != nil || got != rulesText {
		t.Fatalf("saved DnsRouter rules = %q, %v", got, err)
	}
}

func TestDNSRouterNodeRemovesOnlyUnreferencedUnlinkedSlotPorts(t *testing.T) {
	w := newDNSRouterTestWorkspace(t)
	routerID, err := w.AddNodeByClass(NodeTypeDnsRouter, "router")
	if err != nil {
		t.Fatalf("AddNodeByClass DnsRouter: %v", err)
	}
	staticID, err := w.AddNodeByClass(NodeTypeStaticDNS, "static")
	if err != nil {
		t.Fatalf("AddNodeByClass StaticDns: %v", err)
	}

	applyDNSRouterRulesToWorkspace(t, w, routerID, `ADDR_S two.router.test.
BACKEND slot 2

ADDR_S four.router.test.
BACKEND slot 4

TRUE
BACKEND slot 1`)
	assertDNSRouterRightPorts(t, w, routerID, []string{"slot 1", "slot 2", "slot 4"})

	staticOut := testkit.MustNodeSnapshot(t, w, staticID).LeftPorts[0]
	slot2Port := dnsRouterRightPort(t, w, routerID, 2)
	slot4Port := dnsRouterRightPort(t, w, routerID, 4)
	slot2Link, linkType, err := w.AddLink(staticOut, slot2Port)
	if err != nil {
		t.Fatalf("AddLink static->slot 2: %v", err)
	}
	if linkType != TypeDNS {
		t.Fatalf("slot 2 link type = %q, want %q", linkType, TypeDNS)
	}
	slot4Link, linkType, err := w.AddLink(staticOut, slot4Port)
	if err != nil {
		t.Fatalf("AddLink static->slot 4: %v", err)
	}
	if linkType != TypeDNS {
		t.Fatalf("slot 4 link type = %q, want %q", linkType, TypeDNS)
	}

	// Rule edits keep old slots while links still use them and add newly
	// referenced sparse slots in numeric order.
	applyDNSRouterRulesToWorkspace(t, w, routerID, `ADDR_S three.router.test.
BACKEND slot 3

TRUE
BACKEND slot 1`)
	assertDNSRouterRightPorts(t, w, routerID, []string{"slot 1", "slot 2", "slot 3", "slot 4"})

	// Removing one obsolete link removes only its port. Other linked and
	// rule-owned sparse ports must stay available.
	w.RemoveLink(slot2Link)
	assertDNSRouterRightPorts(t, w, routerID, []string{"slot 1", "slot 3", "slot 4"})
	if _, ok := w.PortSnapshot(slot2Port); ok {
		t.Fatal("unreferenced and unlinked slot 2 port still exists")
	}
	if _, ok := w.PortSnapshot(slot4Port); !ok {
		t.Fatal("linked slot 4 port was removed")
	}

	w.RemoveLink(slot4Link)
	assertDNSRouterRightPorts(t, w, routerID, []string{"slot 1", "slot 3"})
	if _, ok := w.PortSnapshot(slot4Port); ok {
		t.Fatal("unreferenced and unlinked slot 4 port still exists")
	}

	// A referenced port stays visible without a link. Removing its rule then
	// leaves only the router's default slot.
	applyDNSRouterRulesToWorkspace(t, w, routerID, "TRUE\nBACKEND slot 1")
	assertDNSRouterRightPorts(t, w, routerID, []string{"slot 1"})
}

func TestDNSRouterNodeValidationReportsStatus(t *testing.T) {
	w := newDNSRouterTestWorkspace(t)
	state := formular.NewMenuSnapshotState()
	sub := w.SubscribeNotifications(func(notification pasta.WorkspaceNotification) {
		if notification.Kind == pasta.NotificationNodeMenu {
			state.Apply(notification.Formular)
		}
	})
	if sub == 0 {
		t.Fatal("Subscribe returned 0")
	}
	defer w.UnsubscribeNotifications(sub)

	routerID, err := w.AddNodeByClass(NodeTypeDnsRouter, "router")
	if err != nil {
		t.Fatalf("AddNodeByClass DnsRouter: %v", err)
	}
	w.SubscribeNodeMenu(routerID, 1)

	w.SendNodeFormularMsg(routerID, formular.FieldValidateMessage{
		MessageBase: testkit.FormMessageBase(formular.MessageFieldValidate, routerID),
		Field:       formular.FieldRef{BlockID: DNSRouterConfigBlockID, FieldID: DNSRouterRulesField},
		Value:       "NOPE\n",
	})
	assertDNSRouterRulesStatus(t, state, routerID, formular.StatusError)

	w.SendNodeFormularMsg(routerID, formular.FieldValidateMessage{
		MessageBase: testkit.FormMessageBase(formular.MessageFieldValidate, routerID),
		Field:       formular.FieldRef{BlockID: DNSRouterConfigBlockID, FieldID: DNSRouterRulesField},
		Value:       "ADDR_S example.test.\nBACKEND slot 1\n",
	})
	assertDNSRouterRulesStatus(t, state, routerID, formular.StatusOK)
}

func compileDNSRouterRulesForTest(t *testing.T, text string) (routing.DNSBytecodeRules, dns.RouteFunc) {
	t.Helper()
	rules, route, err := CompileRouterRules(text)
	if err != nil {
		t.Fatalf("compileRules: %v", err)
	}
	return rules, route
}

func newDNSRouterTestWorkspace(t *testing.T) *pasta.Workspace {
	t.Helper()
	w := pasta.NewWorkspace(logging.NewDebugLogFactory(1024))
	t.Cleanup(func() { w.Close() })
	for _, class := range []pasta.NodeClass{
		DNSRouterClass{},
		StaticDNSClass{},
	} {
		if err := w.AddNodeClass(class); err != nil {
			t.Fatalf("AddNodeClass %s: %v", class.ClassName(), err)
		}
	}
	return w
}

func applyDNSRouterRulesToWorkspace(t *testing.T, w *pasta.Workspace, node uint64, rules string) {
	t.Helper()
	w.SendNodeFormularMsg(node, formular.FormApplyMessage{
		MessageBase: testkit.FormMessageBase(formular.MessageFormApply, node),
		BlockID:     DNSRouterConfigBlockID,
		Values: map[string]any{
			DNSRouterRulesField: rules,
		},
	})
}

func receiveDNSRouterRuntime(t *testing.T, events <-chan DNSPayload) *dns.Router {
	t.Helper()
	payload := receiveReadyDNSPayload(t, events)
	wrapped, ok := payload.DNS.(interface{ GetWrapped() any })
	if !ok {
		t.Fatalf("payload DNS = %T, want wrapper with GetWrapped", payload.DNS)
	}
	router, ok := wrapped.GetWrapped().(*dns.Router)
	if !ok {
		t.Fatalf("wrapped DNS = %T, want *dns.Router", wrapped.GetWrapped())
	}
	return router
}

func assertDNSRouterRightPorts(t *testing.T, w *pasta.Workspace, node uint64, want []string) {
	t.Helper()
	snapshot := testkit.MustNodeSnapshot(t, w, node)
	got := make([]string, 0, len(snapshot.RightPorts))
	for _, port := range snapshot.RightPorts {
		ps, ok := w.PortSnapshot(port)
		if !ok {
			t.Fatalf("PortSnapshot(%d) missing", port)
		}
		got = append(got, ps.Name)
	}
	testkit.AssertStringSlice(t, got, want)
}

func dnsRouterRightPort(t *testing.T, w *pasta.Workspace, node uint64, slot int) uint64 {
	t.Helper()
	want := nodekit.SlotPortName(slot)
	for _, port := range testkit.MustNodeSnapshot(t, w, node).RightPorts {
		snapshot, ok := w.PortSnapshot(port)
		if ok && snapshot.Name == want {
			return port
		}
	}
	t.Fatalf("DnsRouter port %q is missing", want)
	return 0
}

func assertDNSRouterRulesStatus(t *testing.T, state *formular.MenuSnapshotState, node uint64, want string) {
	t.Helper()
	snapshot, ok := state.Snapshot(pasta.NodeMenuID(node))
	if !ok {
		t.Fatal("menu snapshot missing")
	}
	item, ok := testkit.FindItem(testkit.MustFindBlock(t, snapshot, DNSRouterConfigBlockID), DNSRouterRulesField)
	if !ok {
		t.Fatal("rules item missing")
	}
	if item.Status != want {
		t.Fatalf("rules status = %q, want %q", item.Status, want)
	}
}

func receiveReadyDNSPayload(t *testing.T, ch <-chan DNSPayload) DNSPayload {
	t.Helper()
	for {
		payload := receiveDNSPayload(t, ch)
		if DNSIsReady(payload.DNS) {
			return payload
		}
	}
}
