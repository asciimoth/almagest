package dnsnodes

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/asciimoth/bufpool"
	"github.com/asciimoth/gonnect"
	"github.com/asciimoth/gonnect/dns"
	"github.com/asciimoth/gonnect/tun"
)

const (
	defaultTunnelDNSRequestTimeout = 5 * time.Second
	defaultTunnelDNSMaxConcurrent  = 256
	tunnelDNSEphemeralPortFirst    = 49152
	tunnelDNSEphemeralPortCount    = 1 << 14
	defaultTunnelDNSPacketSize     = 1500
)

// TunnelDNSOptions configures a TunnelDNS data path.
type TunnelDNSOptions struct {
	// Addresses are the profile interface prefixes. A query uses the first
	// address with the same family as its DNS server.
	Addresses []string
	// Servers are DNS server IP literals in preference order.
	Servers []string
	// AllowedIPs are the WireGuard peer routes. Servers outside these routes
	// are not used because the WireGuard device cannot send their packets.
	AllowedIPs []string
	// RequestTimeout limits one complete request across all configured servers.
	// The time is divided between servers so one unavailable server does not
	// prevent failover. A non-positive value uses five seconds.
	RequestTimeout time.Duration
	// MaxConcurrentRequests limits active DNS requests. A non-positive value
	// uses 256 requests.
	MaxConcurrentRequests int
}

type tunnelDNSServer struct {
	local  netip.Addr
	remote netip.Addr
}

type tunnelDNSKey struct {
	local, remote netip.Addr
	port, id      uint16
}

type tunnelDNSPending struct {
	questions []dns.Question
	response  chan *dns.Message
}

type tunnelPacket struct {
	buffer []byte
	data   []byte
	pool   bufpool.Pool
}

func (p tunnelPacket) release() {
	bufpool.PutBuffer(p.pool, p.buffer)
}

// TunnelDNS combines a TUN data path with a UDP-only DNS client.
//
// DNS queries are raw IP packets read by the WireGuard device. Matching raw
// UDP responses written by the device complete DNS requests and are also
// forwarded to the attached graph TUN. The implementation does not create a
// virtual network stack or split the TUN into two independently routed paths.
//
// TunnelDNS owns a detached wrapper around source. Close stops this object but
// does not close source, which remains owned by the graph link.
type TunnelDNS struct {
	source *tun.DetachedTun
	pool   bufpool.Pool

	servers        []tunnelDNSServer
	requestTimeout time.Duration
	requestSlots   chan struct{}
	provider       *dnsProvider

	dnsPackets    chan tunnelPacket
	sourcePackets chan tunnelPacket
	sourceErrors  chan error
	done          chan struct{}
	closed        atomic.Bool
	closeOnce     sync.Once
	closeErr      error
	workers       sync.WaitGroup

	readMu      sync.Mutex
	readPending []tunnelPacket

	pendingMu    sync.Mutex
	pending      map[tunnelDNSKey]*tunnelDNSPending
	pendingCount atomic.Int64
	nextPort     atomic.Uint32
}

var (
	_ tun.Tun       = (*TunnelDNS)(nil)
	_ dns.Interface = (*TunnelDNS)(nil)
)

// NewTunnelDNS creates a UDP DNS client on source and returns the combined TUN
// and DNS interface. Spawner tracks its long-running packet reader when set.
func NewTunnelDNS(
	source tun.Tun,
	pool bufpool.Pool,
	spawner gonnect.Spawner,
	options TunnelDNSOptions,
) (*TunnelDNS, error) {
	if source == nil {
		return nil, errors.New("dns tunnel: source TUN is nil")
	}
	servers, err := prepareTunnelDNSServers(options)
	if err != nil {
		return nil, err
	}
	if options.RequestTimeout <= 0 {
		options.RequestTimeout = defaultTunnelDNSRequestTimeout
	}
	if options.MaxConcurrentRequests <= 0 {
		options.MaxConcurrentRequests = defaultTunnelDNSMaxConcurrent
	}

	detached := tun.Detach(source, spawner, pool)
	mtu, err := detached.MTU()
	if err != nil {
		_ = detached.Close()
		return nil, fmt.Errorf("dns tunnel: inspect source TUN: %w", err)
	}
	batch := detached.BatchSize()
	if batch < 1 {
		batch = 1
	}
	queueSize := batch * 2
	if queueSize < 8 {
		queueSize = 8
	}
	if mtu < defaultTunnelDNSPacketSize {
		mtu = defaultTunnelDNSPacketSize
	}

	resolver := &TunnelDNS{
		source:         detached,
		pool:           pool,
		servers:        servers,
		requestTimeout: options.RequestTimeout,
		requestSlots:   make(chan struct{}, options.MaxConcurrentRequests),
		dnsPackets:     make(chan tunnelPacket, options.MaxConcurrentRequests),
		sourcePackets:  make(chan tunnelPacket, queueSize),
		sourceErrors:   make(chan error, 1),
		done:           make(chan struct{}),
		pending:        make(map[tunnelDNSKey]*tunnelDNSPending),
	}
	resolver.provider = newDNSProvider(resolver.handleDNSRequest)
	if err := resolver.startReader(spawner, mtu); err != nil {
		resolver.closed.Store(true)
		close(resolver.done)
		_ = resolver.source.Close()
		_ = resolver.provider.Close()
		return nil, fmt.Errorf("dns tunnel: start packet reader: %w", err)
	}
	return resolver, nil
}

