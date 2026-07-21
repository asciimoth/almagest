package almagest

import (
	"context"
	"testing"
	"time"

	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/formular"
	"github.com/asciimoth/gonnect/dns"
	sysnetdebug "github.com/asciimoth/gonnect/sysnet/debug"
	"github.com/asciimoth/pasta/pasta"
	"github.com/asciimoth/pasta/pasta/std"
)

func TestOutDnsPortsAndLabel(t *testing.T) {
	source := newOutDNSTestProvider(t, "")
	w := pasta.NewWorkspace(NewDebugLogFactory(1024))
	defer w.Close()

	if err := w.AddNodeClass(outDNSClass{engine: &engine{sys: &sysnetdebug.System{OutDNSProvider: source}}}); err != nil {
		t.Fatalf("AddNodeClass OutDns: %v", err)
	}
	outID, err := w.AddNodeByClass(NodeTypeOutDNS, "out")
	if err != nil {
		t.Fatalf("AddNodeByClass OutDns: %v", err)
	}

	snapshot, ok := w.NodeSnapshot(outID)
	if !ok {
		t.Fatalf("NodeSnapshot(%d) missing", outID)
	}
	if snapshot.Label != "out DNS" {
		t.Fatalf("OutDns label = %q, want out DNS", snapshot.Label)
	}
	if len(snapshot.LeftPorts) != 1 || len(snapshot.RightPorts) != 0 {
		t.Fatalf("OutDns ports = left %v right %v, want one left DNS port", snapshot.LeftPorts, snapshot.RightPorts)
	}
	assertOutDNSPort(t, w, snapshot.LeftPorts[0])
}

func TestOutDnsUsesSystemOutDNSOnInit(t *testing.T) {
	source := newOutDNSTestProvider(t, "system-out.test A 10.132.0.1")
	pool := newE2ETestDebugPool(t)
	engineIface, err := BuildEngine(
		context.Background(),
		configer.NewMemory(nil),
		NewDebugLogFactory(1024),
		&sysnetdebug.System{OutDNSProvider: source},
		nil,
		nil,
		nil,
		pool,
		true,
	)
	if err != nil {
		t.Fatalf("BuildEngine error = %v, want nil", err)
	}
	defer func() {
		if err := engineIface.Close(); err != nil {
			t.Errorf("engine.Close error = %v, want nil", err)
		}
	}()

	outID, err := engineIface.AddNode(E2EWorkspace, NodeTypeOutDNS, "out")
	if err != nil {
		t.Fatalf("AddNode OutDns: %v", err)
	}
	engine := engineIface.(*engine)
	out, ok := engine.NodeSnapshot(E2EWorkspace, outID)
	if !ok || len(out.LeftPorts) != 1 {
		t.Fatalf("OutDns snapshot = %#v, %v; want one left port", out, ok)
	}

	receiver := &dnsReceiverNode{events: make(chan DNSPayload, 2)}
	if _, err := engine.mainWorkspace.AddNode(receiver, "test/DNSReceiver", "receiver"); err != nil {
		t.Fatalf("AddNode receiver: %v", err)
	}
	if _, _, err := engine.AddLink(E2EWorkspace, out.LeftPorts[0], receiver.in); err != nil {
		t.Fatalf("AddLink: %v", err)
	}

	payload := receiveReadyDNSPayload(t, receiver.events)
	if got := DNSSource(payload.DNS); got != source {
		t.Fatalf("wrapped DNS source = %T %p, want system OutDNS %p", got, got, source)
	}
	assertDNSAnswer(t, payload.DNS, "system-out.test.", dns.TypeA, "10.132.0.1")
}

