package dnsnodes

import (
	"github.com/asciimoth/gonnect/dns"
	"github.com/asciimoth/pasta/pasta"
	"github.com/asciimoth/pasta/pasta/std"

	"github.com/asciimoth/almagest/lib/nodekit"
)

// SimpleDNSProvider emits one provider on one left-directed DNS port.
type SimpleDNSProvider struct {
	nodekit.Base

	out      uint64
	resolver dns.Interface
	outputs  DNSOutputCache
}

// InitSimpleDNSProvider initializes a one-output DNS provider node.
func (n *SimpleDNSProvider) InitSimpleDNSProvider(
	w *pasta.Workspace,
	logger pasta.Logger,
	id uint64,
	restored *pasta.NodeInitData,
	resolver dns.Interface,
	label string,
) error {
	n.InitBaseNode(w, logger, id)
	n.out = nodekit.RestoredLeft(restored, 0)
	n.resolver = resolver
	if err := n.SetPrimary(TypeDNS); err != nil {
		return err
	}
	_ = n.SetLabel(label)
	return nil
}

func (n *SimpleDNSProvider) OnReady() error {
	n.sendAll()
	return nil
}

func (n *SimpleDNSProvider) OnStop() {
	n.outputs.CloseAll()
	n.resolver = nil
}

func (n *SimpleDNSProvider) PreLinkAdd(port uint64, linkType, portDirection string) error {
	if port != n.out || portDirection != "left" || linkType != TypeDNS {
		return pasta.LinkTypeErr(linkType)
	}
	return nil
}

func (n *SimpleDNSProvider) OnLinkAdd(link, port uint64, linkType, portDirection string) error {
	if port == n.out && portDirection == "left" && linkType == TypeDNS {
		n.sendToLink(link)
	}
	return nil
}

func (n *SimpleDNSProvider) OnLinkRemoved(link uint64, port uint64, linkType, portDirection string) error {
	if port == n.out && portDirection == "left" && linkType == TypeDNS {
		n.outputs.Close(link)
	}
	return nil
}

func (n *SimpleDNSProvider) OnEvent(event pasta.Event, linkType string, _ []string, receiverPortDirection string) error {
	if event.ReceiverPort != n.out || receiverPortDirection != "left" || linkType != TypeDNS || !std.IsRequest(event.Payload) {
		return nil
	}
	n.refreshLink(event.Link)
	return nil
}

func (n *SimpleDNSProvider) sendAll() {
	n.outputs.SendPortLocked(n.W, n.ID, n.out, n.resolver)
}

func (n *SimpleDNSProvider) sendToLink(link uint64) {
	n.outputs.SendLocked(n.W, n.ID, link, n.resolver)
}

func (n *SimpleDNSProvider) refreshLink(link uint64) {
	n.outputs.RefreshLocked(n.W, n.ID, link, n.resolver)
}
