package windowssupport

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"slices"
	"strings"
)

// ExecutionManifest defines the native suites that are active at the current
// implementation milestone. It is separate from the final qualification
// manifest because later backend milestones add more required native cases.
type ExecutionManifest struct {
	SchemaVersion           int              `json:"schemaVersion"`
	PlatformID              string           `json:"platformId"`
	OS                      string           `json:"os"`
	Architecture            string           `json:"architecture"`
	GoPackageTimeoutSeconds int              `json:"goPackageTimeoutSeconds"`
	ProgressIntervalSeconds int              `json:"progressIntervalSeconds"`
	Suites                  []ExecutionSuite `json:"suites"`
}

// ExecutionSuite defines one identity and privilege boundary.
type ExecutionSuite struct {
	ID         string          `json:"id"`
	Identity   string          `json:"identity"`
	Privilege  string          `json:"privilege"`
	Serialized bool            `json:"serialized"`
	Cases      []ExecutionCase `json:"cases"`
}

// ExecutionCase is one required result in a native suite.
type ExecutionCase struct {
	ID              string   `json:"id"`
	Kind            string   `json:"kind"`
	Module          string   `json:"module,omitempty"`
	Arguments       []string `json:"arguments,omitempty"`
	EventFile       string   `json:"eventFile,omitempty"`
	RequiredTests   []string `json:"requiredTests,omitempty"`
	StartServices   []string `json:"startServices,omitempty"`
	Coverage        *bool    `json:"coverage,omitempty"`
	PrebuiltTest    string   `json:"prebuiltTest,omitempty"`
	PrebuiltPackage string   `json:"prebuiltPackage,omitempty"`
	PrebuiltRun     string   `json:"prebuiltRun,omitempty"`
}

// ExecutionIdentity records the token and native platform used by a suite.
type ExecutionIdentity struct {
	Name            string `json:"name"`
	SID             string `json:"sid"`
	Elevated        bool   `json:"elevated"`
	NativeExecution bool   `json:"nativeExecution"`
	OS              string `json:"os"`
	Architecture    string `json:"architecture"`
}

// ProcessEvidence records the exit status for each executed Go test case.
type ProcessEvidence struct {
	Cases map[string]int `json:"cases"`
}

// ExecutionSuiteEvidence is the validated artifact for one native suite.
type ExecutionSuiteEvidence struct {
	SchemaVersion int               `json:"schemaVersion"`
	PlatformID    string            `json:"platformId"`
	OS            string            `json:"os"`
	Architecture  string            `json:"architecture"`
	Native        bool              `json:"nativeExecution"`
	Suite         string            `json:"suite"`
	Identity      ExecutionIdentity `json:"identity"`
	Serialized    bool              `json:"serialized"`
	Outcome       string            `json:"outcome"`
	Cases         map[string]string `json:"cases"`
}

// DecodeExecutionManifest decodes an execution manifest and rejects unknown
// fields.
func DecodeExecutionManifest(data []byte) (ExecutionManifest, error) {
	var manifest ExecutionManifest
	if err := decodeStrict(data, &manifest); err != nil {
		return ExecutionManifest{}, fmt.Errorf("decode execution manifest: %w", err)
	}
	return manifest, nil
}

