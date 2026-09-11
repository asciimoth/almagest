package dnsnodes_test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	almagest "github.com/asciimoth/almagest/lib"
	httpnodes "github.com/asciimoth/almagest/lib/http"
	"github.com/asciimoth/almagest/lib/links"
	"github.com/asciimoth/almagest/lib/logging"
	"github.com/asciimoth/almagest/lib/nodekit"
	"github.com/asciimoth/almagest/lib/storage"
	"github.com/asciimoth/almagest/lib/testkit"
	"github.com/asciimoth/almagest/lib/testkit/e2etest"
	"github.com/asciimoth/almagest/lib/testkit/httpe2etest"
	"github.com/asciimoth/bufpool"
	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/formular"
	"github.com/asciimoth/gonnect/sysnet"
	sysnetdebug "github.com/asciimoth/gonnect/sysnet/debug"
	"github.com/asciimoth/gonnect/tun"
	"github.com/asciimoth/pasta/pasta"
	"github.com/asciimoth/pasta/pasta/std"
)

const E2EWorkspace = e2etest.E2EWorkspace

type (
	Engine  = almagest.Engine
	NodeE2E = e2etest.NodeE2E
)

func BuildEngine(
	ctx context.Context,
	cfg configer.Config,
	logf logging.LogFactory,
	sys sysnet.System,
	kv storage.KVManager,
	dns storage.DnsCacheManager,
	secrets storage.SecretsStorage,
	tlsConfigs storage.TlsConfigStorage,
	extra []pasta.NodeClass,
	pool bufpool.Pool,
	debug bool,
) (almagest.Engine, error) {
	return almagest.BuildEngine(ctx, cfg, logf, sys, kv, dns, secrets, tlsConfigs, extra, pool, debug)
}

func NewDebugLogFactory(buffer int) *logging.DebugLogFactory {
	return logging.NewDebugLogFactory(buffer)
}

func NewNodeE2E(t *testing.T) *NodeE2E {
	return e2etest.NewNodeE2E(t)
}

func NewNodeE2EFromEngine(t *testing.T, engine almagest.Engine, logf *logging.DebugLogFactory, sys *sysnetdebug.System) *NodeE2E {
	return e2etest.NewNodeE2EFromEngine(t, engine, logf, sys, nil)
}

func newE2EDebugSystem() *sysnetdebug.System {
	return e2etest.NewDebugSystem()
}

func newE2ETestDebugPool(t *testing.T) *e2etest.DebugPool {
	return e2etest.NewTestDebugPool(t)
}

func httpTestPort() int {
	return e2etest.TestPort()
}

func Eventually(t *testing.T, condition func() bool, format string, args ...any) {
	testkit.Eventually(t, condition, format, args...)
}

func FormMessageBase(messageType string, node uint64) formular.MessageBase {
	return testkit.FormMessageBase(messageType, node)
}

type httpE2E struct {
	*NodeE2E
	*httpe2etest.HTTPOps
}

func newHTTPE2E(t *testing.T) *httpE2E {
	t.Helper()
	return newHTTPE2EFromNodeE2E(NewNodeE2E(t))
}

func newHTTPE2EFromNodeE2E(e *NodeE2E) *httpE2E {
	return &httpE2E{NodeE2E: e, HTTPOps: httpe2etest.NewHTTPOps(e)}
}

func (e *httpE2E) addHTTPServerOnHost(name, host string, port int) uint64 {
	e.T.Helper()
	id := e.AddNode(httpnodes.NodeTypeHTTPServer, name)
	e.Engine.SendNodeFormularMsg(E2EWorkspace, id, formular.FormApplyMessage{
		MessageBase: testkit.FormMessageBase(formular.MessageFormApply, id),
		BlockID:     "listen",
		Values: map[string]any{
			"host": host,
			"port": port,
		},
	})
	e.WaitNodeLabel(id, fmt.Sprintf("%s:%d", host, port))
	return id
}

