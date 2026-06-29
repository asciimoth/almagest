package almagest

import (
	"bytes"
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/formular"
	"github.com/asciimoth/gonnect/tun"
	"github.com/asciimoth/pasta/pasta"
	"github.com/asciimoth/pasta/pasta/std"
)

const testTunProviderNodeType = "test/TunProvider"

const (
	tunCopyE2EClientAddr = "10.113.212.1"
	tunCopyE2EServerAddr = "10.210.148.1"
)

func TestTunCopyE2EVTunHTTPClientServer(t *testing.T) {
	f := setupTunCopyVTunHTTPE2E(t)
	f.assertClientRequestWorks(f.client)
	f.assertServerLog(f.server, "GET /")
	f.WaitNodeLabel(f.copy, "copying")
}

func TestTunCopyE2EVTunHTTPClientServerSaveRestoreFirstRequest(t *testing.T) {
	f := setupTunCopyVTunHTTPE2E(t)

	copyName := f.nodeName(f.copy)
	vtunAName := f.nodeName(f.vtunA)
	vtunBName := f.nodeName(f.vtunB)
	serverName := f.nodeName(f.server)
	clientName := f.nodeName(f.client)

	cfg := configer.NewMemory(nil)
	if err := f.Engine.SaveConfig(cfg); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	if err := f.Engine.Close(); err != nil {
		t.Fatalf("close saved engine: %v", err)
	}

	restored := newVTunSpooferConfigE2E(t, cfg)
	copyNode := restored.mustNodeByName(copyName)
	vtunA := restored.mustNodeByName(vtunAName)
	vtunB := restored.mustNodeByName(vtunBName)
	server := restored.mustNodeByName(serverName)
	client := restored.mustNodeByName(clientName)
	restored.SubscribeMenus(copyNode, vtunA, vtunB, server, client)
	restored.mustTypedLink(TypeTun, copyNode, "left", "TunA", vtunA, "right", "Tun")
	restored.mustTypedLink(TypeTun, copyNode, "left", "TunB", vtunB, "right", "Tun")
	restored.mustNetworkLink(vtunA, "left", "Network", client, "right", "Network")
	restored.mustNetworkLink(vtunB, "left", "Network", server, "right", "Network")
	restored.WaitNodeLabel(copyNode, "copying")
	restored.waitServerListening(server, fmt.Sprintf("%s:%d", tunCopyE2EServerAddr, f.port))

	restored.sendClientRequest(client)
	if err := restored.waitClientResponseResult(client, "200 OK", "Debug http server response"); err != nil {
		t.Fatal(err)
	}
}

func TestTunCopyE2ECopiesPacketsAfterAttachDetachReattach(t *testing.T) {
	e, providers := newTunCopyE2E(t)
	copyNode := e.AddNode(NodeTypeTunCopy, "copy")
	providerA := e.AddNode(testTunProviderNodeType, "provider-a")
	providerB := e.AddNode(testTunProviderNodeType, "provider-b")

	linkA := e.MustAddTypedLinkByPortName(TypeTun, copyNode, "left", "TunA", providerA, "right", "Tun")
	linkB := e.MustAddTypedLinkByPortName(TypeTun, copyNode, "left", "TunB", providerB, "right", "Tun")
	e.WaitNodeLabel(copyNode, "copying")

	peerA := providers.peer(t, providerA)
	peerB := providers.peer(t, providerB)
	assertTunPacketCopied(t, peerA, peerB, "a-to-b-before-reattach")
	assertTunPacketCopied(t, peerB, peerA, "b-to-a-before-reattach")

	e.Engine.RemoveLink(E2EWorkspace, linkA)
	e.WaitNodeLabel(copyNode, "waiting for TunA")
	providerA2 := e.AddNode(testTunProviderNodeType, "provider-a2")
	linkA = e.MustAddTypedLinkByPortName(TypeTun, copyNode, "left", "TunA", providerA2, "right", "Tun")
	e.WaitNodeLabel(copyNode, "copying")

	peerA2 := providers.peer(t, providerA2)
	assertTunPacketCopied(t, peerA2, peerB, "a-to-b-after-a-reattach")
	assertTunPacketCopied(t, peerB, peerA2, "b-to-a-after-a-reattach")

	e.Engine.RemoveLink(E2EWorkspace, linkB)
	e.WaitNodeLabel(copyNode, "waiting for TunB")
	providerB2 := e.AddNode(testTunProviderNodeType, "provider-b2")
	_ = e.MustAddTypedLinkByPortName(TypeTun, copyNode, "left", "TunB", providerB2, "right", "Tun")
	e.WaitNodeLabel(copyNode, "copying")

	peerB2 := providers.peer(t, providerB2)
	assertTunPacketCopied(t, peerA2, peerB2, "a-to-b-after-b-reattach")
	assertTunPacketCopied(t, peerB2, peerA2, "b-to-a-after-b-reattach")

	e.Engine.RemoveLink(E2EWorkspace, linkA)
	e.WaitNodeLabel(copyNode, "waiting for TunA")
	linkA = e.MustAddTypedLinkByPortName(TypeTun, copyNode, "left", "TunA", providerA2, "right", "Tun")
	e.WaitNodeLabel(copyNode, "copying")
	peerA2 = providers.peer(t, providerA2)
	assertTunPacketCopied(t, peerA2, peerB2, "a-to-b-after-same-provider-reattach")

	_ = linkA
}

