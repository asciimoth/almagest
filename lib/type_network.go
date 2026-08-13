package almagest

import (
	"io"
	"reflect"

	"github.com/asciimoth/gonnect"
	"github.com/asciimoth/pasta/pasta"
	"github.com/asciimoth/pasta/pasta/std"
)

// TypeNetwork is the Pasta link type carrying Network values backed by
// gonnect.Network+io.Closer.
//
// Values flow from left-directed ports to right-directed ports, which
// corresponds to right-to-left graph data flow. A node owning a left-directed
// pasta/network port sends its current Network instance, whose underlying type
// implements gonnect.Network+io.Closer, on every connected link when a new
// link connects, when OnReady runs, when the owned Network instance is replaced,
// and when the peer sends std.RequestValue from the right-directed side.
// If a left-directed port does not yet have a ready Network instance, it
// defers sending until a value becomes available, then broadcasts to all
// connected peers. A node owning a right-directed pasta/network port may send
// RequestValue when it needs the current Network object again. Both sides must
// register any received Network object with the Workspace resource tracking
// system, bound to the owning node and link. The event payload implements
// std.ClosablePayload, so middleware nodes such as std.SelectClass can close a
// previously-routed wrapper when switching paths. Peers that only implement
// gonnect.Network without io.Closer should wrap their instance via
// gonnect.DetachNetwork before using it with this link type. Left-directed
// pasta/network ports allow an arbitrary count of incoming links; right-directed
// pasta/network ports allow at most one outgoing link.
const TypeNetwork = "almagest/network"

var (
	_ std.ClosablePayload = &NetworkPayload{}
)

type NetworkCloser interface {
	gonnect.Network
	io.Closer
}

type NetworkPayload struct {
	Network NetworkCloser
}

func (p NetworkPayload) Close() error {
	if p.Network == nil {
		return nil
	}
	return p.Network.Close()
}

func NetworkPort(direction, name string) pasta.Port {
	return pasta.Port{Direction: direction, Name: name, Types: []string{TypeNetwork}}
}

func BindNetworkResource(w *pasta.Workspace, node, link uint64, n NetworkCloser) {
	if w == nil || n == nil {
		return
	}
	_ = w.AddNodeResourceLocked(node, n)
	if link != 0 {
		_ = w.AddLinkResourceLocked(link, n)
	}
}

// EmitDetachedNetwork sends a detached NetworkPayload for source on link and
// binds the wrapper to node and link resources.
func EmitDetachedNetwork(w *pasta.Workspace, node, link uint64, source gonnect.Network) NetworkCloser {
	if w == nil || link == 0 || source == nil {
		return nil
	}
	if _, ok := w.LinkSnapshotLocked(link); !ok {
		return nil
	}
	// Nil spawner is intentional here; see the Gonnect spawner note in helpers.go.
	wrapper := gonnect.DetachNetwork(source, nil, nil)
	BindNetworkResource(w, node, link, wrapper)
	w.EmitEventLocked(node, link, NetworkPayload{Network: wrapper})
	return wrapper
}

// BroadcastNetworkPort calls send for every link attached to a network output
// port.
func BroadcastNetworkPort(w *pasta.Workspace, _ uint64, port uint64, send func(link uint64)) {
	forEachPortLink(w, port, send)
}

type networkOutputEntry struct {
	source   gonnect.Network
	resolver gonnect.Resolver
	output   NetworkCloser
}

// NetworkOutputCache owns detached network wrappers emitted by a node, keyed
// by link.
type NetworkOutputCache struct {
	entries map[uint64]networkOutputEntry
}

// Send emits source as a detached NetworkPayload on link. It re-emits a
// cached ready wrapper for the same source and replaces stale wrappers.
func (c *NetworkOutputCache) Send(w *pasta.Workspace, node, link uint64, source gonnect.Network) bool {
	return c.SendWrapped(w, node, link, source, nil)
}

// SendResolved emits source as a detached NetworkPayload on link, using
// resolver for lookup methods and hostname pre-resolution before operations
// reach source.
func (c *NetworkOutputCache) SendResolved(w *pasta.Workspace, node, link uint64, source gonnect.Network, resolver gonnect.Resolver) bool {
	return c.SendResolvedWrapped(w, node, link, source, resolver, nil)
}

// SendLocked is Send for callers already running under the workspace lock.
func (c *NetworkOutputCache) SendLocked(w *pasta.Workspace, node, link uint64, source gonnect.Network) bool {
	return c.SendWrappedLocked(w, node, link, source, nil)
}

