package almagest

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/formular"
	"github.com/asciimoth/gonnect"
	"github.com/asciimoth/gonnect/dns"
	"github.com/asciimoth/pasta/pasta"
)

func TestDNSClientProvidesDNSOverNetwork(t *testing.T) {
	w, network := newDNSClientWorkspace(t)
	startDNSClientUDPServer(t, network, "127.0.0.1:5353", "transport.test A 192.0.2.55")

	clientID := addDNSClientNode(t, w, "client")
	networkID := addDNSClientNetworkNode(t, w, "network")
	receiver := addDNSClientReceiver(t, w, 2)
	applyDNSClientServersToWorkspace(t, w, clientID, "udp://127.0.0.1:5353")

	client := mustNodeSnapshot(t, w, clientID)
	networkNode := mustNodeSnapshot(t, w, networkID)
	if _, _, err := w.AddLink(networkNode.LeftPorts[0], client.RightPorts[0]); err != nil {
		t.Fatalf("AddLink network->client: %v", err)
	}
	if _, _, err := w.AddLink(client.LeftPorts[0], receiver.in); err != nil {
		t.Fatalf("AddLink client->receiver: %v", err)
	}

	payload := receiveReadyDNSPayload(t, receiver.events)
	assertDNSAnswer(t, payload.DNS, "transport.test.", dns.TypeA, "192.0.2.55")
	assertNodeLabel(t, w, clientID, "1 DNS server")
}

func TestDNSClientRebuildsOnNetworkAndBootstrapChanges(t *testing.T) {
	w, network := newDNSClientWorkspace(t)
	startDNSClientUDPServer(t, network, "127.0.0.1:5353", "rebuild.test A 192.0.2.56")

	clientID := addDNSClientNode(t, w, "client")
	networkID := addDNSClientNetworkNode(t, w, "network")
	bootstrapID, err := w.AddNodeByClass(NodeTypeStaticDNS, "bootstrap")
	if err != nil {
		t.Fatalf("AddNodeByClass StaticDns: %v", err)
	}
	receiver := addDNSClientReceiver(t, w, 6)
	applyDNSClientServersToWorkspace(t, w, clientID, "udp://127.0.0.1:5353")

	client := mustNodeSnapshot(t, w, clientID)
	networkNode := mustNodeSnapshot(t, w, networkID)
	bootstrap := mustNodeSnapshot(t, w, bootstrapID)
	networkLink, _, err := w.AddLink(networkNode.LeftPorts[0], client.RightPorts[0])
	if err != nil {
		t.Fatalf("AddLink network->client: %v", err)
	}
	if _, _, err := w.AddLink(client.LeftPorts[0], receiver.in); err != nil {
		t.Fatalf("AddLink client->receiver: %v", err)
	}
	first := receiveReadyDNSPayload(t, receiver.events).DNS
	assertDNSAnswer(t, first, "rebuild.test.", dns.TypeA, "192.0.2.56")

	bootstrapLink, _, err := w.AddLink(bootstrap.LeftPorts[0], client.RightPorts[1])
	if err != nil {
		t.Fatalf("AddLink bootstrap->client: %v", err)
	}
	second := receiveReadyDNSPayload(t, receiver.events).DNS
	if SameDNSInstance(first, second) {
		t.Fatal("DnsClient reused wrapper after bootstrap attach")
	}
	if DNSIsReady(first) {
		t.Fatal("first DNS wrapper remained ready after bootstrap attach")
	}
	assertDNSAnswer(t, second, "rebuild.test.", dns.TypeA, "192.0.2.56")

	w.RemoveLink(bootstrapLink)
	third := receiveReadyDNSPayload(t, receiver.events).DNS
	if SameDNSInstance(second, third) {
		t.Fatal("DnsClient reused wrapper after bootstrap detach")
	}
	if DNSIsReady(second) {
		t.Fatal("second DNS wrapper remained ready after bootstrap detach")
	}
	assertDNSAnswer(t, third, "rebuild.test.", dns.TypeA, "192.0.2.56")

	w.RemoveLink(networkLink)
	if DNSIsReady(third) {
		t.Fatal("third DNS wrapper remained ready after network detach")
	}
	assertNodeLabel(t, w, clientID, "waiting for network")

	networkLink, _, err = w.AddLink(networkNode.LeftPorts[0], client.RightPorts[0])
	if err != nil {
		t.Fatalf("reattach network link: %v", err)
	}
	fourth := receiveReadyDNSPayload(t, receiver.events).DNS
	if SameDNSInstance(third, fourth) {
		t.Fatal("DnsClient reused wrapper after network reattach")
	}
	assertDNSAnswer(t, fourth, "rebuild.test.", dns.TypeA, "192.0.2.56")
	_ = networkLink
}

