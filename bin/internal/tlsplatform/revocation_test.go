package tlsplatform

import (
	"crypto/x509"
	"testing"
)

func TestRevocationRequestBounds(t *testing.T) {
	issuer := testCertificateDER(t, true)
	leaf := testCertificateDER(t, false)
	valid := RevocationRequest{Chains: [][][]byte{{leaf, issuer}}}
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, request := range map[string]RevocationRequest{
		"empty":       {},
		"short chain": {Chains: [][][]byte{{leaf}}},
		"bad DER":     {Chains: [][][]byte{{{1}, issuer}}},
		"too many":    {Chains: make([][][]byte, MaxRevocationChains+1)},
		"too large":   {Chains: [][][]byte{{make([]byte, MaxRevocationChainBytes), issuer}}},
	} {
		t.Run(name, func(t *testing.T) {
			if err := request.Validate(); err == nil {
				t.Fatal("invalid request succeeded")
			}
		})
	}
	for _, state := range []RevocationState{RevocationGood, RevocationRevoked, RevocationUnknown, RevocationOffline, RevocationPlatformError} {
		if err := (RevocationResult{States: []RevocationState{state}}).Validate(valid); err != nil {
			t.Fatalf("state %q: %v", state, err)
		}
	}
}

func testCertificateDER(t *testing.T, ca bool) []byte {
	t.Helper()
	candidates, err := fallbackAuthorityCandidates(t.Context())
	if err != nil || len(candidates) == 0 {
		t.Fatalf("fallback certificate: %v", err)
	}
	der := append([]byte(nil), candidates[0].DER...)
	if _, err := x509.ParseCertificate(der); err != nil {
		t.Fatal(err)
	}
	_ = ca
	return der
}
