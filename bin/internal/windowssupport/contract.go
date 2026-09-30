// Package windowssupport validates the Windows support and qualification
// contracts. It does not select or construct a networking backend.
package windowssupport

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
)

const contractSchemaVersion = 1

const (
	windowsBackendModule  = "github.com/asciimoth/sysnet-windows"
	windowsBackendVersion = "v0.1.1-0.20260930092946-a056c520f0fa"
)

var validStatuses = []string{"supported", "unavailable", "unsupported"}

// SupportMatrix records Windows platforms, deployment dependencies, and the
// Almagest features that map to backend capabilities.
type SupportMatrix struct {
	SchemaVersion int          `json:"schemaVersion"`
	Dependencies  []Dependency `json:"dependencies"`
	Platforms     []Platform   `json:"platforms"`
	Features      []Feature    `json:"features"`
}

// Dependency is a deployment input used by one or more features.
type Dependency struct {
	ID          string `json:"id"`
	Description string `json:"description"`
}

// Platform is one independently qualified Windows and architecture pair.
type Platform struct {
	ID             string   `json:"id"`
	OS             string   `json:"os"`
	Architecture   string   `json:"architecture"`
	Status         string   `json:"status"`
	Reason         string   `json:"reason"`
	Dependencies   []string `json:"dependencies"`
	RequiredSuites []string `json:"requiredSuites"`
}

// Feature maps an Almagest operation, rule, menu, or daemon function to its
// Windows status and qualification requirements.
type Feature struct {
	ID             string   `json:"id"`
	Area           string   `json:"area"`
	Status         string   `json:"status"`
	Mapping        string   `json:"mapping"`
	Platforms      []string `json:"platforms"`
	Dependencies   []string `json:"dependencies"`
	RequiredSuites []string `json:"requiredSuites"`
	Notes          string   `json:"notes"`
}

// TestManifest lists tests that evidence must report for each suite.
type TestManifest struct {
	SchemaVersion int         `json:"schemaVersion"`
	Suites        []TestSuite `json:"suites"`
}

// TestSuite identifies where a suite executes. Only native-windows suites can
// qualify a Windows platform.
type TestSuite struct {
	ID            string   `json:"id"`
	Execution     string   `json:"execution"`
	RequiredTests []string `json:"requiredTests"`
}

// DeploymentContract records immutable native inputs, the boundary between
// deployment and the daemon, and the identity needed for each operation.
type DeploymentContract struct {
	SchemaVersion int                  `json:"schemaVersion"`
	Backend       ModuleDependency     `json:"backend"`
	Wintun        WintunInput          `json:"wintun"`
	SplitDriver   SplitDriverInput     `json:"splitDriver"`
	Provisioning  ProvisioningBoundary `json:"provisioning"`
	Privileges    []PrivilegeBoundary  `json:"privileges"`
	Degradation   DegradationContract  `json:"degradation"`
}

// ModuleDependency identifies the exact Windows backend source.
type ModuleDependency struct {
	Module   string `json:"module"`
	Version  string `json:"version"`
	Revision string `json:"revision"`
	License  string `json:"license"`
}

// WintunInput identifies the deployable Wintun archive and DLL load boundary.
type WintunInput struct {
	Version          string   `json:"version"`
	Archive          string   `json:"archive"`
	Source           string   `json:"source"`
	SHA256           string   `json:"sha256"`
	Architectures    []string `json:"architectures"`
	TrustedLocations []string `json:"trustedLocations"`
	License          string   `json:"license"`
}

// SplitDriverInput identifies the compatible signed split-driver deployment.
type SplitDriverInput struct {
	Version          string                     `json:"version"`
	UpstreamRevision string                     `json:"upstreamRevision"`
	BinariesRevision string                     `json:"binariesRevision"`
	ServiceName      string                     `json:"serviceName"`
	ServiceType      string                     `json:"serviceType"`
	StartType        string                     `json:"startType"`
	BinaryPath       string                     `json:"binaryPath"`
	Architectures    map[string]DriverArtifacts `json:"architectures"`
	License          string                     `json:"license"`
}

