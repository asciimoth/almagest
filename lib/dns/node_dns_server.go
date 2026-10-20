package dnsnodes

import (
	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/formular"
	"github.com/asciimoth/gonnect/dns"
	"github.com/asciimoth/pasta/pasta"
	"github.com/asciimoth/pasta/pasta/std"

	"github.com/asciimoth/almagest/lib/nodekit"
)

const (
	// NodeTypeDNSServer is the node class name for a UDP DNS server backed by
	// gonnect/dns.Server.
	NodeTypeDNSServer = "almagest/DnsServer"
)

// DNSServerClass builds DNS server nodes.
type DNSServerClass struct {
	Runtime nodekit.Runtime
}

func (DNSServerClass) ClassName() string        { return NodeTypeDNSServer }
func (DNSServerClass) ShortDescription() string { return "DNS server" }
func (DNSServerClass) LongDescription() string {
	return "Serves DNS requests through an attached Network."
}
func (DNSServerClass) DefaultNodeParams() pasta.NodeClassParams {
	return pasta.NodeClassParams{PrimaryType: TypeDNS, InitialPorts: []pasta.Port{
		nodekit.NetworkPort("right", "Network"),
		DNSPort("right", "DNS"),
	}}
}
func (c DNSServerClass) NewNode(cfg configer.Config, _ ...*pasta.NodeClassState) (pasta.Node, error) {
	config := defaultDNSServerConfig()
	err := config.read(cfg)
	node := &dnsServerNode{Base: nodekit.Base{Runtime: c.Runtime}, config: config}
	if err != nil {
		node.formError = err.Error()
	}
	return node, nil
}

// dnsServerNode owns one UDP packet listener and one gonnect/dns.Server for
// the current network and listen config. The DNS upstream is kept separate from
// listener lifecycle so DNS link changes can attach to the live server without
// closing the packet socket.
type dnsServerNode struct {
	nodekit.Base

	networkIn uint64
	dnsIn     uint64

	networkLink uint64
	dnsLink     uint64

	config dnsServerConfig

	network  nodekit.NetworkCloser
	upstream dns.Interface

	worker    *dnsServerWorker
	server    dnsServerRuntime
	restartID uint64
	listenErr string

	formError string
	logs      []formular.LogLine
}

func (n *dnsServerNode) OnInit(w *pasta.Workspace, logger pasta.Logger, id uint64, _ string, restored *pasta.NodeInitData, _, _, _, _ bool) error {
	n.InitBaseNode(w, logger, id)
	n.networkIn = nodekit.RestoredRight(restored, 0)
	n.dnsIn = nodekit.RestoredRight(restored, 1)
	if err := n.SetPrimary(TypeDNS); err != nil {
		return err
	}
	if n.formError == "" {
		if err := n.config.valid(); err != nil {
			n.formError = err.Error()
		}
	}
	_ = n.updateLabel()
	n.sendMenuSnapshot()
	return nil
}

func (n *dnsServerNode) OnReady() error {
	n.RequestPort(n.networkIn)
	n.RequestPort(n.dnsIn)
	n.restartServer()
	return nil
}

func (n *dnsServerNode) OnStop() {
	n.stopServer()
	_ = nodekit.CloseNetwork(n.network)
	n.network = nil
	CloseDNS(n.upstream)
	n.upstream = nil
	n.networkLink = 0
	n.dnsLink = 0
}

func (n *dnsServerNode) PreLinkAdd(port uint64, linkType, portDirection string) error {
	switch port {
	case n.networkIn:
		if portDirection != "right" || linkType != nodekit.TypeNetwork {
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

func (n *dnsServerNode) OnLinkAdd(link, port uint64, linkType, portDirection string) error {
	switch {
	case port == n.networkIn && portDirection == "right" && linkType == nodekit.TypeNetwork:
		n.networkLink = link
		std.RequestLocked(n.W, n.ID, link)
	case port == n.dnsIn && portDirection == "right" && linkType == TypeDNS:
		n.dnsLink = link
		std.RequestLocked(n.W, n.ID, link)
	}
	return n.updateLabel()
}

func (n *dnsServerNode) OnLinkRemoved(link, port uint64, linkType, portDirection string) error {
	switch {
	case port == n.networkIn && portDirection == "right" && linkType == nodekit.TypeNetwork && link == n.networkLink:
		old := n.network
		n.network = nil
		n.networkLink = 0
		n.restartServer()
		_ = nodekit.CloseNetwork(old)
	case port == n.dnsIn && portDirection == "right" && linkType == TypeDNS && link == n.dnsLink:
		CloseDNS(n.upstream)
		n.upstream = nil
		n.dnsLink = 0
		n.attachUpstream()
	}
	return n.updateLabel()
}

func (n *dnsServerNode) OnEvent(event pasta.Event, linkType string, _ []string, receiverPortDirection string) error {
	if event.ReceiverPort == n.networkIn && receiverPortDirection == "right" && linkType == nodekit.TypeNetwork {
		if event.Link != n.networkLink {
			return nil
		}
		next, changed := nodekit.AcceptNetworkPayload(n.W, n.ID, event, n.network)
		if !changed {
			return nil
		}
		n.network = next
		n.restartServer()
		return n.updateLabel()
	}
	if event.ReceiverPort == n.dnsIn && receiverPortDirection == "right" && linkType == TypeDNS {
		if event.Link != n.dnsLink {
			return nil
		}
		next, changed := AcceptDNSPayload(n.W, n.ID, event, n.upstream)
		if !changed {
			return nil
		}
		n.upstream = next
		n.attachUpstream()
		return n.updateLabel()
	}
	return nil
}

func (n *dnsServerNode) OnInbox(message pasta.InboxMessage) error {
	switch payload := message.Payload.(type) {
	case dnsServerStart:
		if payload.id == n.restartID && n.worker == nil && nodekit.NetworkIsReady(n.network) && n.formError == "" {
			n.startServer(payload.id, payload.cfg)
		}
	case dnsServerReady:
		n.onServerReady(payload)
	case dnsServerRuntimeFailed:
		n.onServerRuntimeFailed(payload)
	case dnsServerLog:
		n.appendLog(payload.level, payload.text)
		n.sendLogsBlock()
	}
	return nil
}

func (n *dnsServerNode) OnFormularMsg(message any) error {
	switch msg := message.(type) {
	case formular.FormApplyMessage:
		if msg.MenuID != pasta.NodeMenuID(n.ID) || msg.BlockID != DNSServerConfigBlockID {
			return nil
		}
		return n.applyConfigForm(msg.Values)
	case formular.FieldValidateMessage:
		if msg.MenuID != pasta.NodeMenuID(n.ID) || msg.Field.BlockID != DNSServerConfigBlockID || len(msg.Field.ElementPath) > 0 {
			return nil
		}
		n.sendConfigFieldStatus(msg)
	}
	return nil
}
