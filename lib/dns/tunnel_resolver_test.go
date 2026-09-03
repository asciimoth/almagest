package dnsnodes

import (
	"bytes"
	"context"
	"errors"
	"net/netip"
	"os"
	"testing"
	"time"

	"github.com/asciimoth/gonnect/dns"
	"github.com/asciimoth/gonnect/tun"
)

func TestTunnelDNSQueriesAndForwardsResponses(t *testing.T) {
	tests := []struct {
		name      string
		address   string
		server    string
		allowedIP string
		answer    netip.Addr
	}{
		{
			name: "IPv4", address: "10.20.0.2/32", server: "10.20.0.1",
			allowedIP: "0.0.0.0/0", answer: netip.MustParseAddr("192.0.2.10"),
		},
		{
			name: "IPv6", address: "fd00::2/128", server: "fd00::1",
			allowedIP: "::/0", answer: netip.MustParseAddr("2001:db8::10"),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			resolver, peer := newTunnelDNSTestPair(t, TunnelDNSOptions{
				Addresses: []string{test.address}, Servers: []string{test.server},
				AllowedIPs: []string{test.allowedIP}, RequestTimeout: time.Second,
			})
			questionType := uint16(dns.TypeA)
			if test.answer.Is6() {
				questionType = dns.TypeAAAA
			}
			request := &dns.Message{ID: 0x1234, RecursionDesired: true, Questions: []dns.Question{{
				Name: "example.test", Type: questionType, Class: dns.ClassIN,
			}}}
			result := queryTunnelDNS(t, resolver, request)

			outbound := readTunnelDNSTestPacket(t, resolver)
			queryPacket, ok := parseUDPIPPacket(outbound)
			if !ok {
				t.Fatalf("outbound DNS packet is invalid: %x", outbound)
			}
			if queryPacket.source != netip.MustParsePrefix(test.address).Addr() ||
				queryPacket.destination != netip.MustParseAddr(test.server) ||
				queryPacket.destinationPort != dnsUDPPort {
				t.Fatalf("outbound DNS tuple = %#v", queryPacket)
			}
			wireQuery, err := dns.Unpack(queryPacket.payload)
			if err != nil {
				t.Fatalf("unpack outbound DNS query: %v", err)
			}
			if !sameDNSQuestions(wireQuery.Questions, request.Questions) {
				t.Fatalf("outbound DNS query = %#v", wireQuery)
			}

			wireResponse := &dns.Message{
				ID: wireQuery.ID, Response: true, RecursionDesired: true, RecursionAvailable: true,
				Questions: wireQuery.Questions,
				Answers: []dns.Resource{{
					Name: "example.test", Type: questionType, Class: dns.ClassIN,
					TTL: 60, Data: append([]byte(nil), test.answer.AsSlice()...),
				}},
			}
			payload, err := dns.Pack(wireResponse)
			if err != nil {
				t.Fatalf("pack DNS response: %v", err)
			}
			inbound, err := buildUDPIPPacket(
				queryPacket.destination, queryPacket.source,
				dnsUDPPort, queryPacket.sourcePort, payload,
			)
			if err != nil {
				t.Fatalf("build inbound packet: %v", err)
			}
			writeTunnelDNSTestPacket(t, resolver, peer, inbound)

			response := <-result
			if response.err != nil {
				t.Fatalf("DNS query: %v", response.err)
			}
			if response.message == nil || response.message.ID != request.ID ||
				len(response.message.Answers) != 1 ||
				!bytes.Equal(response.message.Answers[0].Data, test.answer.AsSlice()) {
				t.Fatalf("DNS response = %#v", response.message)
			}
		})
	}
}

