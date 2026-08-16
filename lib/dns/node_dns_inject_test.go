package dnsnodes_test

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"testing"

	dnsnodes "github.com/asciimoth/almagest/lib/dns"
	httpnodes "github.com/asciimoth/almagest/lib/http"
	"github.com/asciimoth/almagest/lib/links"
	networknodes "github.com/asciimoth/almagest/lib/network"
	"github.com/asciimoth/almagest/lib/nodekit"
	"github.com/asciimoth/almagest/lib/testkit"
	"github.com/asciimoth/almagest/lib/testkit/networktest"

	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/formular"
	"github.com/asciimoth/gonnect"
	"github.com/asciimoth/pasta/pasta"
)

var errDnsInjectCaptureNetwork = errors.New("dns inject capture network called")

func TestDnsInjectNodeRoutesLookupsAndPreResolvesDialThroughDNS(t *testing.T) {
	w, capture := newDnsInjectTestWorkspace(t)
	injectID, err := w.AddNodeByClass(dnsnodes.NodeTypeDnsInject, "inject")
	if err != nil {
		t.Fatalf("AddNodeByClass DnsInject: %v", err)
	}
	backendID, err := w.AddNodeByClass(dnsInjectCaptureNetworkNodeType, "backend")
	if err != nil {
		t.Fatalf("AddNodeByClass capture network: %v", err)
	}
	dnsID, err := w.AddNodeByClass(dnsnodes.NodeTypeStaticDNS, "dns")
	if err != nil {
		t.Fatalf("AddNodeByClass StaticDns: %v", err)
	}
	applyStaticDNSRecordsToWorkspace(t, w, dnsID, "example.test A 192.0.2.10")
	receiver := networktest.NewReceiver(3)
	if _, err := w.AddNode(receiver, "test/NetworkReceiver", "receiver"); err != nil {
		t.Fatalf("AddNode receiver: %v", err)
	}

	inject := testkit.MustNodeSnapshot(t, w, injectID)
	backend := testkit.MustNodeSnapshot(t, w, backendID)
	staticDNS := testkit.MustNodeSnapshot(t, w, dnsID)
	if _, _, err := w.AddLink(backend.LeftPorts[0], inject.RightPorts[0]); err != nil {
		t.Fatalf("AddLink backend->inject: %v", err)
	}
	if _, _, err := w.AddLink(staticDNS.LeftPorts[0], inject.RightPorts[1]); err != nil {
		t.Fatalf("AddLink dns->inject: %v", err)
	}
	if _, _, err := w.AddLink(inject.LeftPorts[0], receiver.In); err != nil {
		t.Fatalf("AddLink inject->receiver: %v", err)
	}

	payload := receiveReadyDnsInjectPayload(t, receiver.Events)
	hosts, err := payload.Network.LookupHost(context.Background(), "example.test")
	if err != nil {
		t.Fatalf("LookupHost through injected network: %v", err)
	}
	testkit.AssertStringSlice(t, hosts, []string{"192.0.2.10"})

	_, err = payload.Network.Dial(context.Background(), "tcp", "example.test:8080")
	if !errors.Is(err, errDnsInjectCaptureNetwork) {
		t.Fatalf("Dial error = %v, want capture error", err)
	}
	capture.assertArgs(t, "Dial", []string{"tcp", "192.0.2.10:8080"})
}

