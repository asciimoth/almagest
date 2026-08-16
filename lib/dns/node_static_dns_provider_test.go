package dnsnodes

import (
	"context"
	"strings"
	"testing"

	"github.com/asciimoth/gonnect/dns"
)

func TestStaticDNSProviderRecordTypes(t *testing.T) {
	provider, err := NewStaticProvider(strings.Join([]string{
		"example.test A 192.0.2.10",
		"example.test AAA 2001:db8::10",
		"example.test TXT \"hello world\"",
		"example.test MX 10 mail.example.test",
		"_svc._tcp.example.test SRV 1 2 443 srv.example.test",
	}, "\n"), 30)
	if err != nil {
		t.Fatalf("NewStaticProvider: %v", err)
	}
	defer func() { _ = provider.Close() }()

	for _, tc := range []struct {
		name     string
		qname    string
		qtype    uint16
		wantPart string
	}{
		{name: "A", qname: "example.test.", qtype: dns.TypeA, wantPart: "192.0.2.10"},
		{name: "AAAA", qname: "example.test.", qtype: dns.TypeAAAA, wantPart: "2001:db8::10"},
		{name: "TXT", qname: "example.test.", qtype: dns.TypeTXT, wantPart: "hello world"},
		{name: "MX", qname: "example.test.", qtype: dns.TypeMX, wantPart: "10 mail.example.test."},
		{name: "SRV", qname: "_svc._tcp.example.test.", qtype: dns.TypeSRV, wantPart: "1 2 443 srv.example.test."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp, err := dns.Query(context.Background(), provider, dnsTestQuery(tc.qname, tc.qtype))
			if err != nil {
				t.Fatalf("Query: %v", err)
			}
			if resp.RCode != dns.RCodeSuccess || len(resp.Answers) == 0 {
				t.Fatalf("response = %#v, want successful answers", resp)
			}
			if got := FormatMessage(resp); !strings.Contains(got, tc.wantPart) {
				t.Fatalf("formatted response = %q, want to contain %q", got, tc.wantPart)
			}
		})
	}
}
