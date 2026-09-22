package dnsnodes

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/asciimoth/almagest/lib/logging"
	"github.com/asciimoth/almagest/lib/nodekit"
	"github.com/asciimoth/almagest/lib/testkit"
	"github.com/asciimoth/gonnect/dns"
	"github.com/asciimoth/pasta/pasta"
)

func TestDNSFallbackPortsGrowToLimitAndShrink(t *testing.T) {
	w := newDNSFallbackTestWorkspace(t)
	fallbackID := mustAddDNSFallbackTestNode(t, w, NodeTypeDnsFallback, "fallback")
	staticID := mustAddDNSFallbackTestNode(t, w, NodeTypeStaticDNS, "static")

	assertDNSFallbackRightPorts(t, w, fallbackID, []string{"slot 1"})
	assertNodeLabel(t, w, fallbackID, "waiting for DNS")

	staticOut := testkit.MustNodeSnapshot(t, w, staticID).LeftPorts[0]
	links := make([]uint64, 0, dnsFallbackMaxSlots)
	for slot := 1; slot <= dnsFallbackMaxSlots; slot++ {
		port := dnsFallbackRightPort(t, w, fallbackID, slot)
		link, linkType, err := w.AddLink(staticOut, port)
		if err != nil {
			t.Fatalf("AddLink slot %d: %v", slot, err)
		}
		if linkType != TypeDNS {
			t.Fatalf("AddLink slot %d type = %q, want %q", slot, linkType, TypeDNS)
		}
		links = append(links, link)
	}

	wantPorts := make([]string, dnsFallbackMaxSlots)
	for i := range wantPorts {
		wantPorts[i] = nodekit.SlotPortName(i + 1)
	}
	assertDNSFallbackRightPorts(t, w, fallbackID, wantPorts)
	assertNodeLabel(t, w, fallbackID, "fallback across 16 DNS backends")

	lastPort := dnsFallbackRightPort(t, w, fallbackID, dnsFallbackMaxSlots)
	if _, _, err := w.AddLink(staticOut, lastPort); !errors.Is(err, pasta.ErrLinkDup) {
		t.Fatalf("duplicate slot link error = %v, want ErrLinkDup", err)
	}

	for i := len(links) - 1; i >= 0; i-- {
		w.RemoveLink(links[i])
	}
	assertDNSFallbackRightPorts(t, w, fallbackID, []string{"slot 1"})
	assertNodeLabel(t, w, fallbackID, "waiting for DNS")
}

func TestDNSFallbackKeepsOutputStableAcrossInputChanges(t *testing.T) {
	w := newDNSFallbackTestWorkspace(t)
	fallbackID := mustAddDNSFallbackTestNode(t, w, NodeTypeDnsFallback, "fallback")
	emptyID := mustAddDNSFallbackTestNode(t, w, NodeTypeStaticDNS, "empty")
	answerID := mustAddDNSFallbackTestNode(t, w, NodeTypeStaticDNS, "answer")
	applyMeshNamesStaticDNSRecords(t, w, answerID, "stable.fallback.test A 192.0.2.44")

	receiver := &dnsReceiverNode{events: make(chan DNSPayload, 8)}
	if _, err := w.AddNode(receiver, "test/DNSReceiver", "receiver"); err != nil {
		t.Fatalf("AddNode receiver: %v", err)
	}

	fallback := testkit.MustNodeSnapshot(t, w, fallbackID)
	outputLink, _, err := w.AddLink(fallback.LeftPorts[0], receiver.in)
	if err != nil {
		t.Fatalf("AddLink fallback->receiver: %v", err)
	}
	output := receiveReadyDNSPayload(t, receiver.events).DNS
	if _, err := dns.Query(context.Background(), output, dnsTestQuery("empty.fallback.test.", dns.TypeA)); !errors.Is(err, dns.ErrNoUpstream) {
		t.Fatalf("empty fallback query error = %v, want ErrNoUpstream", err)
	}

	emptyOut := testkit.MustNodeSnapshot(t, w, emptyID).LeftPorts[0]
	emptyLink, _, err := w.AddLink(emptyOut, dnsFallbackRightPort(t, w, fallbackID, 1))
	if err != nil {
		t.Fatalf("AddLink empty->slot 1: %v", err)
	}
	assertSameDNSFallbackOutput(t, output, receiver.events)

	answerOut := testkit.MustNodeSnapshot(t, w, answerID).LeftPorts[0]
	answerLink, _, err := w.AddLink(answerOut, dnsFallbackRightPort(t, w, fallbackID, 2))
	if err != nil {
		t.Fatalf("AddLink answer->slot 2: %v", err)
	}
	assertSameDNSFallbackOutput(t, output, receiver.events)
	assertDNSAnswer(t, output, "stable.fallback.test.", dns.TypeA, "192.0.2.44")

	w.RemoveLink(answerLink)
	response, err := dns.Query(context.Background(), output, dnsTestQuery("stable.fallback.test.", dns.TypeA))
	if err != nil {
		t.Fatalf("query after slot 2 detach: %v", err)
	}
	if response == nil || response.RCode != dns.RCodeNameError {
		t.Fatalf("query after slot 2 detach response = %#v, want NXDOMAIN", response)
	}
	if !DNSIsReady(output) {
		t.Fatal("fallback output closed when an input detached")
	}

	w.RemoveLink(emptyLink)
	if _, err := dns.Query(context.Background(), output, dnsTestQuery("empty-again.fallback.test.", dns.TypeA)); !errors.Is(err, dns.ErrNoUpstream) {
		t.Fatalf("query after all inputs detached error = %v, want ErrNoUpstream", err)
	}
	w.RemoveLink(outputLink)
	if DNSIsReady(output) {
		t.Fatal("fallback output remained ready after its output link was removed")
	}
}

