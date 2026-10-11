package runtime

import (
	"context"
	"math"
	"slices"
	"sync"
	"time"

	"github.com/asciimoth/almagest/bin/internal/isolation/protocol"
	gdns "github.com/asciimoth/gonnect/dns"
)

const applicationDNSWorkers = 4

type applicationDNSWork struct {
	message *gdns.Message
	token   uint64
}

type applicationDNSResult struct {
	message *gdns.Message
	token   uint64
}

// DNSService forwards N's managed-DNS questions to an application resolver in
// R. It owns the broker endpoint resource, but not upstream. Four workers bound
// concurrent application callbacks; one reader and writer serialize each data
// direction without placing application code in N.
type DNSService struct {
	connection *Conn
	upstream   gdns.Interface
	cancel     context.CancelFunc
	closeOnce  sync.Once
	wg         sync.WaitGroup
	closeErr   error
}

func (c *Client) createDNSEndpoint(ctx context.Context, upstream gdns.Interface) (*DNSService, protocol.ResourceRef, error) {
	if upstream == nil {
		return nil, protocol.ResourceRef{}, protocol.ErrInvalidContract
	}
	result, err := c.openResource(ctx, protocol.OperationCreateDNSEndpoint, protocol.CreateDNSEndpointRequest{Meta: c.meta(true)})
	if err != nil {
		return nil, protocol.ResourceRef{}, err
	}
	if result.Outcome != protocol.OutcomeCommitted {
		if result.Code != "" {
			return nil, protocol.ResourceRef{}, decodeError(result.Code)
		}
		return nil, protocol.ResourceRef{}, protocol.ErrRecoveryRequired
	}
	if err := result.Resource.Validate(c.session, protocol.ResourceDNS, c.epoch.Load(), 1); err != nil {
		_ = c.Close()
		return nil, protocol.ResourceRef{}, err
	}
	service, err := c.ServeDNS(result.Resource, upstream)
	if err != nil {
		_ = c.closeResource(result.Resource)
		return nil, protocol.ResourceRef{}, err
	}
	return service, result.Resource, nil
}

func (c *Client) ServeDNS(ref protocol.ResourceRef, upstream gdns.Interface) (*DNSService, error) {
	if upstream == nil || ref.Type != protocol.ResourceDNS {
		return nil, protocol.ErrInvalidContract
	}
	if err := ref.Validate(c.session, protocol.ResourceDNS, c.epoch.Load(), 1); err != nil {
		return nil, err
	}
	root, cancel := context.WithCancel(context.Background())
	s := &DNSService{connection: &Conn{client: c, ref: ref, packet: true}, upstream: upstream, cancel: cancel}
	work := make(chan applicationDNSWork, applicationDNSWorkers)
	results := make(chan applicationDNSResult, applicationDNSWorkers)
	s.wg.Go(func() { s.read(root, work) })
	for range applicationDNSWorkers {
		s.wg.Go(func() { s.resolve(root, work, results) })
	}
	s.wg.Go(func() { s.write(root, results) })
	s.wg.Go(func() {
		<-root.Done()
		_ = s.connection.Close()
	})
	return s, nil
}

