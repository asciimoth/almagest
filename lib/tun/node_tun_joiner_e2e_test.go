package tunnodes_test

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
	tunJoinerE2EServerAddr2          = "10.210.148.2"
	tunJoinerE2EOverlayServerAddr    = "100.97.143.122"
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

func TestTunJoinerE2ERoutesSharedAddressTCPAndUDPFlowsToTheirOwners(t *testing.T) {
	e, providers, sinks := newTunJoinerE2E(t)
	joiner := e.AddNode(NodeTypeTunJoiner, "joiner")
	defaultProvider := e.AddNode(testTunProviderNodeType, "default")
	secondaryProvider := e.AddNode(testTunProviderNodeType, "secondary")
	sink := e.AddNode(testTunSinkNodeType, "sink")

	e.MustAddTypedLinkByPortName(TypeTun, joiner, "left", "Default", defaultProvider, "right", "Tun")
	e.MustAddTypedLinkByPortName(TypeTun, joiner, "left", "Secondary", secondaryProvider, "right", "Tun")
	e.MustAddTypedLinkByPortName(TypeTun, sink, "left", "Tun", joiner, "right", "Tun")
	e.WaitNodeLabel(joiner, "joined default + 1 secondary")

	defaultPeer := providers.peer(t, defaultProvider)
	secondaryPeer := providers.peer(t, secondaryProvider)
	output := sinks.tun(t, sink)
	sharedIP := [4]byte{10, 90, 0, 1}
	remoteIP := [4]byte{10, 210, 148, 1}

	flows := []struct {
		name       string
		owner      tun.Tun
		protocol   uint8
		localPort  uint16
		remotePort uint16
	}{
		{name: "default TCP", owner: defaultPeer, protocol: 6, localPort: 40001, remotePort: 443},
		{name: "default UDP", owner: defaultPeer, protocol: 17, localPort: 40002, remotePort: 53},
		{name: "secondary TCP", owner: secondaryPeer, protocol: 6, localPort: 41001, remotePort: 443},
		{name: "secondary UDP", owner: secondaryPeer, protocol: 17, localPort: 41002, remotePort: 51820},
	}
	for _, flow := range flows {
		outbound := testIPv4TransportPacket(
			sharedIP,
			remoteIP,
			flow.protocol,
			flow.localPort,
			flow.remotePort,
		)
		assertTunPacketTransferred(t, flow.owner, output, outbound)
	}

	// The secondary input sent the last packet from the shared address. Each
	// response must still return to the owner of its transport flow.
	for _, flow := range flows {
		flow := flow
		t.Run(flow.name, func(t *testing.T) {
			response := testIPv4TransportPacket(
				remoteIP,
				sharedIP,
				flow.protocol,
				flow.remotePort,
				flow.localPort,
			)
			assertTunPacketTransferred(t, output, flow.owner, response)
		})
	}
}

func TestTunJoinerE2EVTunSplitterSharedAddressRouting(t *testing.T) {
	for _, secondaryFirst := range []bool{false, true} {
		name := "default connected first"
		if secondaryFirst {
			name = "secondary connected first"
		}
		t.Run(name, func(t *testing.T) {
			f := setupTunJoinerSplitterSharedAddressE2E(t, secondaryFirst)
			f.assertParallelRequestsWork()
			f.WaitNodeLabel(f.joiner, "joined default + 1 secondary")
			f.WaitNodeLabel(f.routeSplitter, "splitting")
		})
	}
}

type tunJoinerSplitterSharedAddressE2EFixture struct {
	*httpE2E

	joiner        uint64
	routeSplitter uint64
	clients       []uint64
	responses     []string
}

