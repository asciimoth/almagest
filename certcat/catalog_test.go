package certcat

import "testing"

func TestDNSNameMatchesSubtree(t *testing.T) {
	tests := []struct {
		host    string
		subtree string
		want    bool
	}{
		{host: "example.com", subtree: "example.com", want: true},
		{host: "www.example.com", subtree: "example.com", want: true},
		{host: "example.com", subtree: ".example.com", want: false},
		{host: "www.example.com", subtree: ".example.com", want: true},
		{host: "badexample.com", subtree: "example.com", want: false},
		{host: "WWW.EXAMPLE.COM.", subtree: "example.com", want: true},
	}
	for _, tt := range tests {
		got := DNSNameMatchesSubtree(tt.host, tt.subtree)
		if got != tt.want {
			t.Fatalf("DNSNameMatchesSubtree(%q, %q) = %v, want %v", tt.host, tt.subtree, got, tt.want)
		}
	}
}

func TestIndexLookupNormalizesFingerprints(t *testing.T) {
	fingerprint, ok := ParseFingerprint("AABBCCDDEEFF00112233445566778899AABBCCDDEEFF00112233445566778899")
	if !ok {
		t.Fatal("test fingerprint did not parse")
	}
	records := []Record{
		{
			Fingerprint: fingerprint,
			MatchKind:   MatchKindCertificateSHA256,
			Action:      ActionDeny,
		},
	}
	idx := NewIndex(records)
	got := idx.Lookup(MatchKindCertificateSHA256, "aa:bb:cc:dd:ee:ff:00:11:22:33:44:55:66:77:88:99:aa:bb:cc:dd:ee:ff:00:11:22:33:44:55:66:77:88:99")
	if len(got) != 1 || got[0].Fingerprint != fingerprint {
		t.Fatalf("lookup returned %#v", got)
	}
}
