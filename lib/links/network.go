package links

import (
	"io"

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
// implements gonnect.Network+io.Closer, on every connected link when a new link
// connects, when OnReady runs, when the owned Network instance is replaced, and
// when the peer sends std.RequestValue from the right-directed side.
//
// If a left-directed port does not yet have a ready Network instance, it defers
// sending until a value becomes available, then broadcasts to all connected
// peers. A node owning a right-directed pasta/network port may send
// RequestValue when it needs the current Network object again. Both sides must
// register any received Network object with the Workspace resource tracking
// system, bound to the owning node and link.
//
// NetworkPayload can also carry a maximum-UDP-payload capability. Native
// networks derive it from the routed interface and socket PMTU. VTun derives
// it from its effective link MTU. Transparent middleware that uses
// NetworkOutputCache preserves it. NetworkRouter exposes the conservative
// minimum of its active backends. Encapsulating transports, such as SOCKS,
// report unknown unless they implement a specific size contract.
//
// The event payload implements std.ClosablePayload, so middleware nodes such as
// std.SelectClass can close a previously-routed wrapper when switching paths.
// A NetworkPayload with a nil Network explicitly makes the current value
// unavailable while the graph link stays attached.
// Peers that only implement gonnect.Network without io.Closer should wrap their
// instance via gonnect.DetachNetwork before using it with this link type.
// Left-directed pasta/network ports allow an arbitrary count of incoming links;
// right-directed pasta/network ports allow at most one outgoing link.
const TypeNetwork = "almagest/network"

var _ std.ClosablePayload = &NetworkPayload{}

// NetworkCloser is a gonnect network with a closeable lifecycle.
type NetworkCloser interface {
	gonnect.Network
	io.Closer
}

// NetworkPayload carries one network wrapper over an almagest/network event.
type NetworkPayload struct {
	Network NetworkCloser

	// DatagramMTU is an optional destination-aware maximum UDP payload
	// provider. CapabilityRevision lets a consumer process a provider update
	// even when Network keeps the same identity.
	DatagramMTU        DatagramMTUProvider
	CapabilityRevision uint64
}

// Close closes the carried network when present.
func (p NetworkPayload) Close() error {
	if p.Network == nil {
		return nil
	}
	DetachDatagramMTUProvider(p.Network)
	return p.Network.Close()
}

// NetworkPort returns a Pasta port declaration for TypeNetwork.
func NetworkPort(direction, name string) pasta.Port {
	return pasta.Port{Direction: direction, Name: name, Types: []string{TypeNetwork}}
}

// BindNetworkResource binds n to workspace node and link lifecycles when
// possible.
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
	provider := DatagramMTUProviderFromNetwork(source)
	wrapper := gonnect.DetachNetwork(source, nil, nil)
	AttachDatagramMTUProvider(wrapper, provider)
	BindNetworkResource(w, node, link, wrapper)
	w.EmitEventLocked(node, link, networkPayload(wrapper, provider))
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
	provider DatagramMTUProvider
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

// RefreshResolvedLocked emits a fresh resolver-backed NetworkPayload on link
// even when the source and resolver are unchanged.
func (c *NetworkOutputCache) RefreshResolvedLocked(w *pasta.Workspace, node, link uint64, source gonnect.Network, resolver gonnect.Resolver) bool {
	return c.RefreshResolvedWrappedLocked(w, node, link, source, resolver, nil)
}

// SendWrapped emits source as a detached NetworkPayload on link after wrapping
// the detached network with wrap.
func (c *NetworkOutputCache) SendWrapped(w *pasta.Workspace, node, link uint64, source gonnect.Network, wrap func(NetworkCloser) NetworkCloser) bool {
	return c.SendResolvedWrapped(w, node, link, source, nil, wrap)
}

// SendResolvedWrapped emits source as a resolver-backed NetworkPayload on link
// after wrapping the detached network with wrap.
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
			w.EmitEventLocked(node, link, networkPayload(entry.output, entry.provider))
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
	provider := DatagramMTUProviderFromNetwork(source)
	var output NetworkCloser = gonnect.DetachNetwork(source, resolver, nil)
	if wrap != nil {
		wrapped := wrap(output)
		if wrapped == nil {
			_ = output.Close()
			return false
		}
		output = wrapped
	}
	AttachDatagramMTUProvider(output, provider)
	BindNetworkResource(w, node, link, output)
	if c.entries == nil {
		c.entries = make(map[uint64]networkOutputEntry)
	}
	c.entries[link] = networkOutputEntry{source: source, resolver: resolver, output: output, provider: provider}
	w.EmitEventLocked(node, link, networkPayload(output, provider))
	return true
}

func networkPayload(network NetworkCloser, provider DatagramMTUProvider) NetworkPayload {
	payload := NetworkPayload{Network: network, DatagramMTU: provider}
	if provider != nil {
		payload.CapabilityRevision = provider.Revision()
	}
	return payload
}

// Close closes and forgets the wrapper emitted on link.
func (c *NetworkOutputCache) Close(link uint64) {
	if c == nil || c.entries == nil {
		return
	}
	entry := c.entries[link]
	delete(c.entries, link)
	DetachDatagramMTUProvider(entry.output)
	_ = CloseNetwork(entry.output)
}

