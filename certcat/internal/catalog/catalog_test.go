package catalog

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMergeDocumentsDeduplicatesAndKeepsFirstPolicy(t *testing.T) {
	manual := &Document{
		FormatVersion: FormatVersion,
		Entries: []Record{
			{
				ID:                "manual",
				Kind:              "hazard_certificate",
				CertificateSHA256: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
				Action:            "deny_even_if_locally_installed",
				Severity:          "critical",
				Recommendation:    "manual_policy",
				Sources: []Source{
					{ID: "manual-source", URL: "https://example.invalid/manual"},
				},
			},
		},
	}
	machine := &Document{
		FormatVersion: FormatVersion,
		Entries: []Record{
			{
				ID:                "machine",
				Kind:              "hazard_certificate",
				CertificateSHA256: "aa:aa:aa:aa:aa:aa:aa:aa:aa:aa:aa:aa:aa:aa:aa:aa:aa:aa:aa:aa:aa:aa:aa:aa:aa:aa:aa:aa:aa:aa:aa:aa",
				Action:            "deny",
				Severity:          "high",
				Sources: []Source{
					{ID: "machine-source", URL: "https://example.invalid/machine", RetrievedAt: "2026-08-05T00:00:00Z"},
				},
			},
		},
	}
	records, err := MergeDocuments(manual, machine)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 {
		t.Fatalf("len(records) = %d, want 1", len(records))
	}
	if records[0].Action != "deny_even_if_locally_installed" {
		t.Fatalf("action = %q", records[0].Action)
	}
	if records[0].Recommendation != "manual_policy" {
		t.Fatalf("recommendation = %q", records[0].Recommendation)
	}
	if len(records[0].Sources) != 2 {
		t.Fatalf("sources = %#v", records[0].Sources)
	}
}

func TestMergeDocumentsKeepsFirstDocumentPolicy(t *testing.T) {
	manual := &Document{
		FormatVersion: FormatVersion,
		Entries: []Record{
			{
				ID:                "manual",
				Kind:              "hazard_certificate",
				CertificateSHA256: "CCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCC",
				Action:            "warn",
				Severity:          "warning",
				Recommendation:    "manual_policy",
			},
		},
	}
	machine := &Document{
		FormatVersion: FormatVersion,
		Entries: []Record{
			{
				ID:                "machine",
				Kind:              "hazard_certificate",
				CertificateSHA256: "CCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCC",
				Action:            "deny",
				Severity:          "critical",
			},
		},
	}
	records, err := MergeDocuments(manual, machine)
	if err != nil {
		t.Fatal(err)
	}
	if records[0].Action != "warn" || records[0].Severity != "warning" {
		t.Fatalf("manual policy was overridden: %#v", records[0])
	}
}

func TestMachineSourcesValidRequiresRetrievedAt(t *testing.T) {
	doc := Document{
		FormatVersion: FormatVersion,
		Entries: []Record{
			{
				ID:                "missing-retrieved",
				Kind:              "hazard_certificate",
				CertificateSHA256: "BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB",
				Action:            "deny",
				Severity:          "high",
				Sources: []Source{
					{ID: "source", URL: "https://example.invalid"},
				},
			},
		},
	}
	if err := MachineSourcesValid(doc); err == nil {
		t.Fatal("MachineSourcesValid returned nil")
	}
}

func TestDocumentWriteLoadNormalizesAndSorts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "catalog.json")
	doc := Document{Entries: []Record{
		{
			ID: " second ", Kind: " hazard_certificate ", Action: " deny ", Severity: " high ",
			CertificateSHA256: strings.Repeat("b:", 63) + "b",
			Scope:             Scope{PermittedDNSSubtrees: []string{" B.example ", "a.example", "b.example"}, PermittedIPCIDRs: []string{" 192.0.2.0/24 ", "192.0.2.0/24"}},
			Sources:           []Source{{ID: " source ", URL: " https://example.test ", Note: " note "}, {ID: "source", URL: "https://example.test"}, {}},
		},
		{ID: "first", Kind: "hazard_certificate", Action: "deny", Severity: "high", CertificateSHA256: strings.Repeat("A", 64)},
	}}
	if err := WriteDocument(path, doc); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadDocument(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.FormatVersion != FormatVersion || len(loaded.Entries) != 2 || loaded.Entries[0].ID != "first" || loaded.Entries[1].ID != "second" {
		t.Fatalf("loaded document = %#v", loaded)
	}
	second := loaded.Entries[1]
	if second.CertificateSHA256 != strings.Repeat("B", 64) || len(second.Scope.PermittedDNSSubtrees) != 2 || len(second.Sources) != 1 {
		t.Fatalf("normalized record = %#v", second)
	}
}

