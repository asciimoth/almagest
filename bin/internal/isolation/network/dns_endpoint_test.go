package network

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"sync"
	"testing"
	"time"

	"github.com/asciimoth/almagest/bin/internal/isolation/protocol"
	gdns "github.com/asciimoth/gonnect/dns"
)

func TestApplicationDNSRelay(t *testing.T) {
	provider := resolverProvider(t, func(req gdns.Request) gdns.Response {
		response := resolverReply(req)
		response.Message.Answers = []gdns.Resource{{Name: req.Message.Questions[0].Name, Type: gdns.TypeA, Class: gdns.ClassIN, TTL: 5, Data: []byte{198, 51, 100, 7}}}
		return response
	})
	b, _ := testBroker(t)
	endpoint := NewApplicationDNSEndpoint()
	ref, err := b.RegisterDNSEndpoint(context.Background(), 1, endpoint)
	if err != nil {
		t.Fatal(err)
	}
	c := startClient(t, b)
	service, err := c.ServeDNS(ref, provider)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.Close() })

	var group sync.WaitGroup
	for i := range 32 {
		group.Go(func() {
			query := &gdns.Message{ID: uint16(i + 1), RecursionDesired: true, Questions: []gdns.Question{{Name: "application.example.", Type: gdns.TypeA, Class: gdns.ClassIN}}}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			response, err := gdns.Query(ctx, endpoint.Resolver(), query)
			if err != nil || response == nil || !response.Response || response.ID != query.ID || len(response.Answers) != 1 {
				t.Errorf("query %d: response=%+v error=%v", i, response, err)
			}
		})
	}
	group.Wait()
	if err := service.Close(); err != nil {
		t.Fatal(err)
	}
	if err := endpoint.Verify(context.Background()); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("endpoint remained open: %v", err)
	}
}

func FuzzApplicationDNSResponse(f *testing.F) {
	valid, err := gdns.Pack(&gdns.Message{ID: 3, Response: true, Questions: []gdns.Question{{Name: "fuzz.example.", Type: gdns.TypeA, Class: gdns.ClassIN}}})
	if err != nil {
		f.Fatal(err)
	}
	f.Add(valid, uint64(1), false)
	f.Add([]byte{}, uint64(0), false)
	f.Fuzz(func(t *testing.T, packet []byte, correlation uint64, spoof bool) {
		metadata := &protocol.PacketMetadata{OriginalLength: uint32(len(packet)), DNS: &protocol.DNSMetadata{Correlation: correlation}}
		if spoof {
			source := netip.MustParseAddrPort("192.0.2.1:53")
			metadata.Source = &source
		}
		message, token, err := decodeApplicationDNSResponse(packet, metadata)
		if err == nil && (message == nil || !message.Response || token == 0 || spoof || len(packet) < 12 || len(packet) > protocol.MaxPacketRecord) {
			t.Fatal("invalid application DNS response accepted")
		}
	})
}

func TestApplicationDNSRelayConvertsProviderError(t *testing.T) {
	provider := resolverProvider(t, func(gdns.Request) gdns.Response {
		return gdns.Response{Err: errors.New("application resolver failed")}
	})
	b, _ := testBroker(t)
	endpoint := NewApplicationDNSEndpoint()
	ref, err := b.RegisterDNSEndpoint(context.Background(), 1, endpoint)
	if err != nil {
		t.Fatal(err)
	}
	c := startClient(t, b)
	service, err := c.ServeDNS(ref, provider)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.Close() })
	query := &gdns.Message{ID: 77, Questions: []gdns.Question{{Name: "failure.example.", Type: gdns.TypeAAAA, Class: gdns.ClassIN}}}
	response, err := gdns.Query(context.Background(), endpoint.Resolver(), query)
	if err != nil || response == nil || response.ID != query.ID || response.RCode != gdns.RCodeServerFailure {
		t.Fatalf("response=%+v error=%v", response, err)
	}
}

func TestApplicationDNSEndpointBoundsAndValidation(t *testing.T) {
	endpoint := NewApplicationDNSEndpoint()
	t.Cleanup(func() { _ = endpoint.Close(context.Background()) })
	query := &gdns.Message{ID: 9, Questions: []gdns.Question{{Name: "bounded.example.", Type: gdns.TypeA, Class: gdns.ClassIN}}}
	for range maxApplicationDNSPending {
		reply := make(chan gdns.Response, 1)
		select {
		case endpoint.Resolver().Requests() <- gdns.Request{Context: context.Background(), Message: query, Reply: reply}:
		case <-time.After(time.Second):
			t.Fatal("endpoint stopped accepting within its bound")
		}
	}
	overflow := make(chan gdns.Response, 1)
	endpoint.Resolver().Requests() <- gdns.Request{Context: context.Background(), Message: query, Reply: overflow}
	if response := <-overflow; !errors.Is(response.Err, protocol.ErrLimitExceeded) {
		t.Fatalf("overflow error: %v", response.Err)
	}
	endpoint.mu.Lock()
	pending := len(endpoint.pending)
	endpoint.mu.Unlock()
	if pending != maxApplicationDNSPending {
		t.Fatalf("pending=%d", pending)
	}

	first := NewApplicationDNSEndpoint()
	defer first.Close(context.Background()) //nolint:errcheck
	reply := make(chan gdns.Response, 1)
	first.Resolver().Requests() <- gdns.Request{Context: context.Background(), Message: query, Reply: reply}
	packet := make([]byte, protocol.MaxPacketRecord)
	n, metadata, err := first.ReadData(packet)
	if err != nil {
		t.Fatal(err)
	}
	response := query.Copy()
	response.Response = true
	response.ID++
	wire, err := gdns.Pack(response)
	if err != nil {
		t.Fatal(err)
	}
	metadata.OriginalLength = uint32(len(wire))
	if _, err := first.WriteData(wire, metadata); !errors.Is(err, protocol.ErrInvalidContract) {
		t.Fatalf("mismatched response ID: %v", err)
	}
	if result := <-reply; !errors.Is(result.Err, protocol.ErrInvalidContract) {
		t.Fatalf("pending request error: %v", result.Err)
	}
	if n < 12 {
		t.Fatal("short relayed query")
	}
}
