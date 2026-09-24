package dnsnodes

import (
	"testing"

	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/formular"
	"github.com/asciimoth/gonnect/dns"
)

func TestDNSServerConfigValidationBranches(t *testing.T) {
	config := defaultDNSServerConfig()
	config.network = "udp6"
	config.host = ""
	config.port = 0
	config.normalize()
	if config.host != "::1" || config.port != DefaultDNSServerPort {
		t.Fatalf("normalized config = %#v", config)
	}
	invalid := []dnsServerConfig{
		{network: "udp", host: "host", port: 53, requestTimeoutMS: -1},
		{network: "invalid", host: "host", port: 53},
		{network: "udp", host: "", port: 53},
		{network: "udp", host: "bad host", port: 53},
		{network: "udp", host: "host", port: 0},
		{network: "udp", host: "host", port: 65536},
	}
	for _, value := range invalid {
		if err := value.valid(); err == nil {
			t.Fatalf("valid accepted %#v", value)
		}
	}
	if err := (dnsServerConfig{network: "udp", host: "host", port: 53}).valid(); err != nil {
		t.Fatal(err)
	}

	tests := []formular.FieldValidateMessage{
		{Field: formular.FieldRef{FieldID: DNSServerNetworkField}, Value: "invalid"},
		{Field: formular.FieldRef{FieldID: DNSServerHostField}, Value: " "},
		{Field: formular.FieldRef{FieldID: DNSServerHostField}, Value: "bad host"},
		{Field: formular.FieldRef{FieldID: DNSServerPortField}, Value: 0},
		{Field: formular.FieldRef{FieldID: DNSServerRequestTimeoutMSField}, Value: -1},
	}
	for _, msg := range tests {
		if err := validateDNSServerConfigField(msg); err == nil {
			t.Fatalf("field %s accepted %#v", msg.Field.FieldID, msg.Value)
		}
	}
	if err := validateDNSServerConfigField(formular.FieldValidateMessage{Field: formular.FieldRef{FieldID: "unknown"}}); err != nil {
		t.Fatal(err)
	}
	loaded := defaultDNSServerConfig()
	if err := loaded.read(configer.NewMemory(map[string]any{"network": "invalid", "host": "host", "port": 53})); err == nil || loaded.network != "invalid" {
		t.Fatalf("invalid read = %#v, %v", loaded, err)
	}
}

func TestDNSClientServerParsingAndValidationBranches(t *testing.T) {
	want := []string{"udp://one:53", "tcp://two:53", "dot://three:853"}
	got := splitDNSClientServersText("one:53; tcp://two:53\n dot://three:853")
	if len(got) != len(want) {
		t.Fatalf("servers = %#v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("servers = %#v", got)
		}
	}
	for _, server := range []string{"", "http://host", "udp://user@host", "udp://host?q=x", "udp://host#fragment", "udp://", "udp://bad host"} {
		if err := validateDNSClientServerURL(server); err == nil {
			t.Fatalf("accepted server %q", server)
		}
	}
	for _, server := range []string{"udp://host:53", "tcp://host:53", "dot://host:853"} {
		if err := validateDNSClientServerURL(server); err != nil {
			t.Fatalf("rejected server %q: %v", server, err)
		}
	}
	if err := validateDNSClientServerField(formular.FieldValidateMessage{Field: formular.FieldRef{FieldID: DNSClientServerField}, Value: "http://host"}); err == nil {
		t.Fatal("field accepted unsupported scheme")
	}
	if err := validateDNSClientServerField(formular.FieldValidateMessage{Field: formular.FieldRef{FieldID: "unknown"}, Value: "bad"}); err != nil {
		t.Fatal(err)
	}
}

func TestDNSQueryFormattingNameBranches(t *testing.T) {
	if NormalizeQueryName(" ") != DefaultDNSQueryName || NormalizeQueryName("example.test") != "example.test." {
		t.Fatal("NormalizeQueryName returned an unexpected value")
	}
	if NormalizeQueryType(" ") != DefaultDNSQueryType || NormalizeQueryType("aaaa") != "AAAA" {
		t.Fatal("NormalizeQueryType returned an unexpected value")
	}
	for typ, want := range map[uint16]string{
		dns.TypeA: "A", dns.TypeAAAA: "AAAA", dns.TypeCNAME: "CNAME", dns.TypeNS: "NS",
		dns.TypePTR: "PTR", dns.TypeMX: "MX", dns.TypeTXT: "TXT", dns.TypeSRV: "SRV", 65000: "65000",
	} {
		if got := TypeName(typ); got != want {
			t.Fatalf("TypeName(%d) = %q, want %q", typ, got, want)
		}
	}
	for code, want := range map[uint8]string{
		dns.RCodeSuccess: "NOERROR", dns.RCodeFormatError: "FORMERR", dns.RCodeServerFailure: "SERVFAIL",
		dns.RCodeNameError: "NXDOMAIN", dns.RCodeNotImplemented: "NOTIMP", 15: "RCODE 15",
	} {
		if got := RCodeName(code); got != want {
			t.Fatalf("RCodeName(%d) = %q, want %q", code, got, want)
		}
	}
}
