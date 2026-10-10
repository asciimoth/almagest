// Package qualification validates complete-profile isolation evidence.
//
// It is intentionally separate from daemon startup. A component test, skipped
// native case, or incomplete feature list cannot make a profile available.
package qualification

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
)

const (
	SchemaVersion    = 1
	MaxEvidenceBytes = 256 << 10
	MinimumSoakSecs  = 30 * 60
)

var ErrUnqualified = errors.New("isolation profile is not qualified")

type Platform struct {
	OS           string `json:"os"`
	Architecture string `json:"architecture"`
	Release      string `json:"release"`
}

type Launch struct {
	CredentialClass string   `json:"credential_class"`
	ProtectedDigest string   `json:"protected_digest"`
	Restrictions    []string `json:"restrictions"`
}

type Outcome string

const (
	OutcomePass       Outcome = "pass"
	OutcomeLimitation Outcome = "native_limitation"
)

type ModeResult struct {
	Outcome    Outcome `json:"outcome"`
	Limitation string  `json:"limitation,omitempty"`
}

type FeatureResult struct {
	ID       string     `json:"id"`
	Direct   ModeResult `json:"direct"`
	Isolated ModeResult `json:"isolated"`
}

type CheckResult struct {
	ID     string `json:"id"`
	Passed bool   `json:"passed"`
}

type Evidence struct {
	SchemaVersion      uint32          `json:"schema_version"`
	Profile            string          `json:"profile"`
	Platform           Platform        `json:"platform"`
	SourceRevision     string          `json:"source_revision"`
	DirectLaunch       Launch          `json:"direct_launch"`
	IsolatedLaunch     Launch          `json:"isolated_launch"`
	Features           []FeatureResult `json:"features"`
	BoundaryChecks     []CheckResult   `json:"boundary_checks"`
	RestrictionChecks  []CheckResult   `json:"restriction_checks"`
	NativeChecks       []CheckResult   `json:"native_checks"`
	RuntimeThreadCount uint32          `json:"runtime_thread_count"`
	OverloadSeconds    uint32          `json:"overload_seconds"`
	ExperimentalAbsent bool            `json:"experimental_absent"`
	UnexpectedHandles  uint32          `json:"unexpected_handles"`
}

type Manifest struct {
	Profile           string
	Platform          Platform
	Features          []string
	BoundaryChecks    []string
	RestrictionChecks []string
	NativeChecks      []string
}

var commonFeatures = []string{
	"regular_tun", "default_tun", "native_tcp_listeners", "userspace_tcp_listeners",
	"udp_metadata", "outbound_tcp", "outbound_udp", "dns_application", "dns_host_control",
	"rules_and_completion", "process_matching", "node_commands", "backend_callbacks",
	"admin_http_and_stream", "config_transactions", "snapshot_create_and_startup_restore",
	"snapshot_live_restore_and_rollback", "logging_normal_and_overload", "crash_recovery",
	"missing_tun_keeps_unrelated_features", "direct_mode_has_no_internal_ipc",
}

var commonBoundaryChecks = []string{
	"arbitrary_runtime_code", "identity_forgery", "path_escape", "policy_alteration",
	"host_handle_acquisition", "foreign_resource_access", "arbitrary_command_execution",
	"broker_exhaustion", "all_runtime_threads_restricted", "unexpected_inherited_handles",
	"host_file_access", "host_native_socket_access", "broker_process_or_token_access",
	"callback_origin_forgery", "malicious_regular_config_restart", "control_progress_under_overload",
}

var commonRestrictionChecks = []string{
	"deny_default_route", "deny_host_dns", "deny_commands", "resource_limits",
}

