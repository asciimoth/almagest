package lib

import (
	"context"
	"errors"
	"testing"

	"github.com/asciimoth/bufpool"
	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/gonnect"
	"github.com/asciimoth/gonnect/sysnet"
	"github.com/asciimoth/pasta/pasta"
	"github.com/asciimoth/pasta/pasta/std"
)

type outNetReceiver struct {
	pasta.BasicNode
	w      *pasta.Workspace
	id     uint64
	in     uint64
	events chan NetworkPayload
}

func (n *outNetReceiver) OnInit(w *pasta.Workspace, _ pasta.Logger, id uint64, _ string, _ *pasta.NodeInitData, _, _, _, _ bool) error {
	n.w = w
	n.id = id
	port, err := w.AddPort(pasta.Port{
		Node:      id,
		Direction: "right",
		Name:      "Network",
		Types:     []string{TypeNetwork},
	})
	if err != nil {
		return err
	}
	n.in = port
	return nil
}

func (n *outNetReceiver) PreLinkAdd(port uint64, linkType, portDirection string) error {
	if port != n.in || portDirection != "right" || linkType != TypeNetwork {
		return pasta.LinkTypeErr(linkType)
	}
	return nil
}

func (n *outNetReceiver) OnEvent(event pasta.Event, linkType string, _ []string, receiverPortDirection string) error {
	if event.ReceiverPort != n.in || receiverPortDirection != "right" || linkType != TypeNetwork {
		return nil
	}
	payload, ok := event.Payload.(NetworkPayload)
	if !ok {
		return nil
	}
	n.events <- payload
	return nil
}

type captureHTTPServerClass struct {
	engine *engine
	node   **httpServerNode
}

func (captureHTTPServerClass) ClassName() string        { return "test/HTTPServer" }
func (captureHTTPServerClass) ShortDescription() string { return "HTTP server test node" }
func (captureHTTPServerClass) LongDescription() string  { return "HTTP server test node" }
func (captureHTTPServerClass) DefaultNodeParams() pasta.NodeClassParams {
	return httpServerClass{}.DefaultNodeParams()
}
func (c captureHTTPServerClass) NewNode(cfg configer.Config, _ ...*pasta.NodeClassState) (pasta.Node, error) {
	node := &httpServerNode{
		host:     readConfigString(cfg, "host", DefaultHTTPServerHost),
		port:     readConfigInt(cfg, "port", DefaultHTTPServerPort),
		response: readConfigString(cfg, "response", ""),
		engine:   c.engine,
	}
	if c.node != nil {
		*c.node = node
	}
	return node, nil
}

func TestOutNetworkRequestsSysRebuildOnInit(t *testing.T) {
	pool := bufpool.NewTestDebugPool(t)
	defer pool.Close()

	sys := newRecordingSysBuilder()
	engineIface, err := BuildEngine(
		context.Background(),
		configer.NewMemory(nil),
		NewDebugLogFactory(1024),
		sys,
		nil,
		pool,
		true,
	)

	if err != nil {
		t.Fatalf("BuildEngine error = %v, want nil", err)
	}
	defer func() {
		if err := engineIface.Close(); err != nil {
			t.Errorf("engine.Close error = %v, want nil", err)
		}
	}()

	if _, err := engineIface.AddNode("main", NodeTypeOutNetwork, "out"); err != nil {
		t.Fatalf("AddNode OutNetwork error = %v, want nil", err)
	}

	if len(sys.builds) != 2 {
		t.Fatalf("sys build count = %d, want 2", len(sys.builds))
	}
}

func TestOutNetworkWaitsForSuccessfulSysBuildBeforeSending(t *testing.T) {
	w := pasta.NewWorkspace(NewDebugLogFactory(1024))
	defer w.Close()
	if err := w.AddNodeClass(outNetClass{}); err != nil {
		t.Fatalf("AddNodeClass OutNetwork: %v", err)
	}
	outID, err := w.AddNodeByClass(NodeTypeOutNetwork, "out")
	if err != nil {
		t.Fatalf("AddNodeByClass OutNetwork: %v", err)
	}
	out, ok := w.NodeSnapshot(outID)
	if !ok || len(out.LeftPorts) != 1 {
		t.Fatalf("OutNetwork snapshot = %#v, %v; want one left port", out, ok)
	}

	receiver := &outNetReceiver{events: make(chan NetworkPayload, 2)}
	if _, err := w.AddNode(receiver, "test/OutNetworkReceiver", "receiver"); err != nil {
		t.Fatalf("AddNode receiver: %v", err)
	}
	link, _, err := w.AddLink(out.LeftPorts[0], receiver.in)
	if err != nil {
		t.Fatalf("AddLink: %v", err)
	}
	assertNoOutNetEvent(t, receiver.events)

	w.EmitEvent(receiver.id, link, std.RequestValue{})
	assertNoOutNetEvent(t, receiver.events)

	base := gonnect.NewLoopbackNetwok()
	w.SendInbox(pasta.InboxMessage{
		ReceiverNode: outID,
		Payload: SysBuildInboxMessage{
			System: &sysnet.System{OutNet: base},
		},
	})

	payload := receiveOutNetPayload(t, receiver.events)
	if got := payload.Network.(*gonnect.DetachedNetwork).GetWrapped(); got != base {
		t.Fatalf("wrapped network = %T %p, want base %p", got, got, base)
	}
}

