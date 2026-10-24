package tlsplatform

import (
	"context"
	"crypto/sha256"
	"errors"
	"strings"
	"testing"

	"github.com/asciimoth/almagest/bin/internal/isolation/protocol"
	"github.com/asciimoth/gonnect/tlspolicy"
)

func TestSnapshotRejectsEveryBoundViolation(t *testing.T) {
	request := DefaultRequest()
	tests := map[string]Snapshot{
		"empty source":      {Authorities: []AuthorityBlob{{DER: []byte{1}}}},
		"long source":       {Authorities: []AuthorityBlob{{SourceID: strings.Repeat("x", MaxSourceIDBytes+1), DER: []byte{1}}}},
		"empty item":        {Authorities: []AuthorityBlob{{SourceID: "a"}}},
		"large item":        {Authorities: []AuthorityBlob{{SourceID: "a", DER: make([]byte, MaxItemBytes+1)}}},
		"duplicate":         {Authorities: []AuthorityBlob{{SourceID: "a", DER: []byte{1}}, {SourceID: "a", DER: []byte{2}}}},
		"disabled distrust": {Distrust: []DistrustBlob{{SourceID: "a", Kind: DistrustCertificateDER, Value: []byte{1}}}},
		"disabled CRL":      {CRLs: []CRLBlob{{SourceID: "a", DER: []byte{1}}}},
	}
	for name, snapshot := range tests {
		t.Run(name, func(t *testing.T) {
			if err := snapshot.Validate(request); err == nil {
				t.Fatal("invalid snapshot was accepted")
			}
		})
	}
	limited := request
	limited.MaxItems = 1
	if err := (Snapshot{Authorities: []AuthorityBlob{{SourceID: "a", DER: []byte{1}}, {SourceID: "b", DER: []byte{2}}}}).Validate(limited); !errors.Is(err, protocol.ErrLimitExceeded) {
		t.Fatalf("item limit error = %v", err)
	}
	limited = request
	limited.MaxBytes = 1
	if err := (Snapshot{Authorities: []AuthorityBlob{{SourceID: "a", DER: []byte{1, 2}}}}).Validate(limited); !errors.Is(err, protocol.ErrLimitExceeded) {
		t.Fatalf("byte limit error = %v", err)
	}
}

func TestRequestIsClosedAndBounded(t *testing.T) {
	for _, request := range []Request{
		{}, {Source: Source(99), MaxBytes: 1, MaxItems: 1},
		{Source: SourceOS, MaxBytes: MaxSnapshotBytes + 1, MaxItems: 1},
		{Source: SourceOS, MaxBytes: 1, MaxItems: MaxItems + 1},
	} {
		if err := request.Validate(); err == nil {
			t.Fatalf("request %#v was accepted", request)
		}
	}
}

func TestConfiguredRequestKeepsSystemIntegrationsBehindGate(t *testing.T) {
	disabled, err := ConfiguredRequest(false, "off")
	if err != nil || disabled.IncludeDistrust || disabled.IncludeCRLs {
		t.Fatalf("disabled request = %#v, %v", disabled, err)
	}
	enabled, err := ConfiguredRequest(true, "cache-only")
	if err != nil || !enabled.IncludeDistrust || !enabled.IncludeCRLs {
		t.Fatalf("enabled request = %#v, %v", enabled, err)
	}
	for _, test := range []struct {
		enabled bool
		mode    string
	}{{false, "cache-only"}, {true, "online"}, {true, "other"}} {
		if _, err := ConfiguredRequest(test.enabled, test.mode); err == nil {
			t.Fatalf("ConfiguredRequest(%t, %q) succeeded", test.enabled, test.mode)
		}
	}
}

func TestDistrustAndProviderStatusAreClosedAndBounded(t *testing.T) {
	request, err := ConfiguredRequest(true, "off")
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte("certificate"))
	valid := Snapshot{
		Distrust:  []DistrustBlob{{SourceID: "system:item", Kind: DistrustCertificateSHA256, Value: digest[:]}},
		Providers: []ProviderStatus{{Source: "system:test", State: ProviderActive, ObservedAt: 1, ItemCount: 1}},
	}
	data, err := Marshal(valid, request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Unmarshal(data, request); err != nil {
		t.Fatal(err)
	}
	invalid := []Snapshot{
		{Distrust: []DistrustBlob{{SourceID: "a", Kind: DistrustNone, Value: []byte{1}}}},
		{Distrust: []DistrustBlob{{SourceID: "a", Kind: DistrustCertificateSHA256}}},
		{Providers: []ProviderStatus{{Source: "x", State: "mystery"}}},
		{Providers: []ProviderStatus{{Source: "x", State: ProviderActive}, {Source: "x", State: ProviderActive}}},
	}
	for i, snapshot := range invalid {
		if err := snapshot.Validate(request); err == nil {
			t.Fatalf("invalid snapshot %d succeeded", i)
		}
	}
}

func TestAuthorityProviderProducesOwnedStableSnapshot(t *testing.T) {
	candidates, err := fallbackAuthorityCandidates(context.Background())
	if err != nil || len(candidates) == 0 {
		t.Fatalf("fallback candidates: %v", err)
	}
	der := append([]byte(nil), candidates[0].DER...)
	provider := NewAuthorityProvider(tlspolicy.AuthorityFetcherFunc(func(context.Context) ([]tlspolicy.AuthorityCandidate, error) {
		return []tlspolicy.AuthorityCandidate{{DER: der, Source: tlspolicy.AuthoritySource{Name: "test", Trust: tlspolicy.TrustHintAnchor}}}, nil
	}))
	snapshot, err := provider.Snapshot(context.Background(), DefaultRequest())
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Authorities) != 1 || snapshot.Authorities[0].SourceID == "" {
		t.Fatalf("snapshot = %#v", snapshot)
	}
	want := snapshot.Authorities[0].DER[0]
	der[0] ^= 0xff
	if snapshot.Authorities[0].DER[0] != want {
		t.Fatal("snapshot aliases provider input")
	}
}

func TestSystemProviderRejectsRuntimeFeatureEscalation(t *testing.T) {
	allowed := DefaultRequest()
	provider := NewSystemProvider(allowed)
	escalated := allowed
	escalated.IncludeDistrust = true
	if _, err := provider.Snapshot(context.Background(), escalated); err == nil {
		t.Fatal("runtime enabled distrust beyond protected launch authority")
	}
}

func TestUnmarshalRejectsUnknownAndTrailingData(t *testing.T) {
	for _, data := range []string{`{"path":"/etc/ssl/private"}`, `{"authorities":[],"authorities":[]}`, `{} {}`, ``} {
		if _, err := Unmarshal([]byte(data), DefaultRequest()); err == nil {
			t.Fatalf("accepted %q", data)
		}
	}
}
