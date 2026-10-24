package dnsnodes

import (
	"context"
	"fmt"
	"github.com/asciimoth/almagest/lib/logging"
	"github.com/asciimoth/almagest/lib/nodekit"
	"github.com/asciimoth/almagest/lib/testkit"
	"net"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/formular"
	"github.com/asciimoth/gonnect"
	gdns "github.com/asciimoth/gonnect/dns"
	"github.com/asciimoth/pasta/pasta"
	mdns "github.com/miekg/dns"
)

const (
	meshNamesTestAuthority = "200:6fc8:9220:f400:5cc2:305a:4ac6:967e"
	meshNamesTestLabel     = "aiag7sesed2aaxgcgbnevruwpy"
	meshNamesTestMeshHost  = "svc." + meshNamesTestLabel + ".meshname"
	meshNamesTestMeship    = meshNamesTestLabel + ".meship"
	meshNamesTestPKYgg     = "svc.d40d4a7153cf288ea28f1865f6cfe951" +
		"43a478b5c8c9e7cb002a0633d10a53eb.pk.ygg"
	meshNamesTestYgg         = "svc.2aijksahfir2ni44cxylkze4b.ygg"
	meshNamesTestIPv6Literal = "2001-db8--42.ipv6-literal.net"
)

func TestMeshNamesPortsAndResolvesLocalNamesWithoutNetwork(t *testing.T) {
	w, _ := newMeshNamesTestWorkspace(t, nil)
	meshID, err := w.AddNodeByClass(NodeTypeMeshNames, "mesh")
	if err != nil {
		t.Fatalf("AddNodeByClass MeshNames: %v", err)
	}
	receiver := newDNS2NetworkReceiver(1)
	if _, err := w.AddNode(receiver, "test/DNSReceiver", "receiver"); err != nil {
		t.Fatalf("AddNode receiver: %v", err)
	}

	mesh := testkit.MustNodeSnapshot(t, w, meshID)
	if len(mesh.LeftPorts) != 1 || len(mesh.RightPorts) != 2 {
		t.Fatalf("MeshNames ports = left %v right %v, want one left and two right", mesh.LeftPorts, mesh.RightPorts)
	}
	assertMeshNamesPort(t, w, mesh.LeftPorts[0], "left", "DNS", TypeDNS)
	assertMeshNamesPort(t, w, mesh.RightPorts[0], "right", "Network", nodekit.TypeNetwork)
	assertMeshNamesPort(t, w, mesh.RightPorts[1], "right", "Fallback DNS", TypeDNS)

	if _, _, err := w.AddLink(mesh.LeftPorts[0], receiver.in); err != nil {
		t.Fatalf("AddLink mesh->receiver: %v", err)
	}
	payload := receiveReadyDNSPayload(t, receiver.events)
	for _, test := range []struct {
		name string
		want string
	}{
		{name: meshNamesTestMeship, want: meshNamesTestAuthority},
		{name: meshNamesTestPKYgg, want: "200:57e5:6b1d:5861:aee2:bae1:cf34:1260"},
		{name: meshNamesTestIPv6Literal, want: "2001:db8::42"},
	} {
		assertDNSAnswer(t, payload.DNS, test.name+".", gdns.TypeAAAA, test.want)
	}
	assertDNSRCode(t, payload.DNS, meshNamesTestMeshHost+".", gdns.TypeAAAA, gdns.RCodeNameError)
	assertDNSRCode(t, payload.DNS, meshNamesTestYgg+".", gdns.TypeAAAA, gdns.RCodeNameError)
	assertNodeLabel(t, w, meshID, "resolving local MeshNames")
}

