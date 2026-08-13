package almagest

import (
	"context"
	"sync"

	"github.com/asciimoth/gonnect"
	"github.com/asciimoth/gonnect/dns"
	"github.com/asciimoth/pasta/pasta"
	"github.com/asciimoth/pasta/pasta/std"
)

// TypeDNS is the Pasta link type carrying gonnect/dns.Interface values.
//
// DNS values flow from left-directed ports to right-directed ports, matching
// almagest/network graph flow. A node owning a left-directed almagest/dns port
// sends its current dns.Interface on every connected link when a new link
// connects, when OnReady runs, when the owned DNS interface is replaced, and
// when the peer sends std.RequestValue from the right-directed side. Providers
// must wrap each emitted interface with dns.Detach per connected link so link
// removal, selector switching, and downstream shutdown can close that wrapper
// without closing the provider's underlying DNS interface. Both senders and
// receivers must register DNS values as workspace resources, bound to the
// owning node and link. The event payload implements std.ClosablePayload so
// middleware nodes can close previously-routed wrappers when switching paths.
// Left-directed almagest/dns ports allow an arbitrary count of incoming links;
// right-directed almagest/dns ports allow at most one outgoing link.
const TypeDNS = "almagest/dns"

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
	wrapper := newDetachedDNS(source)
	BindDNSResource(w, node, link, wrapper)
	w.EmitEventLocked(node, link, DNSPayload{DNS: wrapper})
	return wrapper
}

