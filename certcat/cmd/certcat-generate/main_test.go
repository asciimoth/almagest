package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/asciimoth/almagest/certcat/internal/catalog"
)

const testFingerprint = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func TestRenderCompleteRecords(t *testing.T) {
	scope := catalog.Scope{
		Basis:                     "service_context",
		PermittedDNSSubtrees:      []string{"example.test"},
		PermittedIPCIDRs:          []string{"192.0.2.0/24"},
		RequiresExplicitAllowlist: true,
		RejectIPTargets:           true,
	}
	conditions := map[string]string{
		"chrome_sct_timestamp_after": "2024-01-01T00:00:00Z",
		"chrome_enforcement_started": "2025-01-01T00:00:00Z",
		"chrome_first_major_version": "120",
	}
	records := []catalog.Record{
		{
			ID: "cert\nrecord", Name: "Certificate\rRecord", Kind: "hazard_certificate",
			CertificateSHA256: testFingerprint, HazardClass: "government_ca", Action: "deny",
			Severity: "high", Recommendation: "deny_exact_match", Scope: scope, Conditions: conditions,
			Sources: []catalog.Source{{ID: "source", URL: "https://example.test/list", RetrievedAt: "now", Note: "note"}},
		},
		{
			ID: "spki-record", Kind: "service_private_anchor", SPKISHA256: testFingerprint,
			Action: "offer_scoped_service_anchor", Severity: "info", Scope: scope, Conditions: conditions,
		},
	}

	src, err := render("generated", records)
	if err != nil {
		t.Fatalf("render() error = %v", err)
	}
	text := string(src)
	for _, want := range []string{
		"package generated", "var recordScopes", "var recordPlatformCutoffs",
		"MatchKindCertificateSHA256", "MatchKindSPKISHA256", "HazardClassGovernmentCA",
		"RequiresExplicitAllowlist: true", "FirstMajorVersion: 120", "cert record: Certificate Record",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("rendered source does not contain %q\n%s", want, text)
		}
	}
	if strings.Count(text, "var recordScopes") != 1 || strings.Count(text, "var recordPlatformCutoffs") != 1 {
		t.Fatalf("shared tables were not deduplicated:\n%s", text)
	}
}

func TestRenderValidationErrors(t *testing.T) {
	valid := catalog.Record{ID: "record", Kind: "hazard_certificate", CertificateSHA256: testFingerprint, Action: "deny", Severity: "high"}
	tests := []struct {
		name   string
		mutate func(*catalog.Record)
		want   string
	}{
		{"missing match", func(r *catalog.Record) { r.CertificateSHA256 = "" }, "certificate_sha256 or spki_sha256 is required"},
		{"unknown kind", func(r *catalog.Record) { r.Kind = "unknown" }, "unknown kind"},
		{"unknown action", func(r *catalog.Record) { r.Action = "unknown" }, "unknown action"},
		{"unknown severity", func(r *catalog.Record) { r.Severity = "unknown" }, "unknown severity"},
		{"unknown hazard", func(r *catalog.Record) { r.HazardClass = "unknown" }, "unknown hazard_class"},
		{"unknown recommendation", func(r *catalog.Record) { r.Recommendation = "unknown" }, "unknown recommendation"},
		{"bad fingerprint", func(r *catalog.Record) { r.CertificateSHA256 = "xyz" }, "decode fingerprint"},
		{"short fingerprint", func(r *catalog.Record) { r.CertificateSHA256 = "00" }, "32 bytes"},
		{"unknown scope basis", func(r *catalog.Record) { r.Scope.Basis = "unknown" }, "unknown scope.basis"},
		{"partial conditions", func(r *catalog.Record) { r.Conditions = map[string]string{"chrome_first_major_version": "1"} }, "unsupported conditions"},
		{"bad version", func(r *catalog.Record) {
			r.Conditions = map[string]string{"chrome_sct_timestamp_after": "a", "chrome_enforcement_started": "b", "chrome_first_major_version": "bad"}
		}, "parse chrome_first_major_version"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			record := valid
			tt.mutate(&record)
			_, err := render("generated", []catalog.Record{record})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("render() error = %v, want text %q", err, tt.want)
			}
		})
	}
}

func TestSmallRenderHelpers(t *testing.T) {
	if !scopeEmpty(catalog.Scope{}) || scopeEmpty(catalog.Scope{RejectIPTargets: true}) {
		t.Fatal("scopeEmpty returned an incorrect result")
	}
	if got := sanitizeComment("  first\r\nsecond  "); got != "first  second" {
		t.Fatalf("sanitizeComment() = %q", got)
	}
	if _, ok, err := platformCutoffFromConditions(nil); err != nil || ok {
		t.Fatalf("empty conditions = ok %t, error %v", ok, err)
	}
	var b bytes.Buffer
	writeComment(&b, catalog.Record{Name: "name"})
	writeComment(&b, catalog.Record{ID: "id"})
	writeStringField(&b, "", "Empty", "")
	writeStringSliceField(&b, "", "Empty", nil)
	writeScopeTable(&b, nil) //nolint:errcheck
	writePlatformCutoffTable(&b, nil)
	if got := b.String(); !strings.Contains(got, "// name") || !strings.Contains(got, "// id") {
		t.Fatalf("helper output = %q", got)
	}
}