func TestDNSClientUsesBootstrapForHostnameServer(t *testing.T) {
	w, network := newDNSClientWorkspace(t)
	startDNSClientUDPServer(t, network, "127.0.0.1:5354", "bootstrap-target.test A 192.0.2.57")

	clientID := addDNSClientNode(t, w, "client")
	networkID := addDNSClientNetworkNode(t, w, "network")
	bootstrapID, err := w.AddNodeByClass(NodeTypeStaticDNS, "bootstrap")
	if err != nil {
		t.Fatalf("AddNodeByClass StaticDns: %v", err)
	}
	receiver := addDNSClientReceiver(t, w, 4)
	applyDNSClientServersToWorkspace(t, w, clientID, "udp://dns-upstream.test:5354")
	applyStaticDNSRecordsToWorkspace(t, w, bootstrapID, "dns-upstream.test A 127.0.0.1")

	client := mustNodeSnapshot(t, w, clientID)
	networkNode := mustNodeSnapshot(t, w, networkID)
	bootstrap := mustNodeSnapshot(t, w, bootstrapID)
	if _, _, err := w.AddLink(networkNode.LeftPorts[0], client.RightPorts[0]); err != nil {
		t.Fatalf("AddLink network->client: %v", err)
	}
	if _, _, err := w.AddLink(bootstrap.LeftPorts[0], client.RightPorts[1]); err != nil {
		t.Fatalf("AddLink bootstrap->client: %v", err)
	}
	if _, _, err := w.AddLink(client.LeftPorts[0], receiver.in); err != nil {
		t.Fatalf("AddLink client->receiver: %v", err)
	}

	payload := receiveReadyDNSPayload(t, receiver.events)
	assertDNSAnswer(t, payload.DNS, "bootstrap-target.test.", dns.TypeA, "192.0.2.57")
}

func TestDNSClientMenuConfigNormalizesAndRebuilds(t *testing.T) {
	w, network := newDNSClientWorkspace(t)
	startDNSClientUDPServer(t, network, "127.0.0.1:5355", "first-config.test A 192.0.2.58")
	startDNSClientUDPServer(t, network, "127.0.0.1:5356", "second-config.test A 192.0.2.59")

	clientID := addDNSClientNode(t, w, "client")
	networkID := addDNSClientNetworkNode(t, w, "network")
	receiver := addDNSClientReceiver(t, w, 4)

	client := mustNodeSnapshot(t, w, clientID)
	networkNode := mustNodeSnapshot(t, w, networkID)
	if _, _, err := w.AddLink(networkNode.LeftPorts[0], client.RightPorts[0]); err != nil {
		t.Fatalf("AddLink network->client: %v", err)
	}
	if _, _, err := w.AddLink(client.LeftPorts[0], receiver.in); err != nil {
		t.Fatalf("AddLink client->receiver: %v", err)
	}
	assertNoDNSPayload(t, receiver.events, 100*time.Millisecond)
	assertNodeLabel(t, w, clientID, "no DNS servers")

	applyDNSClientServersToWorkspace(t, w, clientID, "127.0.0.1:5355")
	first := receiveReadyDNSPayload(t, receiver.events).DNS
	assertDNSAnswer(t, first, "first-config.test.", dns.TypeA, "192.0.2.58")
	savedConfig := configer.NewMemory(nil)
	if err := w.SaveConfig(savedConfig); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	savedServers, ok := configer.SliceAt(savedConfig.View(configer.Path{"client"}), configer.Path{"servers"})
	if !ok {
		t.Fatal("saved normalized servers missing")
	}
	assertStringSlice(t, configStringSlice(t, savedServers), []string{"udp://127.0.0.1:5355"})

	applyDNSClientServersToWorkspace(t, w, clientID, "udp://127.0.0.1:5355")
	assertNoDNSPayload(t, receiver.events, 100*time.Millisecond)
	if !DNSIsReady(first) {
		t.Fatal("equivalent config apply closed the current DNS wrapper")
	}

	applyDNSClientServersToWorkspace(t, w, clientID, "udp://127.0.0.1:5356")
	second := receiveReadyDNSPayload(t, receiver.events).DNS
	if SameDNSInstance(first, second) {
		t.Fatal("DnsClient reused wrapper after server list change")
	}
	if DNSIsReady(first) {
		t.Fatal("first DNS wrapper remained ready after server list change")
	}
	assertDNSAnswer(t, second, "second-config.test.", dns.TypeA, "192.0.2.59")
}