func setupTunJoinerSplitterSharedAddressE2E(t *testing.T, secondaryFirst bool) tunJoinerSplitterSharedAddressE2EFixture {
	t.Helper()
	e := newHTTPE2E(t)
	f := tunJoinerSplitterSharedAddressE2EFixture{
		httpE2E:   e,
		clients:   make([]uint64, 0, 3),
		responses: []string{"default public", "overlay", "secondary public"},
	}
	port := httpTestPort()
	f.joiner = e.AddNode(NodeTypeTunJoiner, "joiner")
	f.routeSplitter = e.AddNode(NodeTypeTunSplitter, "route-splitter")
	publicSplitter := e.AddNode(NodeTypeTunSplitter, "public-splitter")
	applicationVTun := e.AddNode(NodeTypeVTun, "application-vtun")
	transportVTun := e.AddNode(NodeTypeVTun, "transport-vtun")
	overlayCopy := e.AddNode(NodeTypeTunCopy, "overlay-copy")
	overlayVTun := e.AddNode(NodeTypeVTun, "overlay-server-vtun")
	publicCopies := []uint64{
		e.AddNode(NodeTypeTunCopy, "public-copy-1"),
		e.AddNode(NodeTypeTunCopy, "public-copy-2"),
	}
	publicVTuns := []uint64{
		e.AddNode(NodeTypeVTun, "public-server-vtun-1"),
		e.AddNode(NodeTypeVTun, "public-server-vtun-2"),
	}
	serverAddrs := []string{
		tunJoinerE2EServerAddr,
		tunJoinerE2EOverlayServerAddr,
		tunJoinerE2EServerAddr2,
	}
	servers := make([]uint64, 0, len(serverAddrs))
	for i, address := range serverAddrs {
		response := e.addStringConstant(fmt.Sprintf("response-%d", i+1), f.responses[i])
		server := e.addHTTPServerOnHost(fmt.Sprintf("server-%d", i+1), address, port)
		e.MustAddTypedLinkByPortName(std.TypeString, response, "right", "output", server, "left", "Response")
		servers = append(servers, server)
		f.clients = append(f.clients, e.addHTTPClientToURL(
			fmt.Sprintf("client-%d", i+1),
			fmt.Sprintf("http://%s:%d/", address, port),
		))
	}
	e.SubscribeMenus(append(
		[]uint64{f.routeSplitter, publicSplitter, applicationVTun, transportVTun, overlayVTun},
		append(publicVTuns, append(servers, f.clients...)...)...,
	)...)
	for i, client := range f.clients {
		e.waitClientRequestField(client, "url", fmt.Sprintf("http://%s:%d/", serverAddrs[i], port))
	}

	applyTunSplitterE2ERules(e, f.routeSplitter, fmt.Sprintf(
		"SNET4 %s/32\nSLOT 2\n\nTRUE\nSLOT 1",
		tunJoinerE2EOverlayServerAddr,
	))
	applyTunSplitterE2ERules(e, publicSplitter, fmt.Sprintf(
		"SNET4 %s/32\nSLOT 1\n\nSNET4 %s/32\nSLOT 2",
		tunJoinerE2EServerAddr,
		tunJoinerE2EServerAddr2,
	))
	e.applyTunCopyVTunConfig(applicationVTun, "application-vtun", tunJoinerE2EDefaultClientAddr+"/24")
	e.applyTunCopyVTunConfig(transportVTun, "transport-vtun", tunJoinerE2EDefaultClientAddr+"/24")
	e.applyTunCopyVTunConfig(overlayVTun, "overlay-server-vtun", tunJoinerE2EOverlayServerAddr+"/24")
	e.applyTunCopyVTunConfig(publicVTuns[0], "public-server-vtun-1", tunJoinerE2EServerAddr+"/24")
	e.applyTunCopyVTunConfig(publicVTuns[1], "public-server-vtun-2", tunJoinerE2EServerAddr2+"/24")

	e.connectTunByPortName(f.routeSplitter, "left", "Tun", applicationVTun, "right", "Tun")
	connectDefault := func() {
		e.connectTunByPortName(f.joiner, "left", "Default", f.routeSplitter, "right", "slot 1")
	}
	connectSecondary := func() {
		e.connectTunByPortName(f.joiner, "left", "Secondary", transportVTun, "right", "Tun")
	}
	if secondaryFirst {
		connectSecondary()
		connectDefault()
	} else {
		connectDefault()
		connectSecondary()
	}
	e.connectTunByPortName(overlayCopy, "left", "TunA", f.routeSplitter, "right", "slot 2")
	e.connectTunByPortName(overlayCopy, "left", "TunB", overlayVTun, "right", "Tun")
	e.connectTunByPortName(publicSplitter, "left", "Tun", f.joiner, "right", "Tun")
	for i := range publicCopies {
		e.connectTunByPortName(publicCopies[i], "left", "TunA", publicSplitter, "right", fmt.Sprintf("slot %d", i+1))
		e.connectTunByPortName(publicCopies[i], "left", "TunB", publicVTuns[i], "right", "Tun")
	}

	e.connectByPortName(applicationVTun, "left", "Network", f.clients[0], "right", "Network")
	e.connectByPortName(applicationVTun, "left", "Network", f.clients[1], "right", "Network")
	e.connectByPortName(transportVTun, "left", "Network", f.clients[2], "right", "Network")
	e.connectByPortName(publicVTuns[0], "left", "Network", servers[0], "right", "Network")
	e.connectByPortName(overlayVTun, "left", "Network", servers[1], "right", "Network")
	e.connectByPortName(publicVTuns[1], "left", "Network", servers[2], "right", "Network")

	e.WaitNodeLabel(f.joiner, "joined default + 1 secondary")
	e.WaitNodeLabel(f.routeSplitter, "splitting")
	e.WaitNodeLabel(publicSplitter, "splitting")
	e.WaitNodeLabel(overlayCopy, "copying")
	for _, copy := range publicCopies {
		e.WaitNodeLabel(copy, "copying")
	}
	for i, server := range servers {
		e.waitServerListening(server, fmt.Sprintf("%s:%d", serverAddrs[i], port))
	}
	return f
}

