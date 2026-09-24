package tailscalenodes

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"slices"
	"testing"
	"time"

	gdns "github.com/asciimoth/gonnect/dns"
	wgotailscale "github.com/asciimoth/wgo-tailscale"
)

func TestMagicDNSResolverUsesRecordsSearchDomainsAndCNAMEs(t *testing.T) {
	resolver := &magicDNSResolver{}
	resolver.SetView(wgotailscale.DNSView{
		SearchDomains: []string{"tail.example.test."},
		Records: []wgotailscale.DNSRecord{
			{Name: "alpha.tail.example.test.", Type: "A", Value: "100.64.0.1"},
			{Name: "alpha.tail.example.test.", Type: "AAAA", Value: "fd7a:115c:a1e0::1"},
			{Name: "alias.tail.example.test.", Type: "CNAME", Value: "alpha.tail.example.test."},
		},
	})

	addresses, err := resolver.LookupNetIP(context.Background(), "ip", "alpha")
	if err != nil {
		t.Fatalf("LookupNetIP alpha: %v", err)
	}
	want := []netip.Addr{netip.MustParseAddr("100.64.0.1"), netip.MustParseAddr("fd7a:115c:a1e0::1")}
	if len(addresses) != len(want) || addresses[0] != want[0] || addresses[1] != want[1] {
		t.Fatalf("LookupNetIP alpha = %v, want %v", addresses, want)
	}
	addresses, err = resolver.LookupNetIP(context.Background(), "ip4", "alias")
	if err != nil || len(addresses) != 1 || addresses[0] != want[0] {
		t.Fatalf("LookupNetIP alias = %v, %v", addresses, err)
	}
	cname, err := resolver.LookupCNAME(context.Background(), "alias")
	if err != nil || cname != "alpha.tail.example.test." {
		t.Fatalf("LookupCNAME alias = %q, %v", cname, err)
	}
	names, err := resolver.LookupAddr(context.Background(), "100.64.0.1")
	if err != nil || len(names) != 1 || names[0] != "alpha.tail.example.test." {
		t.Fatalf("LookupAddr = %v, %v", names, err)
	}
}

func TestMagicDNSResolverStandardLookupAdapters(t *testing.T) {
	resolver := &magicDNSResolver{}
	resolver.SetView(wgotailscale.DNSView{Records: []wgotailscale.DNSRecord{
		{Name: "host.example.test", Type: "A", Value: "192.0.2.10"},
		{Name: "host.example.test", Type: "AAAA", Value: "2001:db8::10"},
	}})

	ips, err := resolver.LookupIP(context.Background(), "ip4", "host.example.test")
	if err != nil || len(ips) != 1 || !ips[0].Equal(net.IPv4(192, 0, 2, 10)) {
		t.Fatalf("LookupIP = %v, %v", ips, err)
	}
	addrs, err := resolver.LookupIPAddr(context.Background(), "host.example.test")
	if err != nil || len(addrs) != 2 {
		t.Fatalf("LookupIPAddr = %v, %v", addrs, err)
	}
	hosts, err := resolver.LookupHost(context.Background(), "host.example.test")
	if err != nil || !slices.Equal(hosts, []string{"192.0.2.10", "2001:db8::10"}) {
		t.Fatalf("LookupHost = %v, %v", hosts, err)
	}
	port, err := resolver.LookupPort(context.Background(), "tcp", "https")
	if err != nil || port != 443 {
		t.Fatalf("LookupPort = %d, %v", port, err)
	}

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	for name, lookup := range map[string]func() error{
		"IP":     func() error { _, err := resolver.LookupIP(canceled, "ip", "host.example.test"); return err },
		"IPAddr": func() error { _, err := resolver.LookupIPAddr(canceled, "host.example.test"); return err },
		"Host":   func() error { _, err := resolver.LookupHost(canceled, "host.example.test"); return err },
	} {
		if err := lookup(); !errors.Is(err, context.Canceled) {
			t.Fatalf("Lookup%s canceled error = %v", name, err)
		}
	}
}

