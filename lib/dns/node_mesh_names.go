package dnsnodes

import (
	_ "embed"
	"time"

	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/formular"
	"github.com/asciimoth/gonnect"
	"github.com/asciimoth/gonnect/dns"
	"github.com/asciimoth/gonnect/dns/meshnames"
	"github.com/asciimoth/pasta/pasta"
	"github.com/asciimoth/pasta/pasta/std"

	"github.com/asciimoth/almagest/lib/nodekit"
)

const (
	// NodeTypeMeshNames is the node class name for mesh-aware DNS resolution.
	NodeTypeMeshNames = "almagest/MeshNames"

	defaultMeshNamesTTL = time.Minute

	// MeshNamesConfigBlockID is the Formular block ID for resolver options.
	MeshNamesConfigBlockID = "config"
	// MeshNamesRequestTimeoutMSField is the Formular and saved-config key for
	// authoritative mesh DNS exchanges in milliseconds.
	MeshNamesRequestTimeoutMSField = "requestTimeoutMS"
)

var (
	//go:embed node_mesh_names.md
	meshNamesClassLongDescription string
)

// MeshNamesClass builds mesh-aware DNS resolver nodes.
type MeshNamesClass struct {
	Runtime nodekit.Runtime
}

func (MeshNamesClass) ClassName() string        { return NodeTypeMeshNames }
func (MeshNamesClass) ShortDescription() string { return "MeshNames DNS" }
func (MeshNamesClass) LongDescription() string  { return meshNamesClassLongDescription }
func (MeshNamesClass) DefaultNodeParams() pasta.NodeClassParams {
	return pasta.NodeClassParams{PrimaryType: TypeDNS, InitialPorts: []pasta.Port{
		DNSPort("left", "DNS"),
		nodekit.NetworkPort("right", "Network"),
		DNSPort("right", "Fallback DNS"),
	}}
}
func (c MeshNamesClass) NewNode(cfg configer.Config, _ ...*pasta.NodeClassState) (pasta.Node, error) {
	requestTimeoutMS := configer.Int(cfg, configer.Path{MeshNamesRequestTimeoutMSField}, 0)
	return &meshNamesNode{
		Base:             nodekit.Base{Runtime: c.Runtime},
		requestTimeoutMS: requestTimeoutMS,
		formError:        dnsTimeoutErrorText(requestTimeoutMS),
	}, nil
}

// meshNamesNode adapts meshnames.Resolver to the almagest/dns graph type. The
// resolver is created only after an explicit Network input is received, so even
// direct mesh name forms such as .meship cannot resolve through an unlinked
// node. A fallback resolver is installed only from the optional Fallback DNS
// input; nil fallback intentionally lets meshnames reject non-mesh names.
type meshNamesNode struct {
	nodekit.Base

	out        uint64
	networkIn  uint64
	fallbackIn uint64

	networkLink  uint64
	fallbackLink uint64

	network     nodekit.NetworkCloser
	fallbackDNS dns.Interface
	resolver    *meshnames.Resolver
	provider    dns.Interface
	outputs     DNSOutputCache

	requestTimeoutMS int
	formError        string
}

func (n *meshNamesNode) OnInit(w *pasta.Workspace, logger pasta.Logger, id uint64, _ string, restored *pasta.NodeInitData, _, _, _, _ bool) error {
	n.InitBaseNode(w, logger, id)
	n.out = nodekit.RestoredLeft(restored, 0)
	n.networkIn = nodekit.RestoredRight(restored, 0)
	n.fallbackIn = nodekit.RestoredRight(restored, 1)
	if err := n.SetPrimary(TypeDNS); err != nil {
		return err
	}
	n.sendMenuSnapshot()
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
	CloseDNS(n.provider)
	n.provider = nil
	n.resolver = nil
	CloseDNS(n.fallbackDNS)
	n.fallbackDNS = nil
	_ = nodekit.CloseNetwork(n.network)
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
		if portDirection != "right" || linkType != nodekit.TypeNetwork {
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
	case port == n.networkIn && portDirection == "right" && linkType == nodekit.TypeNetwork:
		n.networkLink = link
		std.RequestLocked(n.W, n.ID, link)
	case port == n.fallbackIn && portDirection == "right" && linkType == TypeDNS:
		n.fallbackLink = link
		std.RequestLocked(n.W, n.ID, link)
	}
	return n.updateLabel()
}