func TestMeshNamesSchemeTogglesUseFallback(t *testing.T) {
	for _, test := range []struct {
		name  string
		field string
		host  string
	}{
		{name: "meshname", field: MeshNamesDisableMeshnameField, host: meshNamesTestMeshHost},
		{name: "meship", field: MeshNamesDisableMeshipField, host: meshNamesTestMeship},
		{name: "pk ygg", field: MeshNamesDisablePKYggField, host: meshNamesTestPKYgg},
		{name: "ygg", field: MeshNamesDisableYggField, host: meshNamesTestYgg},
		{name: "IPv6 literal", field: MeshNamesDisableIPv6LiteralNetField, host: meshNamesTestIPv6Literal},
	} {
		t.Run(test.name, func(t *testing.T) {
			w, _ := newMeshNamesTestWorkspace(t, nil)
			meshID, err := w.AddNodeByClass(NodeTypeMeshNames, "mesh")
			if err != nil {
				t.Fatalf("AddNodeByClass MeshNames: %v", err)
			}
			fallbackID, err := w.AddNodeByClass(NodeTypeStaticDNS, "fallback")
			if err != nil {
				t.Fatalf("AddNodeByClass StaticDns: %v", err)
			}
			applyMeshNamesStaticDNSRecords(t, w, fallbackID, test.host+" A 192.0.2.123")

			receiver := newDNS2NetworkReceiver(4)
			if _, err := w.AddNode(receiver, "test/DNSReceiver", "receiver"); err != nil {
				t.Fatalf("AddNode receiver: %v", err)
			}
			mesh := testkit.MustNodeSnapshot(t, w, meshID)
			fallback := testkit.MustNodeSnapshot(t, w, fallbackID)
			if _, _, err := w.AddLink(fallback.LeftPorts[0], mesh.RightPorts[1]); err != nil {
				t.Fatalf("AddLink fallback->mesh: %v", err)
			}
			if _, _, err := w.AddLink(mesh.LeftPorts[0], receiver.in); err != nil {
				t.Fatalf("AddLink mesh->receiver: %v", err)
			}
			first := receiveReadyDNSPayload(t, receiver.events).DNS

			w.SendNodeFormularMsg(meshID, formular.FormApplyMessage{
				MessageBase: testkit.FormMessageBase(formular.MessageFormApply, meshID),
				BlockID:     MeshNamesConfigBlockID,
				Values:      map[string]any{test.field: true},
			})
			if DNSIsReady(first) {
				t.Fatal("old MeshNames provider remained ready after config change")
			}
			second := receiveReadyDNSPayload(t, receiver.events).DNS
			if SameDNSInstance(first, second) {
				t.Fatal("MeshNames reused provider after config change")
			}
			assertDNSAnswer(t, second, test.host+".", gdns.TypeA, "192.0.2.123")
			assertNodeLabel(t, w, meshID, "resolving local MeshNames with fallback")
		})
	}
}

func TestMeshNamesUsesNetworkForMeshAuthorityDNS(t *testing.T) {
	serverAddr := startMeshNamesAuthorityDNSServer(t, meshNamesTestMeshHost, "2001:db8::42")
	network := newMeshNamesTestNetwork()
	network.routes[meshNamesTestAuthority] = serverAddr
	w, _ := newMeshNamesTestWorkspace(t, network)

	meshID, networkID, receiver := addMeshNamesNetworkAndReceiver(t, w)
	mesh := testkit.MustNodeSnapshot(t, w, meshID)
	networkNode := testkit.MustNodeSnapshot(t, w, networkID)
	if _, _, err := w.AddLink(networkNode.LeftPorts[0], mesh.RightPorts[0]); err != nil {
		t.Fatalf("AddLink network->mesh: %v", err)
	}
	if _, _, err := w.AddLink(mesh.LeftPorts[0], receiver.in); err != nil {
		t.Fatalf("AddLink mesh->receiver: %v", err)
	}

	payload := receiveReadyDNSPayload(t, receiver.events)
	assertDNSAnswer(t, payload.DNS, meshNamesTestMeshHost+".", gdns.TypeAAAA, "2001:db8::42")
	network.assertDialHost(t, meshNamesTestAuthority)
	assertNodeLabel(t, w, meshID, "resolving MeshNames")
}

