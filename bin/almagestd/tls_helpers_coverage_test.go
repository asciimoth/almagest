package main

import (
	"strings"
	"testing"

	"github.com/asciimoth/almagest/bin/api"
	"github.com/asciimoth/almagest/certcat"
	"github.com/asciimoth/gonnect/tlspolicy"
)

func TestTLSAPIConversionHelpers(t *testing.T) {
	if match, err := tlsAuthorityMatchFromAPI(" certificate "); err != nil || match != tlspolicy.MatchCertificate {
		t.Fatalf("certificate match = %v, %v", match, err)
	}
	if match, err := tlsAuthorityMatchFromAPI("SPKI"); err != nil || match != tlspolicy.MatchSPKI {
		t.Fatalf("SPKI match = %v, %v", match, err)
	}
	if _, err := tlsAuthorityMatchFromAPI("unknown"); err == nil {
		t.Fatal("unknown authority match succeeded")
	}
	fingerprint := strings.Repeat("01", 32)
	for _, kind := range []string{"certificate", "spki"} {
		if _, err := tlsPinFromAPI(api.TLSPin{Kind: kind, Fingerprint: fingerprint}); err != nil {
			t.Fatalf("tlsPinFromAPI(%q): %v", kind, err)
		}
	}
	if _, err := tlsPinFromAPI(api.TLSPin{Kind: "unknown", Fingerprint: fingerprint}); err == nil {
		t.Fatal("unknown pin kind succeeded")
	}
	if _, err := tlsPinFromAPI(api.TLSPin{Kind: "spki", Fingerprint: "bad"}); err == nil {
		t.Fatal("bad pin fingerprint succeeded")
	}
	for _, value := range []string{"192.0.2.0/24", "2001:db8::1", "192.0.2.1"} {
		if _, err := parseTLSPrefix(value); err != nil {
			t.Fatalf("parseTLSPrefix(%q): %v", value, err)
		}
	}
	if _, err := parseTLSPrefix("bad"); err == nil {
		t.Fatal("bad TLS prefix succeeded")
	}
}

func TestTLSNormalizationHelpers(t *testing.T) {
	if normalizeTLSAuthorityMatchText(" ") != "certificate" || normalizeTLSAuthorityMatchText(" SpKi ") != "spki" || normalizeTLSAuthorityMatchText(" OTHER ") != "other" {
		t.Fatal("normalizeTLSAuthorityMatchText returned an incorrect value")
	}
	if normalizeTLSPinKindText(" Certificate ") != "certificate" || normalizeTLSPinKindText(" SPKI ") != "spki" || normalizeTLSPinKindText(" OTHER ") != "other" {
		t.Fatal("normalizeTLSPinKindText returned an incorrect value")
	}
	if normalizeTLSFingerprintText(" AA:bb ") != "aabb" {
		t.Fatal("normalizeTLSFingerprintText returned an incorrect value")
	}
	if compareBool(false, false) != 0 || compareBool(false, true) >= 0 || compareBool(true, false) <= 0 {
		t.Fatal("compareBool returned an incorrect value")
	}

	spec := api.TLSPolicySpec{
		TrustAnchors: []api.TLSTrustAnchor{{Cert: "old", Scope: api.TLSScope{Any: true}}, {Cert: "keep", Scope: api.TLSScope{Any: true}}},
		Constraints:  []api.TLSAuthorityConstraint{{Cert: "old", Match: "spki", Scope: api.TLSScope{Any: true}}},
	}
	renamed, changed := renameTLSCertInPolicy(spec, "old", "new")
	if !changed || renamed.TrustAnchors[1].Cert != "new" && renamed.TrustAnchors[0].Cert != "new" {
		t.Fatalf("renameTLSCertInPolicy = %#v, %t", renamed, changed)
	}
	if _, changed := renameTLSCertInPolicy(spec, "missing", "new"); changed {
		t.Fatal("renameTLSCertInPolicy changed a missing certificate")
	}
	spec = api.TLSPolicySpec{
		TrustAnchors: []api.TLSTrustAnchor{{Cert: "old", Scope: api.TLSScope{Any: true}}, {Cert: "keep", Scope: api.TLSScope{Any: true}}},
		Constraints:  []api.TLSAuthorityConstraint{{Cert: "old", Match: "spki", Scope: api.TLSScope{Any: true}}},
	}
	deleted, changed := deleteTLSCertFromPolicy(spec, "old")
	if !changed || len(deleted.TrustAnchors) != 1 || len(deleted.Constraints) != 0 {
		t.Fatalf("deleteTLSCertFromPolicy = %#v, %t", deleted, changed)
	}
	if _, changed := deleteTLSCertFromPolicy(spec, "missing"); changed {
		t.Fatal("deleteTLSCertFromPolicy changed a missing certificate")
	}
}