func TestOutDnsSendsSystemOutDNSOnReadyAndRequest(t *testing.T) {
	source := newOutDNSTestProvider(t, "refresh-out.test A 10.132.0.2")
	w := pasta.NewWorkspace(NewDebugLogFactory(1024))
	defer w.Close()
	if err := w.AddNodeClass(outDNSClass{engine: &engine{sys: &sysnetdebug.System{OutDNSProvider: source}}}); err != nil {
		t.Fatalf("AddNodeClass OutDns: %v", err)
	}
	outID, err := w.AddNodeByClass(NodeTypeOutDNS, "out")
	if err != nil {
		t.Fatalf("AddNodeByClass OutDns: %v", err)
	}
	out, ok := w.NodeSnapshot(outID)
	if !ok || len(out.LeftPorts) != 1 {
		t.Fatalf("OutDns snapshot = %#v, %v; want one left port", out, ok)
	}

	receiver := &dnsReceiverNode{events: make(chan DNSPayload, 3)}
	receiverID, err := w.AddNode(receiver, "test/DNSReceiver", "receiver")
	if err != nil {
		t.Fatalf("AddNode receiver: %v", err)
	}
	link, _, err := w.AddLink(out.LeftPorts[0], receiver.in)
	if err != nil {
		t.Fatalf("AddLink: %v", err)
	}
	first := receiveReadyDNSPayload(t, receiver.events).DNS
	if got := DNSSource(first); got != source {
		t.Fatalf("first wrapped DNS source = %T %p, want %p", got, got, source)
	}

	w.EmitEvent(receiverID, link, std.RequestValue{})
	second := receiveReadyDNSPayload(t, receiver.events).DNS
	if got := DNSSource(second); got != source {
		t.Fatalf("request wrapped DNS source = %T %p, want %p", got, got, source)
	}
	if SameDNSInstance(first, second) {
		t.Fatal("OutDns reused wrapper for explicit request, want fresh detached DNS")
	}
	if DNSIsReady(first) {
		t.Fatal("first DNS wrapper remained ready after request refresh")
	}
	assertDNSAnswer(t, second, "refresh-out.test.", dns.TypeA, "10.132.0.2")
}

func TestOutDnsLinkDetachClosesWrapperAndReattachSendsNewWrapper(t *testing.T) {
	source := newOutDNSTestProvider(t, "reattach-out.test A 10.132.0.3")
	w := pasta.NewWorkspace(NewDebugLogFactory(1024))
	defer w.Close()
	if err := w.AddNodeClass(outDNSClass{engine: &engine{sys: &sysnetdebug.System{OutDNSProvider: source}}}); err != nil {
		t.Fatalf("AddNodeClass OutDns: %v", err)
	}
	outID, err := w.AddNodeByClass(NodeTypeOutDNS, "out")
	if err != nil {
		t.Fatalf("AddNodeByClass OutDns: %v", err)
	}
	out, ok := w.NodeSnapshot(outID)
	if !ok || len(out.LeftPorts) != 1 {
		t.Fatalf("OutDns snapshot = %#v, %v; want one left port", out, ok)
	}

	receiver := &dnsReceiverNode{events: make(chan DNSPayload, 3)}
	if _, err := w.AddNode(receiver, "test/DNSReceiver", "receiver"); err != nil {
		t.Fatalf("AddNode receiver: %v", err)
	}
	link, _, err := w.AddLink(out.LeftPorts[0], receiver.in)
	if err != nil {
		t.Fatalf("AddLink: %v", err)
	}
	first := receiveReadyDNSPayload(t, receiver.events).DNS
	w.RemoveLink(link)
	if DNSIsReady(first) {
		t.Fatal("first DNS wrapper remained ready after link removal")
	}

	if _, _, err := w.AddLink(out.LeftPorts[0], receiver.in); err != nil {
		t.Fatalf("reattach link: %v", err)
	}
	second := receiveReadyDNSPayload(t, receiver.events).DNS
	if SameDNSInstance(first, second) {
		t.Fatal("OutDns reused detached wrapper after reattach")
	}
	assertDNSAnswer(t, second, "reattach-out.test.", dns.TypeA, "10.132.0.3")
}