func TestTunnelDNSPassesNonDNSPacketsBothWays(t *testing.T) {
	resolver, peer := newTunnelDNSTestPair(t, TunnelDNSOptions{
		Addresses: []string{"10.20.0.2/32"}, Servers: []string{"10.20.0.1"},
		AllowedIPs: []string{"0.0.0.0/0"},
	})
	outbound, err := buildUDPIPPacket(
		netip.MustParseAddr("10.20.0.2"), netip.MustParseAddr("192.0.2.1"),
		12000, 443, []byte("outbound"),
	)
	if err != nil {
		t.Fatalf("build outbound packet: %v", err)
	}
	writeDone := make(chan error, 1)
	go func() {
		_, writeErr := peer.Write([][]byte{outbound}, peer.MWO())
		writeDone <- writeErr
	}()
	if got := readTunnelDNSTestPacket(t, resolver); !bytes.Equal(got, outbound) {
		t.Fatalf("outbound packet = %x, want %x", got, outbound)
	}
	if err := <-writeDone; err != nil {
		t.Fatalf("write graph outbound packet: %v", err)
	}

	inbound, err := buildUDPIPPacket(
		netip.MustParseAddr("192.0.2.1"), netip.MustParseAddr("10.20.0.2"),
		443, 12000, []byte("inbound"),
	)
	if err != nil {
		t.Fatalf("build inbound packet: %v", err)
	}
	writeTunnelDNSTestPacket(t, resolver, peer, inbound)
}

func TestTunnelDNSFailsOverToNextProfileServer(t *testing.T) {
	resolver, peer := newTunnelDNSTestPair(t, TunnelDNSOptions{
		Addresses:  []string{"10.20.0.2/32"},
		Servers:    []string{"10.20.0.1", "10.20.0.3"},
		AllowedIPs: []string{"10.20.0.0/24"},
		// Each of the two servers gets about half of this total budget.
		RequestTimeout: 400 * time.Millisecond,
	})
	request := &dns.Message{ID: 7, Questions: []dns.Question{{
		Name: "failover.test.", Type: dns.TypeA, Class: dns.ClassIN,
	}}}
	result := queryTunnelDNS(t, resolver, request)
	first, ok := parseUDPIPPacket(readTunnelDNSTestPacket(t, resolver))
	if !ok || first.destination != netip.MustParseAddr("10.20.0.1") {
		t.Fatalf("first DNS attempt = %#v", first)
	}
	second, ok := parseUDPIPPacket(readTunnelDNSTestPacket(t, resolver))
	if !ok || second.destination != netip.MustParseAddr("10.20.0.3") {
		t.Fatalf("second DNS attempt = %#v", second)
	}
	wireQuery, err := dns.Unpack(second.payload)
	if err != nil {
		t.Fatalf("unpack second DNS attempt: %v", err)
	}
	payload, err := dns.Pack(&dns.Message{
		ID: wireQuery.ID, Response: true, Questions: wireQuery.Questions,
	})
	if err != nil {
		t.Fatalf("pack second DNS response: %v", err)
	}
	inbound, err := buildUDPIPPacket(
		second.destination, second.source, dnsUDPPort, second.sourcePort, payload,
	)
	if err != nil {
		t.Fatalf("build second DNS response: %v", err)
	}
	writeTunnelDNSTestPacket(t, resolver, peer, inbound)
	response := <-result
	if response.err != nil || response.message == nil || response.message.ID != request.ID {
		t.Fatalf("failover DNS response = %#v, error %v", response.message, response.err)
	}
}