// SendResolvedLocked is SendResolved for callers already running under the
// workspace lock.
func (c *NetworkOutputCache) SendResolvedLocked(w *pasta.Workspace, node, link uint64, source gonnect.Network, resolver gonnect.Resolver) bool {
	return c.SendResolvedWrappedLocked(w, node, link, source, resolver, nil)
}

// RefreshLocked emits a fresh detached NetworkPayload on link even when the
// source is unchanged. Use it to answer std.Request callbacks from middleware
// that may have just closed the previously routed payload.
func (c *NetworkOutputCache) RefreshLocked(w *pasta.Workspace, node, link uint64, source gonnect.Network) bool {
	return c.RefreshWrappedLocked(w, node, link, source, nil)
}

// RefreshResolvedLocked emits a fresh resolver-backed detached NetworkPayload
// on link even when the source and resolver are unchanged.
func (c *NetworkOutputCache) RefreshResolvedLocked(w *pasta.Workspace, node, link uint64, source gonnect.Network, resolver gonnect.Resolver) bool {
	return c.RefreshResolvedWrappedLocked(w, node, link, source, resolver, nil)
}

// SendWrapped emits source as a detached NetworkPayload on link after wrapping
// the detached network with wrap.
func (c *NetworkOutputCache) SendWrapped(w *pasta.Workspace, node, link uint64, source gonnect.Network, wrap func(NetworkCloser) NetworkCloser) bool {
	return c.SendResolvedWrapped(w, node, link, source, nil, wrap)
}

// SendResolvedWrapped emits source as a resolver-backed detached NetworkPayload
// on link after wrapping the detached network with wrap.
func (c *NetworkOutputCache) SendResolvedWrapped(w *pasta.Workspace, node, link uint64, source gonnect.Network, resolver gonnect.Resolver, wrap func(NetworkCloser) NetworkCloser) bool {
	if w == nil {
		return false
	}
	w.Lock()
	defer w.Unlock()
	return c.SendResolvedWrappedLocked(w, node, link, source, resolver, wrap)
}

// SendWrappedLocked is SendWrapped for callers already running under the
// workspace lock.
func (c *NetworkOutputCache) SendWrappedLocked(w *pasta.Workspace, node, link uint64, source gonnect.Network, wrap func(NetworkCloser) NetworkCloser) bool {
	return c.SendResolvedWrappedLocked(w, node, link, source, nil, wrap)
}

// SendResolvedWrappedLocked is SendResolvedWrapped for callers already running
// under the workspace lock.
func (c *NetworkOutputCache) SendResolvedWrappedLocked(w *pasta.Workspace, node, link uint64, source gonnect.Network, resolver gonnect.Resolver, wrap func(NetworkCloser) NetworkCloser) bool {
	if w == nil || link == 0 || source == nil {
		return false
	}
	if _, ok := w.LinkSnapshotLocked(link); !ok {
		return false
	}
	if c.entries != nil {
		if entry, ok := c.entries[link]; ok && NetworkIsReady(entry.output) && SameNetworkSource(entry.source, source) && SameNetworkResolver(entry.resolver, resolver) {
			w.EmitEventLocked(node, link, NetworkPayload{Network: entry.output})
			return true
		}
	}
	c.Close(link)
	return c.emitWrappedLocked(w, node, link, source, resolver, wrap)
}

// SendPortLocked emits source to every link attached to port.
func (c *NetworkOutputCache) SendPortLocked(w *pasta.Workspace, node, port uint64, source gonnect.Network) {
	c.SendResolvedWrappedPortLocked(w, node, port, source, nil, nil)
}

// SendResolvedPortLocked emits source with resolver to every link attached to
// port.
func (c *NetworkOutputCache) SendResolvedPortLocked(w *pasta.Workspace, node, port uint64, source gonnect.Network, resolver gonnect.Resolver) {
	c.SendResolvedWrappedPortLocked(w, node, port, source, resolver, nil)
}

// SendWrappedPortLocked emits source to every link attached to port after
// wrapping each detached network with wrap.
func (c *NetworkOutputCache) SendWrappedPortLocked(w *pasta.Workspace, node, port uint64, source gonnect.Network, wrap func(NetworkCloser) NetworkCloser) {
	c.SendResolvedWrappedPortLocked(w, node, port, source, nil, wrap)
}

// SendResolvedWrappedPortLocked emits source with resolver to every link
// attached to port after wrapping each detached network with wrap.
func (c *NetworkOutputCache) SendResolvedWrappedPortLocked(w *pasta.Workspace, node, port uint64, source gonnect.Network, resolver gonnect.Resolver, wrap func(NetworkCloser) NetworkCloser) {
	BroadcastNetworkPort(w, node, port, func(link uint64) {
		c.SendResolvedWrappedLocked(w, node, link, source, resolver, wrap)
	})
}