// DriverArtifacts records package hashes for one architecture.
type DriverArtifacts struct {
	Source string            `json:"source"`
	Files  map[string]string `json:"files"`
}

// ProvisioningBoundary makes daemon-owned deployment actions explicit.
type ProvisioningBoundary struct {
	Owner               string `json:"owner"`
	DaemonDownloads     bool   `json:"daemonDownloads"`
	DaemonInstalls      bool   `json:"daemonInstalls"`
	DaemonUpdates       bool   `json:"daemonUpdates"`
	DaemonRegisters     bool   `json:"daemonRegistersService"`
	DaemonStarts        bool   `json:"daemonStartsService"`
	BackendVerification string `json:"backendVerification"`
}

// PrivilegeBoundary identifies the minimum identity for an operation group.
type PrivilegeBoundary struct {
	ID                string   `json:"id"`
	Operations        []string `json:"operations"`
	MinimumIdentity   string   `json:"minimumIdentity"`
	ElevationRequired bool     `json:"elevationRequired"`
}

// DegradationContract records the required behavior when split support is absent.
type DegradationContract struct {
	MissingSplitDriverAffects []string `json:"missingSplitDriverAffects"`
	MustRemainAvailable       []string `json:"mustRemainAvailable"`
}

// QualificationEvidence is the result for one native platform execution.
type QualificationEvidence struct {
	SchemaVersion   int                      `json:"schemaVersion"`
	PlatformID      string                   `json:"platformId"`
	OS              string                   `json:"os"`
	Architecture    string                   `json:"architecture"`
	NativeExecution bool                     `json:"nativeExecution"`
	Suites          map[string]SuiteEvidence `json:"suites"`
}

// SuiteEvidence records the suite outcome and each required test outcome.
type SuiteEvidence struct {
	Outcome string            `json:"outcome"`
	Tests   map[string]string `json:"tests"`
}

// DecodeSupportMatrix decodes a matrix and rejects unknown fields.
func DecodeSupportMatrix(data []byte) (SupportMatrix, error) {
	var matrix SupportMatrix
	if err := decodeStrict(data, &matrix); err != nil {
		return SupportMatrix{}, fmt.Errorf("decode support matrix: %w", err)
	}
	return matrix, nil
}

// DecodeTestManifest decodes a manifest and rejects unknown fields.
func DecodeTestManifest(data []byte) (TestManifest, error) {
	var manifest TestManifest
	if err := decodeStrict(data, &manifest); err != nil {
		return TestManifest{}, fmt.Errorf("decode test manifest: %w", err)
	}
	return manifest, nil
}

// DecodeDeploymentContract decodes a deployment contract and rejects unknown fields.
func DecodeDeploymentContract(data []byte) (DeploymentContract, error) {
	var contract DeploymentContract
	if err := decodeStrict(data, &contract); err != nil {
		return DeploymentContract{}, fmt.Errorf("decode deployment contract: %w", err)
	}
	return contract, nil
}