func TestMeshNamesDoesNotUseNetworkResolverWithoutFallbackLink(t *testing.T) {
	network := newMeshNamesTestNetwork()
	w, _ := newMeshNamesTestWorkspace(t, network)

	meshID, networkID, receiver := addMeshNamesNetworkAndReceiver(t, w)
	mesh := testkit.MustNodeSnapshot(t, w, meshID)
	networkNode := testkit.MustNodeSnapshot(t, w, networkID)
	if _, _, err := w.AddLink(networkNode.LeftPorts[0], mesh.RightPorts[0]); err != nil {
		t.Fatalf("AddLink network->mesh: %v", err)
	}
	if _, _, err := w.AddLink(mesh.LeftPorts[0], receiver.in); err != nil {
		t.Fatalf("AddLink mesh->receiver: %v", err)
	}

	payload := receiveReadyDNSPayload(t, receiver.events)
	resp := assertDNSRCode(t, payload.DNS, "ordinary.test.", gdns.TypeA, gdns.RCodeNameError)
	if len(resp.Answers) != 0 {
		t.Fatalf("ordinary response answers = %v, want none", resp.Answers)
	}
	network.assertNoResolverCalls(t)
	network.assertNoDialCalls(t)
}

func TestMeshNamesFallbackDNSIsExplicitAndRemovedOnDetach(t *testing.T) {
	w, _ := newMeshNamesTestWorkspace(t, newMeshNamesTestNetwork())

	meshID, networkID, receiver := addMeshNamesNetworkAndReceiver(t, w)
	staticID, err := w.AddNodeByClass(NodeTypeStaticDNS, "fallback")
	if err != nil {
		t.Fatalf("AddNodeByClass StaticDns: %v", err)
	}
	applyMeshNamesStaticDNSRecords(t, w, staticID, "ordinary.test A 192.0.2.91")

	mesh := testkit.MustNodeSnapshot(t, w, meshID)
	networkNode := testkit.MustNodeSnapshot(t, w, networkID)
	staticNode := testkit.MustNodeSnapshot(t, w, staticID)
	if _, _, err := w.AddLink(networkNode.LeftPorts[0], mesh.RightPorts[0]); err != nil {
		t.Fatalf("AddLink network->mesh: %v", err)
	}
	fallbackLink, _, err := w.AddLink(staticNode.LeftPorts[0], mesh.RightPorts[1])
	if err != nil {
		t.Fatalf("AddLink fallback->mesh: %v", err)
	}
	if _, _, err := w.AddLink(mesh.LeftPorts[0], receiver.in); err != nil {
		t.Fatalf("AddLink mesh->receiver: %v", err)
	}

	first := receiveReadyDNSPayload(t, receiver.events).DNS
	assertDNSAnswer(t, first, "ordinary.test.", gdns.TypeA, "192.0.2.91")
	assertNodeLabel(t, w, meshID, "resolving MeshNames with fallback")

	w.RemoveLink(fallbackLink)
	if DNSIsReady(first) {
		t.Fatal("MeshNames left stale DNS output ready after fallback detach")
	}
	second := receiveReadyDNSPayload(t, receiver.events).DNS
	assertDNSRCode(t, second, "ordinary.test.", gdns.TypeA, gdns.RCodeNameError)
	assertNodeLabel(t, w, meshID, "resolving MeshNames")
}

