package certcat

import (
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"
)

func TestGeneratedRecordsHaveUniqueMatchKeys(t *testing.T) {
	if len(Records) == 0 {
		t.Fatal("Records is empty")
	}
	seen := map[string]int{}
	for n, rec := range Records {
		key := recordMatchKey(rec)
		if key == "" {
			t.Fatalf("record %d has no match key", n)
		}
		if prior, ok := seen[key]; ok {
			t.Fatalf("match key %s is repeated by records %d and %d", key, prior, n)
		}
		seen[key] = n
	}
}

func TestGeneratedRecordsWarnForRussianGovernmentCertificates(t *testing.T) {
	files := []string{
		"russian_Trusted_Root_CA.cer",
		"russian_Trusted_Sub_CA.cer",
		"russian_Trusted_Sub_CA_2024.cer",
	}
	index := NewIndex(Records)
	for _, name := range files {
		t.Run(name, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join("data", "hazard", name))
			if err != nil {
				t.Fatal(err)
			}
			block, _ := pem.Decode(data)
			if block == nil || block.Type != "CERTIFICATE" {
				t.Fatal("file does not contain a PEM certificate")
			}
			cert, err := x509.ParseCertificate(block.Bytes)
			if err != nil {
				t.Fatal(err)
			}
			matches := index.MatchCertificate(cert)
			if len(matches) != 1 {
				t.Fatalf("matches = %#v, want one match", matches)
			}
			match := matches[0]
			if match.HazardClass != HazardClassGovernmentCA ||
				match.Action != ActionWarn ||
				match.Severity != SeverityWarning ||
				match.Recommendation != RecommendationOnlyUseForRussianGovernmentResources {
				t.Fatalf("match = %#v, want Russian government warning", match)
			}
		})
	}
}

func recordMatchKey(rec Record) string {
	if rec.MatchKind == MatchKindUnknown || rec.Fingerprint.IsZero() {
		return ""
	}
	return rec.MatchKind.String() + ":" + rec.Fingerprint.String()
}