// ValidateDeploymentContract checks immutable inputs and security boundaries.
func ValidateDeploymentContract(contract DeploymentContract) error {
	if contract.SchemaVersion != contractSchemaVersion {
		return fmt.Errorf("deployment contract schema version %d, want %d", contract.SchemaVersion, contractSchemaVersion)
	}
	if contract.Backend.Module != windowsBackendModule || contract.Backend.Version != windowsBackendVersion {
		return fmt.Errorf("backend module must be %s at %s", windowsBackendModule, windowsBackendVersion)
	}
	if contract.Backend.Revision != "a056c520f0faf966f54dd90ef63b15d9aa7382ec" || contract.Backend.License == "" {
		return errors.New("backend revision and license must match the inspected dependency")
	}
	if contract.Wintun.Version != "0.14.1" || contract.Wintun.Archive != "wintun-0.14.1.zip" || contract.Wintun.SHA256 != "07c256185d6ee3652e09fa55c0b673e2624b565e02c4b9091c79ca7d2f24ef51" {
		return errors.New("wintun version, archive, and SHA-256 must match the inspected dependency")
	}
	if err := exactStrings(contract.Wintun.Architectures, []string{"amd64", "arm64"}, "Wintun architectures"); err != nil {
		return err
	}
	if err := exactStrings(contract.Wintun.TrustedLocations, []string{"application-directory", "System32"}, "Wintun trusted locations"); err != nil {
		return err
	}
	if contract.Wintun.Source == "" || contract.Wintun.License == "" {
		return errors.New("wintun source and license must not be empty")
	}
	if contract.SplitDriver.Version != "1.3.0.0" || contract.SplitDriver.ServiceName != "mullvad-split-tunnel" || contract.SplitDriver.ServiceType != "kernel-driver" || contract.SplitDriver.StartType != "demand-start" {
		return errors.New("split-driver service identity does not match the inspected dependency")
	}
	if contract.SplitDriver.UpstreamRevision != "0a0eb97f67d1dbcb3d08bda66d3b24f465d95475" || contract.SplitDriver.BinariesRevision != "5b6f46cde692acb77ee74b37b9fd3f1678c45a52" {
		return errors.New("split-driver revisions do not match the inspected dependency")
	}
	if contract.SplitDriver.BinaryPath != `%SystemRoot%\System32\drivers\mullvad-split-tunnel.sys` || contract.SplitDriver.License == "" {
		return errors.New("split-driver path and license must match the deployment contract")
	}
	if err := validateDriverArtifacts(contract.SplitDriver.Architectures); err != nil {
		return err
	}
	boundary := contract.Provisioning
	if boundary.Owner != "external-deployment-tooling" || boundary.DaemonDownloads || boundary.DaemonInstalls || boundary.DaemonUpdates || boundary.DaemonRegisters || boundary.DaemonStarts {
		return errors.New("daemon must not provision or manage dependency installation")
	}
	if boundary.BackendVerification == "" {
		return errors.New("backend verification contract must not be empty")
	}
	if err := validatePrivileges(contract.Privileges); err != nil {
		return err
	}
	if err := exactStrings(contract.Degradation.MissingSplitDriverAffects, []string{"win-exe-tree-exclusions"}, "split-driver affected features"); err != nil {
		return err
	}
	return exactStrings(contract.Degradation.MustRemainAvailable, []string{"regular-tun", "default-routing-without-exclusions", "outnet", "outdns", "localnet", "win-pid-matcher", "win-exe-path-matcher"}, "split-driver independent features")
}

func validateDriverArtifacts(values map[string]DriverArtifacts) error {
	want := map[string]DriverArtifacts{
		"amd64": {Source: "x86_64-pc-windows-msvc", Files: map[string]string{
			"mullvad-split-tunnel.sys": "10cf25bbcfe51fd663a1fec88a98e9b858f3a579589bb2ec496b66e4fdd1b201",
			"mullvad-split-tunnel.inf": "3dd5905e5fb98d61a942a33e8c9a5ba07c3a2de1e4f319e1fec3e54df6591608",
			"mullvad-split-tunnel.cat": "c599926a0327d7ae06b534f4cd039db30392e1897bb9d03e4fec3631744a4e6d",
		}},
		"arm64": {Source: "aarch64-pc-windows-msvc", Files: map[string]string{
			"mullvad-split-tunnel.sys": "6af8b3bfe5aa095d5276187558c7c7d3a3e0c174b34406cd6c4b3f8e6ffa6534",
			"mullvad-split-tunnel.inf": "0bfdb044e40535dabbeb3620b655c1563714b3a6f3231d3492c972c6e8dea6f1",
			"mullvad-split-tunnel.cat": "c3d27636739ebaa7dde369d113347de83e3e2173b8a512bb8863d11b148de7ce",
		}},
	}
	if len(values) != 2 {
		return errors.New("split-driver artifacts must contain amd64 and arm64")
	}
	for _, architecture := range []string{"amd64", "arm64"} {
		artifacts, exists := values[architecture]
		if !exists || artifacts.Source != want[architecture].Source || len(artifacts.Files) != 3 {
			return fmt.Errorf("split-driver artifacts for %s are incomplete", architecture)
		}
		for _, name := range []string{"mullvad-split-tunnel.sys", "mullvad-split-tunnel.inf", "mullvad-split-tunnel.cat"} {
			if artifacts.Files[name] != want[architecture].Files[name] {
				return fmt.Errorf("split-driver artifact %s/%s does not match its pinned SHA-256", architecture, name)
			}
		}
	}
	return nil
}

