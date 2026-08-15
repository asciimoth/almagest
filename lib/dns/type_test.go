package dnsnodes

import (
	"testing"
	"time"

	"github.com/asciimoth/gonnect/dns"
	"github.com/asciimoth/pasta/pasta"

	"github.com/asciimoth/almagest/lib/logging"
)

func TestAcceptDNSPayloadReplacesReadySameSourceWrapper(t *testing.T) {
	base := newStaticDNSProvider(staticDNSRecords{})
	defer func() { _ = base.Close() }()
	current := NewDetachedDNS(base)
	replacement := NewDetachedDNS(base)
	defer func() { _ = replacement.Close() }()

	next, changed := AcceptDNSPayload(nil, 0, pasta.Event{
		Link:    1,
		Payload: DNSPayload{DNS: replacement},
	}, current)
	if !changed {
		t.Fatal("AcceptDNSPayload() changed = false, want true for fresh same-source wrapper")
	}
	if !SameDNSInstance(next, replacement) {
		t.Fatal("AcceptDNSPayload() did not return replacement")
	}
	if DNSIsReady(current) {
		t.Fatal("current DNS wrapper is still ready after replacement")
	}
	if !DNSIsReady(replacement) {
		t.Fatal("replacement DNS wrapper is not ready")
	}
}

func TestDNSOutputCacheReusesReadyWrapperAndReplacesClosedWrapper(t *testing.T) {
	w := pasta.NewWorkspace(logging.NewDebugLogFactory(1024))
	defer w.Close()
	providerID, out := addDNSOutputCacheTestPort(t, w)
	receiver := &dnsReceiverNode{events: make(chan DNSPayload, 3)}
	_, err := w.AddNode(receiver, "test/DNSReceiver", "receiver")
	if err != nil {
		t.Fatalf("AddNode receiver: %v", err)
	}
	link, _, err := w.AddLink(out, receiver.in)
	if err != nil {
		t.Fatalf("AddLink: %v", err)
	}

	source := newStaticDNSProvider(staticDNSRecords{})
	defer func() { _ = source.Close() }()
	var cache DNSOutputCache
	if !cache.Send(w, providerID, link, source) {
		t.Fatal("DNSOutputCache.Send() = false, want true")
	}
	first := receiveDNSPayload(t, receiver.events).DNS
	if !cache.Send(w, providerID, link, source) {
		t.Fatal("DNSOutputCache.Send() second = false, want true")
	}
	second := receiveDNSPayload(t, receiver.events).DNS
	if !SameDNSInstance(first, second) {
		t.Fatal("DNSOutputCache did not reuse ready wrapper")
	}

	if err := first.Close(); err != nil {
		t.Fatalf("first.Close(): %v", err)
	}
	if !cache.Send(w, providerID, link, source) {
		t.Fatal("DNSOutputCache.Send() after close = false, want true")
	}
	third := receiveDNSPayload(t, receiver.events).DNS
	if SameDNSInstance(first, third) {
		t.Fatal("DNSOutputCache reused closed wrapper, want replacement")
	}
	cache.CloseAll()
}

func TestDNSOutputCacheRefreshReplacesReadySameSourceWrapper(t *testing.T) {
	w := pasta.NewWorkspace(logging.NewDebugLogFactory(1024))
	defer w.Close()
	providerID, out := addDNSOutputCacheTestPort(t, w)
	receiver := &dnsReceiverNode{events: make(chan DNSPayload, 2)}
	_, err := w.AddNode(receiver, "test/DNSReceiver", "receiver")
	if err != nil {
		t.Fatalf("AddNode receiver: %v", err)
	}
	link, _, err := w.AddLink(out, receiver.in)
	if err != nil {
		t.Fatalf("AddLink: %v", err)
	}

	source := newStaticDNSProvider(staticDNSRecords{})
	defer func() { _ = source.Close() }()
	var cache DNSOutputCache
	if !cache.Send(w, providerID, link, source) {
		t.Fatal("DNSOutputCache.Send() = false, want true")
	}
	first := receiveDNSPayload(t, receiver.events).DNS
	w.Lock()
	refreshedOK := cache.RefreshLocked(w, providerID, link, source)
	w.Unlock()
	if !refreshedOK {
		t.Fatal("DNSOutputCache.RefreshLocked() = false, want true")
	}
	second := receiveDNSPayload(t, receiver.events).DNS
	if SameDNSInstance(first, second) {
		t.Fatal("DNSOutputCache.RefreshLocked reused wrapper, want fresh detach")
	}
	if DNSIsReady(first) {
		t.Fatal("DNSOutputCache.RefreshLocked left previous wrapper ready")
	}
	if !DNSIsReady(second) {
		t.Fatal("DNSOutputCache.RefreshLocked emitted non-ready wrapper")
	}
	cache.CloseAll()
}

