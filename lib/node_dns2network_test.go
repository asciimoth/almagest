package almagest

import (
	"context"
	"fmt"
	dnsnodes "github.com/asciimoth/almagest/lib/dns"
	"net"
	"net/netip"
	"strings"
	"sync"
	"testing"

	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/gonnect"
	"github.com/asciimoth/gonnect/dns"
	"github.com/asciimoth/pasta/pasta"
)

func TestDns2NetworkProvidesDNSFromNetworkLookup(t *testing.T) {
	w, lookup := newDns2NetworkTestWorkspace(t)
	adapterID, err := w.AddNodeByClass(dnsnodes.NodeTypeDns2Network, "adapter")
	if err != nil {
		t.Fatalf("AddNodeByClass Dns2Network: %v", err)
	}
	networkID, err := w.AddNodeByClass(dns2NetworkLookupNetworkNodeType, "network")
	if err != nil {
		t.Fatalf("AddNodeByClass lookup network: %v", err)
	}
	receiver := &dnsReceiverNode{events: make(chan dnsnodes.DNSPayload, 2)}
	if _, err := w.AddNode(receiver, "test/DNSReceiver", "receiver"); err != nil {
		t.Fatalf("AddNode receiver: %v", err)
	}

	adapter := mustNodeSnapshot(t, w, adapterID)
	network := mustNodeSnapshot(t, w, networkID)
	if _, _, err := w.AddLink(network.LeftPorts[0], adapter.RightPorts[0]); err != nil {
		t.Fatalf("AddLink network->adapter: %v", err)
	}
	if _, _, err := w.AddLink(adapter.LeftPorts[0], receiver.in); err != nil {
		t.Fatalf("AddLink adapter->receiver: %v", err)
	}

	payload := receiveReadyDNSPayload(t, receiver.events)
	assertDNSAnswer(t, payload.DNS, "example.test.", dns.TypeA, "192.0.2.44")
	lookup.assertCall(t, "LookupIP ip4 example.test")
	assertNodeLabel(t, w, adapterID, "resolving DNS")
}

func TestDns2NetworkLinksAttachDetachReattach(t *testing.T) {
	w, _ := newDns2NetworkTestWorkspace(t)
	adapterID, err := w.AddNodeByClass(dnsnodes.NodeTypeDns2Network, "adapter")
	if err != nil {
		t.Fatalf("AddNodeByClass Dns2Network: %v", err)
	}
	networkID, err := w.AddNodeByClass(dns2NetworkLookupNetworkNodeType, "network")
	if err != nil {
		t.Fatalf("AddNodeByClass lookup network: %v", err)
	}
	receiver := &dnsReceiverNode{events: make(chan dnsnodes.DNSPayload, 4)}
	if _, err := w.AddNode(receiver, "test/DNSReceiver", "receiver"); err != nil {
		t.Fatalf("AddNode receiver: %v", err)
	}

	adapter := mustNodeSnapshot(t, w, adapterID)
	network := mustNodeSnapshot(t, w, networkID)
	networkLink, _, err := w.AddLink(network.LeftPorts[0], adapter.RightPorts[0])
	if err != nil {
		t.Fatalf("AddLink network->adapter: %v", err)
	}
	outputLink, _, err := w.AddLink(adapter.LeftPorts[0], receiver.in)
	if err != nil {
		t.Fatalf("AddLink adapter->receiver: %v", err)
	}
	first := receiveReadyDNSPayload(t, receiver.events).DNS
	assertDNSAnswer(t, first, "example.test.", dns.TypeA, "192.0.2.44")

	w.RemoveLink(networkLink)
	if dnsnodes.DNSIsReady(first) {
		t.Fatal("DNS wrapper remained ready after network link removal")
	}
	assertNodeLabel(t, w, adapterID, "waiting for network")

	networkLink, _, err = w.AddLink(network.LeftPorts[0], adapter.RightPorts[0])
	if err != nil {
		t.Fatalf("reattach network link: %v", err)
	}
	second := receiveReadyDNSPayload(t, receiver.events).DNS
	if dnsnodes.SameDNSInstance(first, second) {
		t.Fatal("Dns2Network reused DNS wrapper after network reattach")
	}
	assertDNSAnswer(t, second, "example.test.", dns.TypeA, "192.0.2.44")
	assertNodeLabel(t, w, adapterID, "resolving DNS")

	w.RemoveLink(outputLink)
	if dnsnodes.DNSIsReady(second) {
		t.Fatal("DNS wrapper remained ready after DNS link removal")
	}

	outputLink, _, err = w.AddLink(adapter.LeftPorts[0], receiver.in)
	if err != nil {
		t.Fatalf("reattach DNS output link: %v", err)
	}
	third := receiveReadyDNSPayload(t, receiver.events).DNS
	if dnsnodes.SameDNSInstance(second, third) {
		t.Fatal("Dns2Network reused DNS wrapper after DNS output reattach")
	}
	assertDNSAnswer(t, third, "example.test.", dns.TypeA, "192.0.2.44")
	_ = outputLink
	_ = networkLink
}

