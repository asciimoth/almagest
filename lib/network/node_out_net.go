package networknodes

import (
	"github.com/asciimoth/almagest/lib/links"
	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/gonnect"
	"github.com/asciimoth/gonnect/sysnet"
	"github.com/asciimoth/pasta/pasta"

	"github.com/asciimoth/almagest/lib/nodekit"
)

// NodeTypeOutNetwork is the node class name for the system outbound network
// provider.
const NodeTypeOutNetwork = "almagest/OutNetwork"

type OutNetworkClass struct {
	Runtime Runtime
}

func (OutNetworkClass) ClassName() string        { return NodeTypeOutNetwork }
func (OutNetworkClass) ShortDescription() string { return "System outbound network" }
func (OutNetworkClass) LongDescription() string {
	return "Provides the outbound OS network sink."
}
func (OutNetworkClass) DefaultNodeParams() pasta.NodeClassParams {
	return pasta.NodeClassParams{PrimaryType: TypeNetwork, InitialPorts: []pasta.Port{
		NetworkPort("left", "Network"),
	}}
}
func (c OutNetworkClass) NewNode(_ configer.Config, _ ...*pasta.NodeClassState) (pasta.Node, error) {
	return &outNetNode{SimpleNetworkProvider: nodekit.SimpleNetworkProvider{Base: NodeBase{Runtime: c.Runtime}}}, nil
}

// outNetNode exposes sysnet.System.OutNet through almagest/network links.
// OutNet is stable for the whole System lifetime, so the node grabs it once
// during initialization and sends detached wrappers to connected links.
type outNetNode struct {
	nodekit.SimpleNetworkProvider
}

func (n *outNetNode) OnInit(w *pasta.Workspace, _ pasta.Logger, id uint64, _ string, restored *pasta.NodeInitData, _, _, _, _ bool) error {
	var network gonnect.Network
	if n.Runtime != nil && n.Runtime.System() != nil {
		system := n.Runtime.System()
		network = system.OutNet()
		attachSystemNetworkMTUProvider(system, network, sysnet.TargetOutNet)
	}
	return n.InitSimpleNetworkProvider(w, nil, id, restored, network, "out")
}

// attachSystemNetworkMTUProvider enables destination-aware MTU discovery for
// a sysnet network that exposes the operations used by the discovery logic.
// A system network can intentionally report IsNative as false to protect its
// socket policy hooks while still providing direct routed UDP and interface
// inspection through its Network methods.
func attachSystemNetworkMTUProvider(system sysnet.System, network gonnect.Network, target sysnet.Target) {
	if system == nil || network == nil || network.IsNative() ||
		links.DatagramMTUProviderFromNetwork(network) != nil {
		return
	}
	report := system.Capabilities()
	interfaces := report.Operation(sysnet.OperationKey{
		Target: target, Operation: sysnet.OpInterfaces, Family: sysnet.FamilyNone,
	})
	if interfaces.State != sysnet.CapabilityAvailable {
		return
	}
	for _, family := range []sysnet.AddressFamily{sysnet.FamilyIPv4, sysnet.FamilyIPv6} {
		dialUDP := report.Operation(sysnet.OperationKey{
			Target: target, Operation: sysnet.OpDialUDP, Family: family,
		})
		if dialUDP.State == sysnet.CapabilityAvailable {
			links.AttachDatagramMTUProvider(network, links.NewNativeDatagramMTUProvider(network))
			return
		}
	}
}
