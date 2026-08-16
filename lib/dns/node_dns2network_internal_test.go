package dnsnodes

import (
	"context"
	"net"
	"net/netip"
	"strings"
	"sync"
	"testing"

	"github.com/asciimoth/almagest/lib/logging"
	"github.com/asciimoth/almagest/lib/nodekit"
	"github.com/asciimoth/almagest/lib/testkit"
	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/gonnect"
	"github.com/asciimoth/gonnect/dns"
	"github.com/asciimoth/pasta/pasta"
)

func TestDns2NetworkProvidesDNSFromNetworkLookup(t *testing.T) {
	w, lookup := newDns2NetworkTestWorkspace(t)
	adapterID, err := w.AddNodeByClass(NodeTypeDns2Network, "adapter")
	if err != nil {
		t.Fatalf("AddNodeByClass Dns2Network: %v", err)
	}
	networkID, err := w.AddNodeByClass(dns2NetworkLookupNetworkNodeType, "network")
	if err != nil {
		t.Fatalf("AddNodeByClass lookup network: %v", err)
	}
	receiver := newDNS2NetworkReceiver(2)
	if _, err := w.AddNode(receiver, "test/DNSReceiver", "receiver"); err != nil {
		t.Fatalf("AddNode receiver: %v", err)
	}

	adapter := testkit.MustNodeSnapshot(t, w, adapterID)
	network := testkit.MustNodeSnapshot(t, w, networkID)
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
	adapterID, err := w.AddNodeByClass(NodeTypeDns2Network, "adapter")
	if err != nil {
		t.Fatalf("AddNodeByClass Dns2Network: %v", err)
	}
	networkID, err := w.AddNodeByClass(dns2NetworkLookupNetworkNodeType, "network")
	if err != nil {
		t.Fatalf("AddNodeByClass lookup network: %v", err)
	}
	receiver := newDNS2NetworkReceiver(4)
	if _, err := w.AddNode(receiver, "test/DNSReceiver", "receiver"); err != nil {
		t.Fatalf("AddNode receiver: %v", err)
	}

	adapter := testkit.MustNodeSnapshot(t, w, adapterID)
	network := testkit.MustNodeSnapshot(t, w, networkID)
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
	if DNSIsReady(first) {
		t.Fatal("DNS wrapper remained ready after network link removal")
	}
	assertNodeLabel(t, w, adapterID, "waiting for network")

	networkLink, _, err = w.AddLink(network.LeftPorts[0], adapter.RightPorts[0])
	if err != nil {
		t.Fatalf("reattach network link: %v", err)
	}
	second := receiveReadyDNSPayload(t, receiver.events).DNS
	if SameDNSInstance(first, second) {
		t.Fatal("Dns2Network reused DNS wrapper after network reattach")
	}
	assertDNSAnswer(t, second, "example.test.", dns.TypeA, "192.0.2.44")
	assertNodeLabel(t, w, adapterID, "resolving DNS")

	w.RemoveLink(outputLink)
	if DNSIsReady(second) {
		t.Fatal("DNS wrapper remained ready after DNS link removal")
	}

	outputLink, _, err = w.AddLink(adapter.LeftPorts[0], receiver.in)
	if err != nil {
		t.Fatalf("reattach DNS output link: %v", err)
	}
	third := receiveReadyDNSPayload(t, receiver.events).DNS
	if SameDNSInstance(second, third) {
		t.Fatal("Dns2Network reused DNS wrapper after DNS output reattach")
	}
	assertDNSAnswer(t, third, "example.test.", dns.TypeA, "192.0.2.44")
	_ = outputLink
	_ = networkLink
}

func newDns2NetworkTestWorkspace(t *testing.T) (*pasta.Workspace, *dns2NetworkLookupNetwork) {
	t.Helper()
	w := pasta.NewWorkspace(logging.NewDebugLogFactory(1024))
	t.Cleanup(func() { w.Close() })
	lookup := newDns2NetworkLookupNetwork()
	for _, class := range []pasta.NodeClass{
		DNS2NetworkClass{},
		dns2NetworkLookupNetworkClass{network: lookup},
	} {
		if err := w.AddNodeClass(class); err != nil {
			t.Fatalf("AddNodeClass %s: %v", class.ClassName(), err)
		}
	}
	return w, lookup
}

func assertNodeLabel(t *testing.T, w *pasta.Workspace, node uint64, want string) {
	t.Helper()
	snapshot := testkit.MustNodeSnapshot(t, w, node)
	if snapshot.Label != want {
		t.Fatalf("node label = %q, want %q", snapshot.Label, want)
	}
}

type dns2NetworkReceiver struct {
	pasta.BasicNode
	in     uint64
	events chan DNSPayload
}

func newDNS2NetworkReceiver(size int) *dns2NetworkReceiver {
	return &dns2NetworkReceiver{events: make(chan DNSPayload, size)}
}

func (n *dns2NetworkReceiver) OnInit(w *pasta.Workspace, _ pasta.Logger, id uint64, _ string, _ *pasta.NodeInitData, _, _, _, _ bool) error {
	p := DNSPort("right", "DNS")
	p.Node = id
	port, err := w.AddPortLocked(p)
	if err != nil {
		return err
	}
	n.in = port
	return nil
}

func (n *dns2NetworkReceiver) PreLinkAdd(port uint64, linkType, portDirection string) error {
	if port != n.in || portDirection != "right" || linkType != TypeDNS {
		return pasta.LinkTypeErr(linkType)
	}
	return nil
}

func (n *dns2NetworkReceiver) OnEvent(event pasta.Event, linkType string, _ []string, receiverPortDirection string) error {
	if event.ReceiverPort != n.in || receiverPortDirection != "right" || linkType != TypeDNS {
		return nil
	}
	payload, ok := event.Payload.(DNSPayload)
	if ok {
		n.events <- payload
	}
	return nil
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
	return pasta.NodeClassParams{PrimaryType: nodekit.TypeNetwork, InitialPorts: []pasta.Port{
		nodekit.NetworkPort("left", "Network"),
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
	nodekit.SimpleNetworkProvider
	network *dns2NetworkLookupNetwork
}

func (n *dns2NetworkLookupNetworkNode) OnInit(w *pasta.Workspace, logger pasta.Logger, id uint64, _ string, restored *pasta.NodeInitData, _, _, _, _ bool) error {
	if n.network == nil {
		n.network = newDns2NetworkLookupNetwork()
	}
	return n.InitSimpleNetworkProvider(w, logger, id, restored, n.network, "lookup")
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