func TestDns2NetworkCopyPastePreservesLinks(t *testing.T) {
	e := newDNSE2E(t)
	graph := e.setupDns2NetworkInjectGraph("copy.test", "10.60.0.1", "copy")
	e.sendDNSRequest(graph.client)
	e.waitDNSResponse(graph.client, "NOERROR", "10.60.0.1")

	clip := e.Engine.Copy(E2EWorkspace, []uint64{graph.mock, graph.staticDNS, graph.inject, graph.adapter, graph.client})
	if clip == "" {
		t.Fatal("Copy returned empty clipboard")
	}
	pasted := e.Engine.Paste(E2EWorkspace, clip)
	if len(pasted) != 5 {
		t.Fatalf("Paste returned %d nodes (%v), want 5", len(pasted), pasted)
	}
	pastedClient := pastedNodeByClass(t, e.Engine, pasted, dnsnodes.NodeTypeDNSQuery)
	e.SubscribeMenus(pastedClient)
	e.sendDNSRequest(pastedClient)
	e.waitDNSResponse(pastedClient, "NOERROR", "10.60.0.1")
}

func TestDns2NetworkSaveRestorePreservesLinks(t *testing.T) {
	e := newDNSE2E(t)
	graph := e.setupDns2NetworkInjectGraph("restore.test", "10.70.0.1", "restore")
	e.sendDNSRequest(graph.client)
	e.waitDNSResponse(graph.client, "NOERROR", "10.70.0.1")

	cfg := configer.NewMemory(nil)
	if err := e.Engine.SaveConfig(cfg); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	restored := newDNSE2EFromConfig(t, cfg)
	client := nodeIDByName(t, restored.Engine, graph.clientName)
	restored.SubscribeMenus(client)
	restored.sendDNSRequest(client)
	restored.waitDNSResponse(client, "NOERROR", "10.70.0.1")
}

func TestDns2NetworkWorksWithDnsInject(t *testing.T) {
	e := newDNSE2E(t)
	graph := e.setupDns2NetworkInjectGraph("inject.test", "10.80.0.1", "inject")
	e.sendDNSRequest(graph.client)
	e.waitDNSResponse(graph.client, "NOERROR", "10.80.0.1")
	e.WaitNodeLabel(graph.inject, "injecting DNS")
	e.WaitNodeLabel(graph.adapter, "resolving DNS")
}

func newDns2NetworkTestWorkspace(t *testing.T) (*pasta.Workspace, *dns2NetworkLookupNetwork) {
	t.Helper()
	w := pasta.NewWorkspace(NewDebugLogFactory(1024))
	t.Cleanup(func() { w.Close() })
	lookup := newDns2NetworkLookupNetwork()
	for _, class := range []pasta.NodeClass{
		dnsnodes.DNS2NetworkClass{},
		dns2NetworkLookupNetworkClass{network: lookup},
	} {
		if err := w.AddNodeClass(class); err != nil {
			t.Fatalf("AddNodeClass %s: %v", class.ClassName(), err)
		}
	}
	return w, lookup
}

func assertDNSAnswer(t *testing.T, d dns.Interface, name string, qtype uint16, want string) {
	t.Helper()
	if !dnsnodes.DNSIsReady(d) {
		t.Fatal("DNS wrapper is not ready before query")
	}
	resp, err := dns.Query(context.Background(), d, dnsTestQuery(name, qtype))
	if err != nil {
		t.Fatalf("DNS query failed: %v", err)
	}
	if resp == nil || resp.RCode != dns.RCodeSuccess {
		t.Fatalf("DNS response = %#v, want success", resp)
	}
	if got := dnsnodes.FormatMessage(resp); !strings.Contains(got, want) {
		t.Fatalf("DNS response = %q, want to contain %q", got, want)
	}
}

func receiveReadyDNSPayload(t *testing.T, ch <-chan dnsnodes.DNSPayload) dnsnodes.DNSPayload {
	t.Helper()
	for {
		payload := receiveDNSPayload(t, ch)
		if dnsnodes.DNSIsReady(payload.DNS) {
			return payload
		}
	}
}

const dns2NetworkLookupNetworkNodeType = "test/Dns2NetworkLookupNetwork"

type dns2NetworkLookupNetworkClass struct {
	network *dns2NetworkLookupNetwork
}

func (dns2NetworkLookupNetworkClass) ClassName() string {
	return dns2NetworkLookupNetworkNodeType
}
func (dns2NetworkLookupNetworkClass) ShortDescription() string { return "lookup network" }
func (dns2NetworkLookupNetworkClass) LongDescription() string  { return "lookup network" }
func (dns2NetworkLookupNetworkClass) DefaultNodeParams() pasta.NodeClassParams {
	return pasta.NodeClassParams{PrimaryType: TypeNetwork, InitialPorts: []pasta.Port{
		NetworkPort("left", "Network"),
	}}
}
func (c dns2NetworkLookupNetworkClass) NewNode(configer.Config, ...*pasta.NodeClassState) (pasta.Node, error) {
	network := c.network
	if network == nil {
		network = newDns2NetworkLookupNetwork()
	}
	return &dns2NetworkLookupNetworkNode{network: network}, nil
}

