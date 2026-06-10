package almagest

import (
	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/gonnect"
	"github.com/asciimoth/pasta/pasta"
	"github.com/asciimoth/pasta/pasta/std"
)

func init() {
	regClass(func(e *engine) pasta.NodeClass {
		return mockNetClass{}
	}, false)
}

const NodeTypeMockNetwork = "almagest/MockNetwork"

type mockNetClass struct{}

func (mockNetClass) ClassName() string        { return NodeTypeMockNetwork }
func (mockNetClass) ShortDescription() string { return "Virtual network mock" }
func (mockNetClass) LongDescription() string {
	return "Emulates network for test/debug purposes. Connections/Listeners created via MockNetwork are fully virtual and never reach real OS network stack."
}
func (mockNetClass) DefaultNodeParams() pasta.NodeClassParams {
	return pasta.NodeClassParams{PrimaryType: TypeNetwork, InitialPorts: []pasta.Port{
		NetworkPort("left", "Network"),
	}}
}
func (mockNetClass) NewNode(_ configer.Config, _ ...*pasta.NodeClassState) (pasta.Node, error) {
	network := gonnect.NewLoopbackNetwok()
	network.AllowAnyHost = true
	return &mockNetNode{base: network}, nil
}

type mockNetNode struct {
	pasta.BasicNode

	base *gonnect.LoopbackNetwork
	w    *pasta.Workspace
	id   uint64
	out  uint64
}

func (n *mockNetNode) OnInit(w *pasta.Workspace, _ pasta.Logger, id uint64, _ string, restored *pasta.NodeInitData, _, _, _, _ bool) error {
	n.w = w
	n.id = id
	if n.base == nil {
		n.base = gonnect.NewLoopbackNetwok()
	}
	if restored != nil && len(restored.LeftPorts) > 0 {
		n.out = restored.LeftPorts[0]
	}
	if err := n.w.SetNodePrimary(n.id, TypeNetwork); err != nil {
		return err
	}
	_ = n.w.SetNodeLabel(n.id, "loopback")
	return n.w.AddNodeResource(n.id, n.base)
}

func (n *mockNetNode) OnReady() error {
	n.sendAll()
	return nil
}

func (n *mockNetNode) PreLinkAdd(port uint64, linkType, portDirection string) error {
	if port != n.out || portDirection != "left" || linkType != TypeNetwork {
		return pasta.LinkTypeErr(linkType)
	}
	return nil
}

func (n *mockNetNode) OnLinkAdd(link, port uint64, _, _ string) error {
	if port == n.out {
		n.sendToLink(link)
	}
	return nil
}

func (n *mockNetNode) OnEvent(event pasta.Event, linkType string, _ []string, receiverPortDirection string) error {
	if event.ReceiverPort != n.out || receiverPortDirection != "left" || linkType != TypeNetwork || !std.IsRequest(event.Payload) {
		return nil
	}
	n.sendToLink(event.Link)
	return nil
}

func (n *mockNetNode) sendAll() {
	port, ok := n.w.PortSnapshot(n.out)
	if !ok {
		return
	}
	for _, link := range port.Links {
		n.sendToLink(link)
	}
}

func (n *mockNetNode) sendToLink(link uint64) {
	wrapper := gonnect.DetachNetwork(n.base, nil)
	BindNetworkResource(n.w, n.id, link, wrapper)
	n.w.EmitEvent(n.id, link, NetworkPayload{Network: wrapper})
}
