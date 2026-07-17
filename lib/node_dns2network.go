package almagest

import (
	"time"

	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/gonnect/dns"
	"github.com/asciimoth/pasta/pasta"
	"github.com/asciimoth/pasta/pasta/std"
)

func init() {
	regClass(func(e *engine) pasta.NodeClass {
		return dns2NetworkClass{engine: e}
	}, false)
}

const (
	// NodeTypeDns2Network is the node class name for a Network-backed DNS provider.
	NodeTypeDns2Network = "almagest/Dns2Network"

	defaultDns2NetworkTTL = time.Minute
)

type dns2NetworkClass struct {
	engine *engine
}

func (dns2NetworkClass) ClassName() string        { return NodeTypeDns2Network }
func (dns2NetworkClass) ShortDescription() string { return "DNS from Network" }
func (dns2NetworkClass) LongDescription() string {
	return "Provides an almagest/dns interface that answers DNS requests by calling resolver lookup methods on a received almagest/network value."
}
func (dns2NetworkClass) DefaultNodeParams() pasta.NodeClassParams {
	return pasta.NodeClassParams{PrimaryType: TypeDNS, InitialPorts: []pasta.Port{
		DNSPort("left", "DNS"),
		NetworkPort("right", "Network"),
	}}
}
func (c dns2NetworkClass) NewNode(_ configer.Config, _ ...*pasta.NodeClassState) (pasta.Node, error) {
	return &dns2NetworkNode{NodeBase: NodeBase{engine: c.engine}}, nil
}

// dns2NetworkNode adapts a received Network into a DNS provider. The node owns
// one dns.ResolverProvider for the current Network input and emits per-link
// detached DNS wrappers, so detaching a consumer or replacing the Network
// closes only the wrappers and provider tied to the old graph generation.
type dns2NetworkNode struct {
	NodeBase

	dnsOut      uint64
	networkIn   uint64
	networkLink uint64

	network  NetworkCloser
	provider dns.Interface
	outputs  DNSOutputCache
}

func (n *dns2NetworkNode) OnInit(w *pasta.Workspace, logger pasta.Logger, id uint64, _ string, restored *pasta.NodeInitData, _, _, _, _ bool) error {
	n.InitBaseNode(w, logger, id)
	n.dnsOut = restoredLeft(restored, 0)
	n.networkIn = restoredRight(restored, 0)
	if err := n.SetPrimary(TypeDNS); err != nil {
		return err
	}
	return n.updateLabel()
}

func (n *dns2NetworkNode) OnReady() error {
	n.RequestPort(n.networkIn)
	n.sendAll()
	return nil
}

func (n *dns2NetworkNode) OnStop() {
	n.outputs.CloseAll()
	closeDNS(n.provider)
	n.provider = nil
	_ = closeNetwork(n.network)
	n.network = nil
	n.networkLink = 0
}

func (n *dns2NetworkNode) PreLinkAdd(port uint64, linkType, portDirection string) error {
	switch port {
	case n.dnsOut:
		if portDirection != "left" || linkType != TypeDNS {
			return pasta.LinkTypeErr(linkType)
		}
	case n.networkIn:
		if portDirection != "right" || linkType != TypeNetwork {
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

func (n *dns2NetworkNode) OnLinkAdd(link, port uint64, linkType, portDirection string) error {
	switch {
	case port == n.dnsOut && portDirection == "left" && linkType == TypeDNS:
		n.sendToLink(link)
	case port == n.networkIn && portDirection == "right" && linkType == TypeNetwork:
		n.networkLink = link
		std.RequestLocked(n.w, n.id, link)
	}
	return n.updateLabel()
}

func (n *dns2NetworkNode) OnLinkRemoved(link, port uint64, linkType, portDirection string) error {
	switch {
	case port == n.dnsOut && portDirection == "left" && linkType == TypeDNS:
		n.outputs.Close(link)
	case port == n.networkIn && portDirection == "right" && linkType == TypeNetwork && link == n.networkLink:
		n.outputs.CloseAll()
		closeDNS(n.provider)
		n.provider = nil
		_ = closeNetwork(n.network)
		n.network = nil
		n.networkLink = 0
	}
	return n.updateLabel()
}

func (n *dns2NetworkNode) OnEvent(event pasta.Event, linkType string, _ []string, receiverPortDirection string) error {
	if event.ReceiverPort == n.dnsOut && receiverPortDirection == "left" && linkType == TypeDNS && std.IsRequest(event.Payload) {
		n.refreshLink(event.Link)
		return nil
	}
	if event.ReceiverPort != n.networkIn || receiverPortDirection != "right" || linkType != TypeNetwork || event.Link != n.networkLink {
		return nil
	}
	next, changed := AcceptNetworkPayload(n.w, n.id, event, n.network)
	if !changed {
		return nil
	}
	n.network = next
	n.rebuildProvider()
	return n.updateLabel()
}

func (n *dns2NetworkNode) rebuildProvider() {
	n.outputs.CloseAll()
	closeDNS(n.provider)
	n.provider = nil
	if !NetworkIsReady(n.network) {
		return
	}
	// Nil spawner is intentional here; see the Gonnect spawner note in helpers.go.
	n.provider = dns.NewResolverProvider(n.network, defaultDns2NetworkTTL, nil)
	n.sendAll()
}

func (n *dns2NetworkNode) sendAll() {
	BroadcastDNSPort(n.w, n.id, n.dnsOut, func(link uint64) {
		n.sendToLink(link)
	})
}

func (n *dns2NetworkNode) sendToLink(link uint64) {
	if n.provider == nil {
		return
	}
	n.outputs.SendLocked(n.w, n.id, link, n.provider)
}

func (n *dns2NetworkNode) refreshLink(link uint64) {
	if n.provider == nil {
		return
	}
	n.outputs.RefreshLocked(n.w, n.id, link, n.provider)
}

func (n *dns2NetworkNode) updateLabel() error {
	if !NetworkIsReady(n.network) {
		return n.SetLabel("waiting for network")
	}
	return n.SetLabel("resolving DNS")
}
