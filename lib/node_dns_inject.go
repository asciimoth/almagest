package almagest

import (
	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/gonnect/dns"
	"github.com/asciimoth/pasta/pasta"
	"github.com/asciimoth/pasta/pasta/std"
)

func init() {
	regClass(func(e *engine) pasta.NodeClass {
		return dnsInjectClass{engine: e}
	}, false)
}

const (
	// NodeTypeDnsInject is the node class name for a network DNS injector.
	NodeTypeDnsInject = "almagest/DnsInject"
)

type dnsInjectClass struct {
	engine *engine
}

func (dnsInjectClass) ClassName() string        { return NodeTypeDnsInject }
func (dnsInjectClass) ShortDescription() string { return "DNS inject" }
func (dnsInjectClass) LongDescription() string {
	return "Publishes a received almagest/network value with DNS lookups and hostname pre-resolution routed through a received almagest/dns interface."
}
func (dnsInjectClass) DefaultNodeParams() pasta.NodeClassParams {
	return pasta.NodeClassParams{PrimaryType: TypeNetwork, InitialPorts: []pasta.Port{
		NetworkPort("left", "Network"),
		NetworkPort("right", "Network"),
		DNSPort("right", "DNS"),
	}}
}
func (c dnsInjectClass) NewNode(_ configer.Config, _ ...*pasta.NodeClassState) (pasta.Node, error) {
	return &dnsInjectNode{NodeBase: NodeBase{engine: c.engine}}, nil
}

// dnsInjectNode is a two-input network middleware. The left-directed Network
// port publishes per-link detached wrappers around the right-directed backend
// Network. Each emitted wrapper uses the right-directed DNS interface through a
// gonnect/dns Resolver, so explicit Lookup calls and implicit Dial/Listen
// hostname resolution use the DNS branch instead of the backend Network's own
// resolver.
type dnsInjectNode struct {
	NodeBase

	out        uint64
	networkIn  uint64
	dnsIn      uint64
	networkLnk uint64
	dnsLink    uint64

	network     NetworkCloser
	dns         dns.Interface
	resolverDNS dns.Interface
	resolver    *dns.Resolver
	outputs     NetworkOutputCache
}

func (n *dnsInjectNode) OnInit(w *pasta.Workspace, logger pasta.Logger, id uint64, _ string, restored *pasta.NodeInitData, _, _, _, _ bool) error {
	n.InitBaseNode(w, logger, id)
	n.out = restoredLeft(restored, 0)
	n.networkIn = restoredRight(restored, 0)
	n.dnsIn = restoredRight(restored, 1)
	if err := n.SetPrimary(TypeNetwork); err != nil {
		return err
	}
	return n.updateLabel()
}

func (n *dnsInjectNode) OnReady() error {
	n.RequestPort(n.networkIn)
	n.RequestPort(n.dnsIn)
	n.sendAll()
	return nil
}

func (n *dnsInjectNode) OnStop() {
	n.outputs.CloseAll()
	_ = closeNetwork(n.network)
	n.network = nil
	closeDNS(n.resolverDNS)
	n.resolverDNS = nil
	closeDNS(n.dns)
	n.dns = nil
	n.resolver = nil
	n.networkLnk = 0
	n.dnsLink = 0
}