func TestDNSFallbackUsesConfiguredAttemptTimeout(t *testing.T) {
	blocked := newBlockingDNS()
	answer, err := NewStaticProvider("timeout.fallback.test A 192.0.2.45", 30)
	if err != nil {
		t.Fatalf("NewStaticProvider: %v", err)
	}
	t.Cleanup(func() {
		_ = blocked.Close()
		_ = answer.Close()
	})

	n := &dnsFallbackNode{attemptTimeoutMS: 20}
	fallback := n.newFallback()
	t.Cleanup(func() { _ = fallback.Close() })
	if err := fallback.Attach(1, blocked); err != nil {
		t.Fatalf("Attach blocked upstream: %v", err)
	}
	if err := fallback.Attach(2, answer); err != nil {
		t.Fatalf("Attach answer upstream: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	started := time.Now()
	response, err := dns.Query(ctx, fallback, dnsTestQuery("timeout.fallback.test.", dns.TypeA))
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if elapsed := time.Since(started); elapsed >= time.Second {
		t.Fatalf("fallback query took %v, want configured timeout to permit failover", elapsed)
	}
	if got := FormatMessage(response); !strings.Contains(got, "192.0.2.45") {
		t.Fatalf("fallback response = %q, want second-slot answer", got)
	}
}

func newDNSFallbackTestWorkspace(t *testing.T) *pasta.Workspace {
	t.Helper()
	w := pasta.NewWorkspace(logging.NewDebugLogFactory(1024))
	t.Cleanup(func() { w.Close() })
	for _, class := range []pasta.NodeClass{
		DNSFallbackClass{},
		StaticDNSClass{},
	} {
		if err := w.AddNodeClass(class); err != nil {
			t.Fatalf("AddNodeClass %s: %v", class.ClassName(), err)
		}
	}
	return w
}

func mustAddDNSFallbackTestNode(t *testing.T, w *pasta.Workspace, class, name string) uint64 {
	t.Helper()
	id, err := w.AddNodeByClass(class, name)
	if err != nil {
		t.Fatalf("AddNodeByClass %s: %v", class, err)
	}
	return id
}

func dnsFallbackRightPort(t *testing.T, w *pasta.Workspace, node uint64, slot int) uint64 {
	t.Helper()
	want := nodekit.SlotPortName(slot)
	for _, port := range testkit.MustNodeSnapshot(t, w, node).RightPorts {
		snapshot, ok := w.PortSnapshot(port)
		if ok && snapshot.Name == want {
			return port
		}
	}
	t.Fatalf("DnsFallback node %d has no right port %q", node, want)
	return 0
}

func assertDNSFallbackRightPorts(t *testing.T, w *pasta.Workspace, node uint64, want []string) {
	t.Helper()
	snapshot := testkit.MustNodeSnapshot(t, w, node)
	got := make([]string, 0, len(snapshot.RightPorts))
	for _, port := range snapshot.RightPorts {
		portSnapshot, ok := w.PortSnapshot(port)
		if !ok {
			t.Fatalf("PortSnapshot(%d) missing", port)
		}
		if portSnapshot.Direction != "right" || len(portSnapshot.Types) != 1 || portSnapshot.Types[0] != TypeDNS {
			t.Fatalf("DnsFallback port = %#v, want right-directed %s", portSnapshot, TypeDNS)
		}
		got = append(got, portSnapshot.Name)
	}
	testkit.AssertStringSlice(t, got, want)
}

func assertSameDNSFallbackOutput(t *testing.T, want dns.Interface, events <-chan DNSPayload) {
	t.Helper()
	got := receiveReadyDNSPayload(t, events).DNS
	if !SameDNSInstance(got, want) {
		t.Fatalf("DnsFallback output changed from %p to %p", want, got)
	}
}