func TestDnsInjectLinksAttachDetachReattach(t *testing.T) {
	w, capture := newDnsInjectTestWorkspace(t)
	injectID, err := w.AddNodeByClass(dnsnodes.NodeTypeDnsInject, "inject")
	if err != nil {
		t.Fatalf("AddNodeByClass DnsInject: %v", err)
	}
	backendID, err := w.AddNodeByClass(dnsInjectCaptureNetworkNodeType, "backend")
	if err != nil {
		t.Fatalf("AddNodeByClass capture network: %v", err)
	}
	dnsID, err := w.AddNodeByClass(dnsnodes.NodeTypeStaticDNS, "dns")
	if err != nil {
		t.Fatalf("AddNodeByClass StaticDns: %v", err)
	}
	applyStaticDNSRecordsToWorkspace(t, w, dnsID, "reattach.test A 192.0.2.20")
	receiver := networktest.NewReceiver(6)
	if _, err := w.AddNode(receiver, "test/NetworkReceiver", "receiver"); err != nil {
		t.Fatalf("AddNode receiver: %v", err)
	}

	inject := testkit.MustNodeSnapshot(t, w, injectID)
	backend := testkit.MustNodeSnapshot(t, w, backendID)
	staticDNS := testkit.MustNodeSnapshot(t, w, dnsID)
	networkLink, _, err := w.AddLink(backend.LeftPorts[0], inject.RightPorts[0])
	if err != nil {
		t.Fatalf("AddLink backend->inject: %v", err)
	}
	dnsLink, _, err := w.AddLink(staticDNS.LeftPorts[0], inject.RightPorts[1])
	if err != nil {
		t.Fatalf("AddLink dns->inject: %v", err)
	}
	if _, _, err := w.AddLink(inject.LeftPorts[0], receiver.In); err != nil {
		t.Fatalf("AddLink inject->receiver: %v", err)
	}
	first := receiveReadyDnsInjectPayload(t, receiver.Events).Network
	assertNetworkDialResolved(t, first, capture, "reattach.test:8081", "192.0.2.20:8081")

	w.RemoveLink(dnsLink)
	if links.NetworkIsReady(first) {
		t.Fatal("network wrapper remained ready after DNS link removal")
	}
	assertNodeLabel(t, w, injectID, "waiting for DNS")

	dnsLink, _, err = w.AddLink(staticDNS.LeftPorts[0], inject.RightPorts[1])
	if err != nil {
		t.Fatalf("reattach dns link: %v", err)
	}
	second := receiveReadyDnsInjectPayload(t, receiver.Events).Network
	if links.SameNetworkInstance(first, second) {
		t.Fatal("DnsInject reused wrapper after DNS reattach")
	}
	assertNetworkDialResolved(t, second, capture, "reattach.test:8082", "192.0.2.20:8082")
	assertNodeLabel(t, w, injectID, "injecting DNS")

	w.RemoveLink(networkLink)
	if links.NetworkIsReady(second) {
		t.Fatal("network wrapper remained ready after backend link removal")
	}
	assertNodeLabel(t, w, injectID, "waiting for network")

	networkLink, _, err = w.AddLink(backend.LeftPorts[0], inject.RightPorts[0])
	if err != nil {
		t.Fatalf("reattach network link: %v", err)
	}
	third := receiveReadyDnsInjectPayload(t, receiver.Events).Network
	if links.SameNetworkInstance(second, third) {
		t.Fatal("DnsInject reused wrapper after backend reattach")
	}
	assertNetworkDialResolved(t, third, capture, "reattach.test:8083", "192.0.2.20:8083")
	assertNodeLabel(t, w, injectID, "injecting DNS")
	_ = dnsLink
	_ = networkLink
}

func TestDnsInjectCopyPastePreservesLinks(t *testing.T) {
	e := newDnsInjectHTTPE2E(t)
	graph := e.setupDnsInjectHTTPGraph("copy.test", "127.0.0.1", httpTestPort(), "copy")
	e.assertClientRequestWorks(graph.client)

	clip := e.Engine.Copy(E2EWorkspace, []uint64{graph.mock, graph.dns, graph.inject, graph.server, graph.client})
	if clip == "" {
		t.Fatal("Copy returned empty clipboard")
	}
	pasted := e.Engine.Paste(E2EWorkspace, clip)
	if len(pasted) != 5 {
		t.Fatalf("Paste returned %d nodes (%v), want 5", len(pasted), pasted)
	}
	pastedClient := testkit.PastedNodeByClass(t, e.Engine, E2EWorkspace, pasted, httpnodes.NodeTypeHTTPClient)
	pastedServer := testkit.PastedNodeByClass(t, e.Engine, E2EWorkspace, pasted, httpnodes.NodeTypeHTTPServer)
	e.SubscribeMenus(pastedServer, pastedClient)
	e.waitServerListening(pastedServer, fmt.Sprintf("%s:%d", "copy.test", graph.port))
	e.assertClientRequestWorks(pastedClient)
}

func TestDnsInjectSaveRestorePreservesLinks(t *testing.T) {
	e := newDnsInjectHTTPE2E(t)
	graph := e.setupDnsInjectHTTPGraph("restore.test", "127.0.0.1", httpTestPort(), "restore")
	e.assertClientRequestWorks(graph.client)

	cfg := configer.NewMemory(nil)
	if err := e.Engine.SaveConfig(cfg); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	restored := newDnsInjectHTTPE2EFromConfig(t, cfg)
	client := testkit.NodeIDByName(t, restored.Engine, E2EWorkspace, graph.clientName)
	server := testkit.NodeIDByName(t, restored.Engine, E2EWorkspace, graph.serverName)
	restored.SubscribeMenus(server, client)
	restored.waitServerListening(server, fmt.Sprintf("%s:%d", "restore.test", graph.port))
	restored.assertClientRequestWorks(client)
}