func TestDocumentIOAndValidationErrors(t *testing.T) {
	tests := []struct {
		name   string
		record Record
		want   string
	}{
		{"id", Record{}, "id is required"},
		{"kind", Record{ID: "id"}, "kind is required"},
		{"action", Record{ID: "id", Kind: "kind"}, "action is required"},
		{"severity", Record{ID: "id", Kind: "kind", Action: "action"}, "severity is required"},
		{"fingerprints", Record{ID: "id", Kind: "kind", Action: "action", Severity: "severity"}, "at least one fingerprint"},
		{"certificate length", Record{ID: "id", Kind: "kind", Action: "action", Severity: "severity", CertificateSHA256: "AA"}, "certificate_sha256"},
		{"spki length", Record{ID: "id", Kind: "kind", Action: "action", Severity: "severity", SPKISHA256: "AA"}, "spki_sha256"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := ValidateRecord(tt.record); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("ValidateRecord() error = %v, want %q", err, tt.want)
			}
		})
	}

	dir := t.TempDir()
	badJSON := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(badJSON, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadDocument(badJSON); err == nil || !strings.Contains(err.Error(), "decode") {
		t.Fatalf("bad JSON error = %v", err)
	}
	wrongVersion := filepath.Join(dir, "version.json")
	if err := os.WriteFile(wrongVersion, []byte(`{"format_version":2,"entries":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadDocument(wrongVersion); err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("wrong version error = %v", err)
	}
	if _, err := LoadDocument(filepath.Join(dir, "missing")); err == nil {
		t.Fatal("LoadDocument missing file succeeded")
	}
	if err := WriteDocument(filepath.Join(dir, "missing", "doc.json"), Document{}); err == nil {
		t.Fatal("WriteDocument missing directory succeeded")
	}
}

func TestCatalogHelperBranches(t *testing.T) {
	fp := strings.Repeat("A", 64)
	a := Record{
		ID: "a", Kind: "service_private_anchor", Name: "first", CertificateSHA256: fp,
		Action: "warn", Severity: "warning", Scope: Scope{Context: "first", PermittedDNSSubtrees: []string{"a"}},
		Conditions: map[string]string{"shared": "first"},
		Sources:    []Source{{ID: "same", URL: "url"}},
	}
	b := Record{
		ID: "b", Kind: "hazard_certificate", Name: "second", SPKISHA256: strings.Repeat("B", 64),
		Action: "deny", Severity: "high", Scope: Scope{Context: "second", PermittedDNSSubtrees: []string{"b"}, RejectIPTargets: true},
		Conditions: map[string]string{"shared": "second", "new": "value"},
		Sources:    []Source{{ID: "same", URL: "url", RetrievedAt: "now", Note: "note"}, {ID: "new", URL: "new-url"}},
	}
	merged := mergeRecords(a, b)
	if merged.Kind != "hazard_certificate" || merged.Name != "first" || merged.Conditions["shared"] != "first" || merged.Conditions["new"] != "value" {
		t.Fatalf("mergeRecords = %#v", merged)
	}
	if len(merged.Sources) != 2 || merged.Sources[0].RetrievedAt != "now" || !merged.Scope.RejectIPTargets || len(merged.Scope.PermittedDNSSubtrees) != 2 {
		t.Fatalf("merged metadata = %#v", merged)
	}
	if got := mergeMap(nil, nil); got != nil {
		t.Fatalf("mergeMap(nil, nil) = %#v", got)
	}
	if firstNonEmpty("a", "b") != "a" || firstNonEmpty("", "b") != "b" || kindRank("unknown") != 0 {
		t.Fatal("scalar helpers returned incorrect results")
	}
	if got := sortKey(Record{ID: "id"}); got != "id" {
		t.Fatalf("sortKey without match = %q", got)
	}
}
