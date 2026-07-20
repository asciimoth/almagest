package almagest

import (
	"context"
	"strings"
	"testing"

	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/formular"
	"github.com/asciimoth/gonnect/dns"
	"github.com/asciimoth/pasta/pasta"
	"github.com/asciimoth/pasta/pasta/std"
)

func TestStaticDNSProviderRecordTypes(t *testing.T) {
	records, err := parseStaticDNSRecords(strings.Join([]string{
		"example.test A 192.0.2.10",
		"example.test AAA 2001:db8::10",
		"example.test TXT \"hello world\"",
		"example.test MX 10 mail.example.test",
		"_svc._tcp.example.test SRV 1 2 443 srv.example.test",
	}, "\n"), 30)
	if err != nil {
		t.Fatalf("parseStaticDNSRecords: %v", err)
	}
	provider := newStaticDNSProvider(records)
	defer func() { _ = provider.Close() }()

	for _, tc := range []struct {
		name     string
		qname    string
		qtype    uint16
		wantPart string
	}{
		{name: "A", qname: "example.test.", qtype: dns.TypeA, wantPart: "192.0.2.10"},
		{name: "AAAA", qname: "example.test.", qtype: dns.TypeAAAA, wantPart: "2001:db8::10"},
		{name: "TXT", qname: "example.test.", qtype: dns.TypeTXT, wantPart: "hello world"},
		{name: "MX", qname: "example.test.", qtype: dns.TypeMX, wantPart: "10 mail.example.test."},
		{name: "SRV", qname: "_svc._tcp.example.test.", qtype: dns.TypeSRV, wantPart: "1 2 443 srv.example.test."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp, err := dns.Query(context.Background(), provider, dnsTestQuery(tc.qname, tc.qtype))
			if err != nil {
				t.Fatalf("Query: %v", err)
			}
			if resp.RCode != dns.RCodeSuccess || len(resp.Answers) == 0 {
				t.Fatalf("response = %#v, want successful answers", resp)
			}
			if got := formatDNSMessage(resp); !strings.Contains(got, tc.wantPart) {
				t.Fatalf("formatted response = %q, want to contain %q", got, tc.wantPart)
			}
		})
	}
}

func TestStaticDNSMultipleConsumers(t *testing.T) {
	e := newDNSE2E(t)
	static := e.AddNode(NodeTypeStaticDNS, "static")
	clientA := e.AddNode(NodeTypeDNSQuery, "client-a")
	clientB := e.AddNode(NodeTypeDNSQuery, "client-b")
	e.SubscribeMenus(static, clientA, clientB)
	e.applyStaticDNSRecords(static, "example.test A 10.10.0.1")

	e.MustAddTypedLinkByPortName(TypeDNS, static, "left", "DNS", clientA, "right", "DNS")
	e.MustAddTypedLinkByPortName(TypeDNS, static, "left", "DNS", clientB, "right", "DNS")

	e.sendDNSRequest(clientA)
	e.waitDNSResponse(clientA, "NOERROR", "10.10.0.1")
	e.sendDNSRequest(clientB)
	e.waitDNSResponse(clientB, "NOERROR", "10.10.0.1")
}

func TestStaticDNSFallbackChain(t *testing.T) {
	e := newDNSE2E(t)
	root := e.AddNode(NodeTypeStaticDNS, "root")
	middle := e.AddNode(NodeTypeStaticDNS, "middle")
	tail := e.AddNode(NodeTypeStaticDNS, "tail")
	client := e.AddNode(NodeTypeDNSQuery, "client")
	e.SubscribeMenus(root, middle, tail, client)
	e.applyStaticDNSRecords(root, "root.test A 10.20.0.1")
	e.applyStaticDNSRecords(tail, "tail.test A 10.20.0.3")

	e.MustAddTypedLinkByPortName(TypeDNS, tail, "left", "DNS", middle, "right", "Fallback")
	e.MustAddTypedLinkByPortName(TypeDNS, middle, "left", "DNS", root, "right", "Fallback")
	e.MustAddTypedLinkByPortName(TypeDNS, root, "left", "DNS", client, "right", "DNS")

	e.setDNSQueryRequest(client, "tail.test.", "A")
	e.sendDNSRequest(client)
	e.waitDNSResponse(client, "NOERROR", "10.20.0.3")

	e.setDNSQueryRequest(client, "root.test.", "A")
	e.sendDNSRequest(client)
	e.waitDNSResponse(client, "NOERROR", "10.20.0.1")
}