func TestMagicDNSResolverTypedRecordsAndMisses(t *testing.T) {
	resolver := &magicDNSResolver{}
	resolver.SetView(wgotailscale.DNSView{Records: []wgotailscale.DNSRecord{
		{Name: "example.test", Type: "MX", Value: "10 mail.example.test."},
		{Name: "_service._tcp.example.test", Type: "SRV", Value: "1 2 443 target.example.test."},
		{Name: "example.test", Type: "TXT", Value: "tailnet metadata"},
		{Name: "example.test", Type: "NS", Value: "ns.example.test."},
	}})
	mx, err := resolver.LookupMX(context.Background(), "example.test")
	if err != nil || len(mx) != 1 || mx[0].Pref != 10 || mx[0].Host != "mail.example.test." {
		t.Fatalf("LookupMX = %#v, %v", mx, err)
	}
	cname, srv, err := resolver.LookupSRV(context.Background(), "service", "tcp", "example.test")
	if err != nil || cname != "_service._tcp.example.test." || len(srv) != 1 || srv[0].Port != 443 {
		t.Fatalf("LookupSRV = %q, %#v, %v", cname, srv, err)
	}
	txt, err := resolver.LookupTXT(context.Background(), "example.test")
	if err != nil || len(txt) != 1 || txt[0] != "tailnet metadata" {
		t.Fatalf("LookupTXT = %v, %v", txt, err)
	}
	ns, err := resolver.LookupNS(context.Background(), "example.test")
	if err != nil || len(ns) != 1 || ns[0].Host != "ns.example.test." {
		t.Fatalf("LookupNS = %#v, %v", ns, err)
	}
	_, err = resolver.LookupNetIP(context.Background(), "ip", "not-in-tailnet.invalid")
	var dnsErr *net.DNSError
	if !errors.As(err, &dnsErr) || !dnsErr.IsNotFound {
		t.Fatalf("MagicDNS miss error = %#v, want not-found DNS error", err)
	}
}

func TestMagicDNSResolverLookupAddr(t *testing.T) {
	resolver := &magicDNSResolver{}
	resolver.SetView(wgotailscale.DNSView{Records: []wgotailscale.DNSRecord{
		{Name: "zeta-v4.example.test", Type: "A", Value: "192.0.2.10"},
		{Name: "alpha-v4.example.test.", Type: "A", Value: "192.0.2.10"},
		{Name: "alpha-v4.example.test.", Type: "A", Value: "192.0.2.10"},
		{Name: "v6.example.test", Type: "AAAA", Value: "2001:db8::10"},
	}})

	tests := []struct {
		name      string
		address   string
		wantNames []string
		wantFound bool
	}{
		{
			name:      "IPv4",
			address:   "192.0.2.10",
			wantNames: []string{"alpha-v4.example.test.", "zeta-v4.example.test."},
			wantFound: true,
		},
		{
			name:      "trimmed IPv4",
			address:   " 192.0.2.10 ",
			wantNames: []string{"alpha-v4.example.test.", "zeta-v4.example.test."},
			wantFound: true,
		},
		{
			name:      "IPv4-mapped IPv6",
			address:   "::ffff:192.0.2.10",
			wantNames: []string{"alpha-v4.example.test.", "zeta-v4.example.test."},
			wantFound: true,
		},
		{
			name:      "IPv6",
			address:   "2001:db8::10",
			wantNames: []string{"v6.example.test."},
			wantFound: true,
		},
		{name: "missing", address: "192.0.2.99"},
		{name: "invalid", address: "10.2.0.192.in-addr.arpa"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			names, err := resolver.LookupAddr(context.Background(), tt.address)
			if tt.wantFound {
				if err != nil || !slices.Equal(names, tt.wantNames) {
					t.Fatalf("LookupAddr(%q) = %v, %v; want %v, nil", tt.address, names, err, tt.wantNames)
				}
				return
			}
			if err == nil || len(names) != 0 {
				t.Fatalf("LookupAddr(%q) = %v, %v; want an error", tt.address, names, err)
			}
		})
	}

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := resolver.LookupAddr(canceled, "192.0.2.10"); !errors.Is(err, context.Canceled) {
		t.Fatalf("LookupAddr canceled error = %v, want context.Canceled", err)
	}
}