func applyTunSplitterE2ERules(e *httpE2E, splitter uint64, rules string) {
	e.T.Helper()
	e.Engine.SendNodeFormularMsg(E2EWorkspace, splitter, formular.FormApplyMessage{
		MessageBase: FormMessageBase(formular.MessageFormApply, splitter),
		BlockID:     "config",
		Values:      map[string]any{"rules": rules},
	})
}

func (f tunJoinerSplitterSharedAddressE2EFixture) assertParallelRequestsWork() {
	f.T.Helper()
	start := make(chan struct{})
	errCh := make(chan error, len(f.clients))
	var wg sync.WaitGroup
	for i, client := range f.clients {
		client := client
		response := f.responses[i]
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			f.Engine.SendNodeFormularMsg(E2EWorkspace, client, formular.ButtonPressMessage{
				MessageBase: FormMessageBase(formular.MessageButtonPress, client),
				BlockID:     "request",
				ButtonID:    "send",
			})
			errCh <- f.waitClientResponseResult(client, "200 OK", response)
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
		nil,
		nil, nil,
		nil,

		[]pasta.NodeClass{
			testTunProviderClass{registry: providers},
			testTunSinkClass{registry: sinks},
		},
		pool,
		true)

	if err != nil {
		t.Fatalf("BuildEngine error = %v, want nil", err)
	}
	t.Cleanup(func() {
		if err := engine.Close(); err != nil {
			t.Errorf("engine.Close error = %v, want nil", err)
		}
	})

	return NewNodeE2EFromEngine(t, engine, logf, sys, nil), providers, sinks
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
		f.WaitNodeLabel(client, "200 OK")
	}
}

