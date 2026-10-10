package network

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/netip"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/asciimoth/almagest/bin/internal/isolation/policy"
	"github.com/asciimoth/almagest/bin/internal/isolation/protocol"
	gdns "github.com/asciimoth/gonnect/dns"
	"github.com/asciimoth/gonnect/sysnet"
)

type resolverTestProvider struct {
	requests chan gdns.Request
	cancel   context.CancelFunc
	closed   atomic.Bool
	done     chan struct{}
}

func (p *resolverTestProvider) Requests() chan<- gdns.Request { return p.requests }
func (p *resolverTestProvider) Close() error                  { p.closed.Store(true); p.cancel(); <-p.done; return nil }
func resolverProvider(t *testing.T, fn func(gdns.Request) gdns.Response) *resolverTestProvider {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	p := &resolverTestProvider{requests: make(chan gdns.Request), cancel: cancel, done: make(chan struct{})}
	go func() {
		var workers sync.WaitGroup
		defer close(p.done)
		defer workers.Wait()
		for {
			select {
			case <-ctx.Done():
				return
			case req := <-p.requests:
				workers.Go(func() {
					response := fn(req)
					select {
					case req.Reply <- response:
					case <-ctx.Done():
					}
				})
			}
		}
	}()
	t.Cleanup(func() { _ = p.Close() })
	return p
}
func resolverReply(req gdns.Request) gdns.Response {
	message := req.Message.Copy()
	message.Response, message.AuthenticatedData, message.CheckingDisabled = true, true, true
	return gdns.Response{Message: message}
}

type resolverTestSystem struct {
	sysnet.System
	provider gdns.Interface
}

func (s resolverTestSystem) OutDNS() gdns.Interface { return s.provider }

func TestNativeOutDNSRelay(t *testing.T) {
	var phase atomic.Uint32
	provider := resolverProvider(t, func(req gdns.Request) gdns.Response {
		response := resolverReply(req)
		response.Message.Truncated = req.Message.Questions[0].Name == "truncated.example."
		response.Message.Answers = []gdns.Resource{{Name: req.Message.Questions[0].Name, Type: gdns.TypeA, Class: gdns.ClassIN, TTL: 3, Data: []byte{192, 0, 2, byte(phase.Load() + 1)}}}
		return response
	})
	b, fake := testBroker(t)
	b.backend = socketTestBackend{fake, &SystemBackend{system: resolverTestSystem{provider: provider}}}
	c := startClient(t, b)
	remote, err := c.OutDNS(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = remote.Close() })
	for _, name := range []string{"split.example.", "truncated.example.", "dynamic.example."} {
		phase.Add(1)
		query := &gdns.Message{ID: 123, RecursionDesired: true, Questions: []gdns.Question{{Name: name, Type: gdns.TypeA, Class: gdns.ClassIN}}}
		want, err := gdns.Query(context.Background(), provider, query)
		if err != nil {
			t.Fatal(err)
		}
		got, err := gdns.Query(context.Background(), remote, query)
		if err != nil {
			t.Fatalf("native DNS parity: got=%+v want=%+v error=%v", got, want, err)
		}
		wantWire, err := gdns.Pack(want)
		if err != nil {
			t.Fatal(err)
		}
		gotWire, err := gdns.Pack(got)
		if err != nil || !bytes.Equal(wantWire, gotWire) {
			t.Fatalf("native DNS wire parity: %v", err)
		}
	}
	var parallel sync.WaitGroup
	for range 32 {
		parallel.Go(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_, err := gdns.Query(ctx, remote, &gdns.Message{Questions: []gdns.Question{{Name: "parallel.example.", Type: gdns.TypeA, Class: gdns.ClassIN}}})
			if err != nil {
				t.Error(err)
			}
		})
	}
	parallel.Wait()
	b.mu.Lock()
	resources, receipts := len(b.resources), len(b.receipts)
	b.mu.Unlock()
	if resources > 4 || receipts > 4 {
		t.Fatalf("per-query resource growth: %d resources, %d receipts", resources, receipts)
	}
	if err := remote.Close(); err != nil {
		t.Fatal(err)
	}
	if provider.closed.Load() {
		t.Fatal("runtime closed the shared native provider")
	}
}