func TestMagicDNSResolverProviderPTRQueries(t *testing.T) {
	resolver := &magicDNSResolver{}
	resolver.SetView(wgotailscale.DNSView{Records: []wgotailscale.DNSRecord{
		{Name: "zeta-v4.example.test", Type: "A", Value: "192.0.2.10"},
		{Name: "alpha-v4.example.test", Type: "A", Value: "192.0.2.10"},
		{Name: "v6.example.test", Type: "AAAA", Value: "2001:db8::10"},
	}})
	provider := gdns.NewResolverProvider(resolver, 90*time.Second, nil)
	t.Cleanup(func() {
		if err := provider.Close(); err != nil {
			t.Errorf("close resolver provider: %v", err)
		}
	})

	tests := []struct {
		name      string
		question  string
		wantCode  uint8
		wantNames []string
	}{
		{
			name:      "IPv4",
			question:  "10.2.0.192.in-addr.arpa.",
			wantCode:  gdns.RCodeSuccess,
			wantNames: []string{"alpha-v4.example.test.", "zeta-v4.example.test."},
		},
		{
			name: "IPv6",
			question: "0.1.0.0.0.0.0.0.0.0.0.0.0.0.0.0." +
				"0.0.0.0.0.0.0.0.8.b.d.0.1.0.0.2.ip6.arpa.",
			wantCode:  gdns.RCodeSuccess,
			wantNames: []string{"v6.example.test."},
		},
		{
			name:     "missing IPv4",
			question: "99.2.0.192.in-addr.arpa.",
			wantCode: gdns.RCodeNameError,
		},
		{
			name:     "malformed IPv4",
			question: "10.2.192.in-addr.arpa.",
			wantCode: gdns.RCodeFormatError,
		},
		{
			name:     "malformed IPv6",
			question: "1.0.ip6.arpa.",
			wantCode: gdns.RCodeFormatError,
		},
		{
			name:     "not a reverse name",
			question: "host.example.test.",
			wantCode: gdns.RCodeFormatError,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			request := &gdns.Message{
				ID: gdns.NextID(),
				Questions: []gdns.Question{{
					Name:  tt.question,
					Type:  gdns.TypePTR,
					Class: gdns.ClassIN,
				}},
			}
			response, err := gdns.Query(context.Background(), provider, request)
			if err != nil {
				t.Fatal(err)
			}
			if response.RCode != tt.wantCode {
				t.Fatalf("PTR response code = %d, want %d", response.RCode, tt.wantCode)
			}
			gotNames := make([]string, 0, len(response.Answers))
			for i, answer := range response.Answers {
				if answer.Name != tt.question || answer.Type != gdns.TypePTR ||
					answer.Class != gdns.ClassIN || answer.TTL != 90 {
					t.Errorf("PTR answer %d metadata = %#v", i, answer)
				}
				gotNames = append(gotNames, string(answer.Data))
			}
			if !slices.Equal(gotNames, tt.wantNames) {
				t.Errorf("PTR answer names = %q, want %q", gotNames, tt.wantNames)
			}
		})
	}
}

func TestMagicDNSResolverCopiesPublishedView(t *testing.T) {
	records := []wgotailscale.DNSRecord{{Name: "copy.test", Type: "A", Value: "100.64.0.8"}}
	resolver := &magicDNSResolver{}
	resolver.SetView(wgotailscale.DNSView{Records: records})
	records[0].Value = "100.64.0.9"
	addresses, err := resolver.LookupNetIP(context.Background(), "ip4", "copy.test")
	if err != nil || len(addresses) != 1 || addresses[0] != netip.MustParseAddr("100.64.0.8") {
		t.Fatalf("copied resolver view = %v, %v", addresses, err)
	}
}