func prepareTunnelDNSServers(options TunnelDNSOptions) ([]tunnelDNSServer, error) {
	locals := make(map[bool][]netip.Addr)
	for _, value := range options.Addresses {
		prefix, err := netip.ParsePrefix(strings.TrimSpace(value))
		if err != nil {
			return nil, fmt.Errorf("dns tunnel: invalid interface address %q: %w", value, err)
		}
		address := prefix.Addr().Unmap()
		locals[address.Is4()] = append(locals[address.Is4()], address)
	}
	routes := make([]netip.Prefix, 0, len(options.AllowedIPs))
	for _, value := range options.AllowedIPs {
		prefix, err := netip.ParsePrefix(strings.TrimSpace(value))
		if err != nil {
			return nil, fmt.Errorf("dns tunnel: invalid allowed IP %q: %w", value, err)
		}
		routes = append(routes, prefix)
	}

	seen := make(map[netip.Addr]struct{})
	servers := make([]tunnelDNSServer, 0, len(options.Servers))
	for _, value := range options.Servers {
		remote, err := netip.ParseAddr(strings.TrimSpace(value))
		if err != nil {
			return nil, fmt.Errorf("dns tunnel: invalid DNS server %q: %w", value, err)
		}
		remote = remote.Unmap()
		if _, duplicate := seen[remote]; duplicate {
			continue
		}
		seen[remote] = struct{}{}
		familyLocals := locals[remote.Is4()]
		if len(familyLocals) == 0 || !tunnelDNSRouteExists(routes, remote) {
			continue
		}
		servers = append(servers, tunnelDNSServer{local: familyLocals[0], remote: remote})
	}
	return servers, nil
}

func tunnelDNSRouteExists(routes []netip.Prefix, address netip.Addr) bool {
	for _, route := range routes {
		if route.Contains(address) {
			return true
		}
	}
	return false
}

func (r *TunnelDNS) startReader(spawner gonnect.Spawner, mtu int) error {
	worker := func() { r.readSource(mtu) }
	if spawner != nil {
		_, err := spawner.SpawnWg(worker, &r.workers, "dns.TunnelDNS.read")
		return err
	}
	r.workers.Add(1)
	go func() {
		defer r.workers.Done()
		worker()
	}()
	return nil
}

// Requests returns the DNS request input for this resolver.
func (r *TunnelDNS) Requests() chan<- dns.Request { return r.provider.Requests() }

// IsUp reports whether both this resolver and its detached TUN source are up.
func (r *TunnelDNS) IsUp() (bool, error) {
	if r == nil || r.closed.Load() {
		return false, nil
	}
	return r.source.IsUp()
}

// File returns nil because bypassing TunnelDNS would bypass DNS packets.
func (r *TunnelDNS) File() *os.File { return nil }

// IsNative reports false because TunnelDNS must inspect TUN writes.
func (r *TunnelDNS) IsNative() bool { return false }

func (r *TunnelDNS) MWO() int { return r.source.MWO() }
func (r *TunnelDNS) MRO() int { return r.source.MRO() }

func (r *TunnelDNS) MTU() (int, error) { return r.source.MTU() }
func (r *TunnelDNS) Name() (string, error) {
	name, err := r.source.Name()
	if err != nil {
		return "", err
	}
	return name + "-dns", nil
}

func (r *TunnelDNS) Events() <-chan tun.Event { return r.source.Events() }
func (r *TunnelDNS) BatchSize() int           { return r.source.BatchSize() }

// Read returns graph TUN packets and injected DNS queries to the WireGuard
// device. Injected DNS packets have priority to keep resolver latency bounded.
func (r *TunnelDNS) Read(buffers [][]byte, sizes []int, offset int) (int, error) {
	if offset < r.MRO() {
		return 0, errors.New("dns tunnel: read offset is too small")
	}
	limit := min(len(buffers), len(sizes))
	if limit == 0 {
		return 0, nil
	}
	r.readMu.Lock()
	defer r.readMu.Unlock()
	if r.closed.Load() {
		return 0, os.ErrClosed
	}

	packet, err := r.nextReadPacket(true)
	if err != nil {
		return 0, err
	}
	count := 0
	for {
		if offset > len(buffers[count]) || len(packet.data) > len(buffers[count])-offset {
			r.readPending = append([]tunnelPacket{packet}, r.readPending...)
			return count, io.ErrShortBuffer
		}
		copy(buffers[count][offset:], packet.data)
		sizes[count] = len(packet.data)
		packet.release()
		count++
		if count == limit {
			return count, nil
		}
		packet, err = r.nextReadPacket(false)
		if err != nil {
			return count, nil
		}
	}
}