func (e *httpE2E) addHTTPClientToURL(name, url string) uint64 {
	e.T.Helper()
	id := e.AddNode(httpnodes.NodeTypeHTTPClient, name)
	e.Engine.SendNodeFormularMsg(E2EWorkspace, id, formular.FieldUpdateMessage{
		MessageBase: testkit.FormMessageBase(formular.MessageFieldUpdate, id),
		Field:       formular.FieldRef{BlockID: "request", FieldID: "url"},
		Value:       url,
	})
	return id
}

func (e *httpE2E) waitServerListening(server uint64, address string) {
	e.WaitServerListening(server, address)
}

func (e *httpE2E) waitClientRequestField(client uint64, field, want string) {
	e.WaitClientRequestField(client, field, want)
}

func (e *httpE2E) assertClientRequestWorks(client uint64) {
	e.AssertClientRequestWorks(client)
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
		t.Fatal("timed out waiting for tun packet")
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

const testTunProviderNodeType = "test/TunProvider"

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

func (r *testTunProviderRegistry) requestCount(t *testing.T, id uint64) int {
	t.Helper()
	r.mu.Lock()
	node := r.nodes[id]
	r.mu.Unlock()
	if node == nil {
		t.Fatalf("test tun provider %d is not registered", id)
	}
	node.W.Lock()
	defer node.W.Unlock()
	return node.requests
}

func (r *testTunProviderRegistry) closeOutput(t *testing.T, id, link uint64) {
	t.Helper()
	r.mu.Lock()
	node := r.nodes[id]
	r.mu.Unlock()
	if node == nil {
		t.Fatalf("test tun provider %d is not registered", id)
	}
	node.W.Lock()
	defer node.W.Unlock()
	node.outputs.Close(link)
}

func (r *testTunProviderRegistry) ignoreRequests(t *testing.T, id uint64, count int) {
	t.Helper()
	r.mu.Lock()
	node := r.nodes[id]
	r.mu.Unlock()
	if node == nil {
		t.Fatalf("test tun provider %d is not registered", id)
	}
	node.W.Lock()
	defer node.W.Unlock()
	node.ignoredRequests = count
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
	return pasta.NodeClassParams{PrimaryType: links.TypeTun, InitialPorts: []pasta.Port{
		links.TunPort("right", "Tun"),
	}}
}
func (c testTunProviderClass) NewNode(configer.Config, ...*pasta.NodeClassState) (pasta.Node, error) {
	return &testTunProviderNode{registry: c.registry}, nil
}

type testTunProviderNode struct {
	nodekit.Base

	registry *testTunProviderRegistry
	out      uint64

	source          tun.Tun
	peer            tun.Tun
	outputs         links.TunOutputCache
	requests        int
	ignoredRequests int
}

func (n *testTunProviderNode) OnInit(w *pasta.Workspace, _ pasta.Logger, id uint64, _ string, restored *pasta.NodeInitData, _, _, _, _ bool) error {
	n.InitBaseNode(w, nil, id)
	n.out = nodekit.RestoredRight(restored, 0)
	if n.registry != nil {
		n.registry.register(id, n)
	}
	if err := n.SetPrimary(links.TypeTun); err != nil {
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
	return links.TunPreLinkAdd(n.W, port, linkType)
}

func (n *testTunProviderNode) OnLinkAdd(link, port uint64, linkType, portDirection string) error {
	if port == n.out && portDirection == "right" && linkType == links.TypeTun {
		n.sendTunToLink(link)
	}
	return nil
}

func (n *testTunProviderNode) OnLinkRemoved(link uint64, port uint64, linkType, portDirection string) error {
	if port == n.out && portDirection == "right" && linkType == links.TypeTun {
		n.outputs.Close(link)
		n.closePipe()
	}
	return nil
}

func (n *testTunProviderNode) OnEvent(event pasta.Event, linkType string, _ []string, receiverPortDirection string) error {
	if event.ReceiverPort != n.out || receiverPortDirection != "right" || linkType != links.TypeTun || !std.IsRequest(event.Payload) {
		return nil
	}
	n.requests++
	if n.ignoredRequests > 0 {
		n.ignoredRequests--
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
	n.outputs.SendLocked(n.W, n.ID, link, n.source, nil)
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