func TestMeshNamesLinksAttachDetachReattach(t *testing.T) {
	w, _ := newMeshNamesTestWorkspace(t, newMeshNamesTestNetwork())

	meshID, networkID, receiver := addMeshNamesNetworkAndReceiver(t, w)
	mesh := testkit.MustNodeSnapshot(t, w, meshID)
	networkNode := testkit.MustNodeSnapshot(t, w, networkID)
	networkLink, _, err := w.AddLink(networkNode.LeftPorts[0], mesh.RightPorts[0])
	if err != nil {
		t.Fatalf("AddLink network->mesh: %v", err)
	}
	outputLink, _, err := w.AddLink(mesh.LeftPorts[0], receiver.in)
	if err != nil {
		t.Fatalf("AddLink mesh->receiver: %v", err)
	}
	first := receiveReadyDNSPayload(t, receiver.events).DNS
	assertDNSAnswer(t, first, meshNamesTestMeship+".", gdns.TypeAAAA, meshNamesTestAuthority)

	w.RemoveLink(networkLink)
	if DNSIsReady(first) {
		t.Fatal("MeshNames DNS wrapper remained ready after network detach")
	}
	local := receiveReadyDNSPayload(t, receiver.events).DNS
	if SameDNSInstance(first, local) {
		t.Fatal("MeshNames reused network-backed wrapper after network detach")
	}
	assertDNSAnswer(t, local, meshNamesTestMeship+".", gdns.TypeAAAA, meshNamesTestAuthority)
	assertDNSRCode(t, local, meshNamesTestMeshHost+".", gdns.TypeAAAA, gdns.RCodeNameError)
	assertNodeLabel(t, w, meshID, "resolving local MeshNames")

	networkLink, _, err = w.AddLink(networkNode.LeftPorts[0], mesh.RightPorts[0])
	if err != nil {
		t.Fatalf("reattach network link: %v", err)
	}
	second := receiveReadyDNSPayload(t, receiver.events).DNS
	if SameDNSInstance(first, second) || SameDNSInstance(local, second) {
		t.Fatal("MeshNames reused DNS wrapper after network reattach")
	}
	assertDNSAnswer(t, second, meshNamesTestMeship+".", gdns.TypeAAAA, meshNamesTestAuthority)

	w.RemoveLink(outputLink)
	if DNSIsReady(second) {
		t.Fatal("MeshNames DNS wrapper remained ready after output link detach")
	}
	_ = networkLink
}

func newMeshNamesTestWorkspace(t *testing.T, network *meshNamesTestNetwork) (*pasta.Workspace, *meshNamesTestNetwork) {
	t.Helper()
	if network == nil {
		network = newMeshNamesTestNetwork()
	}
	w := pasta.NewWorkspace(logging.NewDebugLogFactory(1024))
	t.Cleanup(func() { w.Close() })
	for _, class := range []pasta.NodeClass{
		MeshNamesClass{},
		StaticDNSClass{},
		meshNamesTestNetworkClass{network: network},
	} {
		if err := w.AddNodeClass(class); err != nil {
			t.Fatalf("AddNodeClass %s: %v", class.ClassName(), err)
		}
	}
	return w, network
}

func addMeshNamesNetworkAndReceiver(t *testing.T, w *pasta.Workspace) (uint64, uint64, *dns2NetworkReceiver) {
	t.Helper()
	meshID, err := w.AddNodeByClass(NodeTypeMeshNames, "mesh")
	if err != nil {
		t.Fatalf("AddNodeByClass MeshNames: %v", err)
	}
	networkID, err := w.AddNodeByClass(meshNamesTestNetworkNodeType, "network")
	if err != nil {
		t.Fatalf("AddNodeByClass mesh test network: %v", err)
	}
	receiver := newDNS2NetworkReceiver(6)
	if _, err := w.AddNode(receiver, "test/DNSReceiver", "receiver"); err != nil {
		t.Fatalf("AddNode receiver: %v", err)
	}
	return meshID, networkID, receiver
}

func assertMeshNamesPort(t *testing.T, w *pasta.Workspace, port uint64, direction, name, typ string) {
	t.Helper()
	snapshot, ok := w.PortSnapshot(port)
	if !ok {
		t.Fatalf("PortSnapshot(%d) missing", port)
	}
	if snapshot.Direction != direction || snapshot.Name != name || len(snapshot.Types) != 1 || snapshot.Types[0] != typ {
		t.Fatalf("port = %#v, want %s %q %s", snapshot, direction, name, typ)
	}
}

