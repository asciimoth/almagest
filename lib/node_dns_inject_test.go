package almagest

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"testing"

	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/formular"
	"github.com/asciimoth/gonnect"
	"github.com/asciimoth/pasta/pasta"
)

var errDnsInjectCaptureNetwork = errors.New("dns inject capture network called")

func TestDnsInjectNodeRoutesLookupsAndPreResolvesDialThroughDNS(t *testing.T) {
	w, capture := newDnsInjectTestWorkspace(t)
	injectID, err := w.AddNodeByClass(NodeTypeDnsInject, "inject")
	if err != nil {
		t.Fatalf("AddNodeByClass DnsInject: %v", err)
	}
	backendID, err := w.AddNodeByClass(dnsInjectCaptureNetworkNodeType, "backend")
	if err != nil {
		t.Fatalf("AddNodeByClass capture network: %v", err)
	}
	dnsID, err := w.AddNodeByClass(NodeTypeStaticDNS, "dns")
	if err != nil {
		t.Fatalf("AddNodeByClass StaticDns: %v", err)
	}
	applyStaticDNSRecordsToWorkspace(t, w, dnsID, "example.test A 192.0.2.10")
	receiver := &outNetReceiver{events: make(chan NetworkPayload, 3)}
	if _, err := w.AddNode(receiver, "test/NetworkReceiver", "receiver"); err != nil {
		t.Fatalf("AddNode receiver: %v", err)
	}

	inject := mustNodeSnapshot(t, w, injectID)
	backend := mustNodeSnapshot(t, w, backendID)
	staticDNS := mustNodeSnapshot(t, w, dnsID)
	if _, _, err := w.AddLink(backend.LeftPorts[0], inject.RightPorts[0]); err != nil {
		t.Fatalf("AddLink backend->inject: %v", err)
	}
	if _, _, err := w.AddLink(staticDNS.LeftPorts[0], inject.RightPorts[1]); err != nil {
		t.Fatalf("AddLink dns->inject: %v", err)
	}
	if _, _, err := w.AddLink(inject.LeftPorts[0], receiver.in); err != nil {
		t.Fatalf("AddLink inject->receiver: %v", err)
	}

	payload := receiveReadyDnsInjectPayload(t, receiver.events)
	hosts, err := payload.Network.LookupHost(context.Background(), "example.test")
	if err != nil {
		t.Fatalf("LookupHost through injected network: %v", err)
	}
	assertStringSlice(t, hosts, []string{"192.0.2.10"})

	_, err = payload.Network.Dial(context.Background(), "tcp", "example.test:8080")
	if !errors.Is(err, errDnsInjectCaptureNetwork) {
		t.Fatalf("Dial error = %v, want capture error", err)
	}
	capture.assertArgs(t, "Dial", []string{"tcp", "192.0.2.10:8080"})
}

func TestDnsInjectLinksAttachDetachReattach(t *testing.T) {
	w, capture := newDnsInjectTestWorkspace(t)
	injectID, err := w.AddNodeByClass(NodeTypeDnsInject, "inject")
	if err != nil {
		t.Fatalf("AddNodeByClass DnsInject: %v", err)
	}
	backendID, err := w.AddNodeByClass(dnsInjectCaptureNetworkNodeType, "backend")
	if err != nil {
		t.Fatalf("AddNodeByClass capture network: %v", err)
	}
	dnsID, err := w.AddNodeByClass(NodeTypeStaticDNS, "dns")
	if err != nil {
		t.Fatalf("AddNodeByClass StaticDns: %v", err)
	}
	applyStaticDNSRecordsToWorkspace(t, w, dnsID, "reattach.test A 192.0.2.20")
	receiver := &outNetReceiver{events: make(chan NetworkPayload, 6)}
	if _, err := w.AddNode(receiver, "test/NetworkReceiver", "receiver"); err != nil {
		t.Fatalf("AddNode receiver: %v", err)
	}

	inject := mustNodeSnapshot(t, w, injectID)
	backend := mustNodeSnapshot(t, w, backendID)
	staticDNS := mustNodeSnapshot(t, w, dnsID)
	networkLink, _, err := w.AddLink(backend.LeftPorts[0], inject.RightPorts[0])
	if err != nil {
		t.Fatalf("AddLink backend->inject: %v", err)
	}
	dnsLink, _, err := w.AddLink(staticDNS.LeftPorts[0], inject.RightPorts[1])
	if err != nil {
		t.Fatalf("AddLink dns->inject: %v", err)
	}
	if _, _, err := w.AddLink(inject.LeftPorts[0], receiver.in); err != nil {
		t.Fatalf("AddLink inject->receiver: %v", err)
	}
	first := receiveReadyDnsInjectPayload(t, receiver.events).Network
	assertNetworkDialResolved(t, first, capture, "reattach.test:8081", "192.0.2.20:8081")

	w.RemoveLink(dnsLink)
	if NetworkIsReady(first) {
		t.Fatal("network wrapper remained ready after DNS link removal")
	}
	assertNodeLabel(t, w, injectID, "waiting for DNS")

	dnsLink, _, err = w.AddLink(staticDNS.LeftPorts[0], inject.RightPorts[1])
	if err != nil {
		t.Fatalf("reattach dns link: %v", err)
	}
	second := receiveReadyDnsInjectPayload(t, receiver.events).Network
	if SameNetworkInstance(first, second) {
		t.Fatal("DnsInject reused wrapper after DNS reattach")
	}
	assertNetworkDialResolved(t, second, capture, "reattach.test:8082", "192.0.2.20:8082")
	assertNodeLabel(t, w, injectID, "injecting DNS")

	w.RemoveLink(networkLink)
	if NetworkIsReady(second) {
		t.Fatal("network wrapper remained ready after backend link removal")
	}
	assertNodeLabel(t, w, injectID, "waiting for network")

	networkLink, _, err = w.AddLink(backend.LeftPorts[0], inject.RightPorts[0])
	if err != nil {
		t.Fatalf("reattach network link: %v", err)
	}
	third := receiveReadyDnsInjectPayload(t, receiver.events).Network
	if SameNetworkInstance(second, third) {
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
	pastedClient := pastedNodeByClass(t, e.Engine, pasted, NodeTypeHTTPClient)
	pastedServer := pastedNodeByClass(t, e.Engine, pasted, NodeTypeHTTPServer)
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
	client := nodeIDByName(t, restored.Engine, graph.clientName)
	server := nodeIDByName(t, restored.Engine, graph.serverName)
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
		dnsInjectClass{},
		staticDNSClass{},
		dnsInjectCaptureNetworkClass{network: capture},
	} {
		if err := w.AddNodeClass(class); err != nil {
			t.Fatalf("AddNodeClass %s: %v", class.ClassName(), err)
		}
	}
	return w, capture
}

