package tlsplatform

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/asn1"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/asciimoth/almagest/bin/internal/isolation/protocol"
	"github.com/asciimoth/gonnect/tlspolicy"
	"github.com/kataras/golog"
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

func TestAuthorityProviderSkipsInvalidOSCertificates(t *testing.T) {
	candidates, err := fallbackAuthorityCandidates(t.Context())
	if err != nil || len(candidates) < 2 {
		t.Fatalf("fallback candidates: %v", err)
	}
	negativeSerial := bytes.Clone(candidates[0].DER)
	// Walk Certificate, TBSCertificate, version, then serialNumber. RawValue
	// slices refer to the original DER, so this changes only the serial's sign.
	var certificate, tbs, version, serial asn1.RawValue
	if _, err := asn1.Unmarshal(negativeSerial, &certificate); err != nil {
		t.Fatal(err)
	}
	if _, err := asn1.Unmarshal(certificate.Bytes, &tbs); err != nil {
		t.Fatal(err)
	}
	rest, err := asn1.Unmarshal(tbs.Bytes, &version)
	if err != nil || version.Class != asn1.ClassContextSpecific || version.Tag != 0 {
		t.Fatalf("expected explicit certificate version: %v", err)
	}
	if _, err := asn1.Unmarshal(rest, &serial); err != nil || serial.Tag != asn1.TagInteger || len(serial.Bytes) == 0 {
		t.Fatalf("expected certificate serial: %v", err)
	}
	serial.Bytes[0] |= 0x80
	if _, err := tlspolicy.ParseAuthorityDER(negativeSerial); err == nil || !strings.Contains(err.Error(), "negative serial number") {
		t.Fatalf("negative serial fixture error = %v", err)
	}

	for name, der := range map[string][]byte{
		"negative serial": negativeSerial,
		"malformed":       []byte("not a certificate"),
		"empty":           nil,
	} {
		t.Run(name, func(t *testing.T) {
			var logs bytes.Buffer
			originalLogger := golog.Default
			golog.Default = golog.New().SetOutput(&logs)
			t.Cleanup(func() { golog.Default = originalLogger })
			input := []tlspolicy.AuthorityCandidate{
				candidates[0],
				{DER: der, Source: tlspolicy.AuthoritySource{Name: `windows:LocalMachine\Root`, Trust: tlspolicy.TrustHintAnchor}},
				candidates[1], candidates[0],
			}
			provider := NewAuthorityProvider(tlspolicy.AuthorityFetcherFunc(func(context.Context) ([]tlspolicy.AuthorityCandidate, error) {
				return input, nil
			}))
			snapshot, err := provider.Snapshot(t.Context(), DefaultRequest())
			if err != nil {
				t.Fatal(err)
			}
			if len(snapshot.Authorities) != 2 {
				t.Fatalf("got %d authorities, want two valid, distinct certificates", len(snapshot.Authorities))
			}
			for _, candidate := range candidates[:2] {
				found := false
				for _, authority := range snapshot.Authorities {
					found = found || bytes.Equal(authority.DER, candidate.DER)
				}
				if !found {
					t.Fatal("valid certificate was lost")
				}
			}
			if !bytes.Equal(input[1].DER, der) || input[1].Source.Name != `windows:LocalMachine\Root` {
				t.Fatal("provider changed the fetcher's candidates")
			}
			digest := sha256.Sum256(der)
			if !strings.Contains(logs.String(), "[WARN]") ||
				!strings.Contains(logs.String(), `windows:LocalMachine\Root`) ||
				!strings.Contains(logs.String(), fmt.Sprintf("%x", digest)) {
				t.Fatalf("warning must identify the skipped certificate: %s", logs.String())
			}
		})
	}
}