func TestOutDnsWithoutSystemDoesNotSend(t *testing.T) {
	w := pasta.NewWorkspace(NewDebugLogFactory(1024))
	defer w.Close()
	if err := w.AddNodeClass(outDNSClass{}); err != nil {
		t.Fatalf("AddNodeClass OutDns: %v", err)
	}
	outID, err := w.AddNodeByClass(NodeTypeOutDNS, "out")
	if err != nil {
		t.Fatalf("AddNodeByClass OutDns: %v", err)
	}
	out, ok := w.NodeSnapshot(outID)
	if !ok || len(out.LeftPorts) != 1 {
		t.Fatalf("OutDns snapshot = %#v, %v; want one left port", out, ok)
	}

	receiver := &dnsReceiverNode{events: make(chan DNSPayload, 2)}
	receiverID, err := w.AddNode(receiver, "test/DNSReceiver", "receiver")
	if err != nil {
		t.Fatalf("AddNode receiver: %v", err)
	}
	link, _, err := w.AddLink(out.LeftPorts[0], receiver.in)
	if err != nil {
		t.Fatalf("AddLink: %v", err)
	}
	assertNoDNSPayload(t, receiver.events, 100*time.Millisecond)
	w.EmitEvent(receiverID, link, std.RequestValue{})
	assertNoDNSPayload(t, receiver.events, 100*time.Millisecond)
}

func TestOutDnsResolvesSystemDefaultDNSE2E(t *testing.T) {
	e := newOutDNSE2E(t, nil, "default-out.test A 10.133.0.1", nil)
	out := e.AddNode(NodeTypeOutDNS, "out")
	query := e.AddNode(NodeTypeDNSQuery, "query")
	e.SubscribeMenus(query)
	e.setDNSQueryRequest(query, "default-out.test.", "A")

	e.MustAddTypedLinkByPortName(TypeDNS, out, "left", "DNS", query, "right", "DNS")
	e.sendDNSRequest(query)
	e.waitDNSResponse(query, "NOERROR", "10.133.0.1")
}

func TestOutDnsWorksThroughDnsCacheAndRouterE2E(t *testing.T) {
	manager := newMemoryDNSCacheManager()
	e := newOutDNSE2E(t, nil, "cached-out.test A 10.133.0.2", manager)
	out := e.AddNode(NodeTypeOutDNS, "out")
	cache := e.AddNode(NodeTypeDnsCache, "cache")
	router := e.AddNode(NodeTypeDnsRouter, "router")
	query := e.AddNode(NodeTypeDNSQuery, "query")
	e.SubscribeMenus(cache, router, query)
	e.setDNSQueryRequest(query, "cached-out.test.", "A")

	inputLink := e.MustAddTypedLinkByPortName(TypeDNS, out, "left", "DNS", cache, "right", "DNS")
	e.MustAddTypedLinkByPortName(TypeDNS, cache, "left", "DNS", router, "right", "slot 1")
	e.MustAddTypedLinkByPortName(TypeDNS, router, "left", "DNS", query, "right", "DNS")
	e.WaitNodeLabel(cache, "caching DNS")
	e.WaitNodeLabel(router, "routing DNS")

	e.sendDNSRequest(query)
	e.waitDNSResponse(query, "NOERROR", "10.133.0.2")

	e.Engine.RemoveLink(E2EWorkspace, inputLink)
	e.WaitNodeLabel(cache, "cache-only DNS")
	e.sendDNSRequest(query)
	e.waitDNSResponse(query, "NOERROR", "10.133.0.2")
}

func TestOutDnsCopyPastePreservesLinksE2E(t *testing.T) {
	e := newOutDNSE2E(t, nil, "copy-out.test A 10.133.0.3", nil)
	out := e.AddNode(NodeTypeOutDNS, "out")
	query := e.AddNode(NodeTypeDNSQuery, "query")
	e.SubscribeMenus(query)
	e.setDNSQueryRequest(query, "copy-out.test.", "A")
	e.MustAddTypedLinkByPortName(TypeDNS, out, "left", "DNS", query, "right", "DNS")
	e.sendDNSRequest(query)
	e.waitDNSResponse(query, "NOERROR", "10.133.0.3")

	clip := e.Engine.Copy(E2EWorkspace, []uint64{out, query})
	if clip == "" {
		t.Fatal("Copy returned empty clipboard")
	}
	pasted := e.Engine.Paste(E2EWorkspace, clip)
	if len(pasted) != 2 {
		t.Fatalf("Paste returned %d nodes (%v), want 2", len(pasted), pasted)
	}
	pastedQuery := pastedNodeByClass(t, e.Engine, pasted, NodeTypeDNSQuery)
	e.SubscribeMenus(pastedQuery)
	e.sendDNSRequest(pastedQuery)
	e.waitDNSResponse(pastedQuery, "NOERROR", "10.133.0.3")
}

