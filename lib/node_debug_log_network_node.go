package almagest

import (
	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/pasta/pasta"
	"github.com/asciimoth/pasta/pasta/std"
)

type debugLogNetworkClass struct{}

func (debugLogNetworkClass) ClassName() string        { return NodeTypeDebugLogNetwork }
func (debugLogNetworkClass) ShortDescription() string { return "DEBUG network logger" }
func (debugLogNetworkClass) LongDescription() string {
	return "Passes almagest/network operations through unchanged while debug logging operations and payload bytes."
}
func (debugLogNetworkClass) DefaultNodeParams() pasta.NodeClassParams {
	return pasta.NodeClassParams{PrimaryType: TypeNetwork, InitialPorts: []pasta.Port{
		NetworkPort("left", "Network"),
		NetworkPort("right", "Network"),
	}}
}
func (debugLogNetworkClass) NewNode(_ configer.Config, _ ...*pasta.NodeClassState) (pasta.Node, error) {
	return &debugLogNetworkNode{}, nil
}

type debugLogNetworkNode struct {
	NodeBase

	out uint64
	in  uint64

	network NetworkCloser
	outputs NetworkOutputCache
}

func (n *debugLogNetworkNode) OnInit(w *pasta.Workspace, logger pasta.Logger, id uint64, _ string, restored *pasta.NodeInitData, _, _, _, _ bool) error {
	n.InitBaseNode(w, logger, id)
	n.out = restoredLeft(restored, 0)
	n.in = restoredRight(restored, 0)
	if err := n.SetPrimary(TypeNetwork); err != nil {
		return err
	}
	_ = n.SetLabel("network log")
	return nil
}

func (n *debugLogNetworkNode) OnStop() {
	n.outputs.CloseAll()
	if n.network != nil {
		_ = n.network.Close()
		n.network = nil
	}
}

func (n *debugLogNetworkNode) OnReady() error {
	n.requestInput()
	n.sendAll()
	return nil
}

func (n *debugLogNetworkNode) PreLinkAdd(port uint64, linkType, portDirection string) error {
	switch port {
	case n.out:
		if portDirection != "left" || linkType != TypeNetwork {
			return pasta.LinkTypeErr(linkType)
		}
	case n.in:
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

func (n *debugLogNetworkNode) OnLinkAdd(link, port uint64, linkType, portDirection string) error {
	if port == n.out && portDirection == "left" && linkType == TypeNetwork {
		n.sendNetworkToLink(link)
		return nil
	}
	if port == n.in && portDirection == "right" && linkType == TypeNetwork {
		std.RequestLocked(n.w, n.id, link)
	}
	return nil
}

func (n *debugLogNetworkNode) OnLinkRemoved(link, port uint64, linkType, portDirection string) error {
	if port == n.out && portDirection == "left" && linkType == TypeNetwork {
		n.outputs.Close(link)
		return nil
	}
	if port == n.in && portDirection == "right" && linkType == TypeNetwork {
		if n.network != nil {
			_ = n.network.Close()
		}
		n.network = nil
		n.outputs.CloseAll()
	}
	return nil
}

func (n *debugLogNetworkNode) OnEvent(event pasta.Event, linkType string, _ []string, receiverPortDirection string) error {
	if event.ReceiverPort == n.out && receiverPortDirection == "left" && linkType == TypeNetwork && std.IsRequest(event.Payload) {
		n.refreshNetworkToLink(event.Link)
		return nil
	}
	if event.ReceiverPort != n.in || receiverPortDirection != "right" || linkType != TypeNetwork {
		return nil
	}
	next, changed := AcceptNetworkPayload(n.w, n.id, event, n.network)
	if !changed {
		return nil
	}
	if n.network != nil {
		_ = n.network.Close()
	}
	n.outputs.CloseAll()
	n.network = next
	n.sendAll()
	return nil
}

func (n *debugLogNetworkNode) requestInput() {
	n.RequestPort(n.in)
}

func (n *debugLogNetworkNode) sendAll() {
	n.outputs.SendWrappedPortLocked(n.w, n.id, n.out, n.network, n.wrapNetwork)
}

func (n *debugLogNetworkNode) sendNetworkToLink(link uint64) {
	n.outputs.SendWrappedLocked(n.w, n.id, link, n.network, n.wrapNetwork)
}

func (n *debugLogNetworkNode) refreshNetworkToLink(link uint64) {
	n.outputs.RefreshWrappedLocked(n.w, n.id, link, n.network, n.wrapNetwork)
}

func (n *debugLogNetworkNode) wrapNetwork(output NetworkCloser) NetworkCloser {
	return &debugLogNetwork{NetworkCloser: output, logger: n.logger}
}