func TestResolverSocketBoundsAndLifetime(t *testing.T) {
	provider := resolverProvider(t, resolverReply)
	s := newResolverSocket(provider)
	var held atomic.Int64
	s.reserve = func(n int) (func(), error) { held.Add(int64(n)); return func() { held.Add(-int64(n)) }, nil }
	ctx := context.Background()
	t.Cleanup(func() { _ = s.Close(ctx) })
	packet, err := gdns.Pack(&gdns.Message{ID: 8, Questions: []gdns.Question{{Name: "budget.example.", Type: gdns.TypeA, Class: gdns.ClassIN}}})
	if err != nil {
		t.Fatal(err)
	}
	meta := &protocol.PacketMetadata{OriginalLength: uint32(len(packet))}
	if _, err := s.WriteData(packet, meta); err != nil {
		t.Fatal(err)
	}
	if held.Load() != protocol.MaxPacketRecord {
		t.Fatal("response was not reserved")
	}
	if _, err := s.WriteData(packet, meta); !errors.Is(err, protocol.ErrOperationConflict) {
		t.Fatal(err)
	}
	buffer := make([]byte, 2)
	if _, m, err := s.ReadData(buffer); !errors.Is(err, io.ErrShortBuffer) || m == nil || !m.Truncated {
		t.Fatalf("short response: %+v %v", m, err)
	}
	if held.Load() != 0 {
		t.Fatal("response budget leaked")
	}
	if _, err := s.WriteData(packet, meta); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if held.Load() != 0 || provider.closed.Load() {
		t.Fatal("close ownership or budget")
	}
	if _, err := s.WriteData(packet, meta); !errors.Is(err, net.ErrClosed) {
		t.Fatal(err)
	}
	if _, _, err := s.ReadData(buffer); !errors.Is(err, net.ErrClosed) {
		t.Fatal(err)
	}
}

func TestResolverSocketCancellation(t *testing.T) {
	started := make(chan struct{}, 1)
	provider := resolverProvider(t, func(req gdns.Request) gdns.Response {
		started <- struct{}{}
		<-req.Context.Done()
		return gdns.Response{Err: req.Context.Err()}
	})
	s := newResolverSocket(provider)
	s.reserve = func(int) (func(), error) { return func() {}, nil }
	t.Cleanup(func() { _ = s.Close(context.Background()) })
	packet, err := gdns.Pack(&gdns.Message{Questions: []gdns.Question{{Name: "cancel.example.", Type: gdns.TypeA, Class: gdns.ClassIN}}})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := s.WriteData(packet, &protocol.PacketMetadata{OriginalLength: uint32(len(packet))})
		done <- err
	}()
	<-started
	if err := s.SetWriteDeadline(time.Now()); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if !errors.Is(err, os.ErrDeadlineExceeded) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("native query was not canceled")
	}
}

func TestResolverSocketValidation(t *testing.T) {
	valid := protocol.SocketOptions{Purpose: protocol.NetworkOutbound, Kind: protocol.SocketResolver}
	if err := validateSocket(valid); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*protocol.SocketOptions){
		func(o *protocol.SocketOptions) { o.Purpose = protocol.NetworkLocal },
		func(o *protocol.SocketOptions) { o.Remote = netip.MustParseAddrPort("192.0.2.1:53") },
		func(o *protocol.SocketOptions) { o.Network = "udp" },
		func(o *protocol.SocketOptions) { o.NoDelay = true },
	} {
		invalid := valid
		change(&invalid)
		if err := validateSocket(invalid); !errors.Is(err, protocol.ErrInvalidContract) {
			t.Fatal(err)
		}
	}
	provider := resolverProvider(t, resolverReply)
	s := newResolverSocket(provider)
	for _, packet := range [][]byte{nil, make([]byte, 11), bytes.Repeat([]byte{0xff}, 12), make([]byte, protocol.MaxPacketRecord+1)} {
		if _, err := s.WriteData(packet, &protocol.PacketMetadata{OriginalLength: uint32(len(packet))}); !errors.Is(err, protocol.ErrInvalidContract) {
			t.Fatal(err)
		}
	}
}

