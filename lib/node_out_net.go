package almagest

import (
	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/gonnect"
	"github.com/asciimoth/pasta/pasta"
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
	return &outNetNode{simpleNetworkProvider: simpleNetworkProvider{NodeBase: NodeBase{engine: c.engine}}}, nil
}

// outNetNode exposes sysnet.System.OutNet through almagest/network links.
// OutNet is stable for the whole System lifetime, so the node grabs it once
// during initialization and sends detached wrappers to connected links.
type outNetNode struct {
	simpleNetworkProvider
}

func (n *outNetNode) OnInit(w *pasta.Workspace, _ pasta.Logger, id uint64, _ string, restored *pasta.NodeInitData, _, _, _, _ bool) error {
	var network gonnect.Network
	if n.engine != nil && n.engine.sys != nil {
		network = n.engine.sys.OutNet()
	}
	return n.InitSimpleNetworkProvider(w, nil, id, restored, network, "out")
}