func (n *dnsInjectNode) PreLinkAdd(port uint64, linkType, portDirection string) error {
	switch port {
	case n.out:
		if portDirection != "left" || linkType != TypeNetwork {
			return pasta.LinkTypeErr(linkType)
		}
	case n.networkIn:
		if portDirection != "right" || linkType != TypeNetwork {
			return pasta.LinkTypeErr(linkType)
		}
		if n.PortHasLinks(port) {
			return pasta.ErrLinkDup
		}
	case n.dnsIn:
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

func (n *dnsInjectNode) OnLinkAdd(link, port uint64, linkType, portDirection string) error {
	switch {
	case port == n.out && portDirection == "left" && linkType == TypeNetwork:
		n.sendOutputToLink(link)
	case port == n.networkIn && portDirection == "right" && linkType == TypeNetwork:
		n.networkLnk = link
		std.RequestLocked(n.w, n.id, link)
	case port == n.dnsIn && portDirection == "right" && linkType == TypeDNS:
		n.dnsLink = link
		std.RequestLocked(n.w, n.id, link)
	}
	return n.updateLabel()
}

func (n *dnsInjectNode) OnLinkRemoved(link uint64, port uint64, linkType, portDirection string) error {
	switch {
	case port == n.out && portDirection == "left" && linkType == TypeNetwork:
		n.outputs.Close(link)
	case port == n.networkIn && portDirection == "right" && linkType == TypeNetwork && link == n.networkLnk:
		n.outputs.CloseAll()
		_ = closeNetwork(n.network)
		n.network = nil
		n.networkLnk = 0
	case port == n.dnsIn && portDirection == "right" && linkType == TypeDNS && link == n.dnsLink:
		n.outputs.CloseAll()
		closeDNS(n.resolverDNS)
		n.resolverDNS = nil
		closeDNS(n.dns)
		n.dns = nil
		n.resolver = nil
		n.dnsLink = 0
	}
	return n.updateLabel()
}

func (n *dnsInjectNode) OnEvent(event pasta.Event, linkType string, _ []string, receiverPortDirection string) error {
	if event.ReceiverPort == n.out && receiverPortDirection == "left" && linkType == TypeNetwork && std.IsRequest(event.Payload) {
		n.refreshOutputToLink(event.Link)
		return nil
	}
	if event.ReceiverPort == n.networkIn && receiverPortDirection == "right" && linkType == TypeNetwork {
		if event.Link != n.networkLnk {
			return nil
		}
		next, changed := AcceptNetworkPayload(n.w, n.id, event, n.network)
		if !changed {
			return nil
		}
		n.outputs.CloseAll()
		n.network = next
		n.sendAll()
		return n.updateLabel()
	}
	if event.ReceiverPort == n.dnsIn && receiverPortDirection == "right" && linkType == TypeDNS {
		if event.Link != n.dnsLink {
			return nil
		}
		next, changed := AcceptDNSPayload(n.w, n.id, event, n.dns)
		if !changed {
			return nil
		}
		n.outputs.CloseAll()
		closeDNS(n.resolverDNS)
		n.dns = next
		n.resolverDNS = n.newResolverDNS(next)
		n.resolver = dns.NewResolver(n.resolverDNS)
		n.sendAll()
		return n.updateLabel()
	}
	return nil
}

func (n *dnsInjectNode) ready() bool {
	return NetworkIsReady(n.network) && DNSIsReady(n.resolverDNS) && n.resolver != nil
}

func (n *dnsInjectNode) newResolverDNS(input dns.Interface) dns.Interface {
	if source, ok := DNSSource(input).(dns.Interface); ok && source != nil {
		return newDetachedDNS(source)
	}
	return newDetachedDNS(input)
}

func (n *dnsInjectNode) sendAll() {
	if !n.ready() {
		return
	}
	n.outputs.SendResolvedPortLocked(n.w, n.id, n.out, n.network, n.resolver)
}

func (n *dnsInjectNode) sendOutputToLink(link uint64) {
	if n.ready() {
		n.outputs.SendResolvedLocked(n.w, n.id, link, n.network, n.resolver)
	}
}

func (n *dnsInjectNode) refreshOutputToLink(link uint64) {
	if n.ready() {
		n.outputs.RefreshResolvedLocked(n.w, n.id, link, n.network, n.resolver)
	}
}

func (n *dnsInjectNode) updateLabel() error {
	switch {
	case !NetworkIsReady(n.network):
		return n.SetLabel("waiting for network")
	case !DNSIsReady(n.dns):
		return n.SetLabel("waiting for DNS")
	default:
		return n.SetLabel("injecting DNS")
	}
}
