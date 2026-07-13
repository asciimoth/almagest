package almagest

import (
	"net/netip"

	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/formular"
	netstackvtun "github.com/asciimoth/gonnect-netstack/vtun"
	"github.com/asciimoth/pasta/pasta"
	"github.com/asciimoth/pasta/pasta/std"
)

func init() {
	regClass(func(e *engine) pasta.NodeClass {
		return vtunClass{engine: e}
	}, false)
}

const (
	// NodeTypeVTun is the node class name for a virtual network-to-TUN adapter.
	NodeTypeVTun = "almagest/VTun"

	defaultVTunName       = "vtun"
	defaultVTunMTU        = 1500
	defaultVTunEndpointCh = 1024
	defaultVTunEventCh    = 10
)

type vtunClass struct {
	engine *engine
}

func (vtunClass) ClassName() string        { return NodeTypeVTun }
func (vtunClass) ShortDescription() string { return "Virtual TUN network" }
func (vtunClass) LongDescription() string {
	return "Creates one virtual netstack instance and exposes it as both an almagest/network and an almagest/tun endpoint."
}
func (vtunClass) DefaultNodeParams() pasta.NodeClassParams {
	return pasta.NodeClassParams{PrimaryType: TypeTun, InitialPorts: []pasta.Port{
		NetworkPort("left", "Network"),
		TunPort("right", "Tun"),
	}}
}
func (c vtunClass) NewNode(cfg configer.Config, _ ...*pasta.NodeClassState) (pasta.Node, error) {
	config := defaultVTunConfig()
	config.read(cfg)
	return &vtunNode{NodeBase: NodeBase{engine: c.engine}, config: config}, nil
}

// vtunNode owns one netstack VTun and publishes detached wrappers for each
// connected network or TUN link. The underlying VTun is lazy: links create it,
// options replace it, and the last detached link closes it.
type vtunNode struct {
	NodeBase

	network uint64
	tunp    uint64

	config vtunConfig

	tunConfig TunConfig

	generatedAddrs []netip.Addr
	vtun           *netstackvtun.VTun
	vtunRuntime    vtunRuntimeConfig
	outputNetworks NetworkOutputCache
	outputTuns     TunOutputCache
	formError      string
}

func (n *vtunNode) OnInit(w *pasta.Workspace, logger pasta.Logger, id uint64, _ string, restored *pasta.NodeInitData, _, _, _, _ bool) error {
	n.InitBaseNode(w, logger, id)
	n.config.normalize()
	n.network = restoredLeft(restored, 0)
	n.tunp = restoredRight(restored, 0)
	if err := n.SetPrimary(TypeTun); err != nil {
		return err
	}
	_ = n.updateLabel()
	n.sendMenuSnapshot()
	return nil
}

func (n *vtunNode) OnReady() error {
	if err := n.ensureVTun(); err != nil {
		return nil
	}
	n.sendAll()
	return nil
}

func (n *vtunNode) OnStop() {
	n.dropVTun()
	n.freeGeneratedAddrs()
}

func (n *vtunNode) PreLinkAdd(port uint64, linkType, portDirection string) error {
	switch port {
	case n.network:
		if portDirection != "left" || linkType != TypeNetwork {
			return pasta.LinkTypeErr(linkType)
		}
	case n.tunp:
		if portDirection != "right" {
			return pasta.LinkTypeErr(linkType)
		}
		return TunPreLinkAdd(n.w, port, linkType)
	default:
		return pasta.LinkTypeErr(linkType)
	}
	return nil
}

func (n *vtunNode) OnLinkAdd(link, port uint64, _, _ string) error {
	if port != n.network && port != n.tunp {
		return nil
	}
	if n.vtun == nil {
		if err := n.rebuildVTunActive(); err != nil {
			return nil
		}
	} else if err := n.ensureVTun(); err != nil {
		return nil
	}
	switch port {
	case n.network:
		n.sendNetworkToLink(link)
	case n.tunp:
		n.sendTunToLink(link)
	}
	return nil
}

