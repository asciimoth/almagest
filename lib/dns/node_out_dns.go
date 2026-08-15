package dnsnodes

import (
	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/pasta/pasta"

	"github.com/asciimoth/almagest/lib/nodekit"
)

// NodeTypeOutDNS is the node class name for the system outbound DNS provider.
const NodeTypeOutDNS = "almagest/OutDns"

// OutDNSClass builds system outbound DNS provider nodes.
type OutDNSClass struct {
	Runtime nodekit.Runtime
}

func (OutDNSClass) ClassName() string        { return NodeTypeOutDNS }
func (OutDNSClass) ShortDescription() string { return "System outbound DNS" }
func (OutDNSClass) LongDescription() string {
	return "Provides the DNS resolver used for system outbound requests."
}
func (OutDNSClass) DefaultNodeParams() pasta.NodeClassParams {
	return pasta.NodeClassParams{PrimaryType: TypeDNS, InitialPorts: []pasta.Port{
		DNSPort("left", "DNS"),
	}}
}
func (c OutDNSClass) NewNode(_ configer.Config, _ ...*pasta.NodeClassState) (pasta.Node, error) {
	return &outDNSNode{
		SimpleDNSProvider: SimpleDNSProvider{
			Base: nodekit.Base{Runtime: c.Runtime},
		},
	}, nil
}

// outDNSNode exposes sysnet.System.OutDNS through almagest/dns links. The
// system owns the underlying DNS source; this node owns only the detached
// per-link wrappers emitted into the workspace, so detaching a graph link never
// closes the system resolver used by other nodes.
type outDNSNode struct {
	SimpleDNSProvider
}

func (n *outDNSNode) OnInit(w *pasta.Workspace, logger pasta.Logger, id uint64, _ string, restored *pasta.NodeInitData, _, _, _, _ bool) error {
	if sys := n.System(); sys != nil {
		return n.InitSimpleDNSProvider(w, logger, id, restored, sys.OutDNS(), "out DNS")
	}
	return n.InitSimpleDNSProvider(w, logger, id, restored, nil, "out DNS")
}
