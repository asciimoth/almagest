package almagest

import (
	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/pasta/pasta"
)

func init() {
	regClass(func(e *engine) pasta.NodeClass {
		return outDNSClass{engine: e}
	}, false)
}

// NodeTypeOutDNS is the node class name for the system outbound DNS provider.
const NodeTypeOutDNS = "almagest/OutDns"

type outDNSClass struct {
	engine *engine
}

func (outDNSClass) ClassName() string        { return NodeTypeOutDNS }
func (outDNSClass) ShortDescription() string { return "System outbound DNS" }
func (outDNSClass) LongDescription() string {
	return "Provides the DNS resolver used for system outbound requests."
}
func (outDNSClass) DefaultNodeParams() pasta.NodeClassParams {
	return pasta.NodeClassParams{PrimaryType: TypeDNS, InitialPorts: []pasta.Port{
		DNSPort("left", "DNS"),
	}}
}
func (c outDNSClass) NewNode(_ configer.Config, _ ...*pasta.NodeClassState) (pasta.Node, error) {
	return &outDNSNode{simpleDNSProvider: simpleDNSProvider{NodeBase: NodeBase{engine: c.engine}}}, nil
}

// outDNSNode exposes sysnet.System.OutDNS through almagest/dns links. The
// system owns the underlying DNS source; this node owns only the detached
// per-link wrappers emitted into the workspace, so detaching a graph link never
// closes the system resolver used by other nodes.
type outDNSNode struct {
	simpleDNSProvider
}

func (n *outDNSNode) OnInit(w *pasta.Workspace, logger pasta.Logger, id uint64, _ string, restored *pasta.NodeInitData, _, _, _, _ bool) error {
	if n.engine != nil && n.engine.sys != nil {
		return n.InitSimpleDNSProvider(w, logger, id, restored, n.engine.sys.OutDNS(), "out DNS")
	}
	return n.InitSimpleDNSProvider(w, logger, id, restored, nil, "out DNS")
}
