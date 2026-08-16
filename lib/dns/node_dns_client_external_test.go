package dnsnodes_test

import (
	"context"
	"crypto/tls"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	dnsnodes "github.com/asciimoth/almagest/lib/dns"
	"github.com/asciimoth/almagest/lib/links"
	"github.com/asciimoth/almagest/lib/logging"
	"github.com/asciimoth/almagest/lib/network"
	"github.com/asciimoth/almagest/lib/nodekit"
	"github.com/asciimoth/almagest/lib/storage"
	"github.com/asciimoth/almagest/lib/testkit"
	"github.com/asciimoth/almagest/lib/testkit/dnstest"
	"github.com/asciimoth/almagest/lib/testkit/e2etest"

	"github.com/asciimoth/bufpool"
	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/formular"
	"github.com/asciimoth/gonnect"
	"github.com/asciimoth/gonnect/dns"
	"github.com/asciimoth/gonnect/sysnet"
	"github.com/asciimoth/pasta/pasta"
)

func TestDNSClientProvidesDNSOverNetwork(t *testing.T) {
	w, network := newDNSClientWorkspace(t)
	startDNSClientUDPServer(t, network, "127.0.0.1:5353", "transport.test A 192.0.2.55")

	clientID := addDNSClientNode(t, w, "client")
	networkID := addDNSClientNetworkNode(t, w, "network")
	receiver := addDNSClientReceiver(t, w, 2)
	applyDNSClientServersToWorkspace(t, w, clientID, "udp://127.0.0.1:5353")

	client := testkit.MustNodeSnapshot(t, w, clientID)
	networkNode := testkit.MustNodeSnapshot(t, w, networkID)
	if _, _, err := w.AddLink(networkNode.LeftPorts[0], client.RightPorts[0]); err != nil {
		t.Fatalf("AddLink network->client: %v", err)
	}
	if _, _, err := w.AddLink(client.LeftPorts[0], receiver.In); err != nil {
		t.Fatalf("AddLink client->receiver: %v", err)
	}

	payload := receiveReadyDNSPayload(t, receiver.Events)
	assertDNSAnswer(t, payload.DNS, "transport.test.", dns.TypeA, "192.0.2.55")
	assertNodeLabel(t, w, clientID, "1 DNS server")
}