func TestOutNetworkSendsCachedNetworkToNewLinks(t *testing.T) {
	w := pasta.NewWorkspace(NewDebugLogFactory(1024))
	defer w.Close()
	if err := w.AddNodeClass(outNetClass{}); err != nil {
		t.Fatalf("AddNodeClass OutNetwork: %v", err)
	}
	outID, err := w.AddNodeByClass(NodeTypeOutNetwork, "out")
	if err != nil {
		t.Fatalf("AddNodeByClass OutNetwork: %v", err)
	}
	out, ok := w.NodeSnapshot(outID)
	if !ok || len(out.LeftPorts) != 1 {
		t.Fatalf("OutNetwork snapshot = %#v, %v; want one left port", out, ok)
	}

	base := gonnect.NewLoopbackNetwok()
	w.SendInbox(pasta.InboxMessage{
		ReceiverNode: outID,
		Payload:      SysBuildInboxMessage{System: &sysnet.System{OutNet: base}},
	})

	receiver := &outNetReceiver{events: make(chan NetworkPayload, 2)}
	if _, err := w.AddNode(receiver, "test/OutNetworkReceiver", "receiver"); err != nil {
		t.Fatalf("AddNode receiver: %v", err)
	}
	if _, _, err := w.AddLink(out.LeftPorts[0], receiver.in); err != nil {
		t.Fatalf("AddLink: %v", err)
	}

	payload := receiveOutNetPayload(t, receiver.events)
	if got := payload.Network.(*gonnect.DetachedNetwork).GetWrapped(); got != base {
		t.Fatalf("wrapped network = %T %p, want base %p", got, got, base)
	}
}

func TestOutNetworkSendsCachedNetworkToHTTPServerOnLinkAdd(t *testing.T) {
	w := pasta.NewWorkspace(NewDebugLogFactory(1024))
	defer w.Close()
	if err := w.AddNodeClass(outNetClass{}); err != nil {
		t.Fatalf("AddNodeClass OutNetwork: %v", err)
	}
	ctx := t.Context()
	var server *httpServerNode
	if err := w.AddNodeClass(captureHTTPServerClass{
		engine: &engine{ctx: ctx},
		node:   &server,
	}); err != nil {
		t.Fatalf("AddNodeClass HTTPServer: %v", err)
	}

	outID, err := w.AddNodeByClass(NodeTypeOutNetwork, "out")
	if err != nil {
		t.Fatalf("AddNodeByClass OutNetwork: %v", err)
	}
	out, ok := w.NodeSnapshot(outID)
	if !ok || len(out.LeftPorts) != 1 {
		t.Fatalf("OutNetwork snapshot = %#v, %v; want one left port", out, ok)
	}

	base := gonnect.NewLoopbackNetwok()
	w.SendInbox(pasta.InboxMessage{
		ReceiverNode: outID,
		Payload:      SysBuildInboxMessage{System: &sysnet.System{OutNet: base}},
	})

	serverID, err := w.AddNodeByClass("test/HTTPServer", "server")
	if err != nil {
		t.Fatalf("AddNodeByClass HTTPServer: %v", err)
	}
	if server == nil {
		t.Fatal("captured HTTPServer node = nil")
	}
	serverSnapshot, ok := w.NodeSnapshot(serverID)
	if !ok || len(serverSnapshot.RightPorts) != 1 {
		t.Fatalf("HTTPServer snapshot = %#v, %v; want one right port", serverSnapshot, ok)
	}

	if _, _, err := w.AddLink(out.LeftPorts[0], serverSnapshot.RightPorts[0]); err != nil {
		t.Fatalf("AddLink: %v", err)
	}

	if server.network == nil {
		t.Fatal("server.network = nil, want cached OutNetwork network")
	}
	if got := server.network.(*gonnect.DetachedNetwork).GetWrapped(); got != base {
		t.Fatalf("server wrapped network = %T %p, want base %p", got, got, base)
	}
}