func validatePrivileges(values []PrivilegeBoundary) error {
	want := map[string]bool{"read-only-and-userspace": false, "native-network-mutation": true, "dependency-provisioning": true}
	if len(values) != len(want) {
		return errors.New("privilege contract must define all operation groups")
	}
	for _, value := range values {
		elevated, exists := want[value.ID]
		if !exists || len(value.Operations) == 0 || value.MinimumIdentity == "" || value.ElevationRequired != elevated {
			return fmt.Errorf("invalid privilege boundary %q", value.ID)
		}
		delete(want, value.ID)
	}
	return nil
}

func exactStrings(got, want []string, label string) error {
	if !slices.Equal(got, want) {
		return fmt.Errorf("%s = %v, want %v", label, got, want)
	}
	return nil
}

// DecodeQualificationEvidence decodes evidence and rejects unknown fields.
func DecodeQualificationEvidence(data []byte) (QualificationEvidence, error) {
	var evidence QualificationEvidence
	if err := decodeStrict(data, &evidence); err != nil {
		return QualificationEvidence{}, fmt.Errorf("decode qualification evidence: %w", err)
	}
	return evidence, nil
}

func decodeStrict(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err != nil {
			return err
		}
		return errors.New("multiple JSON values")
	}
	return nil
}

// ValidateContracts checks referential integrity and prevents portable or
// cross-build suites from being used as native qualification evidence.
func ValidateContracts(matrix SupportMatrix, manifest TestManifest) error {
	if matrix.SchemaVersion != contractSchemaVersion {
		return fmt.Errorf("support matrix schema version %d, want %d", matrix.SchemaVersion, contractSchemaVersion)
	}
	if manifest.SchemaVersion != contractSchemaVersion {
		return fmt.Errorf("test manifest schema version %d, want %d", manifest.SchemaVersion, contractSchemaVersion)
	}

	dependencies, err := dependencyIndex(matrix.Dependencies)
	if err != nil {
		return err
	}
	suites, err := suiteIndex(manifest.Suites)
	if err != nil {
		return err
	}
	platforms, err := platformIndex(matrix.Platforms, dependencies, suites)
	if err != nil {
		return err
	}
	if err := validateFeatures(matrix.Features, dependencies, platforms, suites); err != nil {
		return err
	}
	return nil
}

func dependencyIndex(values []Dependency) (map[string]Dependency, error) {
	if len(values) == 0 {
		return nil, errors.New("support matrix has no dependencies")
	}
	result := make(map[string]Dependency, len(values))
	for _, dependency := range values {
		if dependency.ID == "" || dependency.Description == "" {
			return nil, errors.New("dependency id and description must not be empty")
		}
		if _, exists := result[dependency.ID]; exists {
			return nil, fmt.Errorf("duplicate dependency %q", dependency.ID)
		}
		result[dependency.ID] = dependency
	}
	return result, nil
}

