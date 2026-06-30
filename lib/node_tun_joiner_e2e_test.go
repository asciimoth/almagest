package almagest

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
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

const testTunSinkNodeType = "test/TunSink"

const (
	tunJoinerE2EDefaultClientAddr    = "10.113.212.1"
	tunJoinerE2ESecondaryClientAddr1 = "10.2.0.1"
	tunJoinerE2ESecondaryClientAddr2 = "10.7.160.1"
	tunJoinerE2EServerAddr           = "10.210.148.1"
)

func TestTunJoinerE2EVTunHTTPParallelClients(t *testing.T) {
	f := setupTunJoinerVTunHTTPE2E(t)

	f.assertParallelClientRequestsWork()
	f.assertServerLog(f.server, "GET /")
	f.WaitNodeLabel(f.joiner, "joined default + 2 secondary")
	f.WaitNodeLabel(f.copy, "copying")
}

func TestTunJoinerE2EJoinsMultipleSecondariesAndReattaches(t *testing.T) {
	e, providers, sinks := newTunJoinerE2E(t)
	joiner := e.AddNode(NodeTypeTunJoiner, "joiner")
	defaultProvider := e.AddNode(testTunProviderNodeType, "default")
	secondary1 := e.AddNode(testTunProviderNodeType, "secondary-1")
	secondary2 := e.AddNode(testTunProviderNodeType, "secondary-2")
	sink := e.AddNode(testTunSinkNodeType, "sink")

	defaultLink := e.MustAddTypedLinkByPortName(TypeTun, joiner, "left", "Default", defaultProvider, "right", "Tun")
	secondary1Link := e.MustAddTypedLinkByPortName(TypeTun, joiner, "left", "Secondary", secondary1, "right", "Tun")
	e.MustAddTypedLinkByPortName(TypeTun, joiner, "left", "Secondary", secondary2, "right", "Tun")
	e.MustAddTypedLinkByPortName(TypeTun, sink, "left", "Tun", joiner, "right", "Tun")
	e.WaitNodeLabel(joiner, "joined default + 2 secondary")
	assertPortLinkCount(t, e, e.LeftPort(joiner, "Secondary"), 2)

	defaultPeer := providers.peer(t, defaultProvider)
	secondary1Peer := providers.peer(t, secondary1)
	secondary2Peer := providers.peer(t, secondary2)
	output := sinks.tun(t, sink)

	clientIP := [4]byte{10, 90, 0, 1}
	secondary1IP := [4]byte{10, 90, 0, 2}
	secondary2IP := [4]byte{10, 90, 0, 3}
	unknownIP := [4]byte{10, 90, 0, 99}

	learnSecondary1 := testIPv4Packet(secondary1IP, clientIP)
	assertTunPacketTransferred(t, secondary1Peer, output, learnSecondary1)
	assertTunPacketTransferred(t, output, secondary1Peer, testIPv4Packet(clientIP, secondary1IP))

	assertTunPacketTransferred(t, secondary2Peer, output, testIPv4Packet(secondary2IP, clientIP))
	assertTunPacketTransferred(t, output, secondary2Peer, testIPv4Packet(clientIP, secondary2IP))

	assertTunPacketTransferred(t, output, defaultPeer, testIPv4Packet(clientIP, unknownIP))

	e.Engine.RemoveLink(E2EWorkspace, secondary1Link)
	e.WaitNodeLabel(joiner, "joined default + 1 secondary")
	assertTunPacketTransferred(t, output, defaultPeer, testIPv4Packet(clientIP, secondary1IP))

	secondary1Replacement := e.AddNode(testTunProviderNodeType, "secondary-1-replacement")
	e.MustAddTypedLinkByPortName(TypeTun, joiner, "left", "Secondary", secondary1Replacement, "right", "Tun")
	e.WaitNodeLabel(joiner, "joined default + 2 secondary")
	secondary1ReplacementPeer := providers.peer(t, secondary1Replacement)
	assertTunPacketTransferred(t, secondary1ReplacementPeer, output, testIPv4Packet(secondary1IP, clientIP))
	assertTunPacketTransferred(t, output, secondary1ReplacementPeer, testIPv4Packet(clientIP, secondary1IP))

	e.Engine.RemoveLink(E2EWorkspace, defaultLink)
	e.WaitNodeLabel(joiner, "waiting for default")
	defaultReplacement := e.AddNode(testTunProviderNodeType, "default-replacement")
	e.MustAddTypedLinkByPortName(TypeTun, joiner, "left", "Default", defaultReplacement, "right", "Tun")
	e.WaitNodeLabel(joiner, "joined default + 2 secondary")
	defaultReplacementPeer := providers.peer(t, defaultReplacement)
	assertTunPacketTransferred(t, output, defaultReplacementPeer, testIPv4Packet(clientIP, unknownIP))
}

