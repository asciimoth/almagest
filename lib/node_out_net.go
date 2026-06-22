package almagest

import (
	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/gonnect"
	"github.com/asciimoth/pasta/pasta"
	"github.com/asciimoth/pasta/pasta/std"
)

func init() {
	regClass(func(e *engine) pasta.NodeClass {
		return outNetClass{engine: e}
	}, false)
}

// NodeTypeOutNetwork is the node class name for the system outbound network
// provider.
const NodeTypeOutNetwork = "almagest/OutNetwork"

type outNetClass struct {
	engine *engine
}

func (outNetClass) ClassName() string        { return NodeTypeOutNetwork }
func (outNetClass) ShortDescription() string { return "System outbound network" }
func (outNetClass) LongDescription() string {
	return "Provides the outbound OS network sink."
}
func (outNetClass) DefaultNodeParams() pasta.NodeClassParams {
	return pasta.NodeClassParams{PrimaryType: TypeNetwork, InitialPorts: []pasta.Port{
		NetworkPort("left", "Network"),
	}}
}
func (c outNetClass) NewNode(_ configer.Config, _ ...*pasta.NodeClassState) (pasta.Node, error) {
	return &outNetNode{engine: c.engine}, nil
}

// outNetNode exposes sysnet.System.OutNet through almagest/network links.
// OutNet is stable for the whole System lifetime, so the node grabs it once
// during initialization and sends detached wrappers to connected links.
type outNetNode struct {
	pasta.BasicNode

	engine *engine
	w      *pasta.Workspace
	id     uint64
	out    uint64

	network gonnect.Network
}

func (n *outNetNode) OnInit(w *pasta.Workspace, _ pasta.Logger, id uint64, _ string, restored *pasta.NodeInitData, _, _, _, _ bool) error {
	n.w = w
	n.id = id
	if restored != nil && len(restored.LeftPorts) > 0 {
		n.out = restored.LeftPorts[0]
	}
	if err := n.w.SetNodePrimary(n.id, TypeNetwork); err != nil {
		return err
	}
	_ = n.w.SetNodeLabel(n.id, "out")
	if n.engine != nil && n.engine.sys != nil {
		n.network = n.engine.sys.OutNet()
	}
	return nil
}

func (n *outNetNode) OnReady() error {
	n.sendAll()
	return nil
}

func (n *outNetNode) PreLinkAdd(port uint64, linkType, portDirection string) error {
	if port != n.out || portDirection != "left" || linkType != TypeNetwork {
		return pasta.LinkTypeErr(linkType)
	}
	return nil
}

func (n *outNetNode) OnLinkAdd(link, port uint64, linkType, portDirection string) error {
	if port == n.out && portDirection == "left" && linkType == TypeNetwork && n.network != nil {
		n.sendNetworkToLink(link, n.network)
	}
	return nil
}

func (n *outNetNode) OnEvent(event pasta.Event, linkType string, _ []string, receiverPortDirection string) error {
	if event.ReceiverPort != n.out || receiverPortDirection != "left" || linkType != TypeNetwork || !std.IsRequest(event.Payload) {
		return nil
	}
	if n.network != nil {
		n.sendNetworkToLink(event.Link, n.network)
	}
	return nil
}

func (n *outNetNode) sendAll() {
	port, ok := n.w.PortSnapshot(n.out)
	if !ok {
		return
	}
	for _, link := range port.Links {
		n.sendNetworkToLink(link, n.network)
	}
}

func (n *outNetNode) sendNetworkToLink(link uint64, network gonnect.Network) {
	if network == nil {
		return
	}
	wrapper := gonnect.DetachNetwork(network, nil)
	BindNetworkResource(n.w, n.id, link, wrapper)
	n.w.EmitEvent(n.id, link, NetworkPayload{Network: wrapper})
}