func TestDnsInjectHTTPClientServerUsesDNSOutput(t *testing.T) {
	e := newDnsInjectHTTPE2E(t)
	graph := e.setupDnsInjectHTTPGraph("http.test", "127.0.0.1", httpTestPort(), "http")
	e.assertClientRequestWorks(graph.client)
}

func newDnsInjectTestWorkspace(t *testing.T) (*pasta.Workspace, *dnsInjectCaptureNetwork) {
	t.Helper()
	w := pasta.NewWorkspace(NewDebugLogFactory(1024))
	t.Cleanup(func() { w.Close() })
	capture := newDnsInjectCaptureNetwork()
	for _, class := range []pasta.NodeClass{
		dnsnodes.DNSInjectClass{},
		dnsnodes.StaticDNSClass{},
		dnsInjectCaptureNetworkClass{network: capture},
	} {
		if err := w.AddNodeClass(class); err != nil {
			t.Fatalf("AddNodeClass %s: %v", class.ClassName(), err)
		}
	}
	return w, capture
}

func assertNetworkDialResolved(t *testing.T, network links.NetworkCloser, capture *dnsInjectCaptureNetwork, address, want string) {
	t.Helper()
	if !links.NetworkIsReady(network) {
		t.Fatal("network wrapper is not ready before Dial")
	}
	capture.reset()
	_, err := network.Dial(context.Background(), "tcp", address)
	if !errors.Is(err, errDnsInjectCaptureNetwork) {
		host, _, splitErr := net.SplitHostPort(address)
		var lookupErr error
		var hosts []string
		if splitErr == nil {
			hosts, lookupErr = network.LookupHost(context.Background(), host)
		}
		t.Fatalf("Dial error = %v, want capture error; LookupHost(%q) = %v, %v", err, host, hosts, lookupErr)
	}
	capture.assertArgs(t, "Dial", []string{"tcp", want})
}

func receiveReadyDnsInjectPayload(t *testing.T, events <-chan links.NetworkPayload) links.NetworkPayload {
	t.Helper()
	for {
		payload := networktest.ReceivePayload(t, events)
		if links.NetworkIsReady(payload.Network) {
			return payload
		}
	}
}

const dnsInjectCaptureNetworkNodeType = "test/DnsInjectCaptureNetwork"

type dnsInjectCaptureNetworkClass struct {
	network *dnsInjectCaptureNetwork
}

func (dnsInjectCaptureNetworkClass) ClassName() string        { return dnsInjectCaptureNetworkNodeType }
func (dnsInjectCaptureNetworkClass) ShortDescription() string { return "capture network" }
func (dnsInjectCaptureNetworkClass) LongDescription() string  { return "capture network" }
func (dnsInjectCaptureNetworkClass) DefaultNodeParams() pasta.NodeClassParams {
	return pasta.NodeClassParams{PrimaryType: links.TypeNetwork, InitialPorts: []pasta.Port{
		links.NetworkPort("left", "Network"),
	}}
}
func (c dnsInjectCaptureNetworkClass) NewNode(configer.Config, ...*pasta.NodeClassState) (pasta.Node, error) {
	network := c.network
	if network == nil {
		network = newDnsInjectCaptureNetwork()
	}
	return &dnsInjectCaptureNetworkNode{network: network}, nil
}

type dnsInjectCaptureNetworkNode struct {
	nodekit.SimpleNetworkProvider
	network *dnsInjectCaptureNetwork
}

func (n *dnsInjectCaptureNetworkNode) OnInit(w *pasta.Workspace, _ pasta.Logger, id uint64, _ string, restored *pasta.NodeInitData, _, _, _, _ bool) error {
	if n.network == nil {
		n.network = newDnsInjectCaptureNetwork()
	}
	return n.InitSimpleNetworkProvider(w, nil, id, restored, n.network, "capture")
}

type dnsInjectCaptureNetwork struct {
	*gonnect.RejectNetwork
	mu    sync.Mutex
	calls map[string][]string
}

func newDnsInjectCaptureNetwork() *dnsInjectCaptureNetwork {
	return &dnsInjectCaptureNetwork{
		RejectNetwork: &gonnect.RejectNetwork{},
		calls:         make(map[string][]string),
	}
}

func (n *dnsInjectCaptureNetwork) Dial(_ context.Context, network, address string) (net.Conn, error) {
	n.record("Dial", network, address)
	return nil, errDnsInjectCaptureNetwork
}