func TestDNSLinksAttachDetachReattach(t *testing.T) {
	e := newDNSE2E(t)
	static := e.AddNode(NodeTypeStaticDNS, "static")
	client := e.AddNode(NodeTypeDNSQuery, "client")
	e.SubscribeMenus(static, client)
	e.applyStaticDNSRecords(static, "example.test A 10.30.0.1")

	link := e.MustAddTypedLinkByPortName(TypeDNS, static, "left", "DNS", client, "right", "DNS")
	e.sendDNSRequest(client)
	e.waitDNSResponse(client, "NOERROR", "10.30.0.1")

	e.Engine.RemoveLink(E2EWorkspace, link)
	e.waitDNSReady(client)
	e.sendDNSRequest(client)
	e.waitDNSFailure(client, "dns is not ready")

	e.MustAddTypedLinkByPortName(TypeDNS, static, "left", "DNS", client, "right", "DNS")
	e.waitDNSReady(client)
	e.sendDNSRequest(client)
	e.waitDNSResponse(client, "NOERROR", "10.30.0.1")
}

func TestStaticDNSCopyPastePreservesConfigAndLinks(t *testing.T) {
	e := newDNSE2E(t)
	static := e.AddNode(NodeTypeStaticDNS, "static")
	client := e.AddNode(NodeTypeDNSQuery, "client")
	e.SubscribeMenus(static, client)
	e.applyStaticDNSRecords(static, "copy.test A 10.40.0.1")
	e.setDNSQueryRequest(client, "copy.test.", "A")
	e.MustAddTypedLinkByPortName(TypeDNS, static, "left", "DNS", client, "right", "DNS")
	e.sendDNSRequest(client)
	e.waitDNSResponse(client, "NOERROR", "10.40.0.1")

	clip := e.Engine.Copy(E2EWorkspace, []uint64{static, client})
	if clip == "" {
		t.Fatal("Copy returned empty clipboard")
	}
	pasted := e.Engine.Paste(E2EWorkspace, clip)
	if len(pasted) != 2 {
		t.Fatalf("Paste returned %d nodes (%v), want 2", len(pasted), pasted)
	}
	pastedClient := pastedNodeByClass(t, e.Engine, pasted, NodeTypeDNSQuery)
	e.SubscribeMenus(pastedClient)
	e.sendDNSRequest(pastedClient)
	e.waitDNSResponse(pastedClient, "NOERROR", "10.40.0.1")
}

func TestStaticDNSSaveRestorePreservesConfigAndLinks(t *testing.T) {
	e := newDNSE2E(t)
	static := e.AddNode(NodeTypeStaticDNS, "static")
	client := e.AddNode(NodeTypeDNSQuery, "client")
	e.SubscribeMenus(static, client)
	e.applyStaticDNSRecords(static, "restore.test A 10.50.0.1")
	e.setDNSQueryRequest(client, "restore.test.", "A")
	e.MustAddTypedLinkByPortName(TypeDNS, static, "left", "DNS", client, "right", "DNS")

	cfg := configer.NewMemory(nil)
	if err := e.Engine.SaveConfig(cfg); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}

	restored := newDNSE2EFromConfig(t, cfg)
	restoredClient := nodeIDByName(t, restored.Engine, "client")
	restored.SubscribeMenus(restoredClient)
	restored.sendDNSRequest(restoredClient)
	restored.waitDNSResponse(restoredClient, "NOERROR", "10.50.0.1")
}

func dnsTestQuery(name string, typ uint16) *dns.Message {
	return &dns.Message{
		ID:               dns.NextID(),
		Opcode:           dns.OpcodeQuery,
		RecursionDesired: true,
		Questions: []dns.Question{{
			Name:  name,
			Type:  typ,
			Class: dns.ClassIN,
		}},
	}
}

type dnsE2E struct {
	*NodeE2E
}

func newDNSE2E(t *testing.T) *dnsE2E {
	t.Helper()
	return &dnsE2E{NodeE2E: NewNodeE2E(t)}
}

func newDNSE2EFromConfig(t *testing.T, cfg configer.Config) *dnsE2E {
	t.Helper()
	pool := newE2ETestDebugPool(t)
	logf := NewDebugLogFactory(16384)
	sys := newE2EDebugSystem()
	engine, err := BuildEngine(context.Background(), cfg, logf, sys, nil, nil, nil, pool, true)
	if err != nil {
		t.Fatalf("BuildEngine restore: %v", err)
	}
	t.Cleanup(func() {
		if err := engine.Close(); err != nil {
			t.Errorf("engine.Close error = %v, want nil", err)
		}
	})

	e := &NodeE2E{T: t, Engine: engine, LogFactory: logf, DebugSystem: sys, menus: formular.NewMenuSnapshotState()}
	sub := engine.SubscribeNotifications(func(notification EngineNotification) {
		if notification.Kind != NotificationWorkspace || notification.Workspace == nil {
			return
		}
		if notification.Workspace.Kind == pasta.NotificationNodeMenu {
			e.mu.Lock()
			defer e.mu.Unlock()
			e.menus.Apply(notification.Workspace.Formular)
		}
	})
	if sub == 0 {
		t.Fatal("SubscribeNotifications returned 0")
	}
	t.Cleanup(func() { engine.UnsubscribeNotifications(sub) })
	return &dnsE2E{NodeE2E: e}
}

