package catalog

import "testing"

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