func TestOutDnsSaveRestorePreservesLinksE2E(t *testing.T) {
	const records = "restore-out.test A 10.133.0.4"
	e := newOutDNSE2E(t, nil, records, nil)
	out := e.AddNode(NodeTypeOutDNS, "out")
	query := e.AddNode(NodeTypeDNSQuery, "query")
	e.SubscribeMenus(query)
	e.setDNSQueryRequest(query, "restore-out.test.", "A")
	e.MustAddTypedLinkByPortName(TypeDNS, out, "left", "DNS", query, "right", "DNS")
	e.sendDNSRequest(query)
	e.waitDNSResponse(query, "NOERROR", "10.133.0.4")

	cfg := configer.NewMemory(nil)
	if err := e.Engine.SaveConfig(cfg); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}

	restored := newOutDNSE2E(t, cfg, records, nil)
	restoredQuery := nodeIDByName(t, restored.Engine, "query")
	restored.SubscribeMenus(restoredQuery)
	restored.sendDNSRequest(restoredQuery)
	restored.waitDNSResponse(restoredQuery, "NOERROR", "10.133.0.4")
}

func TestOutDnsLinkAttachDetachReattachE2E(t *testing.T) {
	e := newOutDNSE2E(t, nil, "lifecycle-out.test A 10.133.0.5", nil)
	out := e.AddNode(NodeTypeOutDNS, "out")
	query := e.AddNode(NodeTypeDNSQuery, "query")
	e.SubscribeMenus(query)
	e.setDNSQueryRequest(query, "lifecycle-out.test.", "A")

	link := e.MustAddTypedLinkByPortName(TypeDNS, out, "left", "DNS", query, "right", "DNS")
	e.sendDNSRequest(query)
	e.waitDNSResponse(query, "NOERROR", "10.133.0.5")

	e.Engine.RemoveLink(E2EWorkspace, link)
	e.waitDNSReady(query)
	e.sendDNSRequest(query)
	e.waitDNSFailure(query, "dns is not ready")

	e.MustAddTypedLinkByPortName(TypeDNS, out, "left", "DNS", query, "right", "DNS")
	e.waitDNSReady(query)
	e.sendDNSRequest(query)
	e.waitDNSResponse(query, "NOERROR", "10.133.0.5")
}

func assertOutDNSPort(t *testing.T, w *pasta.Workspace, port uint64) {
	t.Helper()
	snapshot, ok := w.PortSnapshot(port)
	if !ok {
		t.Fatalf("PortSnapshot(%d) missing", port)
	}
	if snapshot.Direction != "left" || snapshot.Name != "DNS" || len(snapshot.Types) != 1 || snapshot.Types[0] != TypeDNS {
		t.Fatalf("OutDns port = %#v, want left DNS %s", snapshot, TypeDNS)
	}
}

func newOutDNSTestProvider(t *testing.T, recordsText string) *staticDNSProvider {
	t.Helper()
	records, err := parseStaticDNSRecords(recordsText, defaultStaticDNSTTL)
	if err != nil {
		t.Fatalf("parseStaticDNSRecords(%q): %v", recordsText, err)
	}
	provider := newStaticDNSProvider(records)
	t.Cleanup(func() {
		if err := provider.Close(); err != nil {
			t.Errorf("OutDNS provider Close error = %v, want nil", err)
		}
	})
	return provider
}

func newOutDNSE2E(t *testing.T, cfg configer.Config, recordsText string, manager DnsCacheManager) *dnsE2E {
	t.Helper()
	source := newOutDNSTestProvider(t, recordsText)
	if cfg == nil {
		cfg = configer.NewMemory(nil)
	}
	pool := newE2ETestDebugPool(t)
	logf := NewDebugLogFactory(16384)
	sys := newE2EDebugSystem()
	sys.OutDNSProvider = source
	engine, err := BuildEngine(context.Background(), cfg, logf, sys, nil, manager, nil, pool, true)
	if err != nil {
		t.Fatalf("BuildEngine: %v", err)
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
