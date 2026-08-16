package nodekit

import (
	"github.com/asciimoth/gonnect"
	"github.com/asciimoth/pasta/pasta"
	"github.com/asciimoth/pasta/pasta/std"
)

// SimpleNetworkProvider implements a one-output network provider node.
type SimpleNetworkProvider struct {
	Base

	out     uint64
	network gonnect.Network
	outputs NetworkOutputCache
}

// InitSimpleNetworkProvider initializes a one-output network provider node.
func (n *SimpleNetworkProvider) InitSimpleNetworkProvider(
	w *pasta.Workspace,
	logger pasta.Logger,
	id uint64,
	restored *pasta.NodeInitData,
	network gonnect.Network,
	label string,
) error {
	n.InitBaseNode(w, logger, id)
	n.out = RestoredLeft(restored, 0)
	n.network = network
	if err := n.SetPrimary(TypeNetwork); err != nil {
		return err
	}
	_ = n.SetLabel(label)
	return nil
}

func (n *SimpleNetworkProvider) OnReady() error {
	n.sendAll()
	return nil
}

func (n *SimpleNetworkProvider) OnStop() {
	n.outputs.CloseAll()
}

func (n *SimpleNetworkProvider) PreLinkAdd(port uint64, linkType, portDirection string) error {
	if port != n.out || portDirection != "left" || linkType != TypeNetwork {
		return pasta.LinkTypeErr(linkType)
	}
	return nil
}

func (n *SimpleNetworkProvider) OnLinkAdd(link, port uint64, linkType, portDirection string) error {
	if port == n.out && portDirection == "left" && linkType == TypeNetwork {
		n.sendToLink(link)
	}
	return nil
}

func (n *SimpleNetworkProvider) OnLinkRemoved(link uint64, port uint64, linkType, portDirection string) error {
	if port == n.out && portDirection == "left" && linkType == TypeNetwork {
		n.outputs.Close(link)
	}
	return nil
}

func (n *SimpleNetworkProvider) OnEvent(event pasta.Event, linkType string, _ []string, receiverPortDirection string) error {
	if event.ReceiverPort != n.out || receiverPortDirection != "left" || linkType != TypeNetwork || !std.IsRequest(event.Payload) {
		return nil
	}
	n.refreshLink(event.Link)
	return nil
}

func (n *SimpleNetworkProvider) sendAll() {
	n.outputs.SendPortLocked(n.W, n.ID, n.out, n.network)
}

func (n *SimpleNetworkProvider) sendToLink(link uint64) {
	n.outputs.SendLocked(n.W, n.ID, link, n.network)
}

func (n *SimpleNetworkProvider) refreshLink(link uint64) {
	n.outputs.RefreshLocked(n.W, n.ID, link, n.network)
}
