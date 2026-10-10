package qualification

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
)

func validEvidence(t *testing.T, profile string) Evidence {
	t.Helper()
	manifest, ok := ProfileManifest(profile)
	if !ok {
		t.Fatal("missing test profile")
	}
	digest := strings.Repeat("a", 64)
	evidence := Evidence{
		SchemaVersion: SchemaVersion, Profile: profile, Platform: manifest.Platform,
		SourceRevision: "0123456789abcdef", DirectLaunch: Launch{CredentialClass: "ordinary", ProtectedDigest: digest},
		IsolatedLaunch: Launch{CredentialClass: "ordinary", ProtectedDigest: digest}, RuntimeThreadCount: 8,
		OverloadSeconds: MinimumSoakSecs, ExperimentalAbsent: true,
	}
	for _, id := range manifest.Features {
		evidence.Features = append(evidence.Features, FeatureResult{ID: id, Direct: ModeResult{Outcome: OutcomePass}, Isolated: ModeResult{Outcome: OutcomePass}})
	}
	for _, id := range manifest.BoundaryChecks {
		evidence.BoundaryChecks = append(evidence.BoundaryChecks, CheckResult{ID: id, Passed: true})
	}
	for _, id := range manifest.RestrictionChecks {
		evidence.RestrictionChecks = append(evidence.RestrictionChecks, CheckResult{ID: id, Passed: true})
	}
	for _, id := range manifest.NativeChecks {
		evidence.NativeChecks = append(evidence.NativeChecks, CheckResult{ID: id, Passed: true})
	}
	return evidence
}

func cloneEvidence(t *testing.T, evidence Evidence) Evidence {
	t.Helper()
	data, err := json.Marshal(evidence)
	if err != nil {
		t.Fatal(err)
	}
	var cloned Evidence
	if err := json.Unmarshal(data, &cloned); err != nil {
		t.Fatal(err)
	}
	return cloned
}

