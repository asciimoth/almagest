package tlsplatform

import (
	"context"
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

func FuzzRevocationRequest(f *testing.F) {
	candidates, err := fallbackAuthorityCandidates(context.Background())
	if err != nil || len(candidates) == 0 {
		f.Fatalf("fallback certificate: %v", err)
	}
	valid := candidates[0].DER
	f.Add(valid, uint8(1), uint8(2))
	f.Add([]byte{0x30, 0}, uint8(MaxRevocationChains+1), uint8(1))
	f.Add([]byte{}, uint8(0), uint8(0))
	f.Fuzz(func(t *testing.T, der []byte, chainCount, certificateCount uint8) {
		chains := int(chainCount) % (MaxRevocationChains + 3)
		certificates := int(certificateCount) % (MaxRevocationChainCerts + 3)
		request := RevocationRequest{Chains: make([][][]byte, chains)}
		for i := range request.Chains {
			request.Chains[i] = make([][]byte, certificates)
			for j := range request.Chains[i] {
				request.Chains[i][j] = der
			}
		}
		if err := request.Validate(); err != nil {
			return
		}
		if chains == 0 || chains > MaxRevocationChains || certificates < 2 || certificates > MaxRevocationChainCerts {
			t.Fatalf("invalid request dimensions passed validation: %d x %d", chains, certificates)
		}
		for _, chain := range request.Chains {
			for _, certificate := range chain {
				parsed, err := x509.ParseCertificate(certificate)
				if err != nil || len(parsed.Raw) != len(certificate) {
					t.Fatalf("invalid DER passed validation: %v", err)
				}
			}
		}
	})
}

func FuzzRevocationResult(f *testing.F) {
	issuer := testCertificateDERForFuzz(f)
	request := RevocationRequest{Chains: [][][]byte{{issuer, issuer}}}
	f.Add("good", uint8(1))
	f.Add("revoked", uint8(1))
	f.Add("attacker-controlled", uint8(1))
	f.Add("good", uint8(0))
	f.Fuzz(func(t *testing.T, state string, count uint8) {
		result := RevocationResult{States: make([]RevocationState, int(count)%4)}
		for i := range result.States {
			result.States[i] = RevocationState(state)
		}
		if err := result.Validate(request); err == nil && (len(result.States) != 1 || !RevocationState(state).Valid()) {
			t.Fatalf("invalid revocation result passed validation: %#v", result)
		}
	})
}

func testCertificateDERForFuzz(t testing.TB) []byte {
	t.Helper()
	candidates, err := fallbackAuthorityCandidates(context.Background())
	if err != nil || len(candidates) == 0 {
		t.Fatalf("fallback certificate: %v", err)
	}
	return append([]byte(nil), candidates[0].DER...)
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
