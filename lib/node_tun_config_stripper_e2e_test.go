package almagest

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/formular"
	"github.com/asciimoth/pasta/pasta"
	"github.com/asciimoth/pasta/pasta/std"
)

const (
	testTunConfigRecorderNodeType = "test/TunConfigRecorder"
	testTunConfigEmitterNodeType  = "test/TunConfigEmitter"
)

func TestTunConfigStripperE2EVTunHTTPClientServer(t *testing.T) {
	f := setupTunConfigStripperVTunHTTPE2E(t)
	f.assertClientRequestWorks(f.client)
	f.assertServerLog(f.server, "GET /")
	f.WaitNodeLabel(f.clientStripper, "stripping config")
	f.WaitNodeLabel(f.serverStripper, "stripping config")
	f.WaitNodeLabel(f.copy, "copying")
}

func TestTunConfigStripperE2EDropsTunConfigButPassesRequests(t *testing.T) {
	e, recorders := newTunConfigStripperConfigE2E(t)
	recorder := e.AddNode(testTunConfigRecorderNodeType, "recorder")
	stripper := e.AddNode(NodeTypeTunConfigStripper, "stripper")
	emitter := e.AddNode(testTunConfigEmitterNodeType, "emitter")

	e.MustAddTypedLinkByPortName(TypeTun, stripper, "left", "Tun", recorder, "right", "Tun")
	e.MustAddTypedLinkByPortName(TypeTun, emitter, "left", "Tun", stripper, "right", "Tun")
	e.WaitNodeLabel(stripper, "stripping config")

	recorders.assertRequestReceived(t, recorder)
	recorders.assertNoConfigReceived(t, recorder)
}

type tunConfigStripperVTunHTTPE2EFixture struct {
	*httpE2E

	copy           uint64
	clientStripper uint64
	serverStripper uint64
	vtunA          uint64
	vtunB          uint64
	server         uint64
	client         uint64
	port           int
}

func setupTunConfigStripperVTunHTTPE2E(t *testing.T) tunConfigStripperVTunHTTPE2EFixture {
	t.Helper()
	e := newHTTPE2E(t)
	f := tunConfigStripperVTunHTTPE2EFixture{httpE2E: e, port: httpTestPort()}
	f.copy = e.AddNode(NodeTypeTunCopy, "copy")
	f.clientStripper = e.AddNode(NodeTypeTunConfigStripper, "client-stripper")
	f.serverStripper = e.AddNode(NodeTypeTunConfigStripper, "server-stripper")
	f.vtunA = e.AddNode(NodeTypeVTun, "vtun-a")
	f.vtunB = e.AddNode(NodeTypeVTun, "vtun-b")
	f.server = e.addHTTPServerOnHost("server", tunCopyE2EServerAddr, f.port)
	f.client = e.addHTTPClientToURL("client", fmt.Sprintf("http://%s:%d/", tunCopyE2EServerAddr, f.port))
	e.SubscribeMenus(f.vtunA, f.vtunB, f.server, f.client)
	e.waitClientRequestField(f.client, "url", fmt.Sprintf("http://%s:%d/", tunCopyE2EServerAddr, f.port))

	e.applyTunCopyVTunConfig(f.vtunA, "client-vtun", tunCopyE2EClientAddr+"/24")
	e.applyTunCopyVTunConfig(f.vtunB, "server-vtun", tunCopyE2EServerAddr+"/24")
	e.connectTunByPortName(f.clientStripper, "left", "Tun", f.vtunA, "right", "Tun")
	e.connectTunByPortName(f.copy, "left", "TunA", f.clientStripper, "right", "Tun")
	e.connectTunByPortName(f.serverStripper, "left", "Tun", f.vtunB, "right", "Tun")
	e.connectTunByPortName(f.copy, "left", "TunB", f.serverStripper, "right", "Tun")
	e.connectByPortName(f.vtunA, "left", "Network", f.client, "right", "Network")
	e.connectByPortName(f.vtunB, "left", "Network", f.server, "right", "Network")

	e.WaitNodeLabel(f.copy, "copying")
	e.WaitNodeLabel(f.clientStripper, "stripping config")
	e.WaitNodeLabel(f.serverStripper, "stripping config")
	e.WaitNodeLabel(f.vtunA, "client-vtun")
	e.WaitNodeLabel(f.vtunB, "server-vtun")
	e.waitServerListening(f.server, fmt.Sprintf("%s:%d", tunCopyE2EServerAddr, f.port))
	return f
}

