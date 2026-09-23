package dnsnodes

import (
	"sync"

	"github.com/asciimoth/gonnect"
	"github.com/asciimoth/gonnect/dns"
	"github.com/asciimoth/pasta/pasta"
	"github.com/asciimoth/pasta/pasta/std"

	"github.com/asciimoth/almagest/lib/links"
)

// TypeDNS is the Pasta link type carrying gonnect/dns.Interface values.
//
// DNS values flow from left-directed ports to right-directed ports, matching
// almagest/network graph flow. A node owning a left-directed almagest/dns port
// sends its current dns.Interface on every connected link when a new link
// connects, when OnReady runs, when the owned DNS interface is replaced, and
// when the peer sends std.RequestValue from the right-directed side.
//
// Providers must wrap each emitted interface with dns.Detach per connected
// link so link removal, selector switching, and downstream shutdown can close
// that wrapper without closing the provider's underlying DNS interface. Both
// senders and receivers must register DNS values as workspace resources, bound
// to the owning node and link.
//
// The event payload implements std.ClosablePayload so middleware nodes can
// close previously-routed wrappers when switching paths. A DNSPayload with a
// nil DNS value explicitly makes the current value unavailable while the graph
// link stays attached. Left-directed almagest/dns ports allow an arbitrary
// count of incoming links; right-directed almagest/dns ports allow at most one
// outgoing link.
const TypeDNS = links.TypeDNS

var _ std.ClosablePayload = &DNSPayload{}

// DNSPayload carries one DNS interface over an almagest/dns event.
type DNSPayload struct {
	DNS dns.Interface
}

// Close closes the carried DNS interface when present.
func (p DNSPayload) Close() error {
	if p.DNS == nil {
		return nil
	}
	return p.DNS.Close()
}

// DNSPort returns a Pasta port declaration for TypeDNS.
func DNSPort(direction, name string) pasta.Port {
	return pasta.Port{Direction: direction, Name: name, Types: []string{TypeDNS}}
}

// BindDNSResource binds d to workspace node and link lifecycles when possible.
func BindDNSResource(w *pasta.Workspace, node, link uint64, d dns.Interface) {
	if w == nil || d == nil {
		return
	}
	_ = w.AddNodeResourceLocked(node, d)
	if link != 0 {
		_ = w.AddLinkResourceLocked(link, d)
	}
}

// EmitDetachedDNS sends a detached DNSPayload for source on link and binds the
// wrapper to node and link resources.
func EmitDetachedDNS(w *pasta.Workspace, node, link uint64, source dns.Interface) dns.Interface {
	if w == nil || link == 0 || source == nil {
		return nil
	}
	if _, ok := w.LinkSnapshotLocked(link); !ok {
		return nil
	}
	wrapper := NewDetachedDNS(source)
	BindDNSResource(w, node, link, wrapper)
	w.EmitEventLocked(node, link, DNSPayload{DNS: wrapper})
	return wrapper
}

// BroadcastDNSPort calls send for every link attached to a DNS output port.
func BroadcastDNSPort(w *pasta.Workspace, _ uint64, port uint64, send func(link uint64)) {
	if w == nil || port == 0 || send == nil {
		return
	}
	snapshot, ok := w.PortSnapshotLocked(port)
	if !ok {
		return
	}
	for _, link := range snapshot.Links {
		send(link)
	}
}

type dnsOutputEntry struct {
	source dns.Interface
	output dns.Interface
}

// DNSOutputCache owns detached DNS wrappers emitted by a node, keyed by link.
type DNSOutputCache struct {
	entries map[uint64]dnsOutputEntry
}

// Send emits source as a detached DNSPayload on link. It re-emits a cached
// ready wrapper for the same source and replaces stale wrappers.
func (c *DNSOutputCache) Send(w *pasta.Workspace, node, link uint64, source dns.Interface) bool {
	return c.SendWrapped(w, node, link, source, nil)
}

