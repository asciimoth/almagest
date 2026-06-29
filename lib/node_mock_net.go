package almagest

import (
	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/gonnect"
	"github.com/asciimoth/pasta/pasta"
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
	simpleNetworkProvider

	base *gonnect.LoopbackNetwork
}

func (n *mockNetNode) OnInit(w *pasta.Workspace, _ pasta.Logger, id uint64, _ string, restored *pasta.NodeInitData, _, _, _, _ bool) error {
	if n.base == nil {
		n.base = gonnect.NewLoopbackNetwok()
	}
	if err := n.InitSimpleNetworkProvider(w, nil, id, restored, n.base, "loopback"); err != nil {
		return err
	}
	return n.w.AddNodeResource(n.id, n.base)
}
