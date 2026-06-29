package almagest

import (
	"context"
	"testing"

	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/gonnect"
	sysnetdebug "github.com/asciimoth/gonnect/sysnet/debug"
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
		NodeBase: NodeBase{engine: c.engine},
		host:     configer.String(cfg, configer.Path{"host"}, DefaultHTTPServerHost),
		port:     configer.Int(cfg, configer.Path{"port"}, DefaultHTTPServerPort),
		response: configer.String(cfg, configer.Path{"response"}, ""),
	}
	if c.node != nil {
		*c.node = node
	}
	return node, nil
}

func TestOutNetworkUsesSystemOutNetOnInit(t *testing.T) {
	pool := newE2ETestDebugPool(t)

	base := gonnect.NewLoopbackNetwok()
	sys := &sysnetdebug.System{OutNetwork: base}
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

	outID, err := engineIface.AddNode("main", NodeTypeOutNetwork, "out")
	if err != nil {
		t.Fatalf("AddNode OutNetwork error = %v, want nil", err)
	}

	engine := engineIface.(*engine)
	out, ok := engine.NodeSnapshot("main", outID)
	if !ok || len(out.LeftPorts) != 1 {
		t.Fatalf("OutNetwork snapshot = %#v, %v; want one left port", out, ok)
	}
	receiver := &outNetReceiver{events: make(chan NetworkPayload, 2)}
	if _, err := engine.mainWorkspace.AddNode(receiver, "test/OutNetworkReceiver", "receiver"); err != nil {
		t.Fatalf("AddNode receiver: %v", err)
	}
	if _, _, err := engine.AddLink("main", out.LeftPorts[0], receiver.in); err != nil {
		t.Fatalf("AddLink: %v", err)
	}
	payload := receiveOutNetPayload(t, receiver.events)
	if got := payload.Network.(*gonnect.DetachedNetwork).GetWrapped(); got != base {
		t.Fatalf("wrapped network = %T %p, want system OutNet %p", got, got, base)
	}
}

func TestOutNetworkSendsSystemOutNetOnReadyAndRequest(t *testing.T) {
	w := pasta.NewWorkspace(NewDebugLogFactory(1024))
	defer w.Close()
	base := gonnect.NewLoopbackNetwok()
	if err := w.AddNodeClass(outNetClass{engine: &engine{sys: &sysnetdebug.System{OutNetwork: base}}}); err != nil {
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
	payload := receiveOutNetPayload(t, receiver.events)
	if got := payload.Network.(*gonnect.DetachedNetwork).GetWrapped(); got != base {
		t.Fatalf("wrapped network = %T %p, want base %p", got, got, base)
	}

	w.EmitEvent(receiver.id, link, std.RequestValue{})
	payload = receiveOutNetPayload(t, receiver.events)
	if got := payload.Network.(*gonnect.DetachedNetwork).GetWrapped(); got != base {
		t.Fatalf("request wrapped network = %T %p, want base %p", got, got, base)
	}
}

func TestOutNetworkSendsSystemOutNetToNewLinks(t *testing.T) {
	w := pasta.NewWorkspace(NewDebugLogFactory(1024))
	defer w.Close()
	base := gonnect.NewLoopbackNetwok()
	if err := w.AddNodeClass(outNetClass{engine: &engine{sys: &sysnetdebug.System{OutNetwork: base}}}); err != nil {
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
	base := gonnect.NewLoopbackNetwok()
	if err := w.AddNodeClass(outNetClass{engine: &engine{sys: &sysnetdebug.System{OutNetwork: base}}}); err != nil {
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
	pool := newE2ETestDebugPool(t)

	base := gonnect.NewLoopbackNetwok()
	sys := &sysnetdebug.System{OutNetwork: base}
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

func TestOutNetworkWithoutSystemOutNetDoesNotSend(t *testing.T) {
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