func suiteIndex(values []TestSuite) (map[string]TestSuite, error) {
	if len(values) == 0 {
		return nil, errors.New("test manifest has no suites")
	}
	result := make(map[string]TestSuite, len(values))
	for _, suite := range values {
		if suite.ID == "" || !slices.Contains([]string{"portable", "cross-build", "native-windows"}, suite.Execution) {
			return nil, fmt.Errorf("suite %q has invalid execution %q", suite.ID, suite.Execution)
		}
		if len(suite.RequiredTests) == 0 {
			return nil, fmt.Errorf("suite %q has no required tests", suite.ID)
		}
		if err := uniqueNonempty(suite.RequiredTests, "required test in suite "+suite.ID); err != nil {
			return nil, err
		}
		if _, exists := result[suite.ID]; exists {
			return nil, fmt.Errorf("duplicate suite %q", suite.ID)
		}
		result[suite.ID] = suite
	}
	return result, nil
}

func platformIndex(values []Platform, dependencies map[string]Dependency, suites map[string]TestSuite) (map[string]Platform, error) {
	if len(values) == 0 {
		return nil, errors.New("support matrix has no platforms")
	}
	result := make(map[string]Platform, len(values))
	for _, platform := range values {
		if platform.ID == "" || platform.OS == "" {
			return nil, errors.New("platform id and OS must not be empty")
		}
		if platform.Architecture != "amd64" && platform.Architecture != "arm64" {
			return nil, fmt.Errorf("platform %q has invalid architecture %q", platform.ID, platform.Architecture)
		}
		if !slices.Contains(validStatuses, platform.Status) {
			return nil, fmt.Errorf("platform %q has invalid status %q", platform.ID, platform.Status)
		}
		if platform.Status != "supported" && platform.Reason == "" {
			return nil, fmt.Errorf("platform %q has no status reason", platform.ID)
		}
		if err := validateDependencies(platform.Dependencies, dependencies, "platform "+platform.ID); err != nil {
			return nil, err
		}
		if len(platform.RequiredSuites) == 0 {
			return nil, fmt.Errorf("platform %q has no required native suites", platform.ID)
		}
		if err := validateNativeSuites(platform.RequiredSuites, suites, "platform "+platform.ID); err != nil {
			return nil, err
		}
		if _, exists := result[platform.ID]; exists {
			return nil, fmt.Errorf("duplicate platform %q", platform.ID)
		}
		result[platform.ID] = platform
	}
	return result, nil
}

func validateFeatures(values []Feature, dependencies map[string]Dependency, platforms map[string]Platform, suites map[string]TestSuite) error {
	if len(values) == 0 {
		return errors.New("support matrix has no features")
	}
	seen := make(map[string]struct{}, len(values))
	seenAreas := make(map[string]struct{}, 4)
	for _, feature := range values {
		if feature.ID == "" || feature.Mapping == "" || feature.Notes == "" {
			return errors.New("feature id, mapping, and notes must not be empty")
		}
		if !slices.Contains([]string{"node-operation", "rule", "menu", "daemon"}, feature.Area) {
			return fmt.Errorf("feature %q has invalid area %q", feature.ID, feature.Area)
		}
		seenAreas[feature.Area] = struct{}{}
		if !slices.Contains(validStatuses, feature.Status) {
			return fmt.Errorf("feature %q has invalid status %q", feature.ID, feature.Status)
		}
		if _, exists := seen[feature.ID]; exists {
			return fmt.Errorf("duplicate feature %q", feature.ID)
		}
		seen[feature.ID] = struct{}{}
		if err := validateDependencies(feature.Dependencies, dependencies, "feature "+feature.ID); err != nil {
			return err
		}
		if len(feature.Platforms) == 0 {
			return fmt.Errorf("feature %q has no platforms", feature.ID)
		}
		if err := uniqueNonempty(feature.Platforms, "platform in feature "+feature.ID); err != nil {
			return err
		}
		for _, platform := range feature.Platforms {
			if _, exists := platforms[platform]; !exists {
				return fmt.Errorf("feature %q refers to unknown platform %q", feature.ID, platform)
			}
		}
		if len(feature.RequiredSuites) == 0 {
			return fmt.Errorf("feature %q has no required native suites", feature.ID)
		}
		if err := validateNativeSuites(feature.RequiredSuites, suites, "feature "+feature.ID); err != nil {
			return err
		}
	}
	for _, area := range []string{"node-operation", "rule", "menu", "daemon"} {
		if _, exists := seenAreas[area]; !exists {
			return fmt.Errorf("support matrix has no %s feature", area)
		}
	}
	return nil
}