type dns2NetworkLookupNetworkNode struct {
	simpleNetworkProvider
	network *dns2NetworkLookupNetwork
}

func (n *dns2NetworkLookupNetworkNode) OnInit(w *pasta.Workspace, _ pasta.Logger, id uint64, _ string, restored *pasta.NodeInitData, _, _, _, _ bool) error {
	if n.network == nil {
		n.network = newDns2NetworkLookupNetwork()
	}
	return n.InitSimpleNetworkProvider(w, nil, id, restored, n.network, "lookup")
}

type dns2NetworkLookupNetwork struct {
	*gonnect.RejectNetwork

	mu    sync.Mutex
	calls []string
}

func newDns2NetworkLookupNetwork() *dns2NetworkLookupNetwork {
	return &dns2NetworkLookupNetwork{RejectNetwork: &gonnect.RejectNetwork{}}
}

func (n *dns2NetworkLookupNetwork) LookupIP(_ context.Context, network, host string) ([]net.IP, error) {
	n.record("LookupIP", network, host)
	switch strings.TrimSuffix(host, ".") {
	case "example.test":
		if network == "ip6" {
			return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
		}
		return []net.IP{net.ParseIP("192.0.2.44").To4()}, nil
	default:
		return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
	}
}

func (n *dns2NetworkLookupNetwork) LookupHost(ctx context.Context, host string) ([]string, error) {
	ips, err := n.LookupIP(ctx, "ip", host)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(ips))
	for _, ip := range ips {
		out = append(out, ip.String())
	}
	return out, nil
}

func (n *dns2NetworkLookupNetwork) LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error) {
	ips, err := n.LookupIP(ctx, network, host)
	if err != nil {
		return nil, err
	}
	out := make([]netip.Addr, 0, len(ips))
	for _, ip := range ips {
		if addr, ok := netip.AddrFromSlice(ip); ok {
			out = append(out, addr)
		}
	}
	return out, nil
}

func (n *dns2NetworkLookupNetwork) LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error) {
	ips, err := n.LookupIP(ctx, "ip", host)
	if err != nil {
		return nil, err
	}
	out := make([]net.IPAddr, 0, len(ips))
	for _, ip := range ips {
		out = append(out, net.IPAddr{IP: ip})
	}
	return out, nil
}

func (n *dns2NetworkLookupNetwork) record(parts ...string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.calls = append(n.calls, strings.Join(parts, " "))
}

func (n *dns2NetworkLookupNetwork) assertCall(t *testing.T, want string) {
	t.Helper()
	n.mu.Lock()
	calls := append([]string(nil), n.calls...)
	n.mu.Unlock()
	for _, call := range calls {
		if call == want {
			return
		}
	}
	t.Fatalf("lookup calls = %v, want %q", calls, want)
}

type dns2NetworkInjectGraph struct {
	mock       uint64
	staticDNS  uint64
	inject     uint64
	adapter    uint64
	client     uint64
	clientName string
}

func (e *dnsE2E) setupDns2NetworkInjectGraph(host, ip, prefix string) dns2NetworkInjectGraph {
	e.T.Helper()
	graph := dns2NetworkInjectGraph{
		mock:       e.AddNode(NodeTypeMockNetwork, prefix+"-mock"),
		staticDNS:  e.AddNode(dnsnodes.NodeTypeStaticDNS, prefix+"-dns"),
		inject:     e.AddNode(dnsnodes.NodeTypeDnsInject, prefix+"-inject"),
		adapter:    e.AddNode(dnsnodes.NodeTypeDns2Network, prefix+"-adapter"),
		clientName: prefix + "-client",
	}
	graph.client = e.AddNode(dnsnodes.NodeTypeDNSQuery, graph.clientName)
	e.SubscribeMenus(graph.staticDNS, graph.client)
	e.applyStaticDNSRecords(graph.staticDNS, fmt.Sprintf("%s A %s", host, ip))
	e.setDNSQueryRequest(graph.client, host+".", "A")
	e.MustAddTypedLinkByPortName(TypeNetwork, graph.mock, "left", "Network", graph.inject, "right", "Network")
	e.MustAddTypedLinkByPortName(dnsnodes.TypeDNS, graph.staticDNS, "left", "DNS", graph.inject, "right", "DNS")
	e.MustAddTypedLinkByPortName(TypeNetwork, graph.inject, "left", "Network", graph.adapter, "right", "Network")
	e.MustAddTypedLinkByPortName(dnsnodes.TypeDNS, graph.adapter, "left", "DNS", graph.client, "right", "DNS")
	e.WaitNodeLabel(graph.inject, "injecting DNS")
	e.WaitNodeLabel(graph.adapter, "resolving DNS")
	return graph
}
