package certcat

import "testing"

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

func recordMatchKey(rec Record) string {
	if rec.MatchKind == MatchKindUnknown || rec.Fingerprint.IsZero() {
		return ""
	}
	return rec.MatchKind.String() + ":" + rec.Fingerprint.String()
}