func (r *TunnelDNS) nextReadPacket(block bool) (tunnelPacket, error) {
	if len(r.readPending) != 0 {
		packet := r.readPending[0]
		r.readPending = r.readPending[1:]
		return packet, nil
	}
	// Prefer DNS packets without starving the graph source when no query waits.
	select {
	case packet := <-r.dnsPackets:
		return packet, nil
	default:
	}
	if !block {
		select {
		case packet := <-r.dnsPackets:
			return packet, nil
		case packet := <-r.sourcePackets:
			return packet, nil
		default:
			return tunnelPacket{}, io.EOF
		}
	}
	select {
	case <-r.done:
		return tunnelPacket{}, os.ErrClosed
	case err := <-r.sourceErrors:
		return tunnelPacket{}, err
	case packet := <-r.dnsPackets:
		return packet, nil
	case packet := <-r.sourcePackets:
		return packet, nil
	}
}

// Write observes matching DNS replies, then forwards every packet unchanged to
// the graph TUN. Thus a local stack attached to the TUN sees the same traffic.
func (r *TunnelDNS) Write(buffers [][]byte, offset int) (int, error) {
	if r.closed.Load() {
		return 0, os.ErrClosed
	}
	if offset < r.MWO() {
		return 0, errors.New("dns tunnel: write offset is too small")
	}
	if r.pendingCount.Load() != 0 {
		for _, buffer := range buffers {
			if offset <= len(buffer) {
				r.acceptDNSResponse(buffer[offset:])
			}
		}
	}
	return r.source.Write(buffers, offset)
}

// Close stops the DNS provider and its detached TUN wrapper. It is idempotent.
func (r *TunnelDNS) Close() error {
	if r == nil {
		return nil
	}
	r.closeOnce.Do(func() {
		r.closed.Store(true)
		close(r.done)
		r.closeErr = errors.Join(r.source.Close(), r.provider.Close())
		r.workers.Wait()
		r.readMu.Lock()
		for _, packet := range r.readPending {
			packet.release()
		}
		r.readPending = nil
		r.readMu.Unlock()
		r.drainPacketChannel(r.dnsPackets)
		r.drainPacketChannel(r.sourcePackets)
	})
	return r.closeErr
}

func (r *TunnelDNS) drainPacketChannel(packets <-chan tunnelPacket) {
	for {
		select {
		case packet := <-packets:
			packet.release()
		default:
			return
		}
	}
}

func (r *TunnelDNS) readSource(mtu int) {
	batch := r.source.BatchSize()
	if batch < 1 {
		batch = 1
	}
	offset := r.source.MRO()
	for {
		buffers := make([][]byte, batch)
		sizes := make([]int, batch)
		for index := range buffers {
			buffers[index] = bufpool.GetBuffer(r.pool, offset+mtu)
		}
		count, err := r.source.Read(buffers, sizes, offset)
		if count < 0 {
			count = 0
		}
		if count > len(buffers) {
			count = len(buffers)
		}
		for index := 0; index < count; index++ {
			size := sizes[index]
			if size < 0 || offset > len(buffers[index]) || size > len(buffers[index])-offset {
				bufpool.PutBuffer(r.pool, buffers[index])
				continue
			}
			packet := tunnelPacket{
				buffer: buffers[index],
				data:   buffers[index][offset : offset+size],
				pool:   r.pool,
			}
			select {
			case r.sourcePackets <- packet:
			case <-r.done:
				packet.release()
			}
			buffers[index] = nil
		}
		for index := count; index < len(buffers); index++ {
			bufpool.PutBuffer(r.pool, buffers[index])
		}
		if err == nil {
			continue
		}
		select {
		case r.sourceErrors <- err:
		case <-r.done:
			return
		}
		if tun.IsTunTermError(err) {
			return
		}
	}
}