func TestOutDNSLargeResponseAndNativeError(t *testing.T) {
	provider := resolverProvider(t, func(req gdns.Request) gdns.Response {
		if req.Message.ID == 1 {
			return gdns.Response{Err: gdns.ErrNoUpstream}
		}
		response := resolverReply(req)
		// An unknown record type preserves its opaque RDATA exactly and avoids
		// confusing a DNS wire-format length with the control-frame limit.
		response.Message.Answers = []gdns.Resource{{Name: "large.example.", Type: 65000, Class: gdns.ClassIN, Data: bytes.Repeat([]byte{0x61}, 64000)}}
		return response
	})
	b, fake := testBroker(t)
	b.backend = socketTestBackend{fake, &SystemBackend{system: resolverTestSystem{provider: provider}}}
	c := startClient(t, b)
	remote, err := c.OutDNS(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = remote.Close() })
	query := &gdns.Message{Questions: []gdns.Question{{Name: "large.example.", Type: 65000, Class: gdns.ClassIN}}}
	answer, err := gdns.Query(context.Background(), remote, query)
	if err != nil || len(answer.Answers) != 1 || len(answer.Answers[0].Data) != 64000 {
		t.Fatalf("large DNS response: %v", err)
	}
	query.ID = 1
	if _, err := gdns.Query(context.Background(), remote, query); !errors.Is(err, gdns.ErrNoUpstream) {
		t.Fatalf("native DNS error lost: %v", err)
	}
}

func TestOutDNSCancellationKeepsControlProgress(t *testing.T) {
	started := make(chan struct{}, 1)
	provider := resolverProvider(t, func(req gdns.Request) gdns.Response {
		started <- struct{}{}
		<-req.Context.Done()
		return gdns.Response{Err: req.Context.Err()}
	})
	b, fake := testBroker(t)
	b.backend = socketTestBackend{fake, &SystemBackend{system: resolverTestSystem{provider: provider}}}
	c := startClient(t, b)
	remote, err := c.OutDNS(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = remote.Close() })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := gdns.Query(ctx, remote, &gdns.Message{Questions: []gdns.Question{{Name: "blocked.example.", Type: gdns.TypeA, Class: gdns.ClassIN}}})
		done <- err
	}()
	<-started
	statusCtx, stop := context.WithTimeout(context.Background(), time.Second)
	defer stop()
	if _, err := c.Status(statusCtx); err != nil {
		t.Fatalf("DNS held control progress: %v", err)
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("DNS caller did not cancel")
	}
	closed := make(chan struct{})
	go func() { _ = remote.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("resolver close did not cancel native work")
	}
}

func TestOutDNSCloseWakesBackgroundQuery(t *testing.T) {
	started := make(chan struct{}, 1)
	provider := resolverProvider(t, func(req gdns.Request) gdns.Response {
		started <- struct{}{}
		<-req.Context.Done()
		return resolverReply(req) // Even a late successful native reply is stale.
	})
	b, fake := testBroker(t)
	b.backend = socketTestBackend{fake, &SystemBackend{system: resolverTestSystem{provider: provider}}}
	c := startClient(t, b)
	remote, err := c.OutDNS(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = remote.Close() })
	done := make(chan error, 1)
	go func() {
		_, err := gdns.Query(context.Background(), remote, &gdns.Message{Questions: []gdns.Question{{Name: "close.example.", Type: gdns.TypeA, Class: gdns.ClassIN}}})
		done <- err
	}()
	<-started
	if err := remote.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if !errors.Is(err, net.ErrClosed) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("Close abandoned an accepted query")
	}
}