func TestEngineOutNetworkSendsCachedNetworkToHTTPServerOnLinkAdd(t *testing.T) {
	pool := bufpool.NewTestDebugPool(t)
	defer pool.Close()

	sys := newRecordingSysBuilder()
	var server *httpServerNode
	engineIface, err := BuildEngine(
		context.Background(),
		configer.NewMemory(nil),
		NewDebugLogFactory(1024),
		sys,
		[]pasta.NodeClass{captureHTTPServerClass{node: &server}},
		pool,
		true,
	)
	if err != nil {
		t.Fatalf("BuildEngine error = %v, want nil", err)
	}
	defer func() {
		if err := engineIface.Close(); err != nil {
			t.Errorf("engine.Close error = %v, want nil", err)
		}
	}()
	engine := engineIface.(*engine)

	outID, err := engine.AddNode("main", NodeTypeOutNetwork, "out")
	if err != nil {
		t.Fatalf("AddNode OutNetwork: %v", err)
	}
	base := sys.systems[len(sys.systems)-1].OutNet
	out, ok := engine.NodeSnapshot("main", outID)
	if !ok || len(out.LeftPorts) != 1 {
		t.Fatalf("OutNetwork snapshot = %#v, %v; want one left port", out, ok)
	}

	serverID, err := engine.AddNode("main", "test/HTTPServer", "server")
	if err != nil {
		t.Fatalf("AddNode HTTPServer: %v", err)
	}
	if server == nil {
		t.Fatal("captured HTTPServer node = nil")
	}
	server.engine = engine
	serverSnapshot, ok := engine.NodeSnapshot("main", serverID)
	if !ok || len(serverSnapshot.RightPorts) != 1 {
		t.Fatalf("HTTPServer snapshot = %#v, %v; want one right port", serverSnapshot, ok)
	}

	if _, _, err := engine.AddLink("main", out.LeftPorts[0], serverSnapshot.RightPorts[0]); err != nil {
		t.Fatalf("AddLink: %v", err)
	}

	if server.network == nil {
		t.Fatal("server.network = nil, want cached OutNetwork network")
	}
	if got := server.network.(*gonnect.DetachedNetwork).GetWrapped(); got != base {
		t.Fatalf("server wrapped network = %T %p, want base %p", got, got, base)
	}
}

func TestOutNetworkErrorSendsRejectAndSuppressesNewLinksUntilNextSuccess(t *testing.T) {
	w := pasta.NewWorkspace(NewDebugLogFactory(1024))
	defer w.Close()
	if err := w.AddNodeClass(outNetClass{}); err != nil {
		t.Fatalf("AddNodeClass OutNetwork: %v", err)
	}
	outID, err := w.AddNodeByClass(NodeTypeOutNetwork, "out")
	if err != nil {
		t.Fatalf("AddNodeByClass OutNetwork: %v", err)
	}
	out, ok := w.NodeSnapshot(outID)
	if !ok || len(out.LeftPorts) != 1 {
		t.Fatalf("OutNetwork snapshot = %#v, %v; want one left port", out, ok)
	}

	first := &outNetReceiver{events: make(chan NetworkPayload, 4)}
	if _, err := w.AddNode(first, "test/OutNetworkReceiver", "first"); err != nil {
		t.Fatalf("AddNode first receiver: %v", err)
	}
	if _, _, err := w.AddLink(out.LeftPorts[0], first.in); err != nil {
		t.Fatalf("AddLink first: %v", err)
	}

	base := gonnect.NewLoopbackNetwok()
	w.SendInbox(pasta.InboxMessage{
		ReceiverNode: outID,
		Payload:      SysBuildInboxMessage{System: &sysnet.System{OutNet: base}},
	})
	_ = receiveOutNetPayload(t, first.events)

	w.SendInbox(pasta.InboxMessage{
		ReceiverNode: outID,
		Payload:      SysBuildInboxMessage{Err: errors.New("sysbuild failed")},
	})
	reject := receiveOutNetPayload(t, first.events)
	if _, ok := reject.Network.(*gonnect.DetachedNetwork).GetWrapped().(*gonnect.RejectNetwork); !ok {
		t.Fatalf("wrapped network after error = %T, want *gonnect.RejectNetwork", reject.Network.(*gonnect.DetachedNetwork).GetWrapped())
	}

	second := &outNetReceiver{events: make(chan NetworkPayload, 4)}
	if _, err := w.AddNode(second, "test/OutNetworkReceiver", "second"); err != nil {
		t.Fatalf("AddNode second receiver: %v", err)
	}
	if _, _, err := w.AddLink(out.LeftPorts[0], second.in); err != nil {
		t.Fatalf("AddLink second: %v", err)
	}
	assertNoOutNetEvent(t, second.events)

	next := gonnect.NewLoopbackNetwok()
	w.SendInbox(pasta.InboxMessage{
		ReceiverNode: outID,
		Payload:      SysBuildInboxMessage{System: &sysnet.System{OutNet: next}},
	})

	firstPayload := receiveOutNetPayload(t, first.events)
	if got := firstPayload.Network.(*gonnect.DetachedNetwork).GetWrapped(); got != next {
		t.Fatalf("first wrapped network = %T %p, want next %p", got, got, next)
	}
	secondPayload := receiveOutNetPayload(t, second.events)
	if got := secondPayload.Network.(*gonnect.DetachedNetwork).GetWrapped(); got != next {
		t.Fatalf("second wrapped network = %T %p, want next %p", got, got, next)
	}
}

func receiveOutNetPayload(t *testing.T, events <-chan NetworkPayload) NetworkPayload {
	t.Helper()
	select {
	case payload := <-events:
		if payload.Network == nil {
			t.Fatal("payload.Network = nil, want network")
		}
		return payload
	default:
		t.Fatal("no network payload received")
		return NetworkPayload{}
	}
}

func assertNoOutNetEvent(t *testing.T, events <-chan NetworkPayload) {
	t.Helper()
	select {
	case payload := <-events:
		t.Fatalf("unexpected network payload: %#v", payload)
	default:
	}
}
