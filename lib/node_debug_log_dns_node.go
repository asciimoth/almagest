package almagest

import (
	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/gonnect/dns"
	"github.com/asciimoth/pasta/pasta"
	"github.com/asciimoth/pasta/pasta/std"
)

type debugLogDNSClass struct{}

func (debugLogDNSClass) ClassName() string        { return NodeTypeDebugLogDNS }
func (debugLogDNSClass) ShortDescription() string { return "DEBUG DNS logger" }
func (debugLogDNSClass) LongDescription() string {
	return "Passes almagest/dns requests through unchanged while debug logging DNS requests, responses, and errors."
}
func (debugLogDNSClass) DefaultNodeParams() pasta.NodeClassParams {
	return pasta.NodeClassParams{PrimaryType: TypeDNS, InitialPorts: []pasta.Port{
		DNSPort("left", "DNS"),
		DNSPort("right", "DNS"),
	}}
}
func (debugLogDNSClass) NewNode(_ configer.Config, _ ...*pasta.NodeClassState) (pasta.Node, error) {
	return &debugLogDNSNode{}, nil
}

// debugLogDNSNode is transparent almagest/dns middleware. It keeps one
// received upstream DNS interface and emits one independently closable,
// logging wrapper per output link.
type debugLogDNSNode struct {
	NodeBase

	out uint64
	in  uint64

	upstream dns.Interface
	outputs  DNSOutputCache
}

func (n *debugLogDNSNode) OnInit(w *pasta.Workspace, logger pasta.Logger, id uint64, _ string, restored *pasta.NodeInitData, _, _, _, _ bool) error {
	n.InitBaseNode(w, logger, id)
	n.out = restoredLeft(restored, 0)
	n.in = restoredRight(restored, 0)
	if err := n.SetPrimary(TypeDNS); err != nil {
		return err
	}
	_ = n.SetLabel("dns log")
	return nil
}

func (n *debugLogDNSNode) OnStop() {
	n.outputs.CloseAll()
	closeDNS(n.upstream)
	n.upstream = nil
}

func (n *debugLogDNSNode) OnReady() error {
	n.requestInput()
	n.sendAll()
	return nil
}

func (n *debugLogDNSNode) PreLinkAdd(port uint64, linkType, portDirection string) error {
	switch port {
	case n.out:
		if portDirection != "left" || linkType != TypeDNS {
			return pasta.LinkTypeErr(linkType)
		}
	case n.in:
		if portDirection != "right" || linkType != TypeDNS {
			return pasta.LinkTypeErr(linkType)
		}
		if n.PortHasLinks(port) {
			return pasta.ErrLinkDup
		}
	default:
		return pasta.LinkTypeErr(linkType)
	}
	return nil
}

func (n *debugLogDNSNode) OnLinkAdd(link, port uint64, linkType, portDirection string) error {
	if port == n.out && portDirection == "left" && linkType == TypeDNS {
		n.sendDNSToLink(link)
		return nil
	}
	if port == n.in && portDirection == "right" && linkType == TypeDNS {
		std.RequestLocked(n.w, n.id, link)
	}
	return nil
}

func (n *debugLogDNSNode) OnLinkRemoved(link, port uint64, linkType, portDirection string) error {
	if port == n.out && portDirection == "left" && linkType == TypeDNS {
		n.outputs.Close(link)
		return nil
	}
	if port == n.in && portDirection == "right" && linkType == TypeDNS {
		closeDNS(n.upstream)
		n.upstream = nil
		n.outputs.CloseAll()
	}
	return nil
}

func (n *debugLogDNSNode) OnEvent(event pasta.Event, linkType string, _ []string, receiverPortDirection string) error {
	if event.ReceiverPort == n.out && receiverPortDirection == "left" && linkType == TypeDNS && std.IsRequest(event.Payload) {
		n.refreshDNSToLink(event.Link)
		return nil
	}
	if event.ReceiverPort != n.in || receiverPortDirection != "right" || linkType != TypeDNS {
		return nil
	}
	next, changed := AcceptDNSPayload(n.w, n.id, event, n.upstream)
	if !changed {
		return nil
	}
	n.outputs.CloseAll()
	n.upstream = next
	n.sendAll()
	return nil
}

func (n *debugLogDNSNode) requestInput() {
	n.RequestPort(n.in)
}

func (n *debugLogDNSNode) sendAll() {
	n.outputs.SendWrappedPortLocked(n.w, n.id, n.out, n.upstream, n.wrapDNS)
}

func (n *debugLogDNSNode) sendDNSToLink(link uint64) {
	n.outputs.SendWrappedLocked(n.w, n.id, link, n.upstream, n.wrapDNS)
}

func (n *debugLogDNSNode) refreshDNSToLink(link uint64) {
	n.outputs.RefreshWrappedLocked(n.w, n.id, link, n.upstream, n.wrapDNS)
}

func (n *debugLogDNSNode) wrapDNS(output dns.Interface) dns.Interface {
	return newDebugLogDNS(output, n.logger)
}
