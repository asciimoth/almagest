package dnsnodes

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/asciimoth/almagest/lib/logging"
	"github.com/asciimoth/gonnect/dns"
	sysnetdebug "github.com/asciimoth/gonnect/sysnet/debug"
	"github.com/asciimoth/pasta/pasta"
	"github.com/asciimoth/pasta/pasta/std"
)

func TestOutDnsPortsAndLabel(t *testing.T) {
	source := newOutDNSTestProvider(t, "")
	w := pasta.NewWorkspace(logging.NewDebugLogFactory(1024))
	defer w.Close()

	if err := w.AddNodeClass(OutDNSClass{Runtime: testRuntime{sys: &sysnetdebug.System{OutDNSProvider: source}}}); err != nil {
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
	w := pasta.NewWorkspace(logging.NewDebugLogFactory(1024))
	defer w.Close()
	if err := w.AddNodeClass(OutDNSClass{Runtime: testRuntime{sys: &sysnetdebug.System{OutDNSProvider: source}}}); err != nil {
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
	if _, err := w.AddNode(receiver, "test/DNSReceiver", "receiver"); err != nil {
		t.Fatalf("AddNode receiver: %v", err)
	}
	if _, _, err := w.AddLink(out.LeftPorts[0], receiver.in); err != nil {
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
	w := pasta.NewWorkspace(logging.NewDebugLogFactory(1024))
	defer w.Close()
	if err := w.AddNodeClass(OutDNSClass{Runtime: testRuntime{sys: &sysnetdebug.System{OutDNSProvider: source}}}); err != nil {
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
	w := pasta.NewWorkspace(logging.NewDebugLogFactory(1024))
	defer w.Close()
	if err := w.AddNodeClass(OutDNSClass{Runtime: testRuntime{sys: &sysnetdebug.System{OutDNSProvider: source}}}); err != nil {
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
	w := pasta.NewWorkspace(logging.NewDebugLogFactory(1024))
	defer w.Close()
	if err := w.AddNodeClass(OutDNSClass{}); err != nil {
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

func newOutDNSTestProvider(t *testing.T, recordsText string) dns.Interface {
	t.Helper()
	provider, err := NewStaticProvider(recordsText, DefaultStaticDNSTTL)
	if err != nil {
		t.Fatalf("NewStaticProvider(%q): %v", recordsText, err)
	}
	t.Cleanup(func() {
		if err := provider.Close(); err != nil {
			t.Errorf("OutDNS provider Close error = %v, want nil", err)
		}
	})
	return provider
}

func assertDNSAnswer(t *testing.T, d dns.Interface, name string, qtype uint16, want string) {
	t.Helper()
	if !DNSIsReady(d) {
		t.Fatal("DNS wrapper is not ready before query")
	}
	resp, err := dns.Query(context.Background(), d, dnsTestQuery(name, qtype))
	if err != nil {
		t.Fatalf("DNS query failed: %v", err)
	}
	if resp == nil || resp.RCode != dns.RCodeSuccess {
		t.Fatalf("DNS response = %#v, want success", resp)
	}
	if got := FormatMessage(resp); !strings.Contains(got, want) {
		t.Fatalf("DNS response = %q, want to contain %q", got, want)
	}
}

func assertNoDNSPayload(t *testing.T, ch <-chan DNSPayload, d time.Duration) {
	t.Helper()
	select {
	case payload := <-ch:
		t.Fatalf("unexpected DNS payload: %#v", payload)
	case <-time.After(d):
	}
}