// ConfigureDNS attaches a bootstrap-created application DNS endpoint to a
// default TUN. A committed change replaces the stale adapter with one for the
// broker-issued generation. Provider selection and native DNS work remain in N.
func (c *Client) ConfigureDNS(ctx context.Context, device *Tun, endpoint protocol.ResourceRef, enabled bool) (*Tun, error) {
	if device == nil {
		return nil, protocol.ErrInvalidContract
	}
	tun := device.ref
	if tun.Type != protocol.ResourceDefaultTun {
		return nil, protocol.ErrResourceType
	}
	if tun.Generation == 0 {
		return nil, protocol.ErrInvalidContract
	}
	if tun.Generation == math.MaxUint64 {
		return nil, protocol.ErrLimitExceeded
	}
	if err := tun.Validate(c.session, protocol.ResourceDefaultTun, c.epoch.Load(), tun.Generation); err != nil {
		return nil, err
	}
	if enabled {
		if err := endpoint.Validate(c.session, protocol.ResourceDNS, c.epoch.Load(), 1); err != nil {
			return nil, err
		}
	} else if endpoint != (protocol.ResourceRef{}) {
		return nil, protocol.ErrInvalidContract
	}
	req := protocol.ConfigureDNSRequest{Meta: c.meta(true), Tun: tun, Enabled: enabled, Endpoint: endpoint}
	var result protocol.MutationResult
	if err := c.peer.CallSettled(ctx, protocol.OperationConfigureDNS, req, &result); err != nil {
		return nil, err
	}
	if result.Outcome != protocol.OutcomeCommitted {
		if result.Code != "" {
			return nil, decodeError(result.Code)
		}
		return nil, protocol.ErrRecoveryRequired
	}
	current := result.CurrentResource
	if current.ID != tun.ID || current.SessionID != tun.SessionID || current.Type != tun.Type ||
		current.GraphEpoch != tun.GraphEpoch || current.Generation < tun.Generation || current.Generation > tun.Generation+1 {
		_ = c.Close()
		return nil, protocol.ErrInvalidContract
	}
	if current.Generation == tun.Generation {
		return device, nil
	}
	device.stateMu.Lock()
	state := device.state
	state.Capabilities = slices.Clone(state.Capabilities)
	state.Addresses = slices.Clone(state.Addresses)
	state.Routes = slices.Clone(state.Routes)
	state.Warnings = slices.Clone(state.Warnings)
	device.stateMu.Unlock()
	return c.Tun(current, state)
}

func (s *DNSService) read(ctx context.Context, work chan<- applicationDNSWork) {
	buffer := make([]byte, protocol.MaxPacketRecord)
	for {
		n, meta, err := s.connection.readPacket(buffer)
		if err != nil {
			s.cancel()
			return
		}
		if n < 12 || meta == nil || meta.DNS == nil || meta.DNS.Correlation == 0 || meta.Truncated || meta.OriginalLength != uint32(n) {
			s.cancel()
			return
		}
		dns := meta.DNS
		if *meta != (protocol.PacketMetadata{OriginalLength: uint32(n), DNS: dns}) {
			s.cancel()
			return
		}
		message, err := gdns.Unpack(buffer[:n])
		if err != nil || message.Response {
			s.cancel()
			return
		}
		select {
		case work <- applicationDNSWork{message: message, token: dns.Correlation}:
		case <-ctx.Done():
			return
		}
	}
}

func (s *DNSService) resolve(ctx context.Context, work <-chan applicationDNSWork, results chan<- applicationDNSResult) {
	for {
		select {
		case <-ctx.Done():
			return
		case request := <-work:
			queryContext, cancel := context.WithTimeout(ctx, 5*time.Second)
			response, err := gdns.Query(queryContext, s.upstream, request.message)
			cancel()
			if err != nil || response == nil || !response.Response || response.ID != request.message.ID {
				response = applicationDNSFailure(request.message)
			}
			wire, packErr := gdns.Pack(response)
			if packErr != nil || len(wire) < 12 || len(wire) > protocol.MaxPacketRecord {
				response = applicationDNSFailure(request.message)
			}
			select {
			case results <- applicationDNSResult{message: response, token: request.token}:
			case <-ctx.Done():
				return
			}
		}
	}
}

func applicationDNSFailure(query *gdns.Message) *gdns.Message {
	response := &gdns.Message{Response: true, RCode: gdns.RCodeServerFailure, RecursionAvailable: true}
	if query != nil {
		response.ID = query.ID
		response.Opcode = query.Opcode
		response.RecursionDesired = query.RecursionDesired
		response.Questions = append([]gdns.Question(nil), query.Questions...)
	}
	return response
}

func (s *DNSService) write(ctx context.Context, results <-chan applicationDNSResult) {
	for {
		select {
		case <-ctx.Done():
			return
		case result := <-results:
			wire, err := gdns.Pack(result.message)
			if err != nil || len(wire) < 12 || len(wire) > protocol.MaxPacketRecord {
				s.cancel()
				return
			}
			meta := &protocol.PacketMetadata{OriginalLength: uint32(len(wire)), DNS: &protocol.DNSMetadata{Correlation: result.token}}
			if n, err := s.connection.writeRecord(wire, meta); err != nil || n != len(wire) {
				s.cancel()
				return
			}
		}
	}
}

func (s *DNSService) Close() error {
	s.closeOnce.Do(func() {
		s.cancel()
		s.closeErr = s.connection.Close()
		s.wg.Wait()
	})
	return s.closeErr
}

var _ interface{ Close() error } = (*DNSService)(nil)
