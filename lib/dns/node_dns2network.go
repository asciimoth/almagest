package dnsnodes

import (
	"time"

	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/gonnect/dns"
	"github.com/asciimoth/pasta/pasta"
	"github.com/asciimoth/pasta/pasta/std"

	"github.com/asciimoth/almagest/lib/nodekit"
)

const (
	// NodeTypeDns2Network is the node class name for a Network-backed DNS provider.
	NodeTypeDns2Network = "almagest/Dns2Network"

	defaultDns2NetworkTTL = time.Minute
)

// DNS2NetworkClass builds Network-backed DNS provider nodes.
type DNS2NetworkClass struct {
	Runtime nodekit.Runtime
}

func (DNS2NetworkClass) ClassName() string        { return NodeTypeDns2Network }
func (DNS2NetworkClass) ShortDescription() string { return "DNS from Network" }
func (DNS2NetworkClass) LongDescription() string {
	return "Provides an almagest/dns interface that answers DNS requests by calling resolver lookup methods on a received almagest/network value."
}
func (DNS2NetworkClass) DefaultNodeParams() pasta.NodeClassParams {
	return pasta.NodeClassParams{PrimaryType: TypeDNS, InitialPorts: []pasta.Port{
		DNSPort("left", "DNS"),
		nodekit.NetworkPort("right", "Network"),
	}}
}
func (c DNS2NetworkClass) NewNode(_ configer.Config, _ ...*pasta.NodeClassState) (pasta.Node, error) {
	return &dns2NetworkNode{Base: nodekit.Base{Runtime: c.Runtime}}, nil
}

// dns2NetworkNode adapts a received Network into a DNS provider. The node owns
// one dns.ResolverProvider for the current Network input and emits per-link
// detached DNS wrappers, so detaching a consumer or replacing the Network
// closes only the wrappers and provider tied to the old graph generation.
type dns2NetworkNode struct {
	nodekit.Base

	dnsOut      uint64
	networkIn   uint64
	networkLink uint64

	network  nodekit.NetworkCloser
	provider dns.Interface
	outputs  DNSOutputCache
}

func (n *dns2NetworkNode) OnInit(w *pasta.Workspace, logger pasta.Logger, id uint64, _ string, restored *pasta.NodeInitData, _, _, _, _ bool) error {
	n.InitBaseNode(w, logger, id)
	n.dnsOut = nodekit.RestoredLeft(restored, 0)
	n.networkIn = nodekit.RestoredRight(restored, 0)
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
	CloseDNS(n.provider)
	n.provider = nil
	_ = nodekit.CloseNetwork(n.network)
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
		if portDirection != "right" || linkType != nodekit.TypeNetwork {
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
	case port == n.networkIn && portDirection == "right" && linkType == nodekit.TypeNetwork:
		n.networkLink = link
		std.RequestLocked(n.W, n.ID, link)
	}
	return n.updateLabel()
}

func (n *dns2NetworkNode) OnLinkRemoved(link, port uint64, linkType, portDirection string) error {
	switch {
	case port == n.dnsOut && portDirection == "left" && linkType == TypeDNS:
		n.outputs.Close(link)
	case port == n.networkIn && portDirection == "right" && linkType == nodekit.TypeNetwork && link == n.networkLink:
		n.outputs.CloseAll()
		CloseDNS(n.provider)
		n.provider = nil
		_ = nodekit.CloseNetwork(n.network)
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
	if event.ReceiverPort != n.networkIn || receiverPortDirection != "right" || linkType != nodekit.TypeNetwork || event.Link != n.networkLink {
		return nil
	}
	next, changed := nodekit.AcceptNetworkPayload(n.W, n.ID, event, n.network)
	if !changed {
		return nil
	}
	n.network = next
	n.rebuildProvider()
	return n.updateLabel()
}

func (n *dns2NetworkNode) rebuildProvider() {
	n.outputs.CloseAll()
	CloseDNS(n.provider)
	n.provider = nil
	if !nodekit.NetworkIsReady(n.network) {
		return
	}
	// Nil spawner is intentional here; see the Gonnect spawner note in helpers.go.
	n.provider = dns.NewResolverProvider(n.network, defaultDns2NetworkTTL, nil)
	n.sendAll()
}

func (n *dns2NetworkNode) sendAll() {
	n.outputs.SendPortLocked(n.W, n.ID, n.dnsOut, n.provider)
}

func (n *dns2NetworkNode) sendToLink(link uint64) {
	n.outputs.SendLocked(n.W, n.ID, link, n.provider)
}

func (n *dns2NetworkNode) refreshLink(link uint64) {
	n.outputs.RefreshLocked(n.W, n.ID, link, n.provider)
}

func (n *dns2NetworkNode) updateLabel() error {
	if !nodekit.NetworkIsReady(n.network) {
		return n.SetLabel("waiting for network")
	}
	return n.SetLabel("resolving DNS")
}