// ValidateExecutionManifest checks the identity split and serial privileged
// execution contract.
func ValidateExecutionManifest(manifest ExecutionManifest) error {
	if manifest.SchemaVersion != contractSchemaVersion {
		return fmt.Errorf("execution manifest schema version %d, want %d", manifest.SchemaVersion, contractSchemaVersion)
	}
	if manifest.PlatformID == "" || manifest.OS == "" || manifest.Architecture == "" {
		return errors.New("execution platform fields must not be empty")
	}
	if manifest.GoPackageTimeoutSeconds <= 0 || manifest.GoPackageTimeoutSeconds > 3600 {
		return errors.New("go package timeout must be between 1 and 3600 seconds")
	}
	if manifest.ProgressIntervalSeconds <= 0 || manifest.ProgressIntervalSeconds >= manifest.GoPackageTimeoutSeconds {
		return errors.New("progress interval must be positive and less than the go package timeout")
	}
	if len(manifest.Suites) != 2 {
		return errors.New("execution manifest must contain ordinary and privileged suites")
	}
	want := map[string]string{"ordinary": "standard", "privileged": "elevated"}
	caseIDs := make(map[string]struct{})
	for _, suite := range manifest.Suites {
		expectedIdentity, exists := want[suite.Privilege]
		if !exists || suite.ID == "" || suite.Identity != expectedIdentity {
			return fmt.Errorf("suite %q has invalid privilege or identity", suite.ID)
		}
		if suite.Privilege == "privileged" && !suite.Serialized {
			return fmt.Errorf("privileged suite %q must be serialized", suite.ID)
		}
		if len(suite.Cases) == 0 {
			return fmt.Errorf("suite %q has no required cases", suite.ID)
		}
		for _, testCase := range suite.Cases {
			if testCase.ID == "" || !slices.Contains([]string{"identity", "go", "serialization"}, testCase.Kind) {
				return fmt.Errorf("suite %q has invalid case %q", suite.ID, testCase.ID)
			}
			if _, exists := caseIDs[testCase.ID]; exists {
				return fmt.Errorf("duplicate execution case %q", testCase.ID)
			}
			caseIDs[testCase.ID] = struct{}{}
			if testCase.Kind == "go" {
				if !slices.Contains([]string{"certcat", "lib", "bin"}, testCase.Module) || testCase.EventFile == "" || filepath.Base(testCase.EventFile) != testCase.EventFile {
					return fmt.Errorf("go case %q has invalid module or event file", testCase.ID)
				}
				if len(testCase.Arguments) == 0 {
					return fmt.Errorf("go case %q has no arguments", testCase.ID)
				}
				prebuiltFields := 0
				for _, value := range []string{testCase.PrebuiltTest, testCase.PrebuiltPackage, testCase.PrebuiltRun} {
					if value != "" {
						prebuiltFields++
					}
				}
				portablePrebuilt := strings.ReplaceAll(testCase.PrebuiltTest, `\`, "/")
				if prebuiltFields != 0 && (prebuiltFields != 3 || filepath.IsAbs(portablePrebuilt) ||
					strings.HasPrefix(filepath.Clean(portablePrebuilt), "../") || filepath.Ext(portablePrebuilt) != ".exe") {
					return fmt.Errorf("go case %q has invalid prebuilt test fields", testCase.ID)
				}
				if testCase.Coverage != nil && !*testCase.Coverage && !slices.Contains(testCase.Arguments, "-fuzz") {
					return fmt.Errorf("go case %q disables coverage without fuzzing", testCase.ID)
				}
				if len(testCase.StartServices) != 0 {
					if suite.Privilege != "privileged" {
						return fmt.Errorf("go case %q starts services outside the privileged suite", testCase.ID)
					}
					if err := uniqueNonempty(testCase.StartServices, "service in "+testCase.ID); err != nil {
						return err
					}
				}
			} else if testCase.Module != "" || testCase.EventFile != "" || len(testCase.Arguments) != 0 || len(testCase.RequiredTests) != 0 || len(testCase.StartServices) != 0 || testCase.Coverage != nil || testCase.PrebuiltTest != "" || testCase.PrebuiltPackage != "" || testCase.PrebuiltRun != "" {
				return fmt.Errorf("non-Go case %q has Go test fields", testCase.ID)
			}
		}
		delete(want, suite.Privilege)
	}
	if len(want) != 0 {
		return errors.New("execution manifest does not contain both privilege boundaries")
	}
	return nil
}

type goTestEvent struct {
	Action  string `json:"Action"`
	Package string `json:"Package"`
	Test    string `json:"Test"`
}

// ValidateGoTestEvents verifies structured go test output. Required test names
// use the package::test form. A missing, skipped, or failed required test makes
// validation fail.
func ValidateGoTestEvents(input io.Reader, required []string) error {
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	startedPackages := make(map[string]bool)
	packagesWithTests := make(map[string]bool)
	packageOutcomes := make(map[string]string)
	testOutcomes := make(map[string]string)
	line := 0
	for scanner.Scan() {
		line++
		data := scanner.Bytes()
		if line == 1 {
			data = []byte(strings.TrimPrefix(string(data), "\ufeff"))
		}
		if len(strings.TrimSpace(string(data))) == 0 {
			continue
		}
		var event goTestEvent
		if err := json.Unmarshal(data, &event); err != nil {
			return fmt.Errorf("corrupt Go test event at line %d: %w", line, err)
		}
		if event.Action == "" || event.Package == "" {
			return fmt.Errorf("incomplete Go test event at line %d", line)
		}
		if event.Test == "" {
			if event.Action == "start" {
				startedPackages[event.Package] = true
			}
			if slices.Contains([]string{"pass", "fail", "skip"}, event.Action) {
				packageOutcomes[event.Package] = event.Action
			}
		} else if slices.Contains([]string{"pass", "fail", "skip"}, event.Action) {
			packagesWithTests[event.Package] = true
			testOutcomes[event.Package+"::"+event.Test] = event.Action
		} else {
			packagesWithTests[event.Package] = true
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read Go test events: %w", err)
	}
	if line == 0 || len(startedPackages) == 0 {
		return errors.New("go test event stream is empty")
	}
	for pkg := range startedPackages {
		outcome, exists := packageOutcomes[pkg]
		if !exists {
			return fmt.Errorf("go test package %q has no terminal event", pkg)
		}
		if outcome != "pass" && (outcome != "skip" || packagesWithTests[pkg]) {
			return fmt.Errorf("go test package %q outcome is %q, want pass", pkg, outcome)
		}
	}
	for _, name := range required {
		outcome, exists := testOutcomes[name]
		if !exists {
			return fmt.Errorf("required Go test %q is missing", name)
		}
		if outcome != "pass" {
			return fmt.Errorf("required Go test %q outcome is %q, want pass", name, outcome)
		}
	}
	return nil
}

// ValidateExecutionSuite validates one native suite and returns its artifact.
func ValidateExecutionSuite(manifest ExecutionManifest, suiteID string, identity ExecutionIdentity, processes ProcessEvidence, openEvents func(string) (io.ReadCloser, error)) (ExecutionSuiteEvidence, error) {
	evidence := ExecutionSuiteEvidence{
		SchemaVersion: manifest.SchemaVersion, PlatformID: manifest.PlatformID,
		OS: manifest.OS, Architecture: manifest.Architecture, Native: true,
		Suite: suiteID, Identity: identity, Outcome: "failed", Cases: make(map[string]string),
	}
	if err := ValidateExecutionManifest(manifest); err != nil {
		return evidence, err
	}
	var suite *ExecutionSuite
	for index := range manifest.Suites {
		if manifest.Suites[index].ID == suiteID {
			suite = &manifest.Suites[index]
			break
		}
	}
	if suite == nil {
		return evidence, fmt.Errorf("execution suite %q is absent", suiteID)
	}
	evidence.Serialized = suite.Serialized
	wantElevated := suite.Identity == "elevated"
	if !identity.NativeExecution || identity.OS != manifest.OS || identity.Architecture != manifest.Architecture || identity.Name == "" || identity.SID == "" || identity.Elevated != wantElevated {
		return evidence, fmt.Errorf("suite %q execution identity does not match %s native %s/%s", suiteID, suite.Identity, manifest.OS, manifest.Architecture)
	}
	for _, testCase := range suite.Cases {
		switch testCase.Kind {
		case "identity":
			evidence.Cases[testCase.ID] = "passed"
		case "serialization":
			if !suite.Serialized {
				return evidence, fmt.Errorf("serialization case %q ran in a concurrent suite", testCase.ID)
			}
			evidence.Cases[testCase.ID] = "passed"
		case "go":
			exitCode, exists := processes.Cases[testCase.ID]
			if !exists {
				return evidence, fmt.Errorf("required case %q has no process result", testCase.ID)
			}
			if exitCode != 0 {
				return evidence, fmt.Errorf("required case %q exit status is %d", testCase.ID, exitCode)
			}
			stream, err := openEvents(testCase.EventFile)
			if err != nil {
				return evidence, fmt.Errorf("required case %q artifact: %w", testCase.ID, err)
			}
			err = ValidateGoTestEvents(stream, testCase.RequiredTests)
			closeErr := stream.Close()
			if err != nil {
				return evidence, fmt.Errorf("required case %q: %w", testCase.ID, err)
			}
			if closeErr != nil {
				return evidence, fmt.Errorf("required case %q artifact close: %w", testCase.ID, closeErr)
			}
			evidence.Cases[testCase.ID] = "passed"
		}
	}
	evidence.Outcome = "passed"
	return evidence, nil
}