func TestDNSClientLiveConfigChangeClosesOldClient(t *testing.T) {
	tracker := installTrackedDNSClientFactory(t)
	w, network := newDNSClientWorkspace(t)
	startDNSClientUDPServer(t, network, "127.0.0.1:5358", "live-config.test A 192.0.2.60")
	startDNSClientUDPServer(t, network, "127.0.0.1:5359", "live-config.test A 192.0.2.61")

	clientID := addDNSClientNode(t, w, "client")
	networkID := addDNSClientNetworkNode(t, w, "network")
	receiver := addDNSClientReceiver(t, w, 8)

	client := mustNodeSnapshot(t, w, clientID)
	networkNode := mustNodeSnapshot(t, w, networkID)
	if _, _, err := w.AddLink(networkNode.LeftPorts[0], client.RightPorts[0]); err != nil {
		t.Fatalf("AddLink network->client: %v", err)
	}
	if _, _, err := w.AddLink(client.LeftPorts[0], receiver.in); err != nil {
		t.Fatalf("AddLink client->receiver: %v", err)
	}

	applyDNSClientServersToWorkspace(t, w, clientID, "udp://127.0.0.1:5358")
	first := receiveReadyDNSPayload(t, receiver.events).DNS
	assertDNSAnswer(t, first, "live-config.test.", dns.TypeA, "192.0.2.60")
	firstClient := tracker.client(t, 0)
	if firstClient.closeCount() != 0 {
		t.Fatalf("first owned client close count = %d, want 0 before replacement", firstClient.closeCount())
	}

	applyDNSClientServersToWorkspace(t, w, clientID, "udp://127.0.0.1:5359")
	second := receiveReadyDNSPayload(t, receiver.events).DNS
	if SameDNSInstance(first, second) {
		t.Fatal("DnsClient reused wrapper after live server change")
	}
	if DNSIsReady(first) {
		t.Fatal("old DNS wrapper remained ready after live server change")
	}
	if firstClient.closeCount() != 1 {
		t.Fatalf("first owned client close count = %d, want 1 after replacement", firstClient.closeCount())
	}
	assertClosedDNSQuery(t, first, "live-config.test.")
	assertDNSAnswer(t, second, "live-config.test.", dns.TypeA, "192.0.2.61")

	secondClient := tracker.client(t, 1)
	applyDNSClientServersToWorkspace(t, w, clientID, "udp://127.0.0.1:5359")
	assertNoDNSPayload(t, receiver.events, 100*time.Millisecond)
	if tracker.count() != 2 {
		t.Fatalf("tracked DNS client count = %d, want 2 after equivalent apply", tracker.count())
	}
	if secondClient.closeCount() != 0 {
		t.Fatalf("second owned client close count = %d, want 0 after equivalent apply", secondClient.closeCount())
	}

	applyDNSClientServersToWorkspace(t, w, clientID, "udp://127.0.0.1:5358")
	third := receiveReadyDNSPayload(t, receiver.events).DNS
	if SameDNSInstance(second, third) {
		t.Fatal("DnsClient reused wrapper after second live server change")
	}
	if DNSIsReady(second) {
		t.Fatal("second DNS wrapper remained ready after second live server change")
	}
	if secondClient.closeCount() != 1 {
		t.Fatalf("second owned client close count = %d, want 1 after second replacement", secondClient.closeCount())
	}
	assertClosedDNSQuery(t, second, "live-config.test.")
	assertDNSAnswer(t, third, "live-config.test.", dns.TypeA, "192.0.2.60")
}