func TestCertcatScopePolicyBranches(t *testing.T) {
	scope := &certcat.Scope{PermittedDNSSubtrees: []string{".example.test", " "}, PermittedIPCIDRs: []string{"192.0.2.0/24"}}
	converted, ok := tlsScopeFromCertcatScope(scope)
	if !ok || len(converted.DNS) != 1 || !converted.DNS[0].IncludeSubdomains || len(converted.IPPrefixes) != 1 {
		t.Fatalf("tlsScopeFromCertcatScope = %#v, %t", converted, ok)
	}
	if _, ok := tlsScopeFromCertcatScope(nil); ok {
		t.Fatal("nil certcat scope succeeded")
	}
	if _, ok := tlsScopeFromCertcatScope(&certcat.Scope{}); ok {
		t.Fatal("empty certcat scope succeeded")
	}

	records := []certcat.Record{{Action: certcat.ActionNarrowScope, Scope: scope}}
	if got, ok := defaultTLSScopeForCertcat(records); !ok || got.Any {
		t.Fatalf("narrow default scope = %#v, %t", got, ok)
	}
	for _, action := range []certcat.Action{certcat.ActionDeny, certcat.ActionWarn, certcat.ActionFollowPlatformOrDeny} {
		if _, ok := defaultTLSScopeForCertcat([]certcat.Record{{Action: action}}); ok {
			t.Fatalf("hazard action %v produced a default scope", action)
		}
	}
	if !certcatSeverityWarns(certcat.SeverityWarning) || !certcatSeverityWarns(certcat.SeverityHigh) || !certcatSeverityWarns(certcat.SeverityCritical) || certcatSeverityWarns(certcat.SeverityInfo) {
		t.Fatal("certcatSeverityWarns returned an incorrect result")
	}
}

func TestCompileTLSPolicySpecBranches(t *testing.T) {
	authority, certificate, err := parseTLSAuthorityPEM(testTLSCAPEM(t, "compile policy"))
	if err != nil {
		t.Fatal(err)
	}
	certs := map[string]tlsEffectiveCert{"ca": {Name: "ca", Authority: authority, Certificate: certificate}}
	any := api.TLSScope{Any: true}
	if _, err := compileTLSPolicySpec(api.TLSPolicySpec{
		TrustAnchors: []api.TLSTrustAnchor{{Cert: "missing", Scope: any}},
	}, certs); err == nil {
		t.Fatal("unknown trust anchor was accepted")
	}
	if _, err := compileTLSPolicySpec(api.TLSPolicySpec{
		Constraints: []api.TLSAuthorityConstraint{{Cert: "missing", Match: "certificate", Scope: any}},
	}, certs); err == nil {
		t.Fatal("unknown constraint certificate was accepted")
	}
	if _, err := compileTLSPolicySpec(api.TLSPolicySpec{
		Constraints: []api.TLSAuthorityConstraint{{Cert: "ca", Match: "bad", Scope: any}},
	}, certs); err == nil {
		t.Fatal("invalid constraint match was accepted")
	}
	if _, err := compileTLSPolicySpec(api.TLSPolicySpec{
		Pins: []api.TLSPin{{Kind: "bad", Fingerprint: strings.Repeat("01", 32), Scope: any}},
	}, certs); err == nil {
		t.Fatal("invalid pin kind was accepted")
	}
	policy, err := compileTLSPolicySpec(api.TLSPolicySpec{
		TrustAnchors: []api.TLSTrustAnchor{{Cert: "ca", Scope: any}},
		Constraints:  []api.TLSAuthorityConstraint{{Cert: "ca", Match: "spki", Scope: any}},
		Pins:         []api.TLSPin{{Kind: "certificate", Fingerprint: strings.Repeat("01", 32), Scope: any}},
	}, certs)
	if err != nil || policy == nil {
		t.Fatalf("valid compiled policy = %#v, %v", policy, err)
	}
}
