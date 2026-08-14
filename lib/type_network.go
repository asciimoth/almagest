package almagest

import (
	"github.com/asciimoth/gonnect"
	"github.com/asciimoth/pasta/pasta"
	"github.com/asciimoth/pasta/pasta/std"

	"github.com/asciimoth/almagest/lib/links"
)

const TypeNetwork = links.TypeNetwork

type NetworkCloser = links.NetworkCloser
type NetworkPayload = links.NetworkPayload
type NetworkOutputCache = links.NetworkOutputCache

func NetworkPort(direction, name string) pasta.Port {
	return links.NetworkPort(direction, name)
}

func BindNetworkResource(w *pasta.Workspace, node, link uint64, n NetworkCloser) {
	links.BindNetworkResource(w, node, link, n)
}

func EmitDetachedNetwork(w *pasta.Workspace, node, link uint64, source gonnect.Network) NetworkCloser {
	return links.EmitDetachedNetwork(w, node, link, source)
}

func BroadcastNetworkPort(w *pasta.Workspace, node, port uint64, send func(link uint64)) {
	links.BroadcastNetworkPort(w, node, port, send)
}

func acceptNetworkPayload(event pasta.Event, current NetworkCloser) (NetworkCloser, bool) {
	return links.AcceptNetworkPayloadValue(event, current)
}

func AcceptNetworkPayload(w *pasta.Workspace, node uint64, event pasta.Event, current NetworkCloser) (NetworkCloser, bool) {
	return links.AcceptNetworkPayload(w, node, event, current)
}

func SameNetworkSource(a, b any) bool {
	return links.SameNetworkSource(a, b)
}

func SameNetworkInstance(a, b NetworkCloser) bool {
	return links.SameNetworkInstance(a, b)
}

func SameNetworkResolver(a, b gonnect.Resolver) bool {
	return links.SameNetworkResolver(a, b)
}

func closeNetwork(n NetworkCloser) error {
	return links.CloseNetwork(n)
}

func NetworkSource(n any) any {
	return links.NetworkSource(n)
}

func SameComparableValue(a, b any) bool {
	return links.SameComparableValue(a, b)
}

func NetworkIsReady(n NetworkCloser) bool {
	return links.NetworkIsReady(n)
}

type simpleNetworkProvider struct {
	NodeBase

	out     uint64
	network gonnect.Network
	outputs NetworkOutputCache
}

func (n *simpleNetworkProvider) InitSimpleNetworkProvider(w *pasta.Workspace, logger pasta.Logger, id uint64, restored *pasta.NodeInitData, network gonnect.Network, label string) error {
	n.InitBaseNode(w, logger, id)
	n.out = restoredLeft(restored, 0)
	n.network = network
	if err := n.SetPrimary(TypeNetwork); err != nil {
		return err
	}
	_ = n.SetLabel(label)
	return nil
}

func (n *simpleNetworkProvider) OnReady() error {
	n.sendAll()
	return nil
}

func (n *simpleNetworkProvider) OnStop() {
	n.outputs.CloseAll()
}

func (n *simpleNetworkProvider) PreLinkAdd(port uint64, linkType, portDirection string) error {
	if port != n.out || portDirection != "left" || linkType != TypeNetwork {
		return pasta.LinkTypeErr(linkType)
	}
	return nil
}

func (n *simpleNetworkProvider) OnLinkAdd(link, port uint64, linkType, portDirection string) error {
	if port == n.out && portDirection == "left" && linkType == TypeNetwork {
		n.sendToLink(link)
	}
	return nil
}

func (n *simpleNetworkProvider) OnLinkRemoved(link uint64, port uint64, linkType, portDirection string) error {
	if port == n.out && portDirection == "left" && linkType == TypeNetwork {
		n.outputs.Close(link)
	}
	return nil
}

func (n *simpleNetworkProvider) OnEvent(event pasta.Event, linkType string, _ []string, receiverPortDirection string) error {
	if event.ReceiverPort != n.out || receiverPortDirection != "left" || linkType != TypeNetwork || !std.IsRequest(event.Payload) {
		return nil
	}
	n.refreshLink(event.Link)
	return nil
}

func (n *simpleNetworkProvider) sendAll() {
	n.outputs.SendPortLocked(n.w, n.id, n.out, n.network)
}

func (n *simpleNetworkProvider) sendToLink(link uint64) {
	n.outputs.SendLocked(n.w, n.id, link, n.network)
}

func (n *simpleNetworkProvider) refreshLink(link uint64) {
	n.outputs.RefreshLocked(n.w, n.id, link, n.network)
}