func (n *dnsInjectCaptureNetwork) record(name string, args ...string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.calls[name] = append([]string(nil), args...)
}

func (n *dnsInjectCaptureNetwork) reset() {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.calls = make(map[string][]string)
}

func (n *dnsInjectCaptureNetwork) assertArgs(t *testing.T, name string, want []string) {
	t.Helper()
	n.mu.Lock()
	got := append([]string(nil), n.calls[name]...)
	n.mu.Unlock()
	testkit.AssertStringSlice(t, got, want)
}

type dnsInjectHTTPGraph struct {
	mock       uint64
	dns        uint64
	inject     uint64
	server     uint64
	client     uint64
	port       int
	serverName string
	clientName string
}

func newDnsInjectHTTPE2E(t *testing.T) *httpE2E {
	t.Helper()
	return newHTTPE2E(t)
}

func newDnsInjectHTTPE2EFromConfig(t *testing.T, cfg configer.Config) *httpE2E {
	t.Helper()
	pool := newE2ETestDebugPool(t)
	logf := NewDebugLogFactory(16384)
	sys := newE2EDebugSystem()
	engine, err := BuildEngine(context.Background(), cfg, logf, sys, nil, nil, nil, nil, nil, pool, true)
	if err != nil {
		t.Fatalf("BuildEngine restore: %v", err)
	}
	t.Cleanup(func() {
		if err := engine.Close(); err != nil {
			t.Errorf("engine.Close error = %v, want nil", err)
		}
	})
	e := NewNodeE2EFromEngine(t, engine, logf, sys)
	return newHTTPE2EFromNodeE2E(e)
}

func (e *httpE2E) setupDnsInjectHTTPGraph(host, ip string, port int, prefix string) dnsInjectHTTPGraph {
	e.T.Helper()
	graph := dnsInjectHTTPGraph{
		mock:       e.AddNode(networknodes.NodeTypeMockNetwork, prefix+"-mock"),
		dns:        e.AddNode(dnsnodes.NodeTypeStaticDNS, prefix+"-dns"),
		inject:     e.AddNode(dnsnodes.NodeTypeDnsInject, prefix+"-inject"),
		port:       port,
		serverName: prefix + "-server",
		clientName: prefix + "-client",
	}
	graph.server = e.addHTTPServerOnHost(graph.serverName, host, port)
	graph.client = e.addHTTPClientToURL(graph.clientName, fmt.Sprintf("http://%s:%d/", host, port))
	e.SubscribeMenus(graph.dns, graph.server, graph.client)
	e.applyStaticDNSRecordsEngine(graph.dns, fmt.Sprintf("%s A %s", host, ip))
	e.waitStaticDNSRecordsEngine(graph.dns, fmt.Sprintf("%s A %s", host, ip))
	e.MustAddTypedLinkByPortName(links.TypeNetwork, graph.mock, "left", "Network", graph.inject, "right", "Network")
	e.MustAddTypedLinkByPortName(dnsnodes.TypeDNS, graph.dns, "left", "DNS", graph.inject, "right", "DNS")
	e.MustAddTypedLinkByPortName(links.TypeNetwork, graph.inject, "left", "Network", graph.server, "right", "Network")
	e.MustAddTypedLinkByPortName(links.TypeNetwork, graph.inject, "left", "Network", graph.client, "right", "Network")
	e.WaitNodeLabel(graph.inject, "injecting DNS")
	e.waitServerListening(graph.server, fmt.Sprintf("%s:%d", host, port))
	e.waitClientRequestField(graph.client, "url", fmt.Sprintf("http://%s:%d/", host, port))
	return graph
}

func (e *httpE2E) applyStaticDNSRecordsEngine(node uint64, records string) {
	e.T.Helper()
	e.Engine.SendNodeFormularMsg(E2EWorkspace, node, formular.FormApplyMessage{
		MessageBase: FormMessageBase(formular.MessageFormApply, node),
		BlockID:     "config",
		Values: map[string]any{
			"records": records,
			"ttl":     dnsnodes.DefaultStaticDNSTTL,
		},
	})
}

func (e *httpE2E) waitStaticDNSRecordsEngine(node uint64, records string) {
	e.T.Helper()
	want := dnsnodes.NormalizeStaticRecordsText(records)
	Eventually(e.T, func() bool {
		item, ok := e.MenuItem(node, "config", "records")
		return ok && item.Field != nil && item.Value == want
	}, "StaticDns records = %q", want)
}