// SendWrapped emits source as a detached DNSPayload on link after wrapping the
// detached DNS interface with wrap. Middleware uses wrap to decorate the
// per-link detached interface before publishing it.
func (c *DNSOutputCache) SendWrapped(w *pasta.Workspace, node, link uint64, source dns.Interface, wrap func(dns.Interface) dns.Interface) bool {
	if w == nil {
		return false
	}
	w.Lock()
	defer w.Unlock()
	return c.SendWrappedLocked(w, node, link, source, wrap)
}

// SendLocked is Send for callers already running under the workspace lock.
func (c *DNSOutputCache) SendLocked(w *pasta.Workspace, node, link uint64, source dns.Interface) bool {
	return c.SendWrappedLocked(w, node, link, source, nil)
}

// SendWrappedLocked is SendWrapped for callers already running under the
// workspace lock.
func (c *DNSOutputCache) SendWrappedLocked(w *pasta.Workspace, node, link uint64, source dns.Interface, wrap func(dns.Interface) dns.Interface) bool {
	if w == nil || link == 0 || source == nil {
		return false
	}
	if _, ok := w.LinkSnapshotLocked(link); !ok {
		return false
	}
	if c.entries != nil {
		if entry, ok := c.entries[link]; ok && DNSIsReady(entry.output) && SameDNSSource(entry.source, source) {
			w.EmitEventLocked(node, link, DNSPayload{DNS: entry.output})
			return true
		}
	}
	c.Close(link)
	return c.emitWrappedLocked(w, node, link, source, wrap)
}

// SendPortLocked emits source to every link attached to port.
func (c *DNSOutputCache) SendPortLocked(w *pasta.Workspace, node, port uint64, source dns.Interface) {
	c.SendWrappedPortLocked(w, node, port, source, nil)
}

// SendWrappedPortLocked emits source to every link attached to port after
// wrapping each detached DNS interface with wrap.
func (c *DNSOutputCache) SendWrappedPortLocked(w *pasta.Workspace, node, port uint64, source dns.Interface, wrap func(dns.Interface) dns.Interface) {
	BroadcastDNSPort(w, node, port, func(link uint64) {
		c.SendWrappedLocked(w, node, link, source, wrap)
	})
}

// RefreshLocked emits a fresh detached DNSPayload on link even when the source
// is unchanged. Use it to answer std.Request callbacks from middleware that may
// have just closed the previously routed payload.
func (c *DNSOutputCache) RefreshLocked(w *pasta.Workspace, node, link uint64, source dns.Interface) bool {
	return c.RefreshWrappedLocked(w, node, link, source, nil)
}

// RefreshWrappedLocked is RefreshLocked with an additional wrapper hook.
func (c *DNSOutputCache) RefreshWrappedLocked(w *pasta.Workspace, node, link uint64, source dns.Interface, wrap func(dns.Interface) dns.Interface) bool {
	if w == nil || link == 0 || source == nil {
		return false
	}
	if _, ok := w.LinkSnapshotLocked(link); !ok {
		return false
	}
	c.Close(link)
	return c.emitWrappedLocked(w, node, link, source, wrap)
}

func (c *DNSOutputCache) emitWrappedLocked(w *pasta.Workspace, node, link uint64, source dns.Interface, wrap func(dns.Interface) dns.Interface) bool {
	var output dns.Interface = NewDetachedDNS(source)
	if wrap != nil {
		wrapped := wrap(output)
		if wrapped == nil {
			CloseDNS(output)
			return false
		}
		output = wrapped
	}
	BindDNSResource(w, node, link, output)
	if c.entries == nil {
		c.entries = make(map[uint64]dnsOutputEntry)
	}
	c.entries[link] = dnsOutputEntry{source: source, output: output}
	w.EmitEventLocked(node, link, DNSPayload{DNS: output})
	return true
}

// Close closes and forgets the wrapper emitted on link.
func (c *DNSOutputCache) Close(link uint64) {
	if c == nil || c.entries == nil {
		return
	}
	entry := c.entries[link]
	delete(c.entries, link)
	CloseDNS(entry.output)
}

// CloseAll closes and forgets all emitted wrappers.
func (c *DNSOutputCache) CloseAll() {
	if c == nil {
		return
	}
	for link, entry := range c.entries {
		delete(c.entries, link)
		CloseDNS(entry.output)
	}
}