func TestTunnelDNSIgnoresResponseForAnotherQuestion(t *testing.T) {
	resolver, peer := newTunnelDNSTestPair(t, TunnelDNSOptions{
		Addresses: []string{"10.20.0.2/32"}, Servers: []string{"10.20.0.1"},
		AllowedIPs: []string{"0.0.0.0/0"}, RequestTimeout: time.Second,
	})
	request := &dns.Message{ID: 99, Questions: []dns.Question{{
		Name: "expected.test.", Type: dns.TypeA, Class: dns.ClassIN,
	}}}
	result := queryTunnelDNS(t, resolver, request)
	queryPacket, ok := parseUDPIPPacket(readTunnelDNSTestPacket(t, resolver))
	if !ok {
		t.Fatal("outbound DNS query packet is invalid")
	}
	wireQuery, err := dns.Unpack(queryPacket.payload)
	if err != nil {
		t.Fatalf("unpack DNS query: %v", err)
	}
	wrongPayload, err := dns.Pack(&dns.Message{
		ID: wireQuery.ID, Response: true, Questions: []dns.Question{{
			Name: "other.test.", Type: dns.TypeA, Class: dns.ClassIN,
		}},
	})
	if err != nil {
		t.Fatalf("pack mismatched DNS response: %v", err)
	}
	wrongPacket, err := buildUDPIPPacket(
		queryPacket.destination, queryPacket.source,
		dnsUDPPort, queryPacket.sourcePort, wrongPayload,
	)
	if err != nil {
		t.Fatalf("build mismatched DNS response: %v", err)
	}
	writeTunnelDNSTestPacket(t, resolver, peer, wrongPacket)
	select {
	case response := <-result:
		t.Fatalf("mismatched response completed query: %#v", response)
	case <-time.After(20 * time.Millisecond):
	}

	correctPayload, err := dns.Pack(&dns.Message{
		ID: wireQuery.ID, Response: true, Questions: wireQuery.Questions,
	})
	if err != nil {
		t.Fatalf("pack matching DNS response: %v", err)
	}
	correctPacket, err := buildUDPIPPacket(
		queryPacket.destination, queryPacket.source,
		dnsUDPPort, queryPacket.sourcePort, correctPayload,
	)
	if err != nil {
		t.Fatalf("build matching DNS response: %v", err)
	}
	writeTunnelDNSTestPacket(t, resolver, peer, correctPacket)
	response := <-result
	if response.err != nil || response.message == nil || response.message.ID != request.ID {
		t.Fatalf("matching DNS response = %#v, error %v", response.message, response.err)
	}
}

func TestTunnelDNSReturnsNoUpstreamWhenProfileHasNoDNSRoute(t *testing.T) {
	resolver, _ := newTunnelDNSTestPair(t, TunnelDNSOptions{
		Addresses: []string{"10.20.0.2/32"}, Servers: []string{"10.20.0.1"},
		AllowedIPs: []string{"192.0.2.0/24"}, RequestTimeout: time.Second,
	})
	result := queryTunnelDNS(t, resolver, &dns.Message{Questions: []dns.Question{{
		Name: "unrouted.test.", Type: dns.TypeA, Class: dns.ClassIN,
	}}})
	response := <-result
	if !errors.Is(response.err, dns.ErrNoUpstream) {
		t.Fatalf("unrouted DNS error = %v, want %v", response.err, dns.ErrNoUpstream)
	}
}

func TestTunnelDNSRejectsInvalidProfileNetworkValues(t *testing.T) {
	tests := []struct {
		name    string
		options TunnelDNSOptions
	}{
		{name: "address", options: TunnelDNSOptions{
			Addresses: []string{"bad"}, Servers: []string{"10.20.0.1"}, AllowedIPs: []string{"0.0.0.0/0"},
		}},
		{name: "server", options: TunnelDNSOptions{
			Addresses: []string{"10.20.0.2/32"}, Servers: []string{"bad"}, AllowedIPs: []string{"0.0.0.0/0"},
		}},
		{name: "route", options: TunnelDNSOptions{
			Addresses: []string{"10.20.0.2/32"}, Servers: []string{"10.20.0.1"}, AllowedIPs: []string{"bad"},
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			source, peer := tun.Pipe(1, 1500, 0, 0)
			t.Cleanup(func() {
				_ = source.Close()
				_ = peer.Close()
			})
			if resolver, err := NewTunnelDNS(source, nil, nil, test.options); err == nil {
				_ = resolver.Close()
				t.Fatal("NewTunnelDNS accepted invalid profile network values")
			}
		})
	}
}