func TestTunCopyE2EClassHasTwoLeftTunPortsAndNoMenu(t *testing.T) {
	e, _ := newTunCopyE2E(t)
	copyNode := e.AddNode(NodeTypeTunCopy, "copy")

	snapshot, ok := e.Engine.NodeSnapshot(E2EWorkspace, copyNode)
	if !ok {
		t.Fatalf("NodeSnapshot(%d) missing", copyNode)
	}
	if len(snapshot.LeftPorts) != 2 {
		t.Fatalf("left ports = %d, want 2", len(snapshot.LeftPorts))
	}
	if len(snapshot.RightPorts) != 0 {
		t.Fatalf("right ports = %d, want 0", len(snapshot.RightPorts))
	}
	assertPortShape(t, e, snapshot.LeftPorts[0], "left", "TunA", TypeTun)
	assertPortShape(t, e, snapshot.LeftPorts[1], "left", "TunB", TypeTun)

	e.SubscribeMenus(copyNode)
	time.Sleep(25 * time.Millisecond)
	if menu := e.Menu(copyNode); menu.Type != "" || len(menu.Blocks) != 0 {
		t.Fatalf("TunCopy menu = %#v, want no menu snapshot", menu)
	}
}

type tunCopyVTunHTTPE2EFixture struct {
	*httpE2E

	copy   uint64
	vtunA  uint64
	vtunB  uint64
	server uint64
	client uint64
	port   int
}

func setupTunCopyVTunHTTPE2E(t *testing.T) tunCopyVTunHTTPE2EFixture {
	t.Helper()
	e := newHTTPE2E(t)
	f := tunCopyVTunHTTPE2EFixture{httpE2E: e, port: httpTestPort()}
	f.copy = e.AddNode(NodeTypeTunCopy, "copy")
	f.vtunA = e.AddNode(NodeTypeVTun, "vtun-a")
	f.vtunB = e.AddNode(NodeTypeVTun, "vtun-b")
	f.server = e.addHTTPServerOnHost("server", tunCopyE2EServerAddr, f.port)
	f.client = e.addHTTPClientToURL("client", fmt.Sprintf("http://%s:%d/", tunCopyE2EServerAddr, f.port))
	e.SubscribeMenus(f.vtunA, f.vtunB, f.server, f.client)
	e.waitClientRequestField(f.client, "url", fmt.Sprintf("http://%s:%d/", tunCopyE2EServerAddr, f.port))

	e.applyTunCopyVTunConfig(f.vtunA, "client-vtun", tunCopyE2EClientAddr+"/24")
	e.applyTunCopyVTunConfig(f.vtunB, "server-vtun", tunCopyE2EServerAddr+"/24")
	e.connectTunByPortName(f.copy, "left", "TunA", f.vtunA, "right", "Tun")
	e.connectTunByPortName(f.copy, "left", "TunB", f.vtunB, "right", "Tun")
	e.connectByPortName(f.vtunA, "left", "Network", f.client, "right", "Network")
	e.connectByPortName(f.vtunB, "left", "Network", f.server, "right", "Network")

	e.WaitNodeLabel(f.copy, "copying")
	e.WaitNodeLabel(f.vtunA, "client-vtun")
	e.WaitNodeLabel(f.vtunB, "server-vtun")
	e.waitServerListening(f.server, fmt.Sprintf("%s:%d", tunCopyE2EServerAddr, f.port))
	return f
}