var manifests = map[string]Manifest{
	"linux-amd64-v1": {
		Profile:  "linux-amd64-v1",
		Platform: Platform{OS: "linux", Architecture: "amd64"},
		Features: commonFeatures, BoundaryChecks: commonBoundaryChecks,
		RestrictionChecks: commonRestrictionChecks,
		NativeChecks:      []string{"low_high_ephemeral_wildcard_bind", "ipv4_ipv6", "tun_packet_flow", "route_firewall_dns_cleanup", "matcher_lifecycle", "namespace_and_capability_state"},
	},
	"windows-server-2022-amd64-v1": {
		Profile:  "windows-server-2022-amd64-v1",
		Platform: Platform{OS: "windows", Architecture: "amd64", Release: "windows-server-2022"},
		Features: commonFeatures, BoundaryChecks: commonBoundaryChecks,
		RestrictionChecks: commonRestrictionChecks,
		NativeChecks:      []string{"low_high_ephemeral_wildcard_bind", "ipv4_ipv6", "tun_packet_flow", "route_wfp_dns_cleanup", "matcher_lifecycle", "service_network_role", "appcontainer_all_threads", "native_flow_qualification"},
	},
}

func Profiles() []string {
	profiles := make([]string, 0, len(manifests))
	for profile := range manifests {
		profiles = append(profiles, profile)
	}
	slices.Sort(profiles)
	return profiles
}

func ProfileManifest(profile string) (Manifest, bool) {
	manifest, ok := manifests[profile]
	if !ok {
		return Manifest{}, false
	}
	manifest.Features = slices.Clone(manifest.Features)
	manifest.BoundaryChecks = slices.Clone(manifest.BoundaryChecks)
	manifest.RestrictionChecks = slices.Clone(manifest.RestrictionChecks)
	manifest.NativeChecks = slices.Clone(manifest.NativeChecks)
	return manifest, true
}