func validateDependencies(values []string, dependencies map[string]Dependency, owner string) error {
	if err := uniqueNonempty(values, "dependency in "+owner); err != nil {
		return err
	}
	for _, dependency := range values {
		if _, exists := dependencies[dependency]; !exists {
			return fmt.Errorf("%s refers to unknown dependency %q", owner, dependency)
		}
	}
	return nil
}

func validateNativeSuites(values []string, suites map[string]TestSuite, owner string) error {
	if err := uniqueNonempty(values, "suite in "+owner); err != nil {
		return err
	}
	for _, suiteID := range values {
		suite, exists := suites[suiteID]
		if !exists {
			return fmt.Errorf("%s refers to unknown suite %q", owner, suiteID)
		}
		if suite.Execution != "native-windows" {
			return fmt.Errorf("%s uses non-native suite %q (%s)", owner, suiteID, suite.Execution)
		}
	}
	return nil
}

func uniqueNonempty(values []string, label string) error {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%s must not be empty", label)
		}
		if _, exists := seen[value]; exists {
			return fmt.Errorf("duplicate %s %q", label, value)
		}
		seen[value] = struct{}{}
	}
	return nil
}

// ValidateQualificationEvidence verifies all native suites and required test
// outcomes for one exact OS and architecture. Missing, skipped, and cross-built
// results do not qualify support.
func ValidateQualificationEvidence(matrix SupportMatrix, manifest TestManifest, evidence QualificationEvidence) error {
	if err := ValidateContracts(matrix, manifest); err != nil {
		return fmt.Errorf("contracts: %w", err)
	}
	if evidence.SchemaVersion != contractSchemaVersion {
		return fmt.Errorf("evidence schema version %d, want %d", evidence.SchemaVersion, contractSchemaVersion)
	}
	platforms := make(map[string]Platform, len(matrix.Platforms))
	for _, platform := range matrix.Platforms {
		platforms[platform.ID] = platform
	}
	platform, exists := platforms[evidence.PlatformID]
	if !exists {
		return fmt.Errorf("evidence refers to unknown platform %q", evidence.PlatformID)
	}
	if evidence.OS != platform.OS || evidence.Architecture != platform.Architecture {
		return fmt.Errorf("evidence platform is %s/%s, want %s/%s", evidence.OS, evidence.Architecture, platform.OS, platform.Architecture)
	}
	if !evidence.NativeExecution {
		return errors.New("evidence is not from native execution")
	}
	suites, _ := suiteIndex(manifest.Suites)
	for _, suiteID := range platform.RequiredSuites {
		required := suites[suiteID]
		actual, exists := evidence.Suites[suiteID]
		if !exists {
			return fmt.Errorf("required suite %q is absent", suiteID)
		}
		if actual.Outcome != "passed" {
			return fmt.Errorf("required suite %q outcome is %q, want passed", suiteID, actual.Outcome)
		}
		for _, testID := range required.RequiredTests {
			outcome, exists := actual.Tests[testID]
			if !exists {
				return fmt.Errorf("required test %q is absent from suite %q", testID, suiteID)
			}
			if outcome != "passed" {
				return fmt.Errorf("required test %q in suite %q outcome is %q, want passed", testID, suiteID, outcome)
			}
		}
	}
	return nil
}