func (e *dnsE2E) applyStaticDNSRecords(node uint64, records string) {
	e.T.Helper()
	e.Engine.SendNodeFormularMsg(E2EWorkspace, node, formular.FormApplyMessage{
		MessageBase: FormMessageBase(formular.MessageFormApply, node),
		BlockID:     "config",
		Values: map[string]any{
			"records": records,
			"ttl":     defaultStaticDNSTTL,
		},
	})
	e.waitStaticDNSField(node, "records", normalizeStaticDNSRecordsText(records))
}

func (e *dnsE2E) waitStaticDNSField(node uint64, field, want string) {
	e.T.Helper()
	Eventually(e.T, func() bool {
		item, ok := e.MenuItem(node, "config", field)
		return ok && item.Field != nil && item.Value == want
	}, "StaticDns field %s = %q", field, want)
}

func (e *dnsE2E) setDNSQueryRequest(client uint64, name, qtype string) {
	e.T.Helper()
	e.Engine.SendNodeFormularMsg(E2EWorkspace, client, formular.FieldUpdateMessage{
		MessageBase: FormMessageBase(formular.MessageFieldUpdate, client),
		Field:       formular.FieldRef{BlockID: "request", FieldID: "name"},
		Value:       name,
	})
	e.Engine.SendNodeFormularMsg(E2EWorkspace, client, formular.FieldUpdateMessage{
		MessageBase: FormMessageBase(formular.MessageFieldUpdate, client),
		Field:       formular.FieldRef{BlockID: "request", FieldID: "type"},
		Value:       qtype,
	})
	e.waitDNSQueryRequestField(client, "name", absDNSName(name))
	e.waitDNSQueryRequestField(client, "type", strings.ToUpper(qtype))
}

func (e *dnsE2E) waitDNSQueryRequestField(client uint64, field, want string) {
	e.T.Helper()
	Eventually(e.T, func() bool {
		item, ok := e.MenuItem(client, "request", field)
		return ok && item.Field != nil && item.Value == want
	}, "DnsQuery field %s = %q", field, want)
}

func (e *dnsE2E) sendDNSRequest(client uint64) {
	e.T.Helper()
	e.Engine.SendNodeFormularMsg(E2EWorkspace, client, formular.ButtonPressMessage{
		MessageBase: FormMessageBase(formular.MessageButtonPress, client),
		BlockID:     "request",
		ButtonID:    "send",
	})
}

func (e *dnsE2E) waitDNSResponse(client uint64, status string, wantParts ...string) {
	e.T.Helper()
	Eventually(e.T, func() bool {
		if e.dnsQueryStatus(client) != "Status: "+status || e.dnsQueryResultField(client, "error") != "" {
			return false
		}
		resp := e.dnsQueryResultField(client, "response")
		for _, want := range wantParts {
			if !strings.Contains(resp, want) {
				return false
			}
		}
		return true
	}, "DNS response %s containing %v", status, wantParts)
	e.AssertNodeLabel(client, status)
}

func (e *dnsE2E) waitDNSFailure(client uint64, wantErrPart string) {
	e.T.Helper()
	Eventually(e.T, func() bool {
		return e.dnsQueryStatus(client) == "Status: fail" &&
			strings.Contains(e.dnsQueryResultField(client, "error"), wantErrPart)
	}, "DNS failure containing %q", wantErrPart)
	e.AssertNodeLabel(client, "fail")
}

func (e *dnsE2E) waitDNSReady(client uint64) {
	e.T.Helper()
	Eventually(e.T, func() bool {
		return e.dnsQueryStatus(client) == "Status: ready" &&
			e.dnsQueryResultField(client, "error") == "" &&
			e.dnsQueryResultField(client, "response") == ""
	}, "DNS query ready")
}

func (e *dnsE2E) dnsQueryStatus(client uint64) string {
	item, ok := e.MenuItem(client, "result", "status")
	if !ok {
		return ""
	}
	return item.Text
}

func (e *dnsE2E) dnsQueryResultField(client uint64, field string) string {
	item, ok := e.MenuItem(client, "result", field)
	if !ok || item.Field == nil {
		return ""
	}
	value, _ := std.StringFromPayload(item.Value)
	return value
}