func TestDNSClientSaveRestorePreservesServersAndLinks(t *testing.T) {
	cfg := configer.NewMemory(map[string]any{
		"nodes": map[string]any{
			"network": map[string]any{
				"Class": NodeTypeMockNetwork,
			},
			"client": map[string]any{
				"Class":   NodeTypeDNSClient,
				"Links":   []string{"Network -> [network] Network"},
				"servers": []string{"127.0.0.1:5357", "dot://dns.example.test:853"},
			},
		},
	})
	e := newDNSE2EFromConfig(t, cfg)
	client := nodeIDByName(t, e.Engine, "client")
	e.SubscribeMenus(client)
	e.WaitNodeLabel(client, "2 DNS servers")

	item := e.waitDNSClientServersItem(client)
	if item.Field == nil || len(item.Elements) != 2 {
		t.Fatalf("servers menu elements = %#v, want two values", item.Field)
	}
	assertStringSlice(t, dnsClientServerElementValues(t, item.Elements), []string{"udp://127.0.0.1:5357", "dot://dns.example.test:853"})

	out := configer.NewMemory(nil)
	if err := e.Engine.SaveConfig(out); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	saved, ok := configer.SliceAt(out.View(configer.Path{"nodes", "client"}), configer.Path{"servers"})
	if !ok {
		t.Fatal("saved servers missing")
	}
	assertStringSlice(t, configStringSlice(t, saved), []string{"udp://127.0.0.1:5357", "dot://dns.example.test:853"})
}

func newDNSClientWorkspace(t *testing.T) (*pasta.Workspace, *mockLoopbackNetwork) {
	t.Helper()
	w := pasta.NewWorkspace(NewDebugLogFactory(1024))
	t.Cleanup(func() { w.Close() })
	network := newMockLoopbackNetwork()
	t.Cleanup(func() { _ = network.Close() })
	for _, class := range []pasta.NodeClass{
		dnsClientClass{},
		staticDNSClass{},
		dnsClientNetworkClass{network: network},
	} {
		if err := w.AddNodeClass(class); err != nil {
			t.Fatalf("AddNodeClass %s: %v", class.ClassName(), err)
		}
	}
	return w, network
}

func startDNSClientUDPServer(t *testing.T, network *mockLoopbackNetwork, address, recordsText string) {
	t.Helper()
	records, err := parseStaticDNSRecords(recordsText, defaultStaticDNSTTL)
	if err != nil {
		t.Fatalf("parseStaticDNSRecords: %v", err)
	}
	upstream := newStaticDNSProvider(records)
	pc, err := network.ListenPacket(context.Background(), "udp", address)
	if err != nil {
		t.Fatalf("ListenPacket %s: %v", address, err)
	}
	server := dns.NewServer(pc, upstream, nil)
	t.Cleanup(func() {
		_ = server.Close()
		_ = upstream.Close()
	})
}

func addDNSClientNode(t *testing.T, w *pasta.Workspace, name string) uint64 {
	t.Helper()
	id, err := w.AddNodeByClass(NodeTypeDNSClient, name)
	if err != nil {
		t.Fatalf("AddNodeByClass DnsClient: %v", err)
	}
	return id
}

func addDNSClientNetworkNode(t *testing.T, w *pasta.Workspace, name string) uint64 {
	t.Helper()
	id, err := w.AddNodeByClass(dnsClientNetworkNodeType, name)
	if err != nil {
		t.Fatalf("AddNodeByClass dns transport network: %v", err)
	}
	return id
}

func addDNSClientReceiver(t *testing.T, w *pasta.Workspace, size int) *dnsReceiverNode {
	t.Helper()
	receiver := &dnsReceiverNode{events: make(chan DNSPayload, size)}
	if _, err := w.AddNode(receiver, "test/DNSReceiver", "receiver"); err != nil {
		t.Fatalf("AddNode receiver: %v", err)
	}
	return receiver
}

func applyDNSClientServersToWorkspace(t *testing.T, w *pasta.Workspace, node uint64, servers ...string) {
	t.Helper()
	w.SendNodeFormularMsg(node, formularApplyMessage(node, dnsClientConfigBlockID, map[string]any{
		dnsClientServersField: dnsClientServerValues(servers...),
	}))
}

func dnsClientServerValues(servers ...string) []formular.ArrayElementValue {
	out := make([]formular.ArrayElementValue, 0, len(servers))
	for i, server := range servers {
		out = append(out, formular.ArrayElementValue{
			ID:       fmt.Sprintf("server-%d", i),
			Template: "server",
			Values:   map[string]any{dnsClientServerField: server},
		})
	}
	return out
}

func (e *dnsE2E) waitDNSClientServersItem(node uint64) formular.Item {
	e.T.Helper()
	var item formular.Item
	Eventually(e.T, func() bool {
		var ok bool
		item, ok = e.MenuItem(node, dnsClientConfigBlockID, dnsClientServersField)
		return ok && item.Field != nil
	}, "DnsClient servers menu item")
	return item
}

