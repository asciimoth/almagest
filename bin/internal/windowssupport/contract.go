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

const windowsBackendModule = "github.com/asciimoth/sysnet-windows"

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

// QualificationEvidence is the result for one native platform execution.
type QualificationEvidence struct {
	SchemaVersion   int                      `json:"schemaVersion"`
	PlatformID      string                   `json:"platformId"`
	OS              string                   `json:"os"`
	Architecture    string                   `json:"architecture"`
	NativeExecution bool                     `json:"nativeExecution"`
	Identity        QualificationIdentity    `json:"identity"`
	Suites          map[string]SuiteEvidence `json:"suites"`
}

// SuiteEvidence records the suite outcome and each required test outcome.
type SuiteEvidence struct {
	Outcome         string                `json:"outcome"`
	Tests           map[string]string     `json:"tests"`
	Identity        QualificationIdentity `json:"identity"`
	CoverageSHA256  string                `json:"coverageSHA256,omitempty"`
	NonInstrumented bool                  `json:"nonInstrumented,omitempty"`
}

// QualificationIdentity binds every suite to the same source, generated
// inputs, native dependencies, toolchain, and executable images.
type QualificationIdentity struct {
	SourceRevision        string            `json:"sourceRevision"`
	SourceArchiveSHA256   string            `json:"sourceArchiveSHA256"`
	GeneratedInputsSHA256 string            `json:"generatedInputsSHA256"`
	OS                    string            `json:"os"`
	Architecture          string            `json:"architecture"`
	GoVersion             string            `json:"goVersion"`
	Binaries              map[string]string `json:"binaries"`
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
		if err := validateQualificationIdentity(actual.Identity, evidence.Identity, platform); err != nil {
			return fmt.Errorf("required suite %q identity: %w", suiteID, err)
		}
		if suiteID == "windows-native-packet-flow" || suiteID == "windows-native-resource" {
			if !actual.NonInstrumented {
				return fmt.Errorf("required suite %q must record non-instrumented execution", suiteID)
			}
		} else if actual.NonInstrumented || !validSHA256(actual.CoverageSHA256) {
			return fmt.Errorf("required suite %q has no valid coverage artifact", suiteID)
		}
	}
	return validateQualificationIdentity(evidence.Identity, evidence.Identity, platform)
}

func validateQualificationIdentity(actual, expected QualificationIdentity, platform Platform) error {
	if actual.OS != platform.OS || actual.Architecture != platform.Architecture {
		return fmt.Errorf("platform is %s/%s, want %s/%s", actual.OS, actual.Architecture, platform.OS, platform.Architecture)
	}
	if actual.SourceRevision == "" || actual.GoVersion == "" {
		return errors.New("source revision and Go toolchain must be recorded")
	}
	for name, value := range map[string]string{
		"source archive":   actual.SourceArchiveSHA256,
		"generated inputs": actual.GeneratedInputsSHA256,
	} {
		if !validSHA256(value) {
			return fmt.Errorf("%s SHA-256 is missing or invalid", name)
		}
	}
	for _, name := range []string{"almagestd.exe", "almagest-cli.exe"} {
		if !validSHA256(actual.Binaries[name]) {
			return fmt.Errorf("binary identity %q is missing or invalid", name)
		}
	}
	if !qualificationIdentitiesEqual(actual, expected) {
		return errors.New("source, build-input, toolchain, driver, DLL, or binary identity does not match the evidence set")
	}
	return nil
}

func qualificationIdentitiesEqual(left, right QualificationIdentity) bool {
	return left.SourceRevision == right.SourceRevision &&
		left.SourceArchiveSHA256 == right.SourceArchiveSHA256 &&
		left.GeneratedInputsSHA256 == right.GeneratedInputsSHA256 &&
		left.OS == right.OS && left.Architecture == right.Architecture &&
		left.GoVersion == right.GoVersion &&
		left.Binaries["almagestd.exe"] == right.Binaries["almagestd.exe"] &&
		left.Binaries["almagest-cli.exe"] == right.Binaries["almagest-cli.exe"]
}

func validSHA256(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, character := range value {
		if !strings.ContainsRune("0123456789abcdef", character) {
			return false
		}
	}
	return true
}
