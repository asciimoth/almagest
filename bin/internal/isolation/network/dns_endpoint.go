package network

import (
	"context"
	"io"
	"net"
	"os"
	"sync"
	"time"

	"github.com/asciimoth/almagest/bin/internal/isolation/protocol"
	gdns "github.com/asciimoth/gonnect/dns"
	"github.com/asciimoth/gonnect/tun"
)

const maxApplicationDNSPending = 64

type applicationDNSPending struct {
	request   gdns.Request
	query     *gdns.Message
	wire      []byte
	delivered bool
}

// ApplicationDNSEndpoint is N's bounded relay to an application resolver in R.
// It owns no host DNS provider. The native backend sees only its DNS interface;
// R sees only correlated DNS packets on the existing authenticated data lane.
type ApplicationDNSEndpoint struct {
	requests chan gdns.Request
	provider gdns.Interface
	ready    chan uint64
	done     chan struct{}
	changed  chan struct{}

	mu                          sync.Mutex
	pending                     map[uint64]*applicationDNSPending
	next                        uint64
	closed                      bool
	readDeadline, writeDeadline time.Time
	closeOnce                   sync.Once
	stopped                     chan struct{}
	wg                          sync.WaitGroup
}

func NewApplicationDNSEndpoint() *ApplicationDNSEndpoint {
	d := &ApplicationDNSEndpoint{
		requests: make(chan gdns.Request),
		ready:    make(chan uint64, maxApplicationDNSPending),
		done:     make(chan struct{}),
		changed:  make(chan struct{}),
		stopped:  make(chan struct{}),
		pending:  make(map[uint64]*applicationDNSPending),
	}
	d.provider = applicationDNSProvider{endpoint: d}
	d.wg.Go(d.accept)
	return d
}

type applicationDNSProvider struct{ endpoint *ApplicationDNSEndpoint }

func (p applicationDNSProvider) Requests() chan<- gdns.Request { return p.endpoint.requests }
func (p applicationDNSProvider) Close() error {
	return p.endpoint.Close(context.Background())
}

func (d *ApplicationDNSEndpoint) Resolver() gdns.Interface { return d.provider }
func (*ApplicationDNSEndpoint) Events() <-chan tun.Event   { return nil }
func (*ApplicationDNSEndpoint) Capabilities(context.Context) (protocol.CapabilitySnapshot, error) {
	return protocol.CapabilitySnapshot{SchemaVersion: 1, Revision: 1}, nil
}
func (*ApplicationDNSEndpoint) Update(context.Context, protocol.UpdateTunRequest) error {
	return protocol.ErrCapabilityUnsupported
}

func (d *ApplicationDNSEndpoint) wakeLocked() {
	close(d.changed)
	d.changed = make(chan struct{})
}

func (d *ApplicationDNSEndpoint) Verify(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return net.ErrClosed
	}
	return nil
}

func (d *ApplicationDNSEndpoint) State(context.Context) (protocol.ResourceState, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return protocol.ResourceState{Revision: 1, Up: !d.closed, Closed: d.closed}, nil
}

func (d *ApplicationDNSEndpoint) accept() {
	for {
		select {
		case <-d.done:
			return
		default:
		}
		select {
		case <-d.done:
			return
		case request := <-d.requests:
			d.acceptRequest(request)
		}
	}
}

func (d *ApplicationDNSEndpoint) acceptRequest(request gdns.Request) {
	if request.Context == nil {
		request.Context = context.Background()
	}
	if err := request.Context.Err(); err != nil {
		d.reply(request, gdns.Response{Err: err})
		return
	}
	if request.Message == nil || request.Message.Response {
		d.reply(request, gdns.Response{Err: protocol.ErrInvalidContract})
		return
	}
	wire, err := gdns.Pack(request.Message)
	if err != nil || len(wire) < 12 || len(wire) > protocol.MaxPacketRecord {
		d.reply(request, gdns.Response{Err: protocol.ErrInvalidContract})
		return
	}
	query, err := gdns.Unpack(wire)
	if err != nil {
		d.reply(request, gdns.Response{Err: protocol.ErrInvalidContract})
		return
	}
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		d.reply(request, gdns.Response{Err: net.ErrClosed})
		return
	}
	if len(d.pending) >= maxApplicationDNSPending {
		d.mu.Unlock()
		d.reply(request, gdns.Response{Err: protocol.ErrLimitExceeded})
		return
	}
	for {
		d.next++
		if d.next != 0 && d.pending[d.next] == nil {
			break
		}
	}
	token := d.next
	d.pending[token] = &applicationDNSPending{request: request, query: query, wire: wire}
	d.mu.Unlock()
	select {
	case d.ready <- token:
	case <-d.done:
		d.failPending(token, net.ErrClosed)
	}
}

func (d *ApplicationDNSEndpoint) reply(request gdns.Request, response gdns.Response) {
	select {
	case request.Reply <- response:
	case <-request.Context.Done():
	case <-d.done:
	}
}

