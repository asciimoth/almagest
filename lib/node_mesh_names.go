package almagest

import (
	_ "embed"
	"time"

	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/gonnect"
	"github.com/asciimoth/gonnect/dns"
	"github.com/asciimoth/mnlib"
	"github.com/asciimoth/pasta/pasta"
	"github.com/asciimoth/pasta/pasta/std"
)

func init() {
	regClass(func(e *engine) pasta.NodeClass {
		return meshNamesClass{engine: e}
	}, false)
}

const (
	// NodeTypeMeshNames is the node class name for mesh-aware DNS resolution.
	NodeTypeMeshNames = "almagest/MeshNames"

	defaultMeshNamesTTL = time.Minute
)

var (
	//go:embed node_mesh_names.md
	meshNamesClassLongDescription string
)

type meshNamesClass struct {
	engine *engine
}

func (meshNamesClass) ClassName() string        { return NodeTypeMeshNames }
func (meshNamesClass) ShortDescription() string { return "MeshNames DNS" }
func (meshNamesClass) LongDescription() string  { return meshNamesClassLongDescription }
func (meshNamesClass) DefaultNodeParams() pasta.NodeClassParams {
	return pasta.NodeClassParams{PrimaryType: TypeDNS, InitialPorts: []pasta.Port{
		DNSPort("left", "DNS"),
		NetworkPort("right", "Network"),
		DNSPort("right", "Fallback DNS"),
	}}
}
func (c meshNamesClass) NewNode(_ configer.Config, _ ...*pasta.NodeClassState) (pasta.Node, error) {
	return &meshNamesNode{NodeBase: NodeBase{engine: c.engine}}, nil
}

// meshNamesNode adapts mnlib.Resolver to the almagest/dns graph type. The
// resolver is created only after an explicit Network input is received, so even
// direct mnlib name forms such as .meship cannot resolve through an unlinked
// node. A fallback resolver is installed only from the optional Fallback DNS
// input; nil fallback intentionally lets mnlib reject non-mesh names.
type meshNamesNode struct {
	NodeBase

	out        uint64
	networkIn  uint64
	fallbackIn uint64

	networkLink  uint64
	fallbackLink uint64

	network     NetworkCloser
	fallbackDNS dns.Interface
	resolver    *mnlib.Resolver
	provider    dns.Interface
	outputs     DNSOutputCache
}

func (n *meshNamesNode) OnInit(w *pasta.Workspace, logger pasta.Logger, id uint64, _ string, restored *pasta.NodeInitData, _, _, _, _ bool) error {
	n.InitBaseNode(w, logger, id)
	n.out = restoredLeft(restored, 0)
	n.networkIn = restoredRight(restored, 0)
	n.fallbackIn = restoredRight(restored, 1)
	if err := n.SetPrimary(TypeDNS); err != nil {
		return err
	}
	return n.updateLabel()
}

func (n *meshNamesNode) OnReady() error {
	n.RequestPort(n.networkIn)
	n.RequestPort(n.fallbackIn)
	n.sendAll()
	return nil
}

func (n *meshNamesNode) OnStop() {
	n.outputs.CloseAll()
	closeDNS(n.provider)
	n.provider = nil
	n.resolver = nil
	closeDNS(n.fallbackDNS)
	n.fallbackDNS = nil
	_ = closeNetwork(n.network)
	n.network = nil
	n.networkLink = 0
	n.fallbackLink = 0
}

func (n *meshNamesNode) PreLinkAdd(port uint64, linkType, portDirection string) error {
	switch port {
	case n.out:
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
	case n.fallbackIn:
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

func (n *meshNamesNode) OnLinkAdd(link, port uint64, linkType, portDirection string) error {
	switch {
	case port == n.out && portDirection == "left" && linkType == TypeDNS:
		n.sendToLink(link)
	case port == n.networkIn && portDirection == "right" && linkType == TypeNetwork:
		n.networkLink = link
		std.RequestLocked(n.w, n.id, link)
	case port == n.fallbackIn && portDirection == "right" && linkType == TypeDNS:
		n.fallbackLink = link
		std.RequestLocked(n.w, n.id, link)
	}
	return n.updateLabel()
}

func (n *meshNamesNode) OnLinkRemoved(link, port uint64, linkType, portDirection string) error {
	switch {
	case port == n.out && portDirection == "left" && linkType == TypeDNS:
		n.outputs.Close(link)
	case port == n.networkIn && portDirection == "right" && linkType == TypeNetwork && link == n.networkLink:
		n.outputs.CloseAll()
		closeDNS(n.provider)
		n.provider = nil
		n.resolver = nil
		_ = closeNetwork(n.network)
		n.network = nil
		n.networkLink = 0
	case port == n.fallbackIn && portDirection == "right" && linkType == TypeDNS && link == n.fallbackLink:
		closeDNS(n.fallbackDNS)
		n.fallbackDNS = nil
		n.fallbackLink = 0
		n.rebuildProvider()
	}
	return n.updateLabel()
}

func (n *meshNamesNode) OnEvent(event pasta.Event, linkType string, _ []string, receiverPortDirection string) error {
	if event.ReceiverPort == n.out && receiverPortDirection == "left" && linkType == TypeDNS && std.IsRequest(event.Payload) {
		n.refreshLink(event.Link)
		return nil
	}
	if event.ReceiverPort == n.networkIn && receiverPortDirection == "right" && linkType == TypeNetwork {
		if event.Link != n.networkLink {
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
	if event.ReceiverPort == n.fallbackIn && receiverPortDirection == "right" && linkType == TypeDNS {
		if event.Link != n.fallbackLink {
			return nil
		}
		next, changed := AcceptDNSPayload(n.w, n.id, event, n.fallbackDNS)
		if !changed {
			return nil
		}
		n.fallbackDNS = next
		n.rebuildProvider()
		return n.updateLabel()
	}
	return nil
}

func (n *meshNamesNode) rebuildProvider() {
	n.outputs.CloseAll()
	closeDNS(n.provider)
	n.provider = nil
	n.resolver = nil
	if !NetworkIsReady(n.network) {
		return
	}
	resolver := mnlib.NewResolver(n.network)
	if DNSIsReady(n.fallbackDNS) {
		resolver.Fallback = dns.NewResolver(n.fallbackDNS)
	}
	n.resolver = resolver
	n.provider = dns.NewResolverProvider(resolver, defaultMeshNamesTTL, n.dnsSpawner())
	BindDNSResource(n.w, n.id, 0, n.provider)
	n.sendAll()
}

func (n *meshNamesNode) dnsSpawner() gonnect.Spawner {
	if n.workerSpawner.tracker == nil {
		return nil
	}
	return n.workerSpawner
}

func (n *meshNamesNode) sendAll() {
	BroadcastDNSPort(n.w, n.id, n.out, func(link uint64) {
		n.sendToLink(link)
	})
}

func (n *meshNamesNode) sendToLink(link uint64) {
	if n.provider == nil {
		return
	}
	n.outputs.SendLocked(n.w, n.id, link, n.provider)
}

func (n *meshNamesNode) refreshLink(link uint64) {
	if n.provider == nil {
		return
	}
	n.outputs.RefreshLocked(n.w, n.id, link, n.provider)
}

func (n *meshNamesNode) updateLabel() error {
	switch {
	case !NetworkIsReady(n.network):
		return n.SetLabel("waiting for network")
	case DNSIsReady(n.fallbackDNS):
		return n.SetLabel("resolving MeshNames with fallback")
	default:
		return n.SetLabel("resolving MeshNames")
	}
}