func TestDNSOutputCacheWrappedSendAndRefresh(t *testing.T) {
	w := pasta.NewWorkspace(logging.NewDebugLogFactory(1024))
	defer w.Close()
	providerID, out := addDNSOutputCacheTestPort(t, w)
	receiver := &dnsReceiverNode{events: make(chan DNSPayload, 3)}
	_, err := w.AddNode(receiver, "test/DNSReceiver", "receiver")
	if err != nil {
		t.Fatalf("AddNode receiver: %v", err)
	}
	link, _, err := w.AddLink(out, receiver.in)
	if err != nil {
		t.Fatalf("AddLink: %v", err)
	}

	source := newStaticDNSProvider(staticDNSRecords{})
	defer func() { _ = source.Close() }()
	var cache DNSOutputCache
	wrap := func(output dns.Interface) dns.Interface {
		return newDebugLogDNS(output, nil)
	}
	w.Lock()
	if !cache.SendWrappedLocked(w, providerID, link, source, wrap) {
		t.Fatal("DNSOutputCache.SendWrappedLocked() = false, want true")
	}
	w.Unlock()
	first := receiveDNSPayload(t, receiver.events).DNS
	if _, ok := first.(*debugLogDNS); !ok {
		t.Fatalf("payload DNS = %T, want *debugLogDNS", first)
	}
	w.Lock()
	if !cache.SendWrappedLocked(w, providerID, link, source, wrap) {
		t.Fatal("DNSOutputCache.SendWrappedLocked() second = false, want true")
	}
	w.Unlock()
	second := receiveDNSPayload(t, receiver.events).DNS
	if !SameDNSInstance(first, second) {
		t.Fatal("DNSOutputCache did not reuse ready wrapped output")
	}

	w.Lock()
	if !cache.RefreshWrappedLocked(w, providerID, link, source, wrap) {
		t.Fatal("DNSOutputCache.RefreshWrappedLocked() = false, want true")
	}
	w.Unlock()
	third := receiveDNSPayload(t, receiver.events).DNS
	if SameDNSInstance(second, third) {
		t.Fatal("DNSOutputCache.RefreshWrappedLocked reused output, want replacement")
	}
	if DNSIsReady(first) {
		t.Fatal("previous wrapped output is still ready after refresh")
	}
	cache.CloseAll()
}

func TestDNSOutputCacheReplacesChangedSource(t *testing.T) {
	w := pasta.NewWorkspace(logging.NewDebugLogFactory(1024))
	defer w.Close()
	providerID, out := addDNSOutputCacheTestPort(t, w)
	receiver := &dnsReceiverNode{events: make(chan DNSPayload, 2)}
	_, err := w.AddNode(receiver, "test/DNSReceiver", "receiver")
	if err != nil {
		t.Fatalf("AddNode receiver: %v", err)
	}
	link, _, err := w.AddLink(out, receiver.in)
	if err != nil {
		t.Fatalf("AddLink: %v", err)
	}

	sourceA := newStaticDNSProvider(staticDNSRecords{})
	defer func() { _ = sourceA.Close() }()
	sourceB := newStaticDNSProvider(staticDNSRecords{})
	defer func() { _ = sourceB.Close() }()
	var cache DNSOutputCache
	if !cache.Send(w, providerID, link, sourceA) {
		t.Fatal("DNSOutputCache.Send() = false, want true")
	}
	first := receiveDNSPayload(t, receiver.events).DNS
	if !cache.Send(w, providerID, link, sourceB) {
		t.Fatal("DNSOutputCache.Send() changed source = false, want true")
	}
	second := receiveDNSPayload(t, receiver.events).DNS
	if SameDNSInstance(first, second) {
		t.Fatal("DNSOutputCache reused wrapper for changed source")
	}
	if DNSIsReady(first) {
		t.Fatal("DNSOutputCache left old wrapper ready after source replacement")
	}
	cache.CloseAll()
}

func addDNSOutputCacheTestPort(t *testing.T, w *pasta.Workspace) (uint64, uint64) {
	t.Helper()
	provider := &dnsOutputCacheTestProvider{}
	nodeID, err := w.AddNode(provider, "test/DNSOutputCacheProvider", "provider")
	if err != nil {
		t.Fatalf("AddNode provider: %v", err)
	}
	return nodeID, provider.out
}

type dnsOutputCacheTestProvider struct {
	pasta.BasicNode
	out uint64
}

func (n *dnsOutputCacheTestProvider) OnInit(w *pasta.Workspace, _ pasta.Logger, id uint64, _ string, _ *pasta.NodeInitData, _, _, _, _ bool) error {
	port, err := w.AddPortLocked(pasta.Port{Node: id, Direction: "left", Name: "DNS", Types: []string{TypeDNS}})
	if err != nil {
		return err
	}
	n.out = port
	return nil
}

func (n *dnsOutputCacheTestProvider) PreLinkAdd(port uint64, linkType, portDirection string) error {
	if port != n.out || portDirection != "left" || linkType != TypeDNS {
		return pasta.LinkTypeErr(linkType)
	}
	return nil
}

type dnsReceiverNode struct {
	pasta.BasicNode
	in     uint64
	events chan DNSPayload
}

func (n *dnsReceiverNode) OnInit(w *pasta.Workspace, _ pasta.Logger, id uint64, _ string, _ *pasta.NodeInitData, _, _, _, _ bool) error {
	port, err := w.AddPortLocked(pasta.Port{Node: id, Direction: "right", Name: "DNS", Types: []string{TypeDNS}})
	if err != nil {
		return err
	}
	n.in = port
	return nil
}

func (n *dnsReceiverNode) PreLinkAdd(port uint64, linkType, portDirection string) error {
	if port != n.in || portDirection != "right" || linkType != TypeDNS {
		return pasta.LinkTypeErr(linkType)
	}
	return nil
}

func (n *dnsReceiverNode) OnEvent(event pasta.Event, linkType string, _ []string, receiverPortDirection string) error {
	if event.ReceiverPort != n.in || receiverPortDirection != "right" || linkType != TypeDNS {
		return nil
	}
	payload, ok := event.Payload.(DNSPayload)
	if !ok {
		return nil
	}
	n.events <- payload
	return nil
}

func receiveDNSPayload(t *testing.T, ch <-chan DNSPayload) DNSPayload {
	t.Helper()
	select {
	case payload := <-ch:
		return payload
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for DNS payload")
		return DNSPayload{}
	}
}
