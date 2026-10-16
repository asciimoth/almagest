// Package policy selects subprogram protection plans. It cannot launch code.
package policy

import (
	"fmt"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/asciimoth/almagest/lib/sandbox"
)

// Probe is a complete, compatible set of protections qualified by a backend.
// An absent feature is unavailable. Probe results must not come from nodes.
type Probe struct {
	Platform          string
	EffectiveIdentity string
	Available         map[sandbox.Feature]bool
	Reasons           map[sandbox.Feature]string
}

var features = []sandbox.Feature{
	sandbox.PrivilegeRemoval, sandbox.Environment, sandbox.HandleInheritance,
	sandbox.WritableStorage, sandbox.FilesystemAccess, sandbox.ExactFilesystemView,
	sandbox.PrivateNetwork, sandbox.ProcessIsolation, sandbox.ProcessTree,
	sandbox.ResourceLimits,
}

func KnownFeature(feature sandbox.Feature) bool { return slices.Contains(features, feature) }

// Select fixes a plan before resource allocation. Application failure must fail
// the instance; callers must not select a weaker plan after such a failure.
func Select(mode sandbox.Mode, probe Probe, required []sandbox.Feature) (sandbox.Status, error) {
	if sandbox.ValidateName(probe.Platform) != nil || len(probe.Platform) > 32 || len(probe.EffectiveIdentity) == 0 || len(probe.EffectiveIdentity) > 256 || !utf8.ValidString(probe.EffectiveIdentity) || strings.ContainsAny(probe.EffectiveIdentity, `/\`) || strings.ContainsFunc(probe.EffectiveIdentity, unicode.IsControl) {
		return sandbox.Status{}, fmt.Errorf("%w: probe metadata", sandbox.ErrInvalid)
	}
	for feature, reason := range probe.Reasons {
		if !KnownFeature(feature) || sandbox.ValidateName(reason) != nil {
			return sandbox.Status{}, fmt.Errorf("%w: probe reason", sandbox.ErrInvalid)
		}
	}
	if mode == "" {
		mode = sandbox.Auto
	}
	result := sandbox.Status{Mode: mode, Profile: probe.Platform + "-subprogram-v1", EffectiveIdentity: probe.EffectiveIdentity, State: sandbox.Preparing}
	if mode != sandbox.Off && mode != sandbox.Auto && mode != sandbox.Strict {
		return result, fmt.Errorf("%w: mode", sandbox.ErrInvalid)
	}
	requiredSet := map[sandbox.Feature]bool{}
	for _, feature := range required {
		if !KnownFeature(feature) || requiredSet[feature] {
			return result, fmt.Errorf("%w: required feature", sandbox.ErrInvalid)
		}
		requiredSet[feature] = true
	}
	for feature := range probe.Available {
		if !KnownFeature(feature) {
			return result, fmt.Errorf("%w: probe feature", sandbox.ErrInvalid)
		}
	}
	if mode == sandbox.Off && len(required) > 0 {
		return result, fmt.Errorf("%w: off with required features", sandbox.ErrDenied)
	}
	supported := probe.Platform == "linux" || probe.Platform == "windows"
	if mode == sandbox.Strict && !supported {
		return result, fmt.Errorf("%w: strict OS profile", sandbox.ErrUnavailable)
	}
	strictSatisfied, full := supported, true
	for _, feature := range features {
		// Native Windows v1 excludes exact filesystem access and private networking.
		excluded := probe.Platform == "windows" && (feature == sandbox.FilesystemAccess || feature == sandbox.ExactFilesystemView || feature == sandbox.PrivateNetwork)
		item := sandbox.FeatureResult{Feature: feature}
		switch {
		case mode == sandbox.Off:
			item.State = sandbox.Disabled
			item.Reason = "protected-mode-off"
			strictSatisfied = false
			full = false
		case probe.Available[feature]:
			item.State = sandbox.Applied
			item.Reason = "qualified-backend"
		case requiredSet[feature] || mode == sandbox.Strict && !excluded:
			return result, fmt.Errorf("%w: required %s", sandbox.ErrUnavailable, feature)
		case excluded:
			item.State = sandbox.ExcludedOS
			item.Reason = "windows-native-v1-exclusion"
			full = false
		default:
			item.State = sandbox.OmittedAuto
			item.Reason = probe.Reasons[feature]
			if item.Reason == "" {
				item.Reason = "backend-unavailable"
			}
			strictSatisfied = false
			full = false
		}
		result.Features = append(result.Features, item)
		if item.State != sandbox.Applied {
			result.Warnings = append(result.Warnings, string(feature)+": "+item.Reason)
		}
	}
	result.StrictSatisfied = strictSatisfied
	result.FullBoundary = full
	if mode == sandbox.Auto && !probe.Available[sandbox.PrivilegeRemoval] {
		result.Warnings = append(result.Warnings, "WARNING: child retains launch authority; identity: "+probe.EffectiveIdentity)
	}
	return result, nil
}

// Verify checks the complete application record, including selected extensions.
// A partial record is not proof that the plan was applied.
func Verify(plan sandbox.Status, applied []sandbox.Feature) error {
	if len(plan.Features) != len(features) || (plan.Mode != sandbox.Off && plan.Mode != sandbox.Auto && plan.Mode != sandbox.Strict) {
		return fmt.Errorf("%w: invalid plan", sandbox.ErrLaunch)
	}
	validated := map[sandbox.Feature]bool{}
	for _, item := range plan.Features {
		if !KnownFeature(item.Feature) || validated[item.Feature] {
			return fmt.Errorf("%w: invalid plan feature", sandbox.ErrLaunch)
		}
		validated[item.Feature] = true
		switch item.State {
		case sandbox.Applied:
			if plan.Mode == sandbox.Off {
				return fmt.Errorf("%w: applied feature in off plan", sandbox.ErrLaunch)
			}
		case sandbox.Disabled:
			if plan.Mode != sandbox.Off {
				return fmt.Errorf("%w: disabled feature in active plan", sandbox.ErrLaunch)
			}
		case sandbox.OmittedAuto:
			if plan.Mode != sandbox.Auto {
				return fmt.Errorf("%w: omission in non-auto plan", sandbox.ErrLaunch)
			}
		case sandbox.ExcludedOS:
			excluded := item.Feature == sandbox.FilesystemAccess || item.Feature == sandbox.ExactFilesystemView || item.Feature == sandbox.PrivateNetwork
			if plan.Mode == sandbox.Off || !excluded || item.Reason != "windows-native-v1-exclusion" {
				return fmt.Errorf("%w: invalid OS exclusion", sandbox.ErrLaunch)
			}
		default:
			return fmt.Errorf("%w: invalid feature state", sandbox.ErrLaunch)
		}
	}
	expected := map[sandbox.Feature]bool{}
	for _, item := range plan.Features {
		if item.State == sandbox.Applied {
			expected[item.Feature] = true
		}
	}
	seen := map[sandbox.Feature]bool{}
	for _, feature := range applied {
		if !expected[feature] || seen[feature] {
			return fmt.Errorf("%w: unexpected verification feature", sandbox.ErrLaunch)
		}
		seen[feature] = true
	}
	if len(seen) != len(expected) {
		return fmt.Errorf("%w: incomplete verification", sandbox.ErrLaunch)
	}
	return nil
}