func FuzzResolverPacket(f *testing.F) {
	packet, err := gdns.Pack(&gdns.Message{Questions: []gdns.Question{{Name: "fuzz.example.", Type: gdns.TypeA, Class: gdns.ClassIN}}})
	if err != nil {
		f.Fatal(err)
	}
	f.Add(packet, false)
	f.Add([]byte{}, true)
	f.Add([]byte{0xff, 0xff}, false)
	f.Fuzz(func(t *testing.T, packet []byte, spoof bool) {
		meta := &protocol.PacketMetadata{OriginalLength: uint32(len(packet))}
		if spoof {
			ep := netip.MustParseAddrPort("192.0.2.1:53")
			meta.Destination = &ep
		}
		message, err := decodeResolverRequest(packet, meta)
		if err == nil && (spoof || len(packet) < 12 || len(packet) > protocol.MaxPacketRecord || message == nil || message.Response) {
			t.Fatal("invalid resolver packet accepted")
		}
	})
}

func TestResolverRejectsUnboundedOrMissingResponse(t *testing.T) {
	for _, missing := range []bool{false, true} {
		provider := resolverProvider(t, func(req gdns.Request) gdns.Response {
			if missing {
				return gdns.Response{}
			}
			response := resolverReply(req)
			response.Message.Answers = []gdns.Resource{{Name: "overflow.example.", Type: 65000, Class: gdns.ClassIN, Data: bytes.Repeat([]byte{1}, 65520)}}
			return response
		})
		s := newResolverSocket(provider)
		var held atomic.Int64
		s.reserve = func(n int) (func(), error) { held.Add(int64(n)); return func() { held.Add(-int64(n)) }, nil }
		t.Cleanup(func() { _ = s.Close(context.Background()) })
		packet, err := gdns.Pack(&gdns.Message{Questions: []gdns.Question{{Name: "overflow.example.", Type: gdns.TypeA, Class: gdns.ClassIN}}})
		if err != nil {
			t.Fatal(err)
		}
		_, err = s.WriteData(packet, &protocol.PacketMetadata{OriginalLength: uint32(len(packet))})
		want := protocol.ErrLimitExceeded
		if missing {
			want = protocol.ErrInvalidContract
		}
		if !errors.Is(err, want) || held.Load() != 0 {
			t.Fatalf("invalid native response: %v, retained=%d", err, held.Load())
		}
	}
}

func TestResolverBudgetRejectsBeforeNativeWork(t *testing.T) {
	var calls atomic.Int32
	provider := resolverProvider(t, func(req gdns.Request) gdns.Response { calls.Add(1); return resolverReply(req) })
	s := newResolverSocket(provider)
	s.reserve = func(int) (func(), error) { return nil, protocol.ErrLimitExceeded }
	packet, err := gdns.Pack(&gdns.Message{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.WriteData(packet, &protocol.PacketMetadata{OriginalLength: uint32(len(packet))}); !errors.Is(err, protocol.ErrLimitExceeded) || calls.Load() != 0 {
		t.Fatalf("unreserved native work: %v, calls=%d", err, calls.Load())
	}
	if err := s.SetReadDeadline(time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.ReadData(make([]byte, 12)); !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatal(err)
	}
	if err := s.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestOutDNSRespectsSmallSocketQuota(t *testing.T) {
	provider := resolverProvider(t, resolverReply)
	b, fake := testBroker(t)
	b.policy.NetworkRestrictions = &policy.NetworkRestrictions{MaxSockets: 1}
	var err error
	b.session.PolicyDigest, err = b.policy.Digest()
	if err != nil {
		t.Fatal(err)
	}
	b.backend = socketTestBackend{fake, &SystemBackend{system: resolverTestSystem{provider: provider}}}
	c := startClient(t, b)
	remote, err := c.OutDNS(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = remote.Close() })
	for range 16 {
		if _, err := gdns.Query(context.Background(), remote, &gdns.Message{}); err != nil {
			t.Fatal(err)
		}
	}
	b.mu.Lock()
	count := len(b.resources)
	b.mu.Unlock()
	if count != 1 {
		t.Fatalf("socket quota bypassed: %d", count)
	}
}
