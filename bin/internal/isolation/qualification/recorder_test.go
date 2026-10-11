package qualification

import (
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
)

func newTestRecorder(t *testing.T, profile string) *Recorder {
	t.Helper()
	manifest, ok := ProfileManifest(profile)
	if !ok {
		t.Fatal("missing profile")
	}
	digest := strings.Repeat("a", 64)
	recorder, err := NewRecorder(profile, manifest.Platform, "revision",
		Launch{CredentialClass: "ordinary", ProtectedDigest: digest},
		Launch{CredentialClass: "ordinary", ProtectedDigest: digest})
	if err != nil {
		t.Fatal(err)
	}
	return recorder
}

func recordComplete(t *testing.T, recorder *Recorder) {
	t.Helper()
	manifest := recorder.manifest
	for index := len(manifest.Features) - 1; index >= 0; index-- {
		result := FeatureResult{ID: manifest.Features[index], Direct: ModeResult{Outcome: OutcomePass}, Isolated: ModeResult{Outcome: OutcomePass}}
		if err := recorder.RecordFeature(result); err != nil {
			t.Fatal(err)
		}
	}
	for _, group := range []struct {
		ids    []string
		record func(CheckResult) error
	}{
		{manifest.BoundaryChecks, recorder.RecordBoundary},
		{manifest.RestrictionChecks, recorder.RecordRestriction},
		{manifest.NativeChecks, recorder.RecordNative},
	} {
		for index := len(group.ids) - 1; index >= 0; index-- {
			if err := group.record(CheckResult{ID: group.ids[index], Passed: true}); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := recorder.RecordRuntime(8, 0, 0, true); err != nil {
		t.Fatal(err)
	}
}

func TestRecorderBuildsManifestOrderedEvidence(t *testing.T) {
	for _, profile := range Profiles() {
		t.Run(profile, func(t *testing.T) {
			recorder := newTestRecorder(t, profile)
			recordComplete(t, recorder)
			evidence := recorder.Snapshot()
			if err := Validate(evidence); err != nil {
				t.Fatal(err)
			}
			manifest, _ := ProfileManifest(profile)
			for index, id := range manifest.Features {
				if evidence.Features[index].ID != id {
					t.Fatalf("feature %d = %q, want %q", index, evidence.Features[index].ID, id)
				}
			}
		})
	}
}

func TestRecorderRejectsUnknownDuplicateAndInvalidHeader(t *testing.T) {
	recorder := newTestRecorder(t, "linux-amd64-v1")
	feature := FeatureResult{ID: recorder.manifest.Features[0], Direct: ModeResult{Outcome: OutcomePass}, Isolated: ModeResult{Outcome: OutcomePass}}
	if err := recorder.RecordFeature(feature); err != nil {
		t.Fatal(err)
	}
	for _, err := range []error{
		recorder.RecordFeature(feature),
		recorder.RecordFeature(FeatureResult{ID: "unknown"}),
		recorder.RecordBoundary(CheckResult{ID: "unknown", Passed: true}),
		recorder.RecordRestriction(CheckResult{ID: "unknown", Passed: true}),
		recorder.RecordNative(CheckResult{ID: "unknown", Passed: true}),
	} {
		if !errors.Is(err, ErrUnqualified) {
			t.Fatalf("error = %v", err)
		}
	}
	check := CheckResult{ID: recorder.manifest.BoundaryChecks[0], Passed: false}
	if err := recorder.RecordBoundary(check); err != nil {
		t.Fatal(err)
	}
	if err := recorder.RecordBoundary(check); !errors.Is(err, ErrUnqualified) {
		t.Fatalf("duplicate check error = %v", err)
	}
	if err := recorder.RecordRuntime(8, 0, 0, true); err != nil {
		t.Fatal(err)
	}
	if err := recorder.RecordRuntime(8, 0, 0, true); !errors.Is(err, ErrUnqualified) {
		t.Fatalf("duplicate runtime error = %v", err)
	}

	manifest, _ := ProfileManifest("linux-amd64-v1")
	digest := strings.Repeat("a", 64)
	if _, err := NewRecorder("linux-amd64-v1", manifest.Platform, "revision",
		Launch{CredentialClass: "ordinary", ProtectedDigest: digest},
		Launch{CredentialClass: "different", ProtectedDigest: digest}); !errors.Is(err, ErrUnqualified) {
		t.Fatalf("mismatched header error = %v", err)
	}
}

func TestRecorderOwnsLaunchSlices(t *testing.T) {
	manifest, _ := ProfileManifest("linux-amd64-v1")
	digest := strings.Repeat("a", 64)
	directRestrictions := make([]string, 0, 1)
	isolatedRestrictions := make([]string, 0, 1)
	recorder, err := NewRecorder(manifest.Profile, manifest.Platform, "revision",
		Launch{CredentialClass: "ordinary", ProtectedDigest: digest, Restrictions: directRestrictions},
		Launch{CredentialClass: "ordinary", ProtectedDigest: digest, Restrictions: isolatedRestrictions})
	if err != nil {
		t.Fatal(err)
	}
	directRestrictions = append(directRestrictions, "changed")
	isolatedRestrictions = append(isolatedRestrictions, "changed")
	if directRestrictions[0] != "changed" || isolatedRestrictions[0] != "changed" {
		t.Fatal("test launch mutation failed")
	}
	evidence := recorder.Snapshot()
	if len(evidence.DirectLaunch.Restrictions) != 0 || len(evidence.IsolatedLaunch.Restrictions) != 0 {
		t.Fatalf("caller changed recorder launch data: %+v %+v", evidence.DirectLaunch, evidence.IsolatedLaunch)
	}
}

func TestRecorderRetainsFailuresAndIncompleteRuns(t *testing.T) {
	recorder := newTestRecorder(t, "windows-server-2022-amd64-v1")
	if err := recorder.RecordBoundary(CheckResult{ID: recorder.manifest.BoundaryChecks[0], Passed: false}); err != nil {
		t.Fatal(err)
	}
	evidence := recorder.Snapshot()
	if len(evidence.BoundaryChecks) != 1 || evidence.BoundaryChecks[0].Passed {
		t.Fatalf("failed observation was not retained: %+v", evidence.BoundaryChecks)
	}
	if err := Validate(evidence); !errors.Is(err, ErrUnqualified) {
		t.Fatalf("incomplete evidence error = %v", err)
	}
}

func TestRecorderIsConcurrentAndSnapshotsAreIndependent(t *testing.T) {
	recorder := newTestRecorder(t, "linux-amd64-v1")
	manifest := recorder.manifest
	var wg sync.WaitGroup
	for _, id := range manifest.Features {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := recorder.RecordFeature(FeatureResult{ID: id, Direct: ModeResult{Outcome: OutcomePass}, Isolated: ModeResult{Outcome: OutcomePass}}); err != nil {
				t.Errorf("feature %s: %v", id, err)
			}
		}()
	}
	for _, group := range []struct {
		ids    []string
		record func(CheckResult) error
	}{{manifest.BoundaryChecks, recorder.RecordBoundary}, {manifest.RestrictionChecks, recorder.RecordRestriction}, {manifest.NativeChecks, recorder.RecordNative}} {
		for _, id := range group.ids {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if err := group.record(CheckResult{ID: id, Passed: true}); err != nil {
					t.Errorf("check %s: %v", id, err)
				}
			}()
		}
	}
	wg.Wait()
	if err := recorder.RecordRuntime(8, 0, 0, true); err != nil {
		t.Fatal(err)
	}
	first := recorder.Snapshot()
	if err := Validate(first); err != nil {
		t.Fatal(err)
	}
	first.Features[0].ID = "changed"
	first.DirectLaunch.Restrictions = append(first.DirectLaunch.Restrictions, "changed")
	second := recorder.Snapshot()
	if second.Features[0].ID == "changed" || slices.Contains(second.DirectLaunch.Restrictions, "changed") {
		t.Fatal("snapshot exposed recorder state")
	}
}

func FuzzRecorderRejectsArbitraryObservationIDs(f *testing.F) {
	f.Add("regular_tun", uint8(0))
	f.Add("unknown", uint8(3))
	f.Add("\x00", uint8(1))
	f.Fuzz(func(t *testing.T, id string, kind uint8) {
		recorder := newTestRecorder(t, "linux-amd64-v1")
		var err error
		switch kind % 4 {
		case 0:
			err = recorder.RecordFeature(FeatureResult{ID: id, Direct: ModeResult{Outcome: OutcomePass}, Isolated: ModeResult{Outcome: OutcomePass}})
		case 1:
			err = recorder.RecordBoundary(CheckResult{ID: id, Passed: true})
		case 2:
			err = recorder.RecordRestriction(CheckResult{ID: id, Passed: true})
		case 3:
			err = recorder.RecordNative(CheckResult{ID: id, Passed: true})
		}
		if err == nil {
			_ = Validate(recorder.Snapshot())
		} else if !errors.Is(err, ErrUnqualified) {
			t.Fatalf("unexpected error: %v", err)
		}
	})
}