// CloseAll closes and forgets all emitted wrappers.
func (c *NetworkOutputCache) CloseAll() {
	if c == nil {
		return
	}
	for link, entry := range c.entries {
		delete(c.entries, link)
		DetachDatagramMTUProvider(entry.output)
		_ = CloseNetwork(entry.output)
	}
}

// InvalidateAllLocked closes all emitted wrappers and tells their consumers
// that no Network value is currently available. The graph links stay attached
// and can receive replacement values later.
func (c *NetworkOutputCache) InvalidateAllLocked(w *pasta.Workspace, node uint64) {
	if c == nil {
		return
	}
	for link, entry := range c.entries {
		delete(c.entries, link)
		DetachDatagramMTUProvider(entry.output)
		_ = CloseNetwork(entry.output)
		if w != nil {
			if _, ok := w.LinkSnapshotLocked(link); ok {
				w.EmitEventLocked(node, link, NetworkPayload{})
			}
		}
	}
}

// AcceptNetworkPayloadValue validates a network event and returns the accepted
// network without binding it to a workspace resource.
func AcceptNetworkPayloadValue(event pasta.Event, current NetworkCloser) (NetworkCloser, bool) {
	if std.IsRequest(event.Payload) {
		return current, false
	}
	payload, ok := event.Payload.(NetworkPayload)
	if !ok {
		return current, false
	}
	if payload.Network == nil {
		if current == nil {
			return nil, false
		}
		_ = CloseNetwork(current)
		return nil, true
	}
	if !NetworkIsReady(payload.Network) {
		return current, false
	}
	provider := payload.DatagramMTU
	if provider == nil {
		provider = DatagramMTUProviderFromNetwork(payload.Network)
	}
	AttachDatagramMTUProvider(payload.Network, provider)
	if SameNetworkInstance(current, payload.Network) {
		return current, false
	}
	_ = CloseNetwork(current)
	return payload.Network, true
}

// AcceptNetworkPayloadUpdate validates a network event and also compares its
// datagram capability. It processes a material capability revision when the
// Network instance is unchanged. It closes the old Network only when the
// Network instance changes.
func AcceptNetworkPayloadUpdate(event pasta.Event, current NetworkPayload) (NetworkPayload, bool) {
	if std.IsRequest(event.Payload) {
		return current, false
	}
	next, ok := event.Payload.(NetworkPayload)
	if !ok {
		return current, false
	}
	if next.Network == nil {
		if current.Network == nil {
			return NetworkPayload{}, false
		}
		_ = CloseNetwork(current.Network)
		return NetworkPayload{}, true
	}
	if !NetworkIsReady(next.Network) {
		return current, false
	}
	if next.DatagramMTU == nil {
		next.DatagramMTU = DatagramMTUProviderFromNetwork(next.Network)
	}
	AttachDatagramMTUProvider(next.Network, next.DatagramMTU)
	if next.DatagramMTU != nil && next.CapabilityRevision == 0 {
		next.CapabilityRevision = next.DatagramMTU.Revision()
	}
	if SameNetworkInstance(current.Network, next.Network) {
		if SameDatagramMTUProvider(current.DatagramMTU, next.DatagramMTU) &&
			current.CapabilityRevision == next.CapabilityRevision {
			return current, false
		}
		return next, true
	}
	_ = CloseNetwork(current.Network)
	return next, true
}

// AcceptNetworkPayload validates and binds a received NetworkPayload.
func AcceptNetworkPayload(w *pasta.Workspace, node uint64, event pasta.Event, current NetworkCloser) (NetworkCloser, bool) {
	next, changed := AcceptNetworkPayloadValue(event, current)
	if !changed {
		return next, false
	}
	BindNetworkResource(w, node, event.Link, next)
	return next, true
}

// SameNetworkSource reports whether a and b ultimately wrap the same
// comparable network source. Non-comparable sources are never considered the
// same unless both values are nil.
func SameNetworkSource(a, b any) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return SameComparableValue(NetworkSource(a), NetworkSource(b))
}

// SameNetworkInstance reports whether a and b are the same comparable network
// object. Non-comparable network values are never considered the same.
func SameNetworkInstance(a, b NetworkCloser) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return SameComparableValue(a, b)
}

// SameNetworkResolver reports whether a and b are the same comparable
// resolver object. Non-comparable resolver values are never considered the same.
func SameNetworkResolver(a, b gonnect.Resolver) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return SameComparableValue(a, b)
}

// SameDatagramMTUProvider reports whether a and b are the same comparable
// provider. Non-comparable providers are never considered the same.
func SameDatagramMTUProvider(a, b DatagramMTUProvider) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return SameComparableValue(a, b)
}

// CloseNetwork closes n when it is not nil.
func CloseNetwork(n NetworkCloser) error {
	if n == nil {
		return nil
	}
	DetachDatagramMTUProvider(n)
	return n.Close()
}

// NetworkSource unwraps gonnect.Wrapper values until it reaches the underlying
// network source, then returns that source.
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

// NetworkIsReady reports whether n is usable now. Detached network wrappers
// expose IsUp; plain network implementations without lifecycle state are
// treated as ready.
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