func (e *httpE2E) applyTunCopyVTunConfig(node uint64, name, localAddrs string) {
	e.T.Helper()
	e.Engine.SendNodeFormularMsg(E2EWorkspace, node, formular.FormApplyMessage{
		MessageBase: FormMessageBase(formular.MessageFormApply, node),
		BlockID:     "config",
		Values: map[string]any{
			"name":           name,
			"localAddrs":     localAddrs,
			"dnsServers":     "",
			"mtu":            1500,
			"endpointCh":     1024,
			"eventCh":        10,
			"noLoopbackAddr": false,
			"disableSACK":    false,
		},
	})
}

func newTunCopyE2E(t *testing.T) (*NodeE2E, *testTunProviderRegistry) {
	t.Helper()

	pool := newE2ETestDebugPool(t)
	logf := NewDebugLogFactory(4096)
	sys := newE2EDebugSystem()
	providers := newTestTunProviderRegistry()
	engine, err := BuildEngine(
		context.Background(),
		configer.NewMemory(nil),
		logf,
		sys,
		[]pasta.NodeClass{testTunProviderClass{registry: providers}},
		pool,
		true,
	)
	if err != nil {
		t.Fatalf("BuildEngine error = %v, want nil", err)
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
	return e, providers
}

type testTunProviderRegistry struct {
	mu    sync.Mutex
	nodes map[uint64]*testTunProviderNode
}

func newTestTunProviderRegistry() *testTunProviderRegistry {
	return &testTunProviderRegistry{nodes: map[uint64]*testTunProviderNode{}}
}

func (r *testTunProviderRegistry) register(id uint64, node *testTunProviderNode) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.nodes[id] = node
}

func (r *testTunProviderRegistry) peer(t *testing.T, id uint64) tun.Tun {
	t.Helper()
	var peer tun.Tun
	Eventually(t, func() bool {
		r.mu.Lock()
		defer r.mu.Unlock()
		node := r.nodes[id]
		if node == nil {
			return false
		}
		peer = node.peer
		return peer != nil
	}, "test tun provider %d peer", id)
	return peer
}

type testTunProviderClass struct {
	registry *testTunProviderRegistry
}

func (testTunProviderClass) ClassName() string        { return testTunProviderNodeType }
func (testTunProviderClass) ShortDescription() string { return "test TUN provider" }
func (testTunProviderClass) LongDescription() string {
	return "Test-only right-directed almagest/tun provider backed by tun.Pipe."
}
func (testTunProviderClass) DefaultNodeParams() pasta.NodeClassParams {
	return pasta.NodeClassParams{PrimaryType: TypeTun, InitialPorts: []pasta.Port{
		TunPort("right", "Tun"),
	}}
}
func (c testTunProviderClass) NewNode(configer.Config, ...*pasta.NodeClassState) (pasta.Node, error) {
	return &testTunProviderNode{registry: c.registry}, nil
}

type testTunProviderNode struct {
	NodeBase

	registry *testTunProviderRegistry
	out      uint64

	source  tun.Tun
	peer    tun.Tun
	outputs TunOutputCache
}

func (n *testTunProviderNode) OnInit(w *pasta.Workspace, _ pasta.Logger, id uint64, _ string, restored *pasta.NodeInitData, _, _, _, _ bool) error {
	n.InitBaseNode(w, nil, id)
	n.out = restoredRight(restored, 0)
	if n.registry != nil {
		n.registry.register(id, n)
	}
	if err := n.SetPrimary(TypeTun); err != nil {
		return err
	}
	return n.SetLabel("ready")
}

func (n *testTunProviderNode) OnStop() {
	n.outputs.CloseAll()
	n.closePipe()
	n.outputs.Wait()
}

func (n *testTunProviderNode) OnReady() error {
	n.sendAll()
	return nil
}