func applyStaticDNSRecordsToWorkspace(t *testing.T, w *pasta.Workspace, node uint64, records string) {
	t.Helper()
	w.SendNodeFormularMsg(node, formularApplyMessage(node, "config", map[string]any{
		"records": records,
		"ttl":     defaultStaticDNSTTL,
	}))
}

func formularApplyMessage(node uint64, block string, values map[string]any) any {
	return formular.FormApplyMessage{
		MessageBase: FormMessageBase(formular.MessageFormApply, node),
		BlockID:     block,
		Values:      values,
	}
}

func assertNetworkDialResolved(t *testing.T, network NetworkCloser, capture *dnsInjectCaptureNetwork, address, want string) {
	t.Helper()
	if !NetworkIsReady(network) {
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

func receiveReadyDnsInjectPayload(t *testing.T, events <-chan NetworkPayload) NetworkPayload {
	t.Helper()
	for {
		payload := receiveOutNetPayload(t, events)
		if NetworkIsReady(payload.Network) {
			return payload
		}
	}
}

func assertNodeLabel(t *testing.T, w *pasta.Workspace, node uint64, want string) {
	t.Helper()
	snapshot := mustNodeSnapshot(t, w, node)
	if snapshot.Label != want {
		t.Fatalf("node label = %q, want %q", snapshot.Label, want)
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
	return pasta.NodeClassParams{PrimaryType: TypeNetwork, InitialPorts: []pasta.Port{
		NetworkPort("left", "Network"),
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
	simpleNetworkProvider
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
	assertStringSlice(t, got, want)
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
	engine, err := BuildEngine(context.Background(), cfg, logf, sys, nil, pool, true)
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
	return &httpE2E{NodeE2E: e}
}

func (e *httpE2E) setupDnsInjectHTTPGraph(host, ip string, port int, prefix string) dnsInjectHTTPGraph {
	e.T.Helper()
	graph := dnsInjectHTTPGraph{
		mock:       e.AddNode(NodeTypeMockNetwork, prefix+"-mock"),
		dns:        e.AddNode(NodeTypeStaticDNS, prefix+"-dns"),
		inject:     e.AddNode(NodeTypeDnsInject, prefix+"-inject"),
		port:       port,
		serverName: prefix + "-server",
		clientName: prefix + "-client",
	}
	graph.server = e.addHTTPServerOnHost(graph.serverName, host, port)
	graph.client = e.addHTTPClientToURL(graph.clientName, fmt.Sprintf("http://%s:%d/", host, port))
	e.SubscribeMenus(graph.dns, graph.server, graph.client)
	e.applyStaticDNSRecordsEngine(graph.dns, fmt.Sprintf("%s A %s", host, ip))
	e.waitStaticDNSRecordsEngine(graph.dns, fmt.Sprintf("%s A %s", host, ip))
	e.MustAddTypedLinkByPortName(TypeNetwork, graph.mock, "left", "Network", graph.inject, "right", "Network")
	e.MustAddTypedLinkByPortName(TypeDNS, graph.dns, "left", "DNS", graph.inject, "right", "DNS")
	e.MustAddTypedLinkByPortName(TypeNetwork, graph.inject, "left", "Network", graph.server, "right", "Network")
	e.MustAddTypedLinkByPortName(TypeNetwork, graph.inject, "left", "Network", graph.client, "right", "Network")
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
			"ttl":     defaultStaticDNSTTL,
		},
	})
}

func (e *httpE2E) waitStaticDNSRecordsEngine(node uint64, records string) {
	e.T.Helper()
	want := normalizeStaticDNSRecordsText(records)
	Eventually(e.T, func() bool {
		item, ok := e.MenuItem(node, "config", "records")
		return ok && item.Field != nil && item.Value == want
	}, "StaticDns records = %q", want)
}