func TestTunnelDNSCloseCancelsRequestAndPreservesGraphTun(t *testing.T) {
	pipeSource, peer := tun.Pipe(1, 1500, 0, 0)
	source := tun.Detach(pipeSource, nil, nil)
	t.Cleanup(func() {
		_ = source.Close()
		_ = pipeSource.Close()
		_ = peer.Close()
	})
	resolver, err := NewTunnelDNS(source, nil, nil, TunnelDNSOptions{
		Addresses: []string{"10.20.0.2/32"}, Servers: []string{"10.20.0.1"},
		AllowedIPs: []string{"0.0.0.0/0"}, RequestTimeout: time.Second,
	})
	if err != nil {
		t.Fatalf("create DNS tunnel: %v", err)
	}
	result := queryTunnelDNS(t, resolver, &dns.Message{Questions: []dns.Question{{
		Name: "close.test.", Type: dns.TypeA, Class: dns.ClassIN,
	}}})
	_ = readTunnelDNSTestPacket(t, resolver)
	if err := resolver.Close(); err != nil {
		t.Fatalf("close DNS tunnel: %v", err)
	}
	response := <-result
	if !errors.Is(response.err, dns.ErrClosed) && !errors.Is(response.err, context.Canceled) {
		t.Fatalf("closed DNS query error = %v", response.err)
	}
	if _, err := resolver.Read([][]byte{make([]byte, 1500)}, make([]int, 1), 0); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("read after close error = %v", err)
	}
	if up, err := source.IsUp(); err != nil || !up {
		t.Fatalf("graph TUN after resolver close: up=%t error=%v", up, err)
	}
}

type tunnelDNSQueryResult struct {
	message *dns.Message
	err     error
}

func newTunnelDNSTestPair(t *testing.T, options TunnelDNSOptions) (*TunnelDNS, tun.Tun) {
	t.Helper()
	pipeSource, peer := tun.Pipe(4, 1500, 0, 0)
	// Model a graph-owned detached TUN. TunnelDNS must close only its nested
	// child and leave this graph payload usable.
	source := tun.Detach(pipeSource, nil, nil)
	resolver, err := NewTunnelDNS(source, nil, nil, options)
	if err != nil {
		_ = pipeSource.Close()
		t.Fatalf("create DNS tunnel: %v", err)
	}
	t.Cleanup(func() {
		_ = resolver.Close()
		_ = source.Close()
		_ = pipeSource.Close()
		_ = peer.Close()
	})
	return resolver, peer
}

func queryTunnelDNS(t *testing.T, resolver dns.Interface, request *dns.Message) <-chan tunnelDNSQueryResult {
	t.Helper()
	result := make(chan tunnelDNSQueryResult, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		message, err := dns.Query(ctx, resolver, request)
		result <- tunnelDNSQueryResult{message: message, err: err}
	}()
	return result
}

func readTunnelDNSTestPacket(t *testing.T, device tun.Tun) []byte {
	t.Helper()
	type readResult struct {
		packet []byte
		err    error
	}
	result := make(chan readResult, 1)
	go func() {
		offset := device.MRO()
		buffers := [][]byte{make([]byte, offset+65535)}
		sizes := make([]int, 1)
		count, err := device.Read(buffers, sizes, offset)
		if err != nil || count != 1 {
			result <- readResult{err: err}
			return
		}
		result <- readResult{packet: append([]byte(nil), buffers[0][offset:offset+sizes[0]]...)}
	}()
	select {
	case read := <-result:
		if read.err != nil {
			t.Fatalf("read TUN packet: %v", read.err)
		}
		return read.packet
	case <-time.After(time.Second):
		t.Fatal("timed out reading TUN packet")
		return nil
	}
}

func writeTunnelDNSTestPacket(t *testing.T, resolver *TunnelDNS, peer tun.Tun, packet []byte) {
	t.Helper()
	writeDone := make(chan error, 1)
	go func() {
		_, err := resolver.Write([][]byte{packet}, resolver.MWO())
		writeDone <- err
	}()
	forwarded := readTunnelDNSTestPacket(t, peer)
	if !bytes.Equal(forwarded, packet) {
		t.Fatalf("forwarded packet = %x, want %x", forwarded, packet)
	}
	if err := <-writeDone; err != nil {
		t.Fatalf("write tunnel packet: %v", err)
	}
}