func TestTunJoinerE2EClassPortsAndNoMenu(t *testing.T) {
	e, _, _ := newTunJoinerE2E(t)
	joiner := e.AddNode(NodeTypeTunJoiner, "joiner")

	snapshot, ok := e.Engine.NodeSnapshot(E2EWorkspace, joiner)
	if !ok {
		t.Fatalf("NodeSnapshot(%d) missing", joiner)
	}
	if len(snapshot.LeftPorts) != 2 {
		t.Fatalf("left ports = %d, want 2", len(snapshot.LeftPorts))
	}
	if len(snapshot.RightPorts) != 1 {
		t.Fatalf("right ports = %d, want 1", len(snapshot.RightPorts))
	}
	assertPortShape(t, e, snapshot.LeftPorts[0], "left", "Default", TypeTun)
	assertPortShape(t, e, snapshot.LeftPorts[1], "left", "Secondary", TypeTun)
	assertPortShape(t, e, snapshot.RightPorts[0], "right", "Tun", TypeTun)

	e.SubscribeMenus(joiner)
	time.Sleep(25 * time.Millisecond)
	if menu := e.Menu(joiner); menu.Type != "" || len(menu.Blocks) != 0 {
		t.Fatalf("TunJoiner menu = %#v, want no menu snapshot", menu)
	}
}

func newTunJoinerE2E(t *testing.T) (*NodeE2E, *testTunProviderRegistry, *testTunSinkRegistry) {
	t.Helper()

	pool := newE2ETestDebugPool(t)
	logf := NewDebugLogFactory(16384)
	sys := newE2EDebugSystem()
	providers := newTestTunProviderRegistry()
	sinks := newTestTunSinkRegistry()
	engine, err := BuildEngine(
		context.Background(),
		configer.NewMemory(nil),
		logf,
		sys,
		[]pasta.NodeClass{
			testTunProviderClass{registry: providers},
			testTunSinkClass{registry: sinks},
		},
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
	return e, providers, sinks
}

type tunJoinerVTunHTTPE2EFixture struct {
	*httpE2E

	joiner      uint64
	copy        uint64
	defaultVTun uint64
	secondary   []uint64
	serverVTun  uint64
	server      uint64
	clients     []uint64
	port        int
}

func setupTunJoinerVTunHTTPE2E(t *testing.T) tunJoinerVTunHTTPE2EFixture {
	t.Helper()
	e := newHTTPE2E(t)
	f := tunJoinerVTunHTTPE2EFixture{
		httpE2E:   e,
		secondary: make([]uint64, 0, 2),
		clients:   make([]uint64, 0, 3),
		port:      httpTestPort(),
	}

	f.joiner = e.AddNode(NodeTypeTunJoiner, "joiner")
	f.copy = e.AddNode(NodeTypeTunCopy, "copy")
	f.defaultVTun = e.AddNode(NodeTypeVTun, "default-vtun")
	f.secondary = append(f.secondary,
		e.AddNode(NodeTypeVTun, "secondary-vtun-1"),
		e.AddNode(NodeTypeVTun, "secondary-vtun-2"),
	)
	f.serverVTun = e.AddNode(NodeTypeVTun, "server-vtun")
	f.server = e.addHTTPServerOnHost("server", tunJoinerE2EServerAddr, f.port)
	for i := 0; i < 3; i++ {
		f.clients = append(f.clients, e.addHTTPClientToURL(
			fmt.Sprintf("client-%d", i+1),
			fmt.Sprintf("http://%s:%d/", tunJoinerE2EServerAddr, f.port),
		))
	}
	e.SubscribeMenus(append([]uint64{f.defaultVTun, f.serverVTun, f.server}, append(f.secondary, f.clients...)...)...)
	for _, client := range f.clients {
		e.waitClientRequestField(client, "url", fmt.Sprintf("http://%s:%d/", tunJoinerE2EServerAddr, f.port))
	}

	e.applyTunCopyVTunConfig(f.defaultVTun, "default-client-vtun", tunJoinerE2EDefaultClientAddr+"/24")
	e.applyTunCopyVTunConfig(f.secondary[0], "secondary-client-vtun-1", tunJoinerE2ESecondaryClientAddr1+"/24")
	e.applyTunCopyVTunConfig(f.secondary[1], "secondary-client-vtun-2", tunJoinerE2ESecondaryClientAddr2+"/24")
	e.applyTunCopyVTunConfig(f.serverVTun, "server-vtun", tunJoinerE2EServerAddr+"/24")

	e.connectTunByPortName(f.joiner, "left", "Default", f.defaultVTun, "right", "Tun")
	for _, secondary := range f.secondary {
		e.connectTunByPortName(f.joiner, "left", "Secondary", secondary, "right", "Tun")
	}
	e.connectTunByPortName(f.copy, "left", "TunA", f.joiner, "right", "Tun")
	e.connectTunByPortName(f.copy, "left", "TunB", f.serverVTun, "right", "Tun")

	e.connectByPortName(f.defaultVTun, "left", "Network", f.clients[0], "right", "Network")
	e.connectByPortName(f.secondary[0], "left", "Network", f.clients[1], "right", "Network")
	e.connectByPortName(f.secondary[1], "left", "Network", f.clients[2], "right", "Network")
	e.connectByPortName(f.serverVTun, "left", "Network", f.server, "right", "Network")

	e.WaitNodeLabel(f.joiner, "joined default + 2 secondary")
	e.WaitNodeLabel(f.copy, "copying")
	e.WaitNodeLabel(f.defaultVTun, "default-client-vtun")
	e.WaitNodeLabel(f.secondary[0], "secondary-client-vtun-1")
	e.WaitNodeLabel(f.secondary[1], "secondary-client-vtun-2")
	e.WaitNodeLabel(f.serverVTun, "server-vtun")
	e.waitServerListening(f.server, fmt.Sprintf("%s:%d", tunJoinerE2EServerAddr, f.port))
	return f
}

func (f tunJoinerVTunHTTPE2EFixture) assertParallelClientRequestsWork() {
	f.T.Helper()

	start := make(chan struct{})
	errCh := make(chan error, len(f.clients))
	var wg sync.WaitGroup
	for _, client := range f.clients {
		client := client
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			f.Engine.SendNodeFormularMsg(E2EWorkspace, client, formular.ButtonPressMessage{
				MessageBase: FormMessageBase(formular.MessageButtonPress, client),
				BlockID:     "request",
				ButtonID:    "send",
			})
			errCh <- f.waitClientResponseResult(client, "200 OK", "Debug http server response")
		}()
	}
	close(start)
	wg.Wait()
	close(errCh)

	for err := range errCh {
		if err != nil {
			f.T.Fatal(err)
		}
	}
	for _, client := range f.clients {
		f.AssertNodeLabel(client, "200 OK")
	}
}

