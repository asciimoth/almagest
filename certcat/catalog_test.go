package certcat

import (
	"crypto/x509"
	"strings"
	"testing"
)

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

func TestEnumStrings(t *testing.T) {
	tests := []struct {
		got  string
		want string
	}{
		{RecordKindHazardCertificate.String(), "hazard_certificate"},
		{RecordKindNarrowScopeCA.String(), "narrow_scope_ca"},
		{RecordKindServicePrivateAnchor.String(), "service_private_anchor"},
		{RecordKindThreatFeedLeaf.String(), "threat_feed_leaf"},
		{RecordKindUnknown.String(), ""},
		{MatchKindCertificateSHA256.String(), "certificate_sha256"},
		{MatchKindSPKISHA256.String(), "spki_sha256"},
		{MatchKindUnknown.String(), ""},
		{ActionDeny.String(), "deny"},
		{ActionDenyEvenIfLocal.String(), "deny_even_if_locally_installed"},
		{ActionWarn.String(), "warn"},
		{ActionNarrowScope.String(), "narrow_scope"},
		{ActionScopedServiceAnchor.String(), "offer_scoped_service_anchor"},
		{ActionFollowPlatformOrDeny.String(), "follow_platform_policy_or_deny"},
		{ActionEnforcePlatformCutoff.String(), "enforce_platform_cutoff"},
		{ActionUnknown.String(), ""},
		{SeverityInfo.String(), "info"}, {SeverityWarning.String(), "warning"},
		{SeverityHigh.String(), "high"}, {SeverityCritical.String(), "critical"}, {SeverityUnknown.String(), ""},
		{HazardClassChromiumCertificateBlocklist.String(), "chromium_certificate_blocklist"},
		{HazardClassChromiumSPKIBlocklist.String(), "chromium_spki_blocklist"},
		{HazardClassGovernmentCA.String(), "government_ca"},
		{HazardClassInterceptionCA.String(), "interception_ca"},
		{HazardClassMalwareOrCommandAndControlLeaf.String(), "malware_or_command_and_control_leaf"},
		{HazardClassPrivateKeyDisclosure.String(), "private_key_disclosure"},
		{HazardClassStateInterceptionCA.String(), "state_interception_ca"},
		{HazardClassTimeScopedRootProgramDistrust.String(), "time_scoped_root_program_distrust"},
		{HazardClassUnknown.String(), ""},
		{RecommendationApplyTargetPlatformDistrustCutoff.String(), "apply_target_platform_distrust_cutoff"},
		{RecommendationDenyExactCertificateEvenIfLocal.String(), "deny_exact_certificate_even_if_user_or_os_installed"},
		{RecommendationDenyExactLeafCertificate.String(), "deny_exact_leaf_certificate"},
		{RecommendationDenyExactMatch.String(), "deny_exact_match"},
		{RecommendationDenyExactSPKI.String(), "deny_exact_spki"},
		{RecommendationOnlyUseForExplicitCloudflareOriginHosts.String(), "only_use_for_explicit_cloudflare_origin_hosts"},
		{RecommendationOnlyUseForRussianGovernmentResources.String(), "only_use_for_russian_government_resources"},
		{RecommendationTrustOnlyForDeclaredScope.String(), "trust_only_for_declared_scope"},
		{RecommendationUnknown.String(), ""},
		{ScopeBasisServiceContext.String(), "service_context"},
		{ScopeBasisX509NameConstraints.String(), "x509_name_constraints"},
		{ScopeBasisUnknown.String(), ""},
	}
	for _, tt := range tests {
		if tt.got != tt.want {
			t.Errorf("String() = %q, want %q", tt.got, tt.want)
		}
	}
}

func TestIndexAllLookupPaths(t *testing.T) {
	certFP, _ := ParseFingerprint(strings.Repeat("A", 64))
	spkiFP, _ := ParseFingerprint(strings.Repeat("B", 64))
	records := []Record{
		{Fingerprint: certFP, MatchKind: MatchKindCertificateSHA256, Action: ActionDeny},
		{Fingerprint: spkiFP, MatchKind: MatchKindSPKISHA256, Action: ActionWarn},
		{Fingerprint: Fingerprint{}, MatchKind: MatchKindCertificateSHA256},
		{Fingerprint: certFP, MatchKind: MatchKindUnknown},
	}
	index := NewIndex(records)
	if got := index.Lookup(MatchKindSPKISHA256, spkiFP.String()); len(got) != 1 || got[0].Action != ActionWarn {
		t.Fatalf("SPKI lookup = %#v", got)
	}
	if index.Lookup(MatchKindUnknown, certFP.String()) != nil || index.Lookup(MatchKindCertificateSHA256, "bad") != nil {
		t.Fatal("invalid lookup returned matches")
	}
	var nilIndex *Index
	if nilIndex.LookupFingerprint(MatchKindCertificateSHA256, certFP) != nil {
		t.Fatal("nil index returned matches")
	}

	cert := &x509.Certificate{Raw: []byte("certificate"), RawSubjectPublicKeyInfo: []byte("spki")}
	certRecord := Record{Fingerprint: certificateSHA256Fingerprint(cert), MatchKind: MatchKindCertificateSHA256, Action: ActionDeny}
	spkiRecord := Record{Fingerprint: spkiSHA256Fingerprint(cert), MatchKind: MatchKindSPKISHA256, Action: ActionWarn}
	matching := NewIndex([]Record{certRecord, certRecord, spkiRecord})
	if got := matching.MatchCertificate(cert); len(got) != 2 {
		t.Fatalf("MatchCertificate = %#v, want two deduplicated matches", got)
	}
	if got := matching.MatchChain([]*x509.Certificate{cert, cert, nil}); len(got) != 2 {
		t.Fatalf("MatchChain = %#v, want two deduplicated matches", got)
	}
	if matching.MatchCertificate(nil) != nil || CertificateSHA256(nil) != "" || SPKISHA256(nil) != "" {
		t.Fatal("nil certificate helpers returned data")
	}
	if CertificateSHA256(cert) == "" || SPKISHA256(cert) == "" {
		t.Fatal("certificate fingerprints are empty")
	}
}

func TestFingerprintParsingRejectsWrongLengths(t *testing.T) {
	if _, ok := ParseFingerprint(strings.Repeat("A", 63)); ok {
		t.Fatal("short fingerprint parsed")
	}
	if _, ok := ParseFingerprint(strings.Repeat("A", 65)); ok {
		t.Fatal("long fingerprint parsed")
	}
	if got := (Fingerprint{}).String(); got != strings.Repeat("0", 64) || !(Fingerprint{}).IsZero() {
		t.Fatalf("zero fingerprint = %q", got)
	}
	if DNSNameMatchesSubtree("", "example.com") || DNSNameMatchesSubtree("example.com", "") {
		t.Fatal("empty DNS name matched")
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
