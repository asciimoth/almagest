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

func TestGeneratedRecordsDenyEmudhraCompromisedKeys(t *testing.T) {
	fingerprints := []string{
		"EBED6475C2C2D80FCB199F3FE8DD6E6B3CE60B77EB15D4E42C3DEC0C6F5B5105",
		"43BFFA19D6EF651451F6841E019931E96F6F2EAEFF28AB4B6F1CE4576DE2717B",
		"E0254C7FCDC265E2F0F8A428DABB75AB4AE039B4872D62F5EE57F0877D5A8A41",
		"49DD353DC2B919A3770645117D0E60C16115E7D1BC259EEFE734B00EA434FE5F",
		"63ECEFEE431D49523BC1136C4E72D0206A477086CAF606CD5CF025686C109CF8",
		"5C0660A2B164910D8FF589C18E14552E9E0EC237DCEFE2CB736D41A4811F81A0",
		"EC4FFA1AD0AC88081314B7AAFFEF6A91F97CD57291BB5BA686C5415F5EB4F6B6",
		"92C063D67E8F14DB71D288E11266FFDAF69CBF115F7BAB14F3BC2F68D25E0E30",
	}
	index := NewIndex(Records)
	for _, fingerprint := range fingerprints {
		matches := index.Lookup(MatchKindSPKISHA256, fingerprint)
		if len(matches) != 1 {
			t.Fatalf("SPKI %s matches = %#v, want one match", fingerprint, matches)
		}
		match := matches[0]
		if match.HazardClass != HazardClassPrivateKeyDisclosure ||
			match.Action != ActionDeny ||
			match.Severity != SeverityCritical ||
			match.Recommendation != RecommendationDenyExactSPKI {
			t.Fatalf("SPKI %s match = %#v, want critical private-key disclosure denial", fingerprint, match)
		}
	}
}

func recordMatchKey(rec Record) string {
	if rec.MatchKind == MatchKindUnknown || rec.Fingerprint.IsZero() {
		return ""
	}
	return rec.MatchKind.String() + ":" + rec.Fingerprint.String()
}