func (e *httpE2E) waitClientResponseResult(client uint64, status, response string) error {
	deadline := time.Now().Add(10 * time.Second)
	for {
		gotStatus := e.clientStatus(client)
		gotErr := e.clientResultField(client, "error")
		gotResp := e.clientResultField(client, "response")
		if gotStatus == "Status: "+status && gotErr == "" && gotResp == response {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf(
				"timed out waiting for client %d response %q %q; got status=%q error=%q response=%q",
				client,
				status,
				response,
				gotStatus,
				gotErr,
				gotResp,
			)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

type testTunSinkRegistry struct {
	mu    sync.Mutex
	nodes map[uint64]*testTunSinkNode
}

func newTestTunSinkRegistry() *testTunSinkRegistry {
	return &testTunSinkRegistry{nodes: map[uint64]*testTunSinkNode{}}
}

func (r *testTunSinkRegistry) register(id uint64, node *testTunSinkNode) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.nodes[id] = node
}

func (r *testTunSinkRegistry) tun(t *testing.T, id uint64) tun.Tun {
	t.Helper()
	var out tun.Tun
	Eventually(t, func() bool {
		r.mu.Lock()
		defer r.mu.Unlock()
		node := r.nodes[id]
		if node == nil {
			return false
		}
		out = node.tun
		return out != nil
	}, "test tun sink %d tun", id)
	return out
}

type testTunSinkClass struct {
	registry *testTunSinkRegistry
}

func (testTunSinkClass) ClassName() string        { return testTunSinkNodeType }
func (testTunSinkClass) ShortDescription() string { return "test TUN sink" }
func (testTunSinkClass) LongDescription() string {
	return "Test-only left-directed almagest/tun sink that records the latest received Tun."
}
func (testTunSinkClass) DefaultNodeParams() pasta.NodeClassParams {
	return pasta.NodeClassParams{PrimaryType: TypeTun, InitialPorts: []pasta.Port{
		TunPort("left", "Tun"),
	}}
}
func (c testTunSinkClass) NewNode(configer.Config, ...*pasta.NodeClassState) (pasta.Node, error) {
	return &testTunSinkNode{registry: c.registry}, nil
}

type testTunSinkNode struct {
	pasta.BasicNode

	registry *testTunSinkRegistry
	w        *pasta.Workspace
	id       uint64
	in       uint64
	link     uint64
	tun      tun.Tun
}

func (n *testTunSinkNode) OnInit(w *pasta.Workspace, _ pasta.Logger, id uint64, _ string, restored *pasta.NodeInitData, _, _, _, _ bool) error {
	n.w = w
	n.id = id
	if restored != nil && len(restored.LeftPorts) > 0 {
		n.in = restored.LeftPorts[0]
	}
	if n.registry != nil {
		n.registry.register(id, n)
	}
	if err := n.w.SetNodePrimary(n.id, TypeTun); err != nil {
		return err
	}
	return n.w.SetNodeLabel(n.id, "waiting")
}

func (n *testTunSinkNode) OnStop() {
	if n.tun != nil {
		_ = n.tun.Close()
		n.tun = nil
	}
}

func (n *testTunSinkNode) PreLinkAdd(port uint64, linkType, portDirection string) error {
	if port != n.in || portDirection != "left" {
		return pasta.LinkTypeErr(linkType)
	}
	return TunPreLinkAdd(n.w, port, linkType)
}

func (n *testTunSinkNode) OnLinkAdd(link, port uint64, linkType, portDirection string) error {
	if port == n.in && portDirection == "left" && linkType == TypeTun {
		n.link = link
		std.Request(n.w, n.id, link)
	}
	return nil
}

func (n *testTunSinkNode) OnLinkRemoved(link uint64, port uint64, linkType, portDirection string) error {
	if port == n.in && portDirection == "left" && linkType == TypeTun && link == n.link {
		if n.tun != nil {
			_ = n.tun.Close()
		}
		n.link = 0
		n.tun = nil
		return n.w.SetNodeLabel(n.id, "waiting")
	}
	return nil
}

func (n *testTunSinkNode) OnEvent(event pasta.Event, linkType string, _ []string, receiverPortDirection string) error {
	if event.ReceiverPort != n.in || receiverPortDirection != "left" || linkType != TypeTun || std.IsRequest(event.Payload) {
		return nil
	}
	payload, ok := event.Payload.(TunPayload)
	if !ok || payload.Tun == nil {
		return nil
	}
	if SameTunInstance(n.tun, payload.Tun) {
		return nil
	}
	if n.tun != nil {
		_ = n.tun.Close()
	}
	BindTunResource(n.w, n.id, event.Link, payload.Tun)
	n.tun = payload.Tun
	return n.w.SetNodeLabel(n.id, "ready")
}

func assertTunPacketTransferred(t *testing.T, from, to tun.Tun, packet []byte) {
	t.Helper()
	type result struct {
		packet []byte
		err    error
	}
	ch := make(chan result, 1)
	go func() {
		offset := to.MRO()
		buf := make([]byte, offset+2048)
		sizes := []int{0}
		n, err := to.Read([][]byte{buf}, sizes, offset)
		if err != nil {
			ch <- result{err: err}
			return
		}
		if n != 1 {
			ch <- result{err: fmt.Errorf("read packet count %d, want 1", n)}
			return
		}
		ch <- result{packet: append([]byte(nil), buf[offset:offset+sizes[0]]...)}
	}()
	writeTunPacket(t, from, packet)
	select {
	case got := <-ch:
		if got.err != nil {
			t.Fatalf("tun Read: %v", got.err)
		}
		if !bytes.Equal(got.packet, packet) {
			t.Fatalf("transferred packet = %v, want %v", got.packet, packet)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for transferred tun packet")
	}
}

func assertPortLinkCount(t *testing.T, e *NodeE2E, port uint64, want int) {
	t.Helper()
	snapshot, ok := e.Engine.PortSnapshot(E2EWorkspace, port)
	if !ok {
		t.Fatalf("PortSnapshot(%d) missing", port)
	}
	if len(snapshot.Links) != want {
		t.Fatalf("port %d links = %d, want %d", port, len(snapshot.Links), want)
	}
}

func testIPv4Packet(src, dst [4]byte) []byte {
	p := make([]byte, 20)
	p[0] = 0x45
	binary.BigEndian.PutUint16(p[2:4], uint16(len(p)))
	copy(p[12:16], src[:])
	copy(p[16:20], dst[:])
	return p
}

func TestTunJoinerE2ERejectsSecondDefaultLink(t *testing.T) {
	e, _, _ := newTunJoinerE2E(t)
	joiner := e.AddNode(NodeTypeTunJoiner, "joiner")
	default1 := e.AddNode(testTunProviderNodeType, "default-1")
	default2 := e.AddNode(testTunProviderNodeType, "default-2")

	e.MustAddTypedLinkByPortName(TypeTun, joiner, "left", "Default", default1, "right", "Tun")
	_, _, err := e.Engine.AddLink(E2EWorkspace, e.LeftPort(joiner, "Default"), e.RightPort(default2, "Tun"))
	if !errors.Is(err, ErrTunPortBusy) {
		t.Fatalf("second Default link error = %v, want ErrTunPortBusy", err)
	}
}