func assertDNSRCode(t *testing.T, d gdns.Interface, name string, qtype uint16, want uint8) *gdns.Message {
	t.Helper()
	if !DNSIsReady(d) {
		t.Fatal("DNS wrapper is not ready before query")
	}
	resp, err := gdns.Query(context.Background(), d, dnsTestQuery(name, qtype))
	if err != nil {
		t.Fatalf("DNS query failed: %v", err)
	}
	if resp == nil {
		t.Fatal("DNS response = nil")
	}
	if resp.RCode != want {
		t.Fatalf("DNS response rcode = %s, want %s; response = %s", RCodeName(resp.RCode), RCodeName(want), FormatMessage(resp))
	}
	return resp
}

func applyMeshNamesStaticDNSRecords(t *testing.T, w *pasta.Workspace, node uint64, records string) {
	t.Helper()
	w.SendNodeFormularMsg(node, formular.FormApplyMessage{
		MessageBase: testkit.FormMessageBase(formular.MessageFormApply, node),
		BlockID:     "config",
		Values: map[string]any{
			"records": records,
			"ttl":     DefaultStaticDNSTTL,
		},
	})
}

func startMeshNamesAuthorityDNSServer(t *testing.T, host, answerIP string) string {
	t.Helper()
	mux := mdns.NewServeMux()
	mux.Handle(".", mdns.HandlerFunc(func(w mdns.ResponseWriter, req *mdns.Msg) {
		msg := new(mdns.Msg)
		msg.SetReply(req)
		for _, q := range req.Question {
			if q.Name == mdns.Fqdn(host) && q.Qtype == mdns.TypeAAAA {
				msg.Answer = append(msg.Answer, &mdns.AAAA{
					Hdr:  mdns.RR_Header{Name: q.Name, Rrtype: mdns.TypeAAAA, Class: mdns.ClassINET, Ttl: 60},
					AAAA: net.ParseIP(answerIP),
				})
			}
		}
		_ = w.WriteMsg(msg)
	}))
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	server := &mdns.Server{Listener: listener, Net: "tcp", Handler: mux}
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = server.ActivateAndServe()
	}()
	t.Cleanup(func() {
		_ = listener.Close()
		_ = server.Shutdown()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Errorf("DNS server shutdown timed out")
		}
	})
	return listener.Addr().String()
}

const meshNamesTestNetworkNodeType = "test/MeshNamesNetwork"

type meshNamesTestNetworkClass struct {
	network *meshNamesTestNetwork
}

func (meshNamesTestNetworkClass) ClassName() string        { return meshNamesTestNetworkNodeType }
func (meshNamesTestNetworkClass) ShortDescription() string { return "mesh test network" }
func (meshNamesTestNetworkClass) LongDescription() string  { return "mesh test network" }
func (meshNamesTestNetworkClass) DefaultNodeParams() pasta.NodeClassParams {
	return pasta.NodeClassParams{PrimaryType: nodekit.TypeNetwork, InitialPorts: []pasta.Port{
		nodekit.NetworkPort("left", "Network"),
	}}
}
func (c meshNamesTestNetworkClass) NewNode(configer.Config, ...*pasta.NodeClassState) (pasta.Node, error) {
	network := c.network
	if network == nil {
		network = newMeshNamesTestNetwork()
	}
	return &meshNamesTestNetworkNode{network: network}, nil
}

type meshNamesTestNetworkNode struct {
	nodekit.SimpleNetworkProvider
	network *meshNamesTestNetwork
}

func (n *meshNamesTestNetworkNode) OnInit(w *pasta.Workspace, logger pasta.Logger, id uint64, _ string, restored *pasta.NodeInitData, _, _, _, _ bool) error {
	if n.network == nil {
		n.network = newMeshNamesTestNetwork()
	}
	return n.InitSimpleNetworkProvider(w, logger, id, restored, n.network, "mesh test network")
}

type meshNamesTestNetwork struct {
	*gonnect.RejectNetwork

	mu            sync.Mutex
	routes        map[string]string
	dialHosts     []string
	resolverCalls []string
}

func newMeshNamesTestNetwork() *meshNamesTestNetwork {
	return &meshNamesTestNetwork{
		RejectNetwork: &gonnect.RejectNetwork{},
		routes:        map[string]string{},
	}
}