func (n *vtunNode) OnLinkRemoved(link uint64, port uint64, _, _ string) error {
	switch port {
	case n.network:
		n.outputNetworks.Close(link)
	case n.tunp:
		n.outputTuns.Close(link)
		n.tunConfig = TunConfig{}
		if n.vtun != nil && n.usesTunConfig() {
			if err := n.rebuildVTun(); err != nil {
				return nil
			}
		}
	}
	if !n.hasAnyLink() {
		n.dropVTun()
		_ = n.updateLabel()
	}
	return nil
}

func (n *vtunNode) OnEvent(event pasta.Event, linkType string, _ []string, receiverPortDirection string) error {
	if event.ReceiverPort == n.network && receiverPortDirection == "left" && linkType == TypeNetwork && std.IsRequest(event.Payload) {
		n.refreshNetworkToLink(event.Link)
		return nil
	}
	if event.ReceiverPort == n.tunp && receiverPortDirection == "right" && linkType == TypeTun {
		if std.IsRequest(event.Payload) {
			n.refreshTunToLink(event.Link)
			return nil
		}
		if cfg, ok := event.Payload.(TunConfig); ok {
			n.tunConfig = cfg.Clone()
			if n.usesTunConfig() {
				if err := n.rebuildVTun(); err != nil {
					return nil
				}
			} else {
				n.sendEffectiveBlock()
			}
		}
	}
	return nil
}

func (n *vtunNode) OnFormularMsg(message any) error {
	switch msg := message.(type) {
	case formular.FormApplyMessage:
		if msg.MenuID != pasta.NodeMenuID(n.id) || msg.BlockID != "config" {
			return nil
		}
		return n.applyConfigForm(msg.Values)
	case formular.FieldValidateMessage:
		if msg.MenuID != pasta.NodeMenuID(n.id) || msg.Field.BlockID != "config" || len(msg.Field.ElementPath) > 0 {
			return nil
		}
		n.sendConfigFieldStatus(msg)
	}
	return nil
}

func (n *vtunNode) OnSave(cfg configer.Config) error {
	if err := pasta.DeleteNodeOwnedConfigKeys(cfg); err != nil {
		return err
	}
	return configer.SetMap(cfg, map[string]any{
		"name":       n.config.name,
		"localAddrs": n.config.localAddrs,
		"dnsServers": n.config.dnsServers,
		"mtu":        n.config.mtu,
		"endpointCh": n.config.endpointCh,
		"eventCh":    n.config.eventCh,
		// "mwo":            n.config.mwo,
		// "mro":            n.config.mro,
		"noLoopbackAddr": n.config.noLoopbackAddr,
		"disableSACK":    n.config.disableSACK,
	})
}

func (n *vtunNode) applyConfigForm(values map[string]any) error {
	next := n.config
	if value, ok := formular.StringValue(values["name"]); ok {
		next.name = value
	}
	if value, ok := formular.StringValue(values["localAddrs"]); ok {
		next.localAddrs = value
	}
	if value, ok := formular.StringValue(values["dnsServers"]); ok {
		next.dnsServers = value
	}
	if value, ok := formular.IntValue(values["mtu"]); ok {
		next.mtu = value
	}
	if value, ok := formular.IntValue(values["endpointCh"]); ok {
		next.endpointCh = value
	}
	if value, ok := formular.IntValue(values["eventCh"]); ok {
		next.eventCh = value
	}
	// if value, ok := formular.IntValue(values["mwo"]); ok {
	// 	next.mwo = value
	// }
	// if value, ok := formular.IntValue(values["mro"]); ok {
	// 	next.mro = value
	// }
	if value, ok := formular.BoolValue(values["noLoopbackAddr"]); ok {
		next.noLoopbackAddr = value
	}
	if value, ok := formular.BoolValue(values["disableSACK"]); ok {
		next.disableSACK = value
	}
	next.normalize()
	if err := validateVTunConfig(next); err != nil {
		n.formError = err.Error()
		n.sendForcedMenuSnapshot()
		_ = n.updateLabel()
		return nil
	}
	n.formError = ""
	n.config = next
	n.sendForcedMenuSnapshot()
	if err := n.rebuildVTun(); err != nil {
		return nil
	}
	return nil
}