func (n *testTunProviderNode) PreLinkAdd(port uint64, linkType, portDirection string) error {
	if port != n.out || portDirection != "right" {
		return pasta.LinkTypeErr(linkType)
	}
	return TunPreLinkAdd(n.w, port, linkType)
}

func (n *testTunProviderNode) OnLinkAdd(link, port uint64, linkType, portDirection string) error {
	if port == n.out && portDirection == "right" && linkType == TypeTun {
		n.sendTunToLink(link)
	}
	return nil
}

func (n *testTunProviderNode) OnLinkRemoved(link uint64, port uint64, linkType, portDirection string) error {
	if port == n.out && portDirection == "right" && linkType == TypeTun {
		n.outputs.Close(link)
		n.closePipe()
	}
	return nil
}

func (n *testTunProviderNode) OnEvent(event pasta.Event, linkType string, _ []string, receiverPortDirection string) error {
	if event.ReceiverPort != n.out || receiverPortDirection != "right" || linkType != TypeTun || !std.IsRequest(event.Payload) {
		return nil
	}
	n.sendTunToLink(event.Link)
	return nil
}

func (n *testTunProviderNode) sendAll() {
	n.ForEachPortLink(n.out, func(link uint64) {
		n.sendTunToLink(link)
	})
}

func (n *testTunProviderNode) sendTunToLink(link uint64) {
	n.ensurePipe()
	n.outputs.Send(n.w, n.id, link, n.source, nil)
}

func (n *testTunProviderNode) ensurePipe() {
	if n.source != nil && n.peer != nil {
		return
	}
	n.source, n.peer = tun.Pipe(4, 1500, 0, 0)
}

func (n *testTunProviderNode) closePipe() {
	if n.source != nil {
		_ = n.source.Close()
		n.source = nil
	}
	if n.peer != nil {
		_ = n.peer.Close()
		n.peer = nil
	}
}

func assertTunPacketCopied(t *testing.T, from, to tun.Tun, text string) {
	t.Helper()
	packet := []byte(text)
	writeTunPacket(t, from, packet)
	got := readTunPacket(t, to)
	if !bytes.Equal(got, packet) {
		t.Fatalf("copied packet = %q, want %q", string(got), text)
	}
}

func writeTunPacket(t *testing.T, target tun.Tun, packet []byte) {
	t.Helper()
	offset := target.MWO()
	buf := make([]byte, offset+len(packet))
	copy(buf[offset:], packet)
	n, err := target.Write([][]byte{buf}, offset)
	if err != nil {
		t.Fatalf("tun Write: %v", err)
	}
	if n != 1 {
		t.Fatalf("tun Write count = %d, want 1", n)
	}
}

func readTunPacket(t *testing.T, source tun.Tun) []byte {
	t.Helper()
	type result struct {
		packet []byte
		err    error
	}
	ch := make(chan result, 1)
	go func() {
		offset := source.MRO()
		buf := make([]byte, offset+2048)
		sizes := []int{0}
		n, err := source.Read([][]byte{buf}, sizes, offset)
		if err != nil {
			ch <- result{err: err}
			return
		}
		if n != 1 {
			ch <- result{err: fmt.Errorf("read packet count %d, want 1", n)}
			return
		}
		packet := append([]byte(nil), buf[offset:offset+sizes[0]]...)
		ch <- result{packet: packet}
	}()
	select {
	case got := <-ch:
		if got.err != nil {
			t.Fatalf("tun Read: %v", got.err)
		}
		return got.packet
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for copied tun packet")
		return nil
	}
}

func assertPortShape(t *testing.T, e *NodeE2E, port uint64, direction, name, linkType string) {
	t.Helper()
	snapshot, ok := e.Engine.PortSnapshot(E2EWorkspace, port)
	if !ok {
		t.Fatalf("PortSnapshot(%d) missing", port)
	}
	if snapshot.Direction != direction || snapshot.Name != name {
		t.Fatalf("port %d = %s %q, want %s %q", port, snapshot.Direction, snapshot.Name, direction, name)
	}
	if len(snapshot.Types) != 1 || snapshot.Types[0] != linkType {
		t.Fatalf("port %d types = %#v, want [%q]", port, snapshot.Types, linkType)
	}
}