func (n *meshNamesTestNetwork) Dial(ctx context.Context, network, address string) (net.Conn, error) {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	n.recordDial(host)
	target, ok := n.route(host)
	if !ok {
		return nil, fmt.Errorf("no mesh test route for %s", host)
	}
	return (&net.Dialer{}).DialContext(ctx, network, target)
}

func (n *meshNamesTestNetwork) LookupIP(_ context.Context, network, host string) ([]net.IP, error) {
	n.recordResolver("LookupIP", network, host)
	return []net.IP{net.ParseIP("198.51.100.77").To4()}, nil
}

func (n *meshNamesTestNetwork) LookupHost(ctx context.Context, host string) ([]string, error) {
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

func (n *meshNamesTestNetwork) LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error) {
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

func (n *meshNamesTestNetwork) LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error) {
	ips, err := n.LookupIP(ctx, network, host)
	if err != nil {
		return nil, err
	}
	out := make([]netip.Addr, 0, len(ips))
	for _, ip := range ips {
		if addr, ok := netip.AddrFromSlice(ip); ok {
			out = append(out, addr.Unmap())
		}
	}
	return out, nil
}

func (n *meshNamesTestNetwork) LookupAddr(_ context.Context, addr string) ([]string, error) {
	n.recordResolver("LookupAddr", addr)
	return []string{"ordinary.test."}, nil
}

func (n *meshNamesTestNetwork) LookupCNAME(_ context.Context, host string) (string, error) {
	n.recordResolver("LookupCNAME", host)
	return "ordinary.test.", nil
}

func (n *meshNamesTestNetwork) LookupNS(_ context.Context, name string) ([]*net.NS, error) {
	n.recordResolver("LookupNS", name)
	return []*net.NS{{Host: "ns.ordinary.test."}}, nil
}

func (n *meshNamesTestNetwork) LookupMX(_ context.Context, name string) ([]*net.MX, error) {
	n.recordResolver("LookupMX", name)
	return []*net.MX{{Host: "mx.ordinary.test.", Pref: 10}}, nil
}

func (n *meshNamesTestNetwork) LookupSRV(_ context.Context, service, proto, name string) (string, []*net.SRV, error) {
	n.recordResolver("LookupSRV", service, proto, name)
	return "", []*net.SRV{{Target: "srv.ordinary.test.", Port: 443}}, nil
}

func (n *meshNamesTestNetwork) LookupTXT(_ context.Context, name string) ([]string, error) {
	n.recordResolver("LookupTXT", name)
	return []string{"ordinary"}, nil
}

func (n *meshNamesTestNetwork) route(host string) (string, bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	target, ok := n.routes[host]
	return target, ok
}

func (n *meshNamesTestNetwork) recordDial(host string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.dialHosts = append(n.dialHosts, host)
}

func (n *meshNamesTestNetwork) recordResolver(parts ...string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.resolverCalls = append(n.resolverCalls, strings.Join(parts, " "))
}

func (n *meshNamesTestNetwork) assertDialHost(t *testing.T, want string) {
	t.Helper()
	n.mu.Lock()
	hosts := append([]string(nil), n.dialHosts...)
	n.mu.Unlock()
	for _, host := range hosts {
		if host == want {
			return
		}
	}
	t.Fatalf("dial hosts = %v, want %q", hosts, want)
}

func (n *meshNamesTestNetwork) assertNoDialCalls(t *testing.T) {
	t.Helper()
	n.mu.Lock()
	hosts := append([]string(nil), n.dialHosts...)
	n.mu.Unlock()
	if len(hosts) != 0 {
		t.Fatalf("dial hosts = %v, want none", hosts)
	}
}

func (n *meshNamesTestNetwork) assertNoResolverCalls(t *testing.T) {
	t.Helper()
	n.mu.Lock()
	calls := append([]string(nil), n.resolverCalls...)
	n.mu.Unlock()
	if len(calls) != 0 {
		t.Fatalf("network resolver calls = %v, want none", calls)
	}
}