// InvalidateAllLocked closes all emitted wrappers and tells their consumers
// that no DNS value is currently available. The graph links stay attached and
// can receive replacement values later.
func (c *DNSOutputCache) InvalidateAllLocked(w *pasta.Workspace, node uint64) {
	if c == nil {
		return
	}
	for link, entry := range c.entries {
		delete(c.entries, link)
		CloseDNS(entry.output)
		if w != nil {
			if _, ok := w.LinkSnapshotLocked(link); ok {
				w.EmitEventLocked(node, link, DNSPayload{})
			}
		}
	}
}

// AcceptDNSPayloadValue validates a DNS event and returns the accepted DNS
// interface without binding it to a workspace resource.
func AcceptDNSPayloadValue(event pasta.Event, current dns.Interface) (dns.Interface, bool) {
	if std.IsRequest(event.Payload) {
		return current, false
	}
	payload, ok := event.Payload.(DNSPayload)
	if !ok {
		return current, false
	}
	if payload.DNS == nil {
		if current == nil {
			return nil, false
		}
		CloseDNS(current)
		return nil, true
	}
	if !DNSIsReady(payload.DNS) {
		return current, false
	}
	if SameDNSInstance(current, payload.DNS) {
		return current, false
	}
	CloseDNS(current)
	return payload.DNS, true
}

// AcceptDNSPayload validates and binds a received DNSPayload.
func AcceptDNSPayload(w *pasta.Workspace, node uint64, event pasta.Event, current dns.Interface) (dns.Interface, bool) {
	next, changed := AcceptDNSPayloadValue(event, current)
	if !changed {
		return next, false
	}
	BindDNSResource(w, node, event.Link, next)
	return next, true
}

// SameDNSSource reports whether a and b ultimately wrap the same comparable
// DNS source. Non-comparable sources are never considered the same unless both
// values are nil.
func SameDNSSource(a, b dns.Interface) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return links.SameComparableValue(DNSSource(a), DNSSource(b))
}

// SameDNSInstance reports whether a and b are the same comparable DNS object.
// Non-comparable DNS values are never considered the same.
func SameDNSInstance(a, b dns.Interface) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return links.SameComparableValue(a, b)
}

// DNSIsReady reports whether d is usable now. Detached DNS wrappers expose
// IsUp; plain DNS implementations without lifecycle state are treated as ready.
func DNSIsReady(d dns.Interface) bool {
	if d == nil {
		return false
	}
	updown, ok := d.(interface {
		IsUp() (bool, error)
	})
	if !ok {
		return true
	}
	up, err := updown.IsUp()
	return err == nil && up
}

// CloseDNS closes d when it is not nil.
func CloseDNS(d dns.Interface) {
	if d != nil {
		_ = d.Close()
	}
}

// DNSSource unwraps gonnect.Wrapper values until it reaches the underlying DNS
// source, then returns that source.
func DNSSource(d any) any {
	for d != nil {
		wrapped := gonnect.GetWrapped(d)
		if wrapped == nil {
			return d
		}
		d = wrapped
	}
	return nil
}

// DetachedDNS is a per-link DNS wrapper created from an underlying source.
type DetachedDNS struct {
	dns.Interface

	source dns.Interface

	mu     sync.Mutex
	closed bool
}

// NewDetachedDNS returns a closeable per-link wrapper for source.
func NewDetachedDNS(source dns.Interface) *DetachedDNS {
	return &DetachedDNS{
		Interface: dns.Detach(source, nil),
		source:    source,
	}
}

// GetWrapped returns the underlying source for comparison helpers.
func (d *DetachedDNS) GetWrapped() any { return d.source }

// IsUp reports whether this wrapper has not been closed.
func (d *DetachedDNS) IsUp() (bool, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return !d.closed, nil
}

// Close closes this detached wrapper without closing the source.
func (d *DetachedDNS) Close() error {
	d.mu.Lock()
	alreadyClosed := d.closed
	d.closed = true
	d.mu.Unlock()
	if alreadyClosed {
		return nil
	}
	return d.Interface.Close()
}