func (e *httpE2E) waitClientResponseResult(client uint64, status, response string) error {
	deadline := time.Now().Add(10 * time.Second)
	for {
		gotStatus := e.clientStatus(client)
		gotErr := e.clientResultField(client, "error")
		gotResp := e.clientResultField(client, "response")
		gotLabel := e.clientLabel(client)
		if gotStatus == "Status: "+status && gotErr == "" && gotResp == response && gotLabel == status {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf(
				"timed out waiting for client %d response %q %q; got status=%q error=%q response=%q label=%q",
				client,
				status,
				response,
				gotStatus,
				gotErr,
				gotResp,
				gotLabel,
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

func (r *testTunSinkRegistry) readyTunAfterPayloads(t *testing.T, id uint64, minimum int) tun.Tun {
	t.Helper()
	var out tun.Tun
	Eventually(t, func() bool {
		r.mu.Lock()
		node := r.nodes[id]
		r.mu.Unlock()
		if node == nil {
			return false
		}
		node.w.Lock()
		defer node.w.Unlock()
		out = node.tun
		return node.payloadCount >= minimum && TunIsReady(out)
	}, "test tun sink %d ready tun after %d payloads", id, minimum)
	return out
}

func (r *testTunSinkRegistry) payloadCount(id uint64) int {
	r.mu.Lock()
	node := r.nodes[id]
	r.mu.Unlock()
	if node == nil {
		return 0
	}
	node.w.Lock()
	defer node.w.Unlock()
	return node.payloadCount
}

func (r *testTunSinkRegistry) configure(t *testing.T, id uint64, cfg TunConfig) {
	t.Helper()
	r.mu.Lock()
	node := r.nodes[id]
	r.mu.Unlock()
	if node == nil {
		t.Fatalf("test tun sink %d is not registered", id)
	}

	node.w.Lock()
	defer node.w.Unlock()
	if node.tun == nil || node.link == 0 {
		t.Fatalf("test tun sink %d is not ready for config", id)
	}
	node.w.EmitEventLocked(node.id, node.link, cfg.Clone())
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
func (c testTunSinkClass) NewNode(cfg configer.Config, _ ...*pasta.NodeClassState) (pasta.Node, error) {
	node := &testTunSinkNode{registry: c.registry}
	if configer.Bool(cfg, configer.Path{"emitStartupConfig"}, false) {
		node.startupConfig = TunConfig{
			Addrs:  []string{"10.221.0.2/32"},
			Routes: []string{"0.0.0.0/0"},
			MTU:    1300,
		}
	}
	return node, nil
}

type testTunSinkNode struct {
	pasta.BasicNode

	registry     *testTunSinkRegistry
	w            *pasta.Workspace
	id           uint64
	in           uint64
	link         uint64
	tun          tun.Tun
	payloadCount int

	startupConfig TunConfig
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
	if err := n.w.SetNodePrimaryLocked(n.id, TypeTun); err != nil {
		return err
	}
	return n.w.SetNodeLabelLocked(n.id, "waiting")
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
		if !n.startupConfig.IsZero() {
			// Model a restored graph where a downstream config reaches the
			// provider before a later startup request.
			n.w.EmitEventLocked(n.id, link, n.startupConfig.Clone())
		}
		std.RequestLocked(n.w, n.id, link)
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
		return n.w.SetNodeLabelLocked(n.id, "waiting")
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
	n.payloadCount++
	return n.w.SetNodeLabelLocked(n.id, "ready")
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
		buf := make([]byte, offset+max(2048, len(packet)))
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

func readUntilTunPacketAsync(source tun.Tun, want []byte, maxReads int) <-chan error {
	result := make(chan error, 1)
	go func() {
		for range maxReads {
			offset := source.MRO()
			buf := make([]byte, offset+len(want))
			sizes := []int{0}
			count, err := source.Read([][]byte{buf}, sizes, offset)
			if err != nil {
				result <- err
				return
			}
			if count != 1 {
				result <- fmt.Errorf("read packet count %d, want 1", count)
				return
			}
			if sizes[0] == len(want) && bytes.Equal(buf[offset:offset+sizes[0]], want) {
				result <- nil
				return
			}
		}
		result <- errors.New("wanted packet did not arrive within read limit")
	}()
	return result
}

func assertMatchedTunPacketResult(t *testing.T, result <-chan error, scenario string) {
	t.Helper()
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("%s tun Read: %v", scenario, err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for %s packet", scenario)
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

func testIPv4TransportPacket(src, dst [4]byte, protocol uint8, srcPort, dstPort uint16) []byte {
	p := make([]byte, 24)
	p[0] = 0x45
	binary.BigEndian.PutUint16(p[2:4], uint16(len(p)))
	p[9] = protocol
	copy(p[12:16], src[:])
	copy(p[16:20], dst[:])
	binary.BigEndian.PutUint16(p[20:22], srcPort)
	binary.BigEndian.PutUint16(p[22:24], dstPort)
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

func TestTunJoinerE2ERejectsSecondOutputLink(t *testing.T) {
	e, _, _ := newTunJoinerE2E(t)
	joiner := e.AddNode(NodeTypeTunJoiner, "joiner")
	sink1 := e.AddNode(testTunSinkNodeType, "sink-1")
	sink2 := e.AddNode(testTunSinkNodeType, "sink-2")

	e.MustAddTypedLinkByPortName(TypeTun, sink1, "left", "Tun", joiner, "right", "Tun")
	_, _, err := e.Engine.AddLink(E2EWorkspace, e.LeftPort(sink2, "Tun"), e.RightPort(joiner, "Tun"))
	if !errors.Is(err, ErrTunPortBusy) {
		t.Fatalf("second output link error = %v, want ErrTunPortBusy", err)
	}
}