func (n *meshNamesNode) OnLinkRemoved(link, port uint64, linkType, portDirection string) error {
	switch {
	case port == n.out && portDirection == "left" && linkType == TypeDNS:
		n.outputs.Close(link)
	case port == n.networkIn && portDirection == "right" && linkType == nodekit.TypeNetwork && link == n.networkLink:
		n.outputs.InvalidateAllLocked(n.W, n.ID)
		CloseDNS(n.provider)
		n.provider = nil
		n.resolver = nil
		_ = nodekit.CloseNetwork(n.network)
		n.network = nil
		n.networkLink = 0
	case port == n.fallbackIn && portDirection == "right" && linkType == TypeDNS && link == n.fallbackLink:
		CloseDNS(n.fallbackDNS)
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
	if event.ReceiverPort == n.networkIn && receiverPortDirection == "right" && linkType == nodekit.TypeNetwork {
		if event.Link != n.networkLink {
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
	if event.ReceiverPort == n.fallbackIn && receiverPortDirection == "right" && linkType == TypeDNS {
		if event.Link != n.fallbackLink {
			return nil
		}
		next, changed := AcceptDNSPayload(n.W, n.ID, event, n.fallbackDNS)
		if !changed {
			return nil
		}
		n.fallbackDNS = next
		n.rebuildProvider()
		return n.updateLabel()
	}
	return nil
}

func (n *meshNamesNode) OnFormularMsg(message any) error {
	switch msg := message.(type) {
	case formular.FormApplyMessage:
		if msg.MenuID != pasta.NodeMenuID(n.ID) || msg.BlockID != MeshNamesConfigBlockID {
			return nil
		}
		return n.applyConfigForm(msg.Values)
	case formular.FieldValidateMessage:
		if msg.MenuID != pasta.NodeMenuID(n.ID) || msg.Field.BlockID != MeshNamesConfigBlockID || msg.Field.FieldID != MeshNamesRequestTimeoutMSField || len(msg.Field.ElementPath) > 0 {
			return nil
		}
		nodekit.SendNodeFieldStatus(n.W, n.ID, msg.Field, validateDNSTimeoutField(msg.Value))
	}
	return nil
}

func (n *meshNamesNode) OnSave(cfg configer.Config) error {
	if err := pasta.DeleteNodeOwnedConfigKeys(cfg); err != nil {
		return err
	}
	return nodekit.SetConfigMap(cfg, map[string]any{
		MeshNamesRequestTimeoutMSField: n.requestTimeoutMS,
	})
}

func (n *meshNamesNode) applyConfigForm(values map[string]any) error {
	next := n.requestTimeoutMS
	if value, ok := formular.IntValue(values[MeshNamesRequestTimeoutMSField]); ok {
		next = value
	}
	if err := validateDNSTimeoutMilliseconds(next); err != nil {
		n.formError = err.Error()
		n.sendForcedMenuSnapshot()
		return n.updateLabel()
	}
	changed := next != n.requestTimeoutMS || n.formError != ""
	n.requestTimeoutMS = next
	n.formError = ""
	n.sendForcedMenuSnapshot()
	if changed {
		n.rebuildProvider()
	}
	return n.updateLabel()
}

func (n *meshNamesNode) rebuildProvider() {
	n.outputs.InvalidateAllLocked(n.W, n.ID)
	CloseDNS(n.provider)
	n.provider = nil
	n.resolver = nil
	if n.formError != "" || !nodekit.NetworkIsReady(n.network) {
		return
	}
	resolver := meshnames.NewResolver(n.network)
	resolver.Timeout = dnsTimeoutDuration(n.requestTimeoutMS)
	if DNSIsReady(n.fallbackDNS) {
		resolver.Fallback = dns.NewResolver(n.fallbackDNS)
	}
	n.resolver = resolver
	n.provider = dns.NewResolverProvider(resolver, defaultMeshNamesTTL, n.dnsSpawner())
	BindDNSResource(n.W, n.ID, 0, n.provider)
	n.sendAll()
}

func (n *meshNamesNode) dnsSpawner() gonnect.Spawner {
	if n.WorkerSpawner == nil {
		return nil
	}
	return n.WorkerSpawner
}

func (n *meshNamesNode) sendAll() {
	n.outputs.SendPortLocked(n.W, n.ID, n.out, n.provider)
}

func (n *meshNamesNode) sendToLink(link uint64) {
	n.outputs.SendLocked(n.W, n.ID, link, n.provider)
}

func (n *meshNamesNode) refreshLink(link uint64) {
	n.outputs.RefreshLocked(n.W, n.ID, link, n.provider)
}

func (n *meshNamesNode) updateLabel() error {
	switch {
	case n.formError != "":
		return n.SetLabel("invalid config")
	case !nodekit.NetworkIsReady(n.network):
		return n.SetLabel("waiting for network")
	case DNSIsReady(n.fallbackDNS):
		return n.SetLabel("resolving MeshNames with fallback")
	default:
		return n.SetLabel("resolving MeshNames")
	}
}

func (n *meshNamesNode) sendMenuSnapshot() {
	nodekit.SendNodeMenuSnapshot(n.W, n.ID, false, n.configBlock())
}

func (n *meshNamesNode) sendForcedMenuSnapshot() {
	nodekit.SendNodeMenuSnapshot(n.W, n.ID, true, n.configBlock())
}

func (n *meshNamesNode) configBlock() formular.Block {
	return formular.Block{ID: MeshNamesConfigBlockID, Order: 10, Generation: 1, Form: true, Items: []formular.Item{
		dnsTimeoutField(
			MeshNamesRequestTimeoutMSField,
			"Request timeout (ms)",
			n.requestTimeoutMS,
			"Maximum time for one DNS exchange with an authoritative mesh node.",
			n.formError,
		),
	}}
}
