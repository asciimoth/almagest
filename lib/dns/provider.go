package dnsnodes

import (
	"context"
	"sync"

	"github.com/asciimoth/gonnect/dns"
)

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
