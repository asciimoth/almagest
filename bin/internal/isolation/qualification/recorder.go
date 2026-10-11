package qualification

import (
	"fmt"
	"slices"
	"sync"
)

// Recorder collects observations from one complete-profile run. It rejects
// unknown and duplicate observations when they are recorded. Snapshot returns
// a stable manifest-ordered copy, including failed checks, so a failed native
// run can be retained without being converted into passing evidence.
type Recorder struct {
	mu sync.Mutex

	evidence     Evidence
	manifest     Manifest
	features     map[string]FeatureResult
	boundary     map[string]CheckResult
	restrictions map[string]CheckResult
	native       map[string]CheckResult
	runtimeSet   bool
}

// NewRecorder validates the immutable identity shared by the direct and
// isolated parts of one run. Optional policy restrictions are not valid in the
// parity record.
func NewRecorder(profile string, platform Platform, sourceRevision string, direct, isolated Launch) (*Recorder, error) {
	manifest, ok := ProfileManifest(profile)
	if !ok {
		return nil, fmt.Errorf("%w: unknown profile", ErrUnqualified)
	}
	direct.Restrictions = slices.Clone(direct.Restrictions)
	isolated.Restrictions = slices.Clone(isolated.Restrictions)
	evidence := Evidence{
		SchemaVersion: SchemaVersion, Profile: profile, Platform: platform,
		SourceRevision: sourceRevision, DirectLaunch: direct, IsolatedLaunch: isolated,
	}
	if err := validateHeader(evidence, manifest); err != nil {
		return nil, err
	}
	return &Recorder{
		evidence: evidence, manifest: manifest,
		features: make(map[string]FeatureResult), boundary: make(map[string]CheckResult),
		restrictions: make(map[string]CheckResult), native: make(map[string]CheckResult),
	}, nil
}

// RecordFeature adds one measured direct/isolated parity result.
func (r *Recorder) RecordFeature(result FeatureResult) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !slices.Contains(r.manifest.Features, result.ID) {
		return fmt.Errorf("%w: unexpected feature %q", ErrUnqualified, result.ID)
	}
	if _, exists := r.features[result.ID]; exists {
		return fmt.Errorf("%w: duplicate feature %q", ErrUnqualified, result.ID)
	}
	r.features[result.ID] = result
	return nil
}

// RecordBoundary adds one hostile-runtime boundary observation.
func (r *Recorder) RecordBoundary(result CheckResult) error {
	return r.recordCheck("boundary", result)
}

// RecordRestriction adds one optional-policy observation.
func (r *Recorder) RecordRestriction(result CheckResult) error {
	return r.recordCheck("restriction", result)
}

// RecordNative adds one platform-native observation.
func (r *Recorder) RecordNative(result CheckResult) error {
	return r.recordCheck("native", result)
}

func (r *Recorder) recordCheck(kind string, result CheckResult) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	var required []string
	var results map[string]CheckResult
	switch kind {
	case "boundary":
		required, results = r.manifest.BoundaryChecks, r.boundary
	case "restriction":
		required, results = r.manifest.RestrictionChecks, r.restrictions
	case "native":
		required, results = r.manifest.NativeChecks, r.native
	default:
		panic("invalid qualification check kind")
	}
	if !slices.Contains(required, result.ID) {
		return fmt.Errorf("%w: unexpected %s check %q", ErrUnqualified, kind, result.ID)
	}
	if _, exists := results[result.ID]; exists {
		return fmt.Errorf("%w: duplicate %s check %q", ErrUnqualified, kind, result.ID)
	}
	results[result.ID] = result
	return nil
}

// RecordRuntime records the complete-run thread, overload, experimental-class,
// and inherited-handle observations. It can be recorded only once.
func (r *Recorder) RecordRuntime(threadCount, overloadSeconds, unexpectedHandles uint32, experimentalAbsent bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.runtimeSet {
		return fmt.Errorf("%w: duplicate runtime summary", ErrUnqualified)
	}
	r.runtimeSet = true
	r.evidence.RuntimeThreadCount = threadCount
	r.evidence.OverloadSeconds = overloadSeconds
	r.evidence.ExperimentalAbsent = experimentalAbsent
	r.evidence.UnexpectedHandles = unexpectedHandles
	return nil
}

// Snapshot returns an independent, manifest-ordered evidence value. Call
// Validate on the result before it is accepted as qualification evidence.
func (r *Recorder) Snapshot() Evidence {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := r.evidence
	out.DirectLaunch.Restrictions = slices.Clone(r.evidence.DirectLaunch.Restrictions)
	out.IsolatedLaunch.Restrictions = slices.Clone(r.evidence.IsolatedLaunch.Restrictions)
	out.Features = orderedFeatures(r.manifest.Features, r.features)
	out.BoundaryChecks = orderedChecks(r.manifest.BoundaryChecks, r.boundary)
	out.RestrictionChecks = orderedChecks(r.manifest.RestrictionChecks, r.restrictions)
	out.NativeChecks = orderedChecks(r.manifest.NativeChecks, r.native)
	return out
}

func orderedFeatures(order []string, values map[string]FeatureResult) []FeatureResult {
	out := make([]FeatureResult, 0, len(values))
	for _, id := range order {
		if result, ok := values[id]; ok {
			out = append(out, result)
		}
	}
	return out
}

func orderedChecks(order []string, values map[string]CheckResult) []CheckResult {
	out := make([]CheckResult, 0, len(values))
	for _, id := range order {
		if result, ok := values[id]; ok {
			out = append(out, result)
		}
	}
	return out
}