func TestDNSClientRebuildsOnNetworkAndBootstrapChanges(t *testing.T) {
	w, network := newDNSClientWorkspace(t)
	startDNSClientUDPServer(t, network, "127.0.0.1:5353", "rebuild.test A 192.0.2.56")

	clientID := addDNSClientNode(t, w, "client")
	networkID := addDNSClientNetworkNode(t, w, "network")
	bootstrapID, err := w.AddNodeByClass(dnsnodes.NodeTypeStaticDNS, "bootstrap")
	if err != nil {
		t.Fatalf("AddNodeByClass StaticDns: %v", err)
	}
	receiver := addDNSClientReceiver(t, w, 6)
	applyDNSClientServersToWorkspace(t, w, clientID, "udp://127.0.0.1:5353")

	client := testkit.MustNodeSnapshot(t, w, clientID)
	networkNode := testkit.MustNodeSnapshot(t, w, networkID)
	bootstrap := testkit.MustNodeSnapshot(t, w, bootstrapID)
	networkLink, _, err := w.AddLink(networkNode.LeftPorts[0], client.RightPorts[0])
	if err != nil {
		t.Fatalf("AddLink network->client: %v", err)
	}
	if _, _, err := w.AddLink(client.LeftPorts[0], receiver.In); err != nil {
		t.Fatalf("AddLink client->receiver: %v", err)
	}
	first := receiveReadyDNSPayload(t, receiver.Events).DNS
	assertDNSAnswer(t, first, "rebuild.test.", dns.TypeA, "192.0.2.56")

	bootstrapLink, _, err := w.AddLink(bootstrap.LeftPorts[0], client.RightPorts[1])
	if err != nil {
		t.Fatalf("AddLink bootstrap->client: %v", err)
	}
	second := receiveReadyDNSPayload(t, receiver.Events).DNS
	if dnsnodes.SameDNSInstance(first, second) {
		t.Fatal("DnsClient reused wrapper after bootstrap attach")
	}
	if dnsnodes.DNSIsReady(first) {
		t.Fatal("first DNS wrapper remained ready after bootstrap attach")
	}
	assertDNSAnswer(t, second, "rebuild.test.", dns.TypeA, "192.0.2.56")

	w.RemoveLink(bootstrapLink)
	third := receiveReadyDNSPayload(t, receiver.Events).DNS
	if dnsnodes.SameDNSInstance(second, third) {
		t.Fatal("DnsClient reused wrapper after bootstrap detach")
	}
	if dnsnodes.DNSIsReady(second) {
		t.Fatal("second DNS wrapper remained ready after bootstrap detach")
	}
	assertDNSAnswer(t, third, "rebuild.test.", dns.TypeA, "192.0.2.56")

	w.RemoveLink(networkLink)
	if dnsnodes.DNSIsReady(third) {
		t.Fatal("third DNS wrapper remained ready after network detach")
	}
	assertNodeLabel(t, w, clientID, "waiting for network")

	networkLink, _, err = w.AddLink(networkNode.LeftPorts[0], client.RightPorts[0])
	if err != nil {
		t.Fatalf("reattach network link: %v", err)
	}
	fourth := receiveReadyDNSPayload(t, receiver.Events).DNS
	if dnsnodes.SameDNSInstance(third, fourth) {
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
	bootstrapID, err := w.AddNodeByClass(dnsnodes.NodeTypeStaticDNS, "bootstrap")
	if err != nil {
		t.Fatalf("AddNodeByClass StaticDns: %v", err)
	}
	receiver := addDNSClientReceiver(t, w, 4)
	applyDNSClientServersToWorkspace(t, w, clientID, "udp://dns-upstream.test:5354")
	applyStaticDNSRecordsToWorkspace(t, w, bootstrapID, "dns-upstream.test A 127.0.0.1")

	client := testkit.MustNodeSnapshot(t, w, clientID)
	networkNode := testkit.MustNodeSnapshot(t, w, networkID)
	bootstrap := testkit.MustNodeSnapshot(t, w, bootstrapID)
	if _, _, err := w.AddLink(networkNode.LeftPorts[0], client.RightPorts[0]); err != nil {
		t.Fatalf("AddLink network->client: %v", err)
	}
	if _, _, err := w.AddLink(bootstrap.LeftPorts[0], client.RightPorts[1]); err != nil {
		t.Fatalf("AddLink bootstrap->client: %v", err)
	}
	if _, _, err := w.AddLink(client.LeftPorts[0], receiver.In); err != nil {
		t.Fatalf("AddLink client->receiver: %v", err)
	}

	payload := receiveReadyDNSPayload(t, receiver.Events)
	assertDNSAnswer(t, payload.DNS, "bootstrap-target.test.", dns.TypeA, "192.0.2.57")
}

func TestDNSClientMenuConfigNormalizesAndRebuilds(t *testing.T) {
	w, network := newDNSClientWorkspace(t)
	startDNSClientUDPServer(t, network, "127.0.0.1:5355", "first-config.test A 192.0.2.58")
	startDNSClientUDPServer(t, network, "127.0.0.1:5356", "second-config.test A 192.0.2.59")

	clientID := addDNSClientNode(t, w, "client")
	networkID := addDNSClientNetworkNode(t, w, "network")
	receiver := addDNSClientReceiver(t, w, 4)

	client := testkit.MustNodeSnapshot(t, w, clientID)
	networkNode := testkit.MustNodeSnapshot(t, w, networkID)
	if _, _, err := w.AddLink(networkNode.LeftPorts[0], client.RightPorts[0]); err != nil {
		t.Fatalf("AddLink network->client: %v", err)
	}
	if _, _, err := w.AddLink(client.LeftPorts[0], receiver.In); err != nil {
		t.Fatalf("AddLink client->receiver: %v", err)
	}
	assertNoDNSPayload(t, receiver.Events, 100*time.Millisecond)
	assertNodeLabel(t, w, clientID, "no DNS servers")

	applyDNSClientServersToWorkspace(t, w, clientID, "127.0.0.1:5355")
	first := receiveReadyDNSPayload(t, receiver.Events).DNS
	assertDNSAnswer(t, first, "first-config.test.", dns.TypeA, "192.0.2.58")
	savedConfig := configer.NewMemory(nil)
	if err := w.SaveConfig(savedConfig); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	savedServers, ok := configer.SliceAt(savedConfig.View(configer.Path{"client"}), configer.Path{"servers"})
	if !ok {
		t.Fatal("saved normalized servers missing")
	}
	testkit.AssertStringSlice(t, configStringSlice(t, savedServers), []string{"udp://127.0.0.1:5355"})

	applyDNSClientServersToWorkspace(t, w, clientID, "udp://127.0.0.1:5355")
	assertNoDNSPayload(t, receiver.Events, 100*time.Millisecond)
	if !dnsnodes.DNSIsReady(first) {
		t.Fatal("equivalent config apply closed the current DNS wrapper")
	}

	applyDNSClientServersToWorkspace(t, w, clientID, "udp://127.0.0.1:5356")
	second := receiveReadyDNSPayload(t, receiver.Events).DNS
	if dnsnodes.SameDNSInstance(first, second) {
		t.Fatal("DnsClient reused wrapper after server list change")
	}
	if dnsnodes.DNSIsReady(first) {
		t.Fatal("first DNS wrapper remained ready after server list change")
	}
	assertDNSAnswer(t, second, "second-config.test.", dns.TypeA, "192.0.2.59")
}

func TestDNSClientLiveConfigChangeClosesOldClient(t *testing.T) {
	tracker := &trackedDNSClientFactory{}
	w, network := newDNSClientWorkspace(t, tracker.newClient)
	startDNSClientUDPServer(t, network, "127.0.0.1:5358", "live-config.test A 192.0.2.60")
	startDNSClientUDPServer(t, network, "127.0.0.1:5359", "live-config.test A 192.0.2.61")

	clientID := addDNSClientNode(t, w, "client")
	networkID := addDNSClientNetworkNode(t, w, "network")
	receiver := addDNSClientReceiver(t, w, 8)

	client := testkit.MustNodeSnapshot(t, w, clientID)
	networkNode := testkit.MustNodeSnapshot(t, w, networkID)
	if _, _, err := w.AddLink(networkNode.LeftPorts[0], client.RightPorts[0]); err != nil {
		t.Fatalf("AddLink network->client: %v", err)
	}
	if _, _, err := w.AddLink(client.LeftPorts[0], receiver.In); err != nil {
		t.Fatalf("AddLink client->receiver: %v", err)
	}

	applyDNSClientServersToWorkspace(t, w, clientID, "udp://127.0.0.1:5358")
	first := receiveReadyDNSPayload(t, receiver.Events).DNS
	assertDNSAnswer(t, first, "live-config.test.", dns.TypeA, "192.0.2.60")
	firstClient := tracker.client(t, 0)
	if firstClient.closeCount() != 0 {
		t.Fatalf("first owned client close count = %d, want 0 before replacement", firstClient.closeCount())
	}

	applyDNSClientServersToWorkspace(t, w, clientID, "udp://127.0.0.1:5359")
	second := receiveReadyDNSPayload(t, receiver.Events).DNS
	if dnsnodes.SameDNSInstance(first, second) {
		t.Fatal("DnsClient reused wrapper after live server change")
	}
	if dnsnodes.DNSIsReady(first) {
		t.Fatal("old DNS wrapper remained ready after live server change")
	}
	if firstClient.closeCount() != 1 {
		t.Fatalf("first owned client close count = %d, want 1 after replacement", firstClient.closeCount())
	}
	assertClosedDNSQuery(t, first, "live-config.test.")
	assertDNSAnswer(t, second, "live-config.test.", dns.TypeA, "192.0.2.61")

	secondClient := tracker.client(t, 1)
	applyDNSClientServersToWorkspace(t, w, clientID, "udp://127.0.0.1:5359")
	assertNoDNSPayload(t, receiver.Events, 100*time.Millisecond)
	if tracker.count() != 2 {
		t.Fatalf("tracked DNS client count = %d, want 2 after equivalent apply", tracker.count())
	}
	if secondClient.closeCount() != 0 {
		t.Fatalf("second owned client close count = %d, want 0 after equivalent apply", secondClient.closeCount())
	}

	applyDNSClientServersToWorkspace(t, w, clientID, "udp://127.0.0.1:5358")
	third := receiveReadyDNSPayload(t, receiver.Events).DNS
	if dnsnodes.SameDNSInstance(second, third) {
		t.Fatal("DnsClient reused wrapper after second live server change")
	}
	if dnsnodes.DNSIsReady(second) {
		t.Fatal("second DNS wrapper remained ready after second live server change")
	}
	if secondClient.closeCount() != 1 {
		t.Fatalf("second owned client close count = %d, want 1 after second replacement", secondClient.closeCount())
	}
	assertClosedDNSQuery(t, second, "live-config.test.")
	assertDNSAnswer(t, third, "live-config.test.", dns.TypeA, "192.0.2.60")
}

func TestDNSClientUsesSelectedTLSConfigAndHandlesRename(t *testing.T) {
	tracker := &trackedDNSClientFactory{}
	tlsStorage := testkit.NewTLSConfigStorage(map[string]string{
		nodekit.DefaultTLSConfigName: "default.test",
		"custom":                     "custom.test",
	})
	w := pasta.NewWorkspace(logging.NewDebugLogFactory(1024))
	t.Cleanup(func() { w.Close() })
	network := networknodes.NewMockLoopbackNetwork(true)
	t.Cleanup(func() { _ = network.Close() })
	for _, class := range []pasta.NodeClass{
		dnsnodes.DNSClientClass{Runtime: dnsClientTestRuntime{tlsConfigs: tlsStorage}, NewClient: tracker.newClient},
		dnsClientNetworkClass{network: network},
	} {
		if err := w.AddNodeClass(class); err != nil {
			t.Fatalf("AddNodeClass %s: %v", class.ClassName(), err)
		}
	}

	clientID := addDNSClientNode(t, w, "client")
	networkID := addDNSClientNetworkNode(t, w, "network")
	client := testkit.MustNodeSnapshot(t, w, clientID)
	networkNode := testkit.MustNodeSnapshot(t, w, networkID)
	if _, _, err := w.AddLink(networkNode.LeftPorts[0], client.RightPorts[0]); err != nil {
		t.Fatalf("AddLink network->client: %v", err)
	}
	w.SendNodeFormularMsg(clientID, formularApplyMessage(clientID, dnsnodes.DNSClientConfigBlockID, map[string]any{
		dnsnodes.DNSClientServersField: dnstest.DNSClientServerValues("dot://dns.example.test:853"),
		nodekit.TLSConfigFieldID:       "custom",
	}))

	first := tracker.client(t, 0)
	if first.tlsConfig == nil || first.tlsConfig.ServerName != "custom.test" {
		t.Fatalf("first DNS client TLS config = %#v, want custom.test", first.tlsConfig)
	}

	tlsStorage.Set("renamed", "renamed.test")
	w.SendInbox(pasta.InboxMessage{
		ReceiverNode: clientID,
		Payload: storage.TlsConfigChanged{
			NamePrev: "custom",
			NameNew:  "renamed",
		},
	})
	second := tracker.client(t, 1)
	if second.tlsConfig == nil || second.tlsConfig.ServerName != "renamed.test" {
		t.Fatalf("second DNS client TLS config = %#v, want renamed.test", second.tlsConfig)
	}
	if first.closeCount() != 1 {
		t.Fatalf("first DNS client close count = %d, want 1 after TLS config rename", first.closeCount())
	}

	out := configer.NewMemory(nil)
	if err := w.SaveConfig(out); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	got := configer.String(out.View(configer.Path{"client"}), configer.Path{nodekit.TLSConfigFieldID}, "")
	if got != "renamed" {
		t.Fatalf("saved TLS config = %q, want renamed", got)
	}
}

func TestDNSClientSaveRestorePreservesServersAndLinks(t *testing.T) {
	cfg := configer.NewMemory(map[string]any{
		"nodes": map[string]any{
			"network": map[string]any{
				"Class": networknodes.NodeTypeMockNetwork,
			},
			"client": map[string]any{
				"Class":   dnsnodes.NodeTypeDNSClient,
				"Links":   []string{"Network -> [network] Network"},
				"servers": []string{"127.0.0.1:5357", "dot://dns.example.test:853"},
			},
		},
	})
	e := e2etest.NewNodeE2EWithConfig(t, cfg)
	client := testkit.NodeIDByName(t, e.Engine, e2etest.E2EWorkspace, "client")
	e.SubscribeMenus(client)
	e.WaitNodeLabel(client, "2 DNS servers")

	item := waitDNSClientServersItem(t, e, client)
	if item.Field == nil || len(item.Elements) != 2 {
		t.Fatalf("servers menu elements = %#v, want two values", item.Field)
	}
	testkit.AssertStringSlice(t, dnstest.DNSClientServerElementValues(t, item.Elements), []string{"udp://127.0.0.1:5357", "dot://dns.example.test:853"})

	out := configer.NewMemory(nil)
	if err := e.Engine.SaveConfig(out); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	saved, ok := configer.SliceAt(out.View(configer.Path{"nodes", "client"}), configer.Path{"servers"})
	if !ok {
		t.Fatal("saved servers missing")
	}
	testkit.AssertStringSlice(t, configStringSlice(t, saved), []string{"udp://127.0.0.1:5357", "dot://dns.example.test:853"})
}

func newDNSClientWorkspace(t *testing.T, factories ...dnsnodes.DNSClientFactory) (*pasta.Workspace, *networknodes.MockLoopbackNetwork) {
	t.Helper()
	w := pasta.NewWorkspace(logging.NewDebugLogFactory(1024))
	t.Cleanup(func() { w.Close() })
	network := networknodes.NewMockLoopbackNetwork(true)
	t.Cleanup(func() { _ = network.Close() })
	var factory dnsnodes.DNSClientFactory
	if len(factories) > 0 {
		factory = factories[0]
	}
	for _, class := range []pasta.NodeClass{
		dnsnodes.DNSClientClass{NewClient: factory},
		dnsnodes.StaticDNSClass{},
		dnsClientNetworkClass{network: network},
	} {
		if err := w.AddNodeClass(class); err != nil {
			t.Fatalf("AddNodeClass %s: %v", class.ClassName(), err)
		}
	}
	return w, network
}

func startDNSClientUDPServer(t *testing.T, network *networknodes.MockLoopbackNetwork, address, recordsText string) {
	t.Helper()
	upstream, err := dnsnodes.NewStaticProvider(recordsText, dnsnodes.DefaultStaticDNSTTL)
	if err != nil {
		t.Fatalf("NewStaticProvider: %v", err)
	}
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
	id, err := w.AddNodeByClass(dnsnodes.NodeTypeDNSClient, name)
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

func addDNSClientReceiver(t *testing.T, w *pasta.Workspace, size int) *dnstest.Receiver {
	t.Helper()
	receiver := dnstest.NewReceiver(size)
	if _, err := w.AddNode(receiver, "test/DNSReceiver", "receiver"); err != nil {
		t.Fatalf("AddNode receiver: %v", err)
	}
	return receiver
}

func receiveReadyDNSPayload(t *testing.T, ch <-chan dnsnodes.DNSPayload) dnsnodes.DNSPayload {
	t.Helper()
	for {
		select {
		case payload := <-ch:
			if dnsnodes.DNSIsReady(payload.DNS) {
				return payload
			}
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for DNS payload")
			return dnsnodes.DNSPayload{}
		}
	}
}

func applyStaticDNSRecordsToWorkspace(t *testing.T, w *pasta.Workspace, node uint64, records string) {
	t.Helper()
	w.SendNodeFormularMsg(node, formularApplyMessage(node, "config", map[string]any{
		"records": records,
		"ttl":     dnsnodes.DefaultStaticDNSTTL,
	}))
}

func formularApplyMessage(node uint64, block string, values map[string]any) any {
	return formular.FormApplyMessage{
		MessageBase: testkit.FormMessageBase(formular.MessageFormApply, node),
		BlockID:     block,
		Values:      values,
	}
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

func dnsTestQuery(name string, typ uint16) *dns.Message {
	return &dns.Message{
		ID:               dns.NextID(),
		Opcode:           dns.OpcodeQuery,
		RecursionDesired: true,
		Questions: []dns.Question{{
			Name:  name,
			Type:  typ,
			Class: dns.ClassIN,
		}},
	}
}

func assertNodeLabel(t *testing.T, w *pasta.Workspace, node uint64, want string) {
	t.Helper()
	testkit.Eventually(t, func() bool {
		snapshot, ok := w.NodeSnapshot(node)
		return ok && snapshot.Label == want
	}, "node %d label %q", node, want)
}

func applyDNSClientServersToWorkspace(t *testing.T, w *pasta.Workspace, node uint64, servers ...string) {
	t.Helper()
	w.SendNodeFormularMsg(node, formularApplyMessage(node, dnsnodes.DNSClientConfigBlockID, map[string]any{
		dnsnodes.DNSClientServersField: dnstest.DNSClientServerValues(servers...),
	}))
}

func waitDNSClientServersItem(t *testing.T, e *e2etest.NodeE2E, node uint64) formular.Item {
	t.Helper()
	var item formular.Item
	testkit.Eventually(e.T, func() bool {
		var ok bool
		item, ok = e.MenuItem(node, dnsnodes.DNSClientConfigBlockID, dnsnodes.DNSClientServersField)
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

func assertNoDNSPayload(t *testing.T, ch <-chan dnsnodes.DNSPayload, d time.Duration) {
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
	tlsConfig *tls.Config
	closes    atomic.Int32
}

func (f *trackedDNSClientFactory) newClient(
	dial gonnect.Dial,
	bootstrap dns.Interface,
	spawner gonnect.Spawner,
	tlsConfig *tls.Config,
	servers ...string,
) dns.Interface {
	dnsClient := dns.NewClient(dial, bootstrap, spawner, servers...)
	dnsClient.TLSConfig = nodekit.CloneTLSClientConfig(tlsConfig)
	client := &trackedDNSClient{
		Interface: dnsClient,
		tlsConfig: nodekit.CloneTLSClientConfig(tlsConfig),
	}
	f.mu.Lock()
	f.clients = append(f.clients, client)
	f.mu.Unlock()
	return client
}

func (f *trackedDNSClientFactory) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.clients)
}

func (f *trackedDNSClientFactory) client(t *testing.T, index int) *trackedDNSClient {
	t.Helper()
	testkit.Eventually(t, func() bool {
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
	network *networknodes.MockLoopbackNetwork
}

func (dnsClientNetworkClass) ClassName() string {
	return dnsClientNetworkNodeType
}
func (dnsClientNetworkClass) ShortDescription() string { return "DNS transport network" }
func (dnsClientNetworkClass) LongDescription() string  { return "DNS transport network" }
func (dnsClientNetworkClass) DefaultNodeParams() pasta.NodeClassParams {
	return pasta.NodeClassParams{PrimaryType: links.TypeNetwork, InitialPorts: []pasta.Port{
		links.NetworkPort("left", "Network"),
	}}
}
func (c dnsClientNetworkClass) NewNode(configer.Config, ...*pasta.NodeClassState) (pasta.Node, error) {
	network := c.network
	if network == nil {
		network = networknodes.NewMockLoopbackNetwork(true)
	}
	return &dnsClientNetworkNode{network: network}, nil
}

type dnsClientNetworkNode struct {
	nodekit.SimpleNetworkProvider
	network *networknodes.MockLoopbackNetwork
}

func (n *dnsClientNetworkNode) OnInit(w *pasta.Workspace, _ pasta.Logger, id uint64, _ string, restored *pasta.NodeInitData, _, _, _, _ bool) error {
	if n.network == nil {
		n.network = networknodes.NewMockLoopbackNetwork(true)
	}
	return n.InitSimpleNetworkProvider(w, nil, id, restored, n.network, "network")
}

type dnsClientTestRuntime struct {
	tlsConfigs storage.TlsConfigStorage
}

func (r dnsClientTestRuntime) Context() context.Context { return context.Background() }
func (r dnsClientTestRuntime) Pool() bufpool.Pool       { return nil }
func (r dnsClientTestRuntime) System() sysnet.System    { return nil }
func (r dnsClientTestRuntime) KVManager() storage.KVManager {
	return nil
}
func (r dnsClientTestRuntime) DnsCacheManager() storage.DnsCacheManager {
	return nil
}
func (r dnsClientTestRuntime) SecretsStorage() storage.SecretsStorage {
	return nil
}
func (r dnsClientTestRuntime) TlsConfigStorage() storage.TlsConfigStorage {
	return r.tlsConfigs
}
func (r dnsClientTestRuntime) WorkerSpawner(uint64) nodekit.WorkerSpawner {
	return nil
}
func (r dnsClientTestRuntime) ObserveNodeSnapshot(uint64, pasta.NodeSnapshot) {}