func (d *ApplicationDNSEndpoint) failPending(token uint64, err error) {
	d.mu.Lock()
	pending := d.pending[token]
	delete(d.pending, token)
	d.mu.Unlock()
	if pending != nil {
		d.reply(pending.request, gdns.Response{Err: err})
	}
}

func (d *ApplicationDNSEndpoint) Close(ctx context.Context) error {
	d.closeOnce.Do(func() {
		d.mu.Lock()
		d.closed = true
		close(d.done)
		d.wakeLocked()
		pending := d.pending
		d.pending = make(map[uint64]*applicationDNSPending)
		d.mu.Unlock()
		for _, request := range pending {
			select {
			case request.request.Reply <- gdns.Response{Err: net.ErrClosed}:
			default:
			}
		}
		go func() { d.wg.Wait(); close(d.stopped) }()
	})
	select {
	case <-d.stopped:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (d *ApplicationDNSEndpoint) SetReadDeadline(deadline time.Time) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return net.ErrClosed
	}
	d.readDeadline = deadline
	d.wakeLocked()
	return nil
}

func (d *ApplicationDNSEndpoint) SetWriteDeadline(deadline time.Time) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return net.ErrClosed
	}
	d.writeDeadline = deadline
	d.wakeLocked()
	return nil
}

func (d *ApplicationDNSEndpoint) ReadData(p []byte) (int, *protocol.PacketMetadata, error) {
	for {
		d.mu.Lock()
		if d.closed {
			d.mu.Unlock()
			return 0, nil, net.ErrClosed
		}
		deadline, changed := d.readDeadline, d.changed
		if !deadline.IsZero() && !time.Now().Before(deadline) {
			d.mu.Unlock()
			return 0, nil, os.ErrDeadlineExceeded
		}
		d.mu.Unlock()

		var token uint64
		var err error
		if deadline.IsZero() {
			select {
			case token = <-d.ready:
			case <-changed:
				continue
			case <-d.done:
				return 0, nil, net.ErrClosed
			}
		} else {
			timer := time.NewTimer(max(time.Until(deadline), 0))
			select {
			case token = <-d.ready:
			case <-changed:
			case <-d.done:
				err = net.ErrClosed
			case <-timer.C:
				err = os.ErrDeadlineExceeded
			}
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			if token == 0 {
				if err != nil {
					return 0, nil, err
				}
				continue
			}
		}

		d.mu.Lock()
		pending := d.pending[token]
		if pending == nil {
			d.mu.Unlock()
			continue
		}
		if err := pending.request.Context.Err(); err != nil {
			delete(d.pending, token)
			d.mu.Unlock()
			d.reply(pending.request, gdns.Response{Err: err})
			continue
		}
		pending.delivered = true
		wire := pending.wire
		d.mu.Unlock()
		n := copy(p, wire)
		meta := &protocol.PacketMetadata{OriginalLength: uint32(len(wire)), Truncated: n < len(wire), DNS: &protocol.DNSMetadata{Correlation: token}}
		if n < len(wire) {
			d.failPending(token, io.ErrShortBuffer)
			return n, meta, io.ErrShortBuffer
		}
		return n, meta, nil
	}
}

func decodeApplicationDNSResponse(p []byte, meta *protocol.PacketMetadata) (*gdns.Message, uint64, error) {
	if len(p) < 12 || len(p) > protocol.MaxPacketRecord || meta == nil || meta.DNS == nil || meta.DNS.Correlation == 0 {
		return nil, 0, protocol.ErrInvalidContract
	}
	dns := meta.DNS
	if *meta != (protocol.PacketMetadata{OriginalLength: uint32(len(p)), DNS: dns}) {
		return nil, 0, protocol.ErrInvalidContract
	}
	response, err := gdns.Unpack(p)
	if err != nil || !response.Response {
		return nil, dns.Correlation, protocol.ErrInvalidContract
	}
	return response, dns.Correlation, nil
}

func (d *ApplicationDNSEndpoint) WriteData(p []byte, meta *protocol.PacketMetadata) (int, error) {
	response, correlation, err := decodeApplicationDNSResponse(p, meta)
	if err != nil {
		if correlation != 0 {
			d.failPending(correlation, protocol.ErrInvalidContract)
		}
		return 0, err
	}
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return 0, net.ErrClosed
	}
	if !d.writeDeadline.IsZero() && !time.Now().Before(d.writeDeadline) {
		d.mu.Unlock()
		d.failPending(correlation, os.ErrDeadlineExceeded)
		return 0, os.ErrDeadlineExceeded
	}
	pending := d.pending[correlation]
	if pending == nil || !pending.delivered || response.ID != pending.query.ID {
		d.mu.Unlock()
		if pending != nil {
			d.failPending(correlation, protocol.ErrInvalidContract)
		}
		return 0, protocol.ErrInvalidContract
	}
	delete(d.pending, correlation)
	d.mu.Unlock()
	d.reply(pending.request, gdns.Response{Message: response})
	return len(p), nil
}

var _ OwnedDNSEndpoint = (*ApplicationDNSEndpoint)(nil)
var _ DataIO = (*ApplicationDNSEndpoint)(nil)
