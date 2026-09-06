package tailscalenodes

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"testing"

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