func TestAuthorityProviderPreservesFetchErrorsAndCancellation(t *testing.T) {
	storeErr := errors.New("certificate store access denied")
	for _, want := range []error{storeErr, context.Canceled} {
		provider := NewAuthorityProvider(tlspolicy.AuthorityFetcherFunc(func(context.Context) ([]tlspolicy.AuthorityCandidate, error) {
			return []tlspolicy.AuthorityCandidate{{DER: []byte("bad")}}, want
		}))
		if _, err := provider.Snapshot(t.Context(), DefaultRequest()); !errors.Is(err, want) {
			t.Fatalf("fetch error = %v, want %v", err, want)
		}
	}
}

func TestUnmarshalRejectsUnknownAndTrailingData(t *testing.T) {
	for _, data := range []string{`{"path":"/etc/ssl/private"}`, `{"authorities":[],"authorities":[]}`, `{} {}`, ``} {
		if _, err := Unmarshal([]byte(data), DefaultRequest()); err == nil {
			t.Fatalf("accepted %q", data)
		}
	}
}

func TestUnmarshalRejectsExcessiveJSONDepth(t *testing.T) {
	data := []byte(strings.Repeat(`[`, protocol.MaxControlDepth+1) + `0` + strings.Repeat(`]`, protocol.MaxControlDepth+1))
	if _, err := Unmarshal(data, DefaultRequest()); !errors.Is(err, protocol.ErrLimitExceeded) {
		t.Fatalf("deep snapshot error = %v", err)
	}
}

func FuzzSnapshotUnmarshal(f *testing.F) {
	request, err := ConfiguredRequest(true, "cache-only")
	if err != nil {
		f.Fatal(err)
	}
	valid, err := json.Marshal(Snapshot{
		Authorities: []AuthorityBlob{{SourceID: "authority", DER: []byte{1}}},
		Distrust:    []DistrustBlob{{SourceID: "digest", Kind: DistrustCertificateSHA256, Value: make([]byte, sha256.Size)}},
		CRLs:        []CRLBlob{{SourceID: "crl", DER: []byte{2}}},
		Providers:   []ProviderStatus{{Source: "provider", State: ProviderActive, ObservedAt: 1, ItemCount: 2}},
	})
	if err != nil {
		f.Fatal(err)
	}
	for _, seed := range [][]byte{
		valid,
		[]byte(`{}`),
		[]byte(`{"authorities":[],"authorities":[]}`),
		[]byte(`{"unknown":{"nested":[{}]}}`),
		[]byte(`{} {}`),
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		snapshot, parseErr := Unmarshal(data, request)
		if parseErr != nil {
			return
		}
		if err := snapshot.Validate(request); err != nil {
			t.Fatalf("successful unmarshal returned invalid snapshot: %v", err)
		}
		canonical, err := Marshal(snapshot, request)
		if err != nil {
			t.Fatalf("successful unmarshal cannot be marshaled: %v", err)
		}
		if _, err := Unmarshal(canonical, request); err != nil {
			t.Fatalf("canonical snapshot cannot be read: %v", err)
		}
		before := bytes.Clone(canonical)
		for i := range data {
			data[i] ^= 0xff
		}
		after, err := Marshal(snapshot, request)
		if err != nil || !bytes.Equal(before, after) {
			t.Fatal("snapshot aliases untrusted JSON input")
		}
	})
}

func FuzzSnapshotRequestBounds(f *testing.F) {
	f.Add(uint8(SourceOS), uint32(MaxSnapshotBytes), uint32(MaxItems), true, true)
	f.Add(uint8(0), uint32(0), uint32(0), false, false)
	f.Fuzz(func(t *testing.T, source uint8, maxBytes, maxItems uint32, includeCRLs, includeDistrust bool) {
		request := Request{Source: Source(source), MaxBytes: maxBytes, MaxItems: maxItems,
			IncludeCRLs: includeCRLs, IncludeDistrust: includeDistrust}
		if err := request.Validate(); err == nil {
			if maxBytes == 0 || maxBytes > MaxSnapshotBytes || maxItems == 0 || maxItems > MaxItems || request.Source != SourceOS {
				t.Fatalf("invalid request passed validation: %#v", request)
			}
		}
	})
}