func TestCompleteProfileEvidence(t *testing.T) {
	profiles := Profiles()
	if !slices.Equal(profiles, []string{"linux-amd64-v1", "windows-server-2022-amd64-v1"}) {
		t.Fatalf("profiles = %v", profiles)
	}
	for _, profile := range profiles {
		t.Run(profile, func(t *testing.T) {
			evidence := validEvidence(t, profile)
			if err := Validate(evidence); err != nil {
				t.Fatal(err)
			}
			data, err := json.Marshal(evidence)
			if err != nil {
				t.Fatal(err)
			}
			decoded, err := Decode(data)
			if err != nil {
				t.Fatal(err)
			}
			if err := Validate(decoded); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestFeatureParityRequiresEveryExactResult(t *testing.T) {
	base := validEvidence(t, "linux-amd64-v1")
	for index, feature := range base.Features {
		t.Run(feature.ID+"_missing", func(t *testing.T) {
			evidence := cloneEvidence(t, base)
			evidence.Features = append(evidence.Features[:index], evidence.Features[index+1:]...)
			if err := Validate(evidence); !errors.Is(err, ErrUnqualified) {
				t.Fatalf("error = %v", err)
			}
		})
		t.Run(feature.ID+"_isolated_loss", func(t *testing.T) {
			evidence := cloneEvidence(t, base)
			evidence.Features[index].Isolated = ModeResult{Outcome: OutcomeLimitation, Limitation: "adapter_gap"}
			if err := Validate(evidence); !errors.Is(err, ErrUnqualified) {
				t.Fatalf("error = %v", err)
			}
		})
	}

	shared := cloneEvidence(t, base)
	shared.Features[0].Direct = ModeResult{Outcome: OutcomeLimitation, Limitation: "native_backend_unavailable"}
	shared.Features[0].Isolated = ModeResult{Outcome: OutcomeLimitation, Limitation: "native_backend_unavailable"}
	if err := Validate(shared); err != nil {
		t.Fatalf("shared native limitation: %v", err)
	}
	for _, change := range []func(*Evidence){
		func(e *Evidence) { e.Features[0].Isolated.Limitation = "isolation_adapter_gap" },
		func(e *Evidence) { e.Features[0].Direct.Limitation = "" },
		func(e *Evidence) { e.Features[0].Direct.Outcome = Outcome("skip") },
		func(e *Evidence) { e.Features = append(e.Features, e.Features[0]) },
		func(e *Evidence) { e.Features[0].ID = "invented_feature" },
	} {
		evidence := cloneEvidence(t, shared)
		change(&evidence)
		if err := Validate(evidence); !errors.Is(err, ErrUnqualified) {
			t.Fatalf("error = %v", err)
		}
	}
}

func TestEverySecurityObservationIsMandatory(t *testing.T) {
	base := validEvidence(t, "windows-server-2022-amd64-v1")
	groups := []struct {
		name string
		get  func(*Evidence) *[]CheckResult
	}{
		{"boundary", func(e *Evidence) *[]CheckResult { return &e.BoundaryChecks }},
		{"restriction", func(e *Evidence) *[]CheckResult { return &e.RestrictionChecks }},
		{"native", func(e *Evidence) *[]CheckResult { return &e.NativeChecks }},
	}
	for _, group := range groups {
		checks := *group.get(&base)
		for index, check := range checks {
			t.Run(group.name+"_missing_"+check.ID, func(t *testing.T) {
				evidence := cloneEvidence(t, base)
				items := group.get(&evidence)
				*items = append((*items)[:index], (*items)[index+1:]...)
				if err := Validate(evidence); !errors.Is(err, ErrUnqualified) {
					t.Fatalf("error = %v", err)
				}
			})
			t.Run(group.name+"_failed_"+check.ID, func(t *testing.T) {
				evidence := cloneEvidence(t, base)
				(*group.get(&evidence))[index].Passed = false
				if err := Validate(evidence); !errors.Is(err, ErrUnqualified) {
					t.Fatalf("error = %v", err)
				}
			})
		}
	}
}

func TestLaunchAndHostileRuntimeConditionsFailClosed(t *testing.T) {
	base := validEvidence(t, "linux-amd64-v1")
	tests := map[string]func(*Evidence){
		"schema":                 func(e *Evidence) { e.SchemaVersion++ },
		"profile":                func(e *Evidence) { e.Profile = "unknown" },
		"os":                     func(e *Evidence) { e.Platform.OS = "windows" },
		"architecture":           func(e *Evidence) { e.Platform.Architecture = "arm64" },
		"source":                 func(e *Evidence) { e.SourceRevision = "" },
		"credentials":            func(e *Evidence) { e.IsolatedLaunch.CredentialClass = "elevated" },
		"protected_settings":     func(e *Evidence) { e.IsolatedLaunch.ProtectedDigest = strings.Repeat("b", 64) },
		"bad_digest":             func(e *Evidence) { e.DirectLaunch.ProtectedDigest = strings.Repeat("G", 64) },
		"baseline_restriction":   func(e *Evidence) { e.IsolatedLaunch.Restrictions = []string{"deny_dns"} },
		"too_few_threads":        func(e *Evidence) { e.RuntimeThreadCount = 7 },
		"short_overload":         func(e *Evidence) { e.OverloadSeconds = MinimumSoakSecs - 1 },
		"experimental_available": func(e *Evidence) { e.ExperimentalAbsent = false },
		"inherited_handle":       func(e *Evidence) { e.UnexpectedHandles = 1 },
	}
	for name, change := range tests {
		t.Run(name, func(t *testing.T) {
			evidence := cloneEvidence(t, base)
			change(&evidence)
			if err := Validate(evidence); !errors.Is(err, ErrUnqualified) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestDecodeRejectsAmbiguousOrUnboundedInput(t *testing.T) {
	valid, err := json.Marshal(validEvidence(t, "linux-amd64-v1"))
	if err != nil {
		t.Fatal(err)
	}
	inputs := [][]byte{
		nil,
		[]byte(`{}` + string(valid)),
		[]byte(`{"schema_version":1,"schema_version":1}`),
		[]byte(`{"unknown":true}`),
		[]byte(strings.Repeat("[", 66) + strings.Repeat("]", 66)),
		append(slices.Clone(valid), []byte(` {}`)...),
		[]byte(strings.Repeat(" ", MaxEvidenceBytes+1)),
	}
	for index, input := range inputs {
		if _, err := Decode(input); !errors.Is(err, ErrUnqualified) {
			t.Errorf("case %d: error = %v", index, err)
		}
	}
}

func TestManifestReturnsIndependentSlices(t *testing.T) {
	first, _ := ProfileManifest("linux-amd64-v1")
	second, _ := ProfileManifest("linux-amd64-v1")
	first.Features[0] = "changed"
	first.BoundaryChecks[0] = "changed"
	first.RestrictionChecks[0] = "changed"
	first.NativeChecks[0] = "changed"
	if second.Features[0] == "changed" || second.BoundaryChecks[0] == "changed" ||
		second.RestrictionChecks[0] == "changed" || second.NativeChecks[0] == "changed" {
		t.Fatal("manifest exposed mutable package state")
	}
}

func FuzzEvidenceDecoder(f *testing.F) {
	for _, profile := range Profiles() {
		data, err := json.Marshal(validEvidenceForFuzz(profile))
		if err != nil {
			f.Fatal(err)
		}
		f.Add(data)
	}
	for _, seed := range [][]byte{nil, []byte(`{}`), []byte(`[]`), []byte(`{"profile":"linux-amd64-v1"}`), []byte{0xff, 0x00}} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		evidence, err := Decode(data)
		if err == nil {
			_ = Validate(evidence)
		}
	})
}

func FuzzEvidenceValidation(f *testing.F) {
	f.Add(uint8(0), uint8(0), uint8(0), "ordinary", strings.Repeat("a", 64), uint32(8), uint32(MinimumSoakSecs), true)
	f.Add(uint8(1), uint8(20), uint8(10), "elevated", "bad", uint32(0), uint32(0), false)
	f.Fuzz(func(t *testing.T, profileIndex, featureIndex, checkIndex uint8, credential, digest string, threads, soak uint32, passed bool) {
		profiles := Profiles()
		evidence := validEvidenceForFuzz(profiles[int(profileIndex)%len(profiles)])
		evidence.DirectLaunch.CredentialClass = credential
		evidence.IsolatedLaunch.CredentialClass = credential
		evidence.DirectLaunch.ProtectedDigest = digest
		evidence.IsolatedLaunch.ProtectedDigest = digest
		evidence.RuntimeThreadCount = threads
		evidence.OverloadSeconds = soak
		evidence.Features[int(featureIndex)%len(evidence.Features)].Isolated.Outcome = Outcome("fuzz")
		evidence.BoundaryChecks[int(checkIndex)%len(evidence.BoundaryChecks)].Passed = passed
		_ = Validate(evidence)
	})
}

func validEvidenceForFuzz(profile string) Evidence {
	manifest, _ := ProfileManifest(profile)
	digest := strings.Repeat("a", 64)
	e := Evidence{SchemaVersion: SchemaVersion, Profile: profile, Platform: manifest.Platform, SourceRevision: "a",
		DirectLaunch: Launch{CredentialClass: "ordinary", ProtectedDigest: digest}, IsolatedLaunch: Launch{CredentialClass: "ordinary", ProtectedDigest: digest},
		RuntimeThreadCount: 8, OverloadSeconds: MinimumSoakSecs, ExperimentalAbsent: true}
	for _, id := range manifest.Features {
		e.Features = append(e.Features, FeatureResult{ID: id, Direct: ModeResult{Outcome: OutcomePass}, Isolated: ModeResult{Outcome: OutcomePass}})
	}
	for _, target := range []struct {
		ids []string
		out *[]CheckResult
	}{{manifest.BoundaryChecks, &e.BoundaryChecks}, {manifest.RestrictionChecks, &e.RestrictionChecks}, {manifest.NativeChecks, &e.NativeChecks}} {
		for _, id := range target.ids {
			*target.out = append(*target.out, CheckResult{ID: id, Passed: true})
		}
	}
	return e
}