// RefreshWrappedLocked is RefreshLocked with an additional wrapper hook.
func (c *NetworkOutputCache) RefreshWrappedLocked(w *pasta.Workspace, node, link uint64, source gonnect.Network, wrap func(NetworkCloser) NetworkCloser) bool {
	return c.RefreshResolvedWrappedLocked(w, node, link, source, nil, wrap)
}

// RefreshResolvedWrappedLocked is RefreshResolvedLocked with an additional
// wrapper hook.
func (c *NetworkOutputCache) RefreshResolvedWrappedLocked(w *pasta.Workspace, node, link uint64, source gonnect.Network, resolver gonnect.Resolver, wrap func(NetworkCloser) NetworkCloser) bool {
	if w == nil || link == 0 || source == nil {
		return false
	}
	if _, ok := w.LinkSnapshotLocked(link); !ok {
		return false
	}
	c.Close(link)
	return c.emitWrappedLocked(w, node, link, source, resolver, wrap)
}

func (c *NetworkOutputCache) emitWrappedLocked(w *pasta.Workspace, node, link uint64, source gonnect.Network, resolver gonnect.Resolver, wrap func(NetworkCloser) NetworkCloser) bool {
	// Nil spawner is intentional here; see the Gonnect spawner note in helpers.go.
	var output NetworkCloser = gonnect.DetachNetwork(source, resolver, nil)
	if wrap != nil {
		wrapped := wrap(output)
		if wrapped == nil {
			_ = output.Close()
			return false
		}
		output = wrapped
	}
	BindNetworkResource(w, node, link, output)
	if c.entries == nil {
		c.entries = make(map[uint64]networkOutputEntry)
	}
	c.entries[link] = networkOutputEntry{source: source, resolver: resolver, output: output}
	w.EmitEventLocked(node, link, NetworkPayload{Network: output})
	return true
}

// Close closes and forgets the wrapper emitted on link.
func (c *NetworkOutputCache) Close(link uint64) {
	if c == nil || c.entries == nil {
		return
	}
	entry := c.entries[link]
	delete(c.entries, link)
	if entry.output != nil {
		_ = entry.output.Close()
	}
}

// CloseAll closes and forgets all emitted wrappers.
func (c *NetworkOutputCache) CloseAll() {
	if c == nil {
		return
	}
	for link, entry := range c.entries {
		delete(c.entries, link)
		if entry.output != nil {
			_ = entry.output.Close()
		}
	}
}

func acceptNetworkPayload(event pasta.Event, current NetworkCloser) (NetworkCloser, bool) {
	if std.IsRequest(event.Payload) {
		return current, false
	}
	payload, ok := event.Payload.(NetworkPayload)
	if !ok || !NetworkIsReady(payload.Network) {
		return current, false
	}
	if SameNetworkInstance(current, payload.Network) {
		return current, false
	}
	_ = closeNetwork(current)
	return payload.Network, true
}

// AcceptNetworkPayload validates and binds a received NetworkPayload.
func AcceptNetworkPayload(w *pasta.Workspace, node uint64, event pasta.Event, current NetworkCloser) (NetworkCloser, bool) {
	next, changed := acceptNetworkPayload(event, current)
	if !changed {
		return next, false
	}
	BindNetworkResource(w, node, event.Link, next)
	return next, true
}

func SameNetworkSource(a, b any) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return SameComparableValue(NetworkSource(a), NetworkSource(b))
}

func SameNetworkInstance(a, b NetworkCloser) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return SameComparableValue(a, b)
}

func SameNetworkResolver(a, b gonnect.Resolver) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return SameComparableValue(a, b)
}

func closeNetwork(n NetworkCloser) error {
	if n == nil {
		return nil
	}
	return n.Close()
}

func NetworkSource(n any) any {
	for n != nil {
		wrapped := gonnect.GetWrapped(n)
		if wrapped == nil {
			return n
		}
		n = wrapped
	}
	return nil
}

func SameComparableValue(a, b any) bool {
	av := reflect.ValueOf(a)
	bv := reflect.ValueOf(b)
	if !av.IsValid() || !bv.IsValid() {
		return !av.IsValid() && !bv.IsValid()
	}
	if av.Type() != bv.Type() || !av.Type().Comparable() {
		return false
	}
	return av.Interface() == bv.Interface()
}

func NetworkIsReady(n NetworkCloser) bool {
	if n == nil {
		return false
	}
	updown, ok := n.(interface {
		IsUp() (bool, error)
	})
	if !ok {
		return true
	}
	up, err := updown.IsUp()
	return err == nil && up
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