func configStringSlice(t *testing.T, values []any) []string {
	t.Helper()
	out := make([]string, 0, len(values))
	for _, raw := range values {
		value, ok := configer.StringValue(raw)
		if !ok {
			t.Fatalf("saved server value %#v is not a string", raw)
		}
		out = append(out, value)
	}
	return out
}

func dnsClientServerElementValues(t *testing.T, elements []formular.ArrayElement) []string {
	t.Helper()
	out := make([]string, 0, len(elements))
	for _, element := range elements {
		var found bool
		for _, item := range element.Items {
			if item.ID != dnsClientServerField || item.Field == nil {
				continue
			}
			value, ok := formular.StringValue(item.Value)
			if !ok {
				t.Fatalf("server field value = %#v, want string", item.Value)
			}
			out = append(out, value)
			found = true
			break
		}
		if !found {
			t.Fatalf("server element %#v missing URL field", element)
		}
	}
	return out
}

func assertNoDNSPayload(t *testing.T, ch <-chan DNSPayload, d time.Duration) {
	t.Helper()
	select {
	case payload := <-ch:
		t.Fatalf("unexpected DNS payload: %#v", payload)
	case <-time.After(d):
	}
}

func assertClosedDNSQuery(t *testing.T, resolver dns.Interface, name string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := dns.Query(ctx, resolver, &dns.Message{
		ID: 1,
		Questions: []dns.Question{{
			Name:  name,
			Type:  dns.TypeA,
			Class: dns.ClassIN,
		}},
	})
	if err == nil {
		t.Fatal("query through closed DNS wrapper succeeded")
	}
}

type trackedDNSClientFactory struct {
	mu      sync.Mutex
	clients []*trackedDNSClient
}

type trackedDNSClient struct {
	dns.Interface
	closes atomic.Int32
}

func installTrackedDNSClientFactory(t *testing.T) *trackedDNSClientFactory {
	t.Helper()
	tracker := &trackedDNSClientFactory{}
	previous := dnsClientNewClient
	dnsClientNewClient = func(dial gonnect.Dial, bootstrap dns.Interface, spawner gonnect.Spawner, servers ...string) dns.Interface {
		client := &trackedDNSClient{
			Interface: dns.NewClient(dial, bootstrap, spawner, servers...),
		}
		tracker.mu.Lock()
		tracker.clients = append(tracker.clients, client)
		tracker.mu.Unlock()
		return client
	}
	t.Cleanup(func() {
		dnsClientNewClient = previous
	})
	return tracker
}

func (f *trackedDNSClientFactory) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.clients)
}

func (f *trackedDNSClientFactory) client(t *testing.T, index int) *trackedDNSClient {
	t.Helper()
	Eventually(t, func() bool {
		return f.count() > index
	}, fmt.Sprintf("tracked DNS client %d", index))
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.clients[index]
}

func (c *trackedDNSClient) Close() error {
	c.closes.Add(1)
	return c.Interface.Close()
}

func (c *trackedDNSClient) closeCount() int {
	return int(c.closes.Load())
}

const dnsClientNetworkNodeType = "test/DNSClientNetwork"

type dnsClientNetworkClass struct {
	network *mockLoopbackNetwork
}

func (dnsClientNetworkClass) ClassName() string {
	return dnsClientNetworkNodeType
}
func (dnsClientNetworkClass) ShortDescription() string { return "DNS transport network" }
func (dnsClientNetworkClass) LongDescription() string  { return "DNS transport network" }
func (dnsClientNetworkClass) DefaultNodeParams() pasta.NodeClassParams {
	return pasta.NodeClassParams{PrimaryType: TypeNetwork, InitialPorts: []pasta.Port{
		NetworkPort("left", "Network"),
	}}
}
func (c dnsClientNetworkClass) NewNode(configer.Config, ...*pasta.NodeClassState) (pasta.Node, error) {
	network := c.network
	if network == nil {
		network = newMockLoopbackNetwork()
	}
	return &dnsClientNetworkNode{network: network}, nil
}

type dnsClientNetworkNode struct {
	simpleNetworkProvider
	network *mockLoopbackNetwork
}

func (n *dnsClientNetworkNode) OnInit(w *pasta.Workspace, _ pasta.Logger, id uint64, _ string, restored *pasta.NodeInitData, _, _, _, _ bool) error {
	if n.network == nil {
		n.network = newMockLoopbackNetwork()
	}
	return n.InitSimpleNetworkProvider(w, nil, id, restored, n.network, "network")
}
