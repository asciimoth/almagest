package runtime

import (
	"context"
	"errors"
	"net"
	"slices"
	"testing"

	gdns "github.com/asciimoth/gonnect/dns"
)

type reverseTestProvider struct {
	requests chan gdns.Request
	done     chan struct{}
}

func newReverseTestProvider() *reverseTestProvider {
	provider := &reverseTestProvider{requests: make(chan gdns.Request), done: make(chan struct{})}
	go func() {
		defer close(provider.done)
		request := <-provider.requests
		response := request.Message.Copy()
		response.Response = true
		response.Answers = []gdns.Resource{{
			Name:  request.Message.Questions[0].Name,
			Type:  gdns.TypePTR,
			Class: gdns.ClassIN,
			Data:  []byte("host.example."),
		}}
		request.Reply <- gdns.Response{Message: response}
	}()
	return provider
}

func (p *reverseTestProvider) Requests() chan<- gdns.Request { return p.requests }
func (p *reverseTestProvider) Close() error                  { <-p.done; return nil }

func TestDNSResolverBuildsReverseQuestion(t *testing.T) {
	provider := newReverseTestProvider()
	resolver := newDNSResolver(provider)
	names, err := resolver.LookupAddr(context.Background(), "192.0.2.10")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(names, []string{"host.example."}) {
		t.Fatalf("names = %v", names)
	}
	if err := provider.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestReverseDNSName(t *testing.T) {
	for _, test := range []struct {
		address string
		want    string
	}{
		{"192.0.2.10", "10.2.0.192.in-addr.arpa."},
		{"2001:db8::10", "0.1.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.8.b.d.0.1.0.0.2.ip6.arpa."},
	} {
		got, err := reverseDNSName(test.address)
		if err != nil || got != test.want {
			t.Fatalf("reverseDNSName(%q) = %q, %v", test.address, got, err)
		}
	}
	_, err := reverseDNSName("not-an-address")
	var dnsErr *net.DNSError
	if !errors.As(err, &dnsErr) {
		t.Fatalf("invalid address error = %v", err)
	}
}