// BroadcastDNSPort calls send for every link attached to a DNS output port.
func BroadcastDNSPort(w *pasta.Workspace, _ uint64, port uint64, send func(link uint64)) {
	forEachPortLink(w, port, send)
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
// detached DNS interface with wrap. The wrap hook is used by middleware that
// decorates the per-link detached interface before publishing it.
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
	var output dns.Interface = newDetachedDNS(source)
	if wrap != nil {
		wrapped := wrap(output)
		if wrapped == nil {
			closeDNS(output)
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
	closeDNS(entry.output)
}

// CloseAll closes and forgets all emitted wrappers.
func (c *DNSOutputCache) CloseAll() {
	if c == nil {
		return
	}
	for link, entry := range c.entries {
		delete(c.entries, link)
		closeDNS(entry.output)
	}
}

func acceptDNSPayload(event pasta.Event, current dns.Interface) (dns.Interface, bool) {
	if std.IsRequest(event.Payload) {
		return current, false
	}
	payload, ok := event.Payload.(DNSPayload)
	if !ok || !DNSIsReady(payload.DNS) {
		return current, false
	}
	if SameDNSInstance(current, payload.DNS) {
		return current, false
	}
	closeDNS(current)
	return payload.DNS, true
}

// AcceptDNSPayload validates and binds a received DNSPayload.
func AcceptDNSPayload(w *pasta.Workspace, node uint64, event pasta.Event, current dns.Interface) (dns.Interface, bool) {
	next, changed := acceptDNSPayload(event, current)
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
	return SameComparableValue(DNSSource(a), DNSSource(b))
}

// SameDNSInstance reports whether a and b are the same comparable DNS object.
// Non-comparable DNS values are never considered the same.
func SameDNSInstance(a, b dns.Interface) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return SameComparableValue(a, b)
}

// DNSIsReady reports whether d is usable now. Detached DNS wrappers created by
// this file expose IsUp; plain DNS implementations without lifecycle state are
// treated as ready.
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

func closeDNS(d dns.Interface) {
	if d != nil {
		_ = d.Close()
	}
}

type simpleDNSProvider struct {
	NodeBase

	out      uint64
	resolver dns.Interface
	outputs  DNSOutputCache
}

func (n *simpleDNSProvider) InitSimpleDNSProvider(w *pasta.Workspace, logger pasta.Logger, id uint64, restored *pasta.NodeInitData, resolver dns.Interface, label string) error {
	n.InitBaseNode(w, logger, id)
	n.out = restoredLeft(restored, 0)
	n.resolver = resolver
	if err := n.SetPrimary(TypeDNS); err != nil {
		return err
	}
	_ = n.SetLabel(label)
	return nil
}

func (n *simpleDNSProvider) OnReady() error {
	n.sendAll()
	return nil
}

func (n *simpleDNSProvider) OnStop() {
	n.outputs.CloseAll()
	n.resolver = nil
}

func (n *simpleDNSProvider) PreLinkAdd(port uint64, linkType, portDirection string) error {
	if port != n.out || portDirection != "left" || linkType != TypeDNS {
		return pasta.LinkTypeErr(linkType)
	}
	return nil
}

func (n *simpleDNSProvider) OnLinkAdd(link, port uint64, linkType, portDirection string) error {
	if port == n.out && portDirection == "left" && linkType == TypeDNS {
		n.sendToLink(link)
	}
	return nil
}

func (n *simpleDNSProvider) OnLinkRemoved(link uint64, port uint64, linkType, portDirection string) error {
	if port == n.out && portDirection == "left" && linkType == TypeDNS {
		n.outputs.Close(link)
	}
	return nil
}

func (n *simpleDNSProvider) OnEvent(event pasta.Event, linkType string, _ []string, receiverPortDirection string) error {
	if event.ReceiverPort != n.out || receiverPortDirection != "left" || linkType != TypeDNS || !std.IsRequest(event.Payload) {
		return nil
	}
	n.refreshLink(event.Link)
	return nil
}

func (n *simpleDNSProvider) sendAll() {
	n.outputs.SendPortLocked(n.w, n.id, n.out, n.resolver)
}

func (n *simpleDNSProvider) sendToLink(link uint64) {
	n.outputs.SendLocked(n.w, n.id, link, n.resolver)
}

func (n *simpleDNSProvider) refreshLink(link uint64) {
	n.outputs.RefreshLocked(n.w, n.id, link, n.resolver)
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

type detachedDNS struct {
	dns.Interface

	source dns.Interface

	mu     sync.Mutex
	closed bool
}

func newDetachedDNS(source dns.Interface) *detachedDNS {
	return &detachedDNS{
		Interface: dns.Detach(source, nil),
		source:    source,
	}
}

func (d *detachedDNS) GetWrapped() any { return d.source }

func (d *detachedDNS) IsUp() (bool, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return !d.closed, nil
}

func (d *detachedDNS) Close() error {
	d.mu.Lock()
	alreadyClosed := d.closed
	d.closed = true
	d.mu.Unlock()
	if alreadyClosed {
		return nil
	}
	return d.Interface.Close()
}

type dnsRequestHandler func(context.Context, dns.Request)

// dnsProvider is a small reusable dns.Interface implementation for node-owned
// providers. It intentionally leaves the request channel open after Close so a
// late dns.Query behaves like Gonnect's DNS providers: the caller's context
// controls when the send gives up.
type dnsProvider struct {
	ch     chan dns.Request
	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}
	once   sync.Once
	wg     sync.WaitGroup

	handle dnsRequestHandler

	mu     sync.Mutex
	closed bool
}

func newDNSProvider(handle dnsRequestHandler) *dnsProvider {
	ctx, cancel := context.WithCancel(context.Background())
	p := &dnsProvider{
		ch:     make(chan dns.Request),
		ctx:    ctx,
		cancel: cancel,
		done:   make(chan struct{}),
		handle: handle,
	}
	go p.run()
	return p
}

func (p *dnsProvider) Requests() chan<- dns.Request { return p.ch }

func (p *dnsProvider) IsUp() (bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return !p.closed, nil
}

func (p *dnsProvider) Close() error {
	p.once.Do(func() {
		p.mu.Lock()
		p.closed = true
		p.mu.Unlock()
		p.cancel()
	})
	<-p.done
	p.wg.Wait()
	return nil
}

func (p *dnsProvider) run() {
	defer close(p.done)
	for {
		select {
		case req := <-p.ch:
			p.wg.Add(1)
			go func() {
				defer p.wg.Done()
				p.handle(p.ctx, req)
			}()
		case <-p.ctx.Done():
			return
		}
	}
}

func dnsRequestContext(root context.Context, req dns.Request) (context.Context, context.CancelFunc) {
	ctx := req.Context
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithCancel(ctx)
	go func() {
		select {
		case <-root.Done():
			cancel()
		case <-ctx.Done():
		}
	}()
	return ctx, cancel
}

func sendDNSResponse(req dns.Request, msg *dns.Message, err error) {
	if req.Reply == nil {
		return
	}
	select {
	case req.Reply <- dns.Response{Message: msg, Err: err}:
	default:
	}
}
