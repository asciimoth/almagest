package almagest

import (
	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/gonnect/dns"
	"github.com/asciimoth/pasta/pasta"
	"github.com/asciimoth/pasta/pasta/std"
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
	return &outDNSNode{NodeBase: NodeBase{engine: c.engine}}, nil
}

// outDNSNode exposes sysnet.System.OutDNS through almagest/dns links. The
// system owns the underlying DNS source; this node owns only the detached
// per-link wrappers emitted into the workspace, so detaching a graph link never
// closes the system resolver used by other nodes.
type outDNSNode struct {
	NodeBase

	out uint64

	resolver dns.Interface
	outputs  DNSOutputCache
}

func (n *outDNSNode) OnInit(w *pasta.Workspace, logger pasta.Logger, id uint64, _ string, restored *pasta.NodeInitData, _, _, _, _ bool) error {
	n.InitBaseNode(w, logger, id)
	n.out = restoredLeft(restored, 0)
	if n.engine != nil && n.engine.sys != nil {
		n.resolver = n.engine.sys.OutDNS()
	}
	if err := n.SetPrimary(TypeDNS); err != nil {
		return err
	}
	_ = n.SetLabel("out DNS")
	return nil
}

func (n *outDNSNode) OnReady() error {
	n.sendAll()
	return nil
}

func (n *outDNSNode) OnStop() {
	n.outputs.CloseAll()
	n.resolver = nil
}

func (n *outDNSNode) PreLinkAdd(port uint64, linkType, portDirection string) error {
	if port != n.out || portDirection != "left" || linkType != TypeDNS {
		return pasta.LinkTypeErr(linkType)
	}
	return nil
}

func (n *outDNSNode) OnLinkAdd(link, port uint64, linkType, portDirection string) error {
	if port == n.out && portDirection == "left" && linkType == TypeDNS {
		n.sendToLink(link)
	}
	return nil
}

func (n *outDNSNode) OnLinkRemoved(link uint64, port uint64, linkType, portDirection string) error {
	if port == n.out && portDirection == "left" && linkType == TypeDNS {
		n.outputs.Close(link)
	}
	return nil
}

func (n *outDNSNode) OnEvent(event pasta.Event, linkType string, _ []string, receiverPortDirection string) error {
	if event.ReceiverPort != n.out || receiverPortDirection != "left" || linkType != TypeDNS || !std.IsRequest(event.Payload) {
		return nil
	}
	n.refreshLink(event.Link)
	return nil
}

func (n *outDNSNode) sendAll() {
	BroadcastDNSPort(n.w, n.id, n.out, func(link uint64) {
		n.sendToLink(link)
	})
}

func (n *outDNSNode) sendToLink(link uint64) {
	n.outputs.SendLocked(n.w, n.id, link, n.resolver)
}

func (n *outDNSNode) refreshLink(link uint64) {
	n.outputs.RefreshLocked(n.w, n.id, link, n.resolver)
}