func Decode(data []byte) (Evidence, error) {
	if len(data) == 0 || len(data) > MaxEvidenceBytes {
		return Evidence{}, fmt.Errorf("%w: evidence size", ErrUnqualified)
	}
	if err := rejectDuplicateMembers(data); err != nil {
		return Evidence{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var evidence Evidence
	if err := decoder.Decode(&evidence); err != nil {
		return Evidence{}, fmt.Errorf("%w: decode evidence: %v", ErrUnqualified, err)
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return Evidence{}, fmt.Errorf("%w: trailing JSON value", ErrUnqualified)
	}
	return evidence, nil
}

func Validate(e Evidence) error {
	manifest, ok := manifests[e.Profile]
	if !ok || e.SchemaVersion != SchemaVersion {
		return fmt.Errorf("%w: unknown schema or profile", ErrUnqualified)
	}
	if e.Platform.OS != manifest.Platform.OS || e.Platform.Architecture != manifest.Platform.Architecture ||
		manifest.Platform.Release != "" && e.Platform.Release != manifest.Platform.Release {
		return fmt.Errorf("%w: platform does not match profile", ErrUnqualified)
	}
	if !validToken(e.SourceRevision, 128) || !validToken(e.DirectLaunch.CredentialClass, 128) ||
		e.DirectLaunch.CredentialClass != e.IsolatedLaunch.CredentialClass ||
		!validDigest(e.DirectLaunch.ProtectedDigest) || e.DirectLaunch.ProtectedDigest != e.IsolatedLaunch.ProtectedDigest {
		return fmt.Errorf("%w: launch authority differs between modes", ErrUnqualified)
	}
	if len(e.DirectLaunch.Restrictions) != 0 || len(e.IsolatedLaunch.Restrictions) != 0 {
		return fmt.Errorf("%w: baseline parity includes optional restrictions", ErrUnqualified)
	}
	if err := validateFeatures(manifest.Features, e.Features); err != nil {
		return err
	}
	if err := validateChecks("boundary", manifest.BoundaryChecks, e.BoundaryChecks); err != nil {
		return err
	}
	if err := validateChecks("restriction", manifest.RestrictionChecks, e.RestrictionChecks); err != nil {
		return err
	}
	if err := validateChecks("native", manifest.NativeChecks, e.NativeChecks); err != nil {
		return err
	}
	if e.RuntimeThreadCount < 8 || e.OverloadSeconds < MinimumSoakSecs || !e.ExperimentalAbsent || e.UnexpectedHandles != 0 {
		return fmt.Errorf("%w: hostile-runtime qualification is incomplete", ErrUnqualified)
	}
	return nil
}

func validateFeatures(required []string, got []FeatureResult) error {
	if len(got) != len(required) {
		return fmt.Errorf("%w: feature inventory is incomplete", ErrUnqualified)
	}
	want := make(map[string]struct{}, len(required))
	for _, id := range required {
		want[id] = struct{}{}
	}
	seen := make(map[string]struct{}, len(got))
	for _, result := range got {
		if _, ok := want[result.ID]; !ok {
			return fmt.Errorf("%w: unexpected feature %q", ErrUnqualified, result.ID)
		}
		if _, duplicate := seen[result.ID]; duplicate {
			return fmt.Errorf("%w: duplicate feature %q", ErrUnqualified, result.ID)
		}
		seen[result.ID] = struct{}{}
		switch {
		case result.Direct.Outcome == OutcomePass && result.Isolated.Outcome == OutcomePass && result.Direct.Limitation == "" && result.Isolated.Limitation == "":
		case result.Direct.Outcome == OutcomeLimitation && result.Isolated.Outcome == OutcomeLimitation &&
			validToken(result.Direct.Limitation, 256) && result.Direct.Limitation == result.Isolated.Limitation:
		default:
			return fmt.Errorf("%w: isolated-only loss or invalid result for %q", ErrUnqualified, result.ID)
		}
	}
	return nil
}

func validateChecks(kind string, required []string, got []CheckResult) error {
	if len(got) != len(required) {
		return fmt.Errorf("%w: %s checks are incomplete", ErrUnqualified, kind)
	}
	want := make(map[string]struct{}, len(required))
	for _, id := range required {
		want[id] = struct{}{}
	}
	seen := make(map[string]struct{}, len(got))
	for _, result := range got {
		if _, ok := want[result.ID]; !ok || !result.Passed {
			return fmt.Errorf("%w: %s check %q did not pass", ErrUnqualified, kind, result.ID)
		}
		if _, duplicate := seen[result.ID]; duplicate {
			return fmt.Errorf("%w: duplicate %s check %q", ErrUnqualified, kind, result.ID)
		}
		seen[result.ID] = struct{}{}
	}
	return nil
}

func validToken(value string, limit int) bool {
	return value != "" && len(value) <= limit && strings.TrimSpace(value) == value && !strings.ContainsAny(value, "\x00\r\n")
}

func validDigest(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, c := range value {
		if c < '0' || c > '9' && c < 'a' || c > 'f' {
			return false
		}
	}
	return true
}

func rejectDuplicateMembers(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	var walk func(int) error
	walk = func(depth int) error {
		if depth > 64 {
			return errors.New("JSON nesting exceeds evidence limit")
		}
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		delim, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		switch delim {
		case '{':
			seen := make(map[string]struct{})
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					return err
				}
				key, ok := keyToken.(string)
				if !ok {
					return errors.New("object key is not a string")
				}
				if _, duplicate := seen[key]; duplicate {
					return fmt.Errorf("duplicate object member %q", key)
				}
				seen[key] = struct{}{}
				if err := walk(depth + 1); err != nil {
					return err
				}
			}
			_, err = decoder.Token()
			return err
		case '[':
			for decoder.More() {
				if err := walk(depth + 1); err != nil {
					return err
				}
			}
			_, err = decoder.Token()
			return err
		default:
			return errors.New("unexpected JSON delimiter")
		}
	}
	if err := walk(0); err != nil {
		return fmt.Errorf("%w: invalid JSON: %v", ErrUnqualified, err)
	}
	return nil
}
