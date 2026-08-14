package almagest

import (
	"context"
	"sync"

	"github.com/asciimoth/gonnect/dns"
	"github.com/asciimoth/pasta/pasta"
	"github.com/asciimoth/pasta/pasta/std"

	"github.com/asciimoth/almagest/lib/links"
)

const TypeDNS = links.TypeDNS

type DNSPayload = links.DNSPayload
type DNSOutputCache = links.DNSOutputCache

func DNSPort(direction, name string) pasta.Port {
	return links.DNSPort(direction, name)
}

func BindDNSResource(w *pasta.Workspace, node, link uint64, d dns.Interface) {
	links.BindDNSResource(w, node, link, d)
}

func EmitDetachedDNS(w *pasta.Workspace, node, link uint64, source dns.Interface) dns.Interface {
	return links.EmitDetachedDNS(w, node, link, source)
}

func BroadcastDNSPort(w *pasta.Workspace, node, port uint64, send func(link uint64)) {
	links.BroadcastDNSPort(w, node, port, send)
}

func acceptDNSPayload(event pasta.Event, current dns.Interface) (dns.Interface, bool) {
	return links.AcceptDNSPayloadValue(event, current)
}

func AcceptDNSPayload(w *pasta.Workspace, node uint64, event pasta.Event, current dns.Interface) (dns.Interface, bool) {
	return links.AcceptDNSPayload(w, node, event, current)
}

func SameDNSSource(a, b dns.Interface) bool {
	return links.SameDNSSource(a, b)
}

func SameDNSInstance(a, b dns.Interface) bool {
	return links.SameDNSInstance(a, b)
}

func DNSIsReady(d dns.Interface) bool {
	return links.DNSIsReady(d)
}

func closeDNS(d dns.Interface) {
	links.CloseDNS(d)
}

func DNSSource(d any) any {
	return links.DNSSource(d)
}

func newDetachedDNS(source dns.Interface) dns.Interface {
	return links.NewDetachedDNS(source)
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

type dnsRequestHandler func(context.Context, dns.Request)

// dnsProvider is a small reusable dns.Interface implementation for node-owned
// providers. It leaves the request channel open after Close so a late dns.Query
// behaves like Gonnect DNS providers: the caller context controls when the send
// gives up.
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