func newTunConfigStripperConfigE2E(t *testing.T) (*NodeE2E, *tunConfigRecorderRegistry) {
	t.Helper()

	pool := newE2ETestDebugPool(t)
	logf := NewDebugLogFactory(16384)
	sys := newE2EDebugSystem()
	recorders := newTunConfigRecorderRegistry()
	engine, err := BuildEngine(
		context.Background(),
		configer.NewMemory(nil),
		logf,
		sys,
		[]pasta.NodeClass{
			tunConfigRecorderClass{registry: recorders},
			tunConfigEmitterClass{},
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
	return e, recorders
}

type tunConfigRecorderRegistry struct {
	mu    sync.Mutex
	nodes map[uint64]*tunConfigRecorderNode
}

func newTunConfigRecorderRegistry() *tunConfigRecorderRegistry {
	return &tunConfigRecorderRegistry{nodes: map[uint64]*tunConfigRecorderNode{}}
}

func (r *tunConfigRecorderRegistry) register(id uint64, node *tunConfigRecorderNode) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.nodes[id] = node
}

func (r *tunConfigRecorderRegistry) assertRequestReceived(t *testing.T, id uint64) {
	t.Helper()
	var requests int
	Eventually(t, func() bool {
		r.mu.Lock()
		defer r.mu.Unlock()
		node := r.nodes[id]
		if node == nil {
			return false
		}
		node.mu.Lock()
		defer node.mu.Unlock()
		requests = node.requests
		return requests > 0
	}, "TunConfig recorder %d request", id)
}

func (r *tunConfigRecorderRegistry) assertNoConfigReceived(t *testing.T, id uint64) {
	t.Helper()
	time.Sleep(25 * time.Millisecond)
	r.mu.Lock()
	defer r.mu.Unlock()
	node := r.nodes[id]
	if node == nil {
		t.Fatalf("TunConfig recorder %d missing", id)
	}
	node.mu.Lock()
	defer node.mu.Unlock()
	if len(node.configs) != 0 {
		t.Fatalf("recorder configs = %#v, want none", node.configs)
	}
}

type tunConfigRecorderClass struct {
	registry *tunConfigRecorderRegistry
}

func (tunConfigRecorderClass) ClassName() string        { return testTunConfigRecorderNodeType }
func (tunConfigRecorderClass) ShortDescription() string { return "test TunConfig recorder" }
func (tunConfigRecorderClass) LongDescription() string {
	return "Test-only right-directed almagest/tun node that records requests and TunConfig payloads."
}
func (tunConfigRecorderClass) DefaultNodeParams() pasta.NodeClassParams {
	return pasta.NodeClassParams{PrimaryType: TypeTun, InitialPorts: []pasta.Port{
		TunPort("right", "Tun"),
	}}
}
func (c tunConfigRecorderClass) NewNode(configer.Config, ...*pasta.NodeClassState) (pasta.Node, error) {
	return &tunConfigRecorderNode{registry: c.registry}, nil
}

type tunConfigRecorderNode struct {
	pasta.BasicNode

	registry *tunConfigRecorderRegistry
	mu       sync.Mutex
	out      uint64
	requests int
	configs  []TunConfig
}

func (n *tunConfigRecorderNode) OnInit(w *pasta.Workspace, _ pasta.Logger, id uint64, _ string, restored *pasta.NodeInitData, _, _, _, _ bool) error {
	if restored != nil && len(restored.RightPorts) > 0 {
		n.out = restored.RightPorts[0]
	}
	if n.registry != nil {
		n.registry.register(id, n)
	}
	return w.SetNodePrimary(id, TypeTun)
}

func (n *tunConfigRecorderNode) PreLinkAdd(port uint64, linkType, portDirection string) error {
	if port != n.out || portDirection != "right" {
		return pasta.LinkTypeErr(linkType)
	}
	return TunPreLinkAdd(nil, 0, linkType)
}

func (n *tunConfigRecorderNode) OnEvent(event pasta.Event, linkType string, _ []string, receiverPortDirection string) error {
	if event.ReceiverPort != n.out || receiverPortDirection != "right" || linkType != TypeTun {
		return nil
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if std.IsRequest(event.Payload) {
		n.requests++
		return nil
	}
	if cfg, ok := event.Payload.(TunConfig); ok {
		n.configs = append(n.configs, cfg.Clone())
	}
	return nil
}

type tunConfigEmitterClass struct{}

func (tunConfigEmitterClass) ClassName() string        { return testTunConfigEmitterNodeType }
func (tunConfigEmitterClass) ShortDescription() string { return "test TunConfig emitter" }
func (tunConfigEmitterClass) LongDescription() string {
	return "Test-only left-directed almagest/tun node that emits a TunConfig and a request when linked."
}
func (tunConfigEmitterClass) DefaultNodeParams() pasta.NodeClassParams {
	return pasta.NodeClassParams{PrimaryType: TypeTun, InitialPorts: []pasta.Port{
		TunPort("left", "Tun"),
	}}
}
func (tunConfigEmitterClass) NewNode(configer.Config, ...*pasta.NodeClassState) (pasta.Node, error) {
	return &tunConfigEmitterNode{}, nil
}

type tunConfigEmitterNode struct {
	pasta.BasicNode

	w  *pasta.Workspace
	id uint64
	in uint64
}

func (n *tunConfigEmitterNode) OnInit(w *pasta.Workspace, _ pasta.Logger, id uint64, _ string, restored *pasta.NodeInitData, _, _, _, _ bool) error {
	n.w = w
	n.id = id
	if restored != nil && len(restored.LeftPorts) > 0 {
		n.in = restored.LeftPorts[0]
	}
	return n.w.SetNodePrimary(n.id, TypeTun)
}

func (n *tunConfigEmitterNode) PreLinkAdd(port uint64, linkType, portDirection string) error {
	if port != n.in || portDirection != "left" {
		return pasta.LinkTypeErr(linkType)
	}
	return TunPreLinkAdd(nil, 0, linkType)
}

func (n *tunConfigEmitterNode) OnLinkAdd(link, port uint64, linkType, portDirection string) error {
	if port != n.in || portDirection != "left" || linkType != TypeTun {
		return nil
	}
	n.w.EmitEvent(n.id, link, TunConfig{Addrs: []string{"10.221.0.2/32"}, MTU: 1300})
	std.Request(n.w, n.id, link)
	return nil
}