func (r *TunnelDNS) handleDNSRequest(root context.Context, request dns.Request) {
	ctx, cancel := dnsRequestContext(root, request)
	defer cancel()
	ctx, cancelTimeout := context.WithTimeout(ctx, r.requestTimeout)
	defer cancelTimeout()
	if request.Message == nil {
		sendDNSResponse(request, nil, errors.New("dns tunnel: request message is nil"))
		return
	}
	if request.Message.Response {
		sendDNSResponse(request, nil, errors.New("dns tunnel: request is already a response"))
		return
	}
	select {
	case r.requestSlots <- struct{}{}:
		defer func() { <-r.requestSlots }()
	case <-ctx.Done():
		sendDNSResponse(request, nil, ctx.Err())
		return
	}

	var lastErr error
	for index, server := range r.servers {
		attempt, cancelAttempt := tunnelDNSAttemptContext(ctx, len(r.servers)-index)
		response, err := r.exchangeDNS(attempt, server, request.Message)
		cancelAttempt()
		if err == nil {
			response.ID = request.Message.ID
			sendDNSResponse(request, response, nil)
			return
		}
		lastErr = fmt.Errorf("dns tunnel: server %s: %w", server.remote, err)
		if ctx.Err() != nil {
			break
		}
	}
	if lastErr == nil {
		lastErr = dns.ErrNoUpstream
	}
	sendDNSResponse(request, nil, lastErr)
}

func tunnelDNSAttemptContext(ctx context.Context, serversRemaining int) (context.Context, context.CancelFunc) {
	deadline, ok := ctx.Deadline()
	if !ok || serversRemaining < 2 {
		return context.WithCancel(ctx)
	}
	remaining := time.Until(deadline)
	if remaining <= 0 {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, remaining/time.Duration(serversRemaining))
}

func (r *TunnelDNS) exchangeDNS(
	ctx context.Context,
	server tunnelDNSServer,
	request *dns.Message,
) (*dns.Message, error) {
	wire := request.Copy()
	wire.ID = dns.NextID()
	payload, err := dns.Pack(wire)
	if err != nil {
		return nil, err
	}
	pending := &tunnelDNSPending{
		questions: append([]dns.Question(nil), wire.Questions...),
		response:  make(chan *dns.Message, 1),
	}
	key, err := r.addPending(server, wire.ID, pending)
	if err != nil {
		return nil, err
	}
	defer r.removePending(key, pending)
	packet, err := buildUDPIPPacket(server.local, server.remote, key.port, dnsUDPPort, payload)
	if err != nil {
		return nil, err
	}
	select {
	case r.dnsPackets <- tunnelPacket{data: packet}:
	case <-r.done:
		return nil, dns.ErrClosed
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	select {
	case response := <-pending.response:
		return response, nil
	case <-r.done:
		return nil, dns.ErrClosed
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (r *TunnelDNS) addPending(
	server tunnelDNSServer,
	id uint16,
	pending *tunnelDNSPending,
) (tunnelDNSKey, error) {
	for range tunnelDNSEphemeralPortCount {
		sequence := r.nextPort.Add(1)
		// The addition stays in the IANA dynamic/private port range.
		port := tunnelDNSEphemeralPortFirst + uint16(sequence%tunnelDNSEphemeralPortCount) //nolint:gosec
		key := tunnelDNSKey{local: server.local, remote: server.remote, port: port, id: id}
		r.pendingMu.Lock()
		if _, exists := r.pending[key]; !exists {
			r.pending[key] = pending
			r.pendingCount.Add(1)
			r.pendingMu.Unlock()
			return key, nil
		}
		r.pendingMu.Unlock()
	}
	return tunnelDNSKey{}, errors.New("dns tunnel: no ephemeral source port is available")
}

func (r *TunnelDNS) removePending(key tunnelDNSKey, pending *tunnelDNSPending) {
	r.pendingMu.Lock()
	if r.pending[key] == pending {
		delete(r.pending, key)
		r.pendingCount.Add(-1)
	}
	r.pendingMu.Unlock()
}

func (r *TunnelDNS) acceptDNSResponse(packetBytes []byte) {
	packet, ok := parseUDPIPPacket(packetBytes)
	if !ok || packet.sourcePort != dnsUDPPort {
		return
	}
	message, err := dns.Unpack(packet.payload)
	if err != nil || !message.Response {
		return
	}
	key := tunnelDNSKey{
		local: packet.destination, remote: packet.source,
		port: packet.destinationPort, id: message.ID,
	}
	r.pendingMu.Lock()
	pending := r.pending[key]
	if pending == nil || !sameDNSQuestions(pending.questions, message.Questions) {
		r.pendingMu.Unlock()
		return
	}
	delete(r.pending, key)
	r.pendingCount.Add(-1)
	r.pendingMu.Unlock()
	select {
	case pending.response <- message:
	default:
	}
}

func sameDNSQuestions(first, second []dns.Question) bool {
	if len(first) != len(second) {
		return false
	}
	for index := range first {
		if first[index].Type != second[index].Type || first[index].Class != second[index].Class ||
			!strings.EqualFold(absoluteDNSName(first[index].Name), absoluteDNSName(second[index].Name)) {
			return false
		}
	}
	return true
}

func absoluteDNSName(name string) string {
	if name == "" || name == "." || strings.HasSuffix(name, ".") {
		return name
	}
	return name + "."
}
