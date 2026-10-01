package windowssupport

import (
	"errors"
	"io"
	"strings"
	"testing"
)

func TestGoTestEventValidationRejectsMissingSkippedAndCorruptEvents(t *testing.T) {
	const pkg = "example.test/package"
	valid := strings.Join([]string{
		`{"Action":"start","Package":"` + pkg + `"}`,
		`{"Action":"run","Package":"` + pkg + `","Test":"TestRequired"}`,
		`{"Action":"pass","Package":"` + pkg + `","Test":"TestRequired"}`,
		`{"Action":"pass","Package":"` + pkg + `"}`,
	}, "\n")
	required := []string{pkg + "::TestRequired"}
	if err := ValidateGoTestEvents(strings.NewReader(valid), required); err != nil {
		t.Fatalf("valid events: %v", err)
	}
	noTests := strings.Join([]string{
		`{"Action":"start","Package":"example.test/empty"}`,
		`{"Action":"output","Package":"example.test/empty","Output":"[no test files]"}`,
		`{"Action":"skip","Package":"example.test/empty"}`,
	}, "\n")
	if err := ValidateGoTestEvents(strings.NewReader(noTests), nil); err != nil {
		t.Fatalf("package without test files: %v", err)
	}
	tests := []struct{ name, events, text string }{
		{name: "missing", events: strings.Replace(valid, `,"Test":"TestRequired"`, `,"Test":"Different"`, 2), text: "is missing"},
		{name: "skipped", events: strings.Replace(valid, `"Action":"pass","Package":"`+pkg+`","Test"`, `"Action":"skip","Package":"`+pkg+`","Test"`, 1), text: `outcome is "skip"`},
		{name: "corrupt", events: valid + "\nnot-json", text: "corrupt Go test event"},
		{name: "timeout", events: strings.TrimSuffix(valid, `{"Action":"pass","Package":"`+pkg+`"}`), text: "no terminal event"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := ValidateGoTestEvents(strings.NewReader(test.events), required)
			if err == nil || !strings.Contains(err.Error(), test.text) {
				t.Fatalf("error = %v, want text %q", err, test.text)
			}
		})
	}
}

func TestExecutionSuiteRejectsIncorrectIdentityAndMissingArtifact(t *testing.T) {
	manifest := testExecutionManifest()
	identity := ExecutionIdentity{Name: `host\winvm`, SID: "S-1-5-21-1", NativeExecution: true, OS: manifest.OS, Architecture: manifest.Architecture}
	processes := ProcessEvidence{Cases: map[string]int{"go": 0}}
	_, err := ValidateExecutionSuite(manifest, "ordinary", identity, processes, func(string) (io.ReadCloser, error) {
		return nil, errors.New("artifact absent")
	})
	if err == nil || !strings.Contains(err.Error(), "artifact absent") {
		t.Fatalf("missing artifact error = %v", err)
	}
	identity.Elevated = true
	_, err = ValidateExecutionSuite(manifest, "ordinary", identity, processes, func(string) (io.ReadCloser, error) { return nil, nil })
	if err == nil || !strings.Contains(err.Error(), "execution identity") {
		t.Fatalf("identity error = %v", err)
	}
}

func TestPrivilegedExecutionMustBeSerialized(t *testing.T) {
	manifest := testExecutionManifest()
	manifest.Suites[1].Serialized = false
	err := ValidateExecutionManifest(manifest)
	if err == nil || !strings.Contains(err.Error(), "must be serialized") {
		t.Fatalf("error = %v, want serialized requirement", err)
	}
}

func TestExecutionServicesRequirePrivilegedGoCase(t *testing.T) {
	manifest := testExecutionManifest()
	manifest.Suites[0].Cases[1].StartServices = []string{"fixture"}
	if err := ValidateExecutionManifest(manifest); err == nil || !strings.Contains(err.Error(), "outside the privileged suite") {
		t.Fatalf("ordinary service start error = %v", err)
	}
	manifest = testExecutionManifest()
	manifest.Suites[1].Cases = append(manifest.Suites[1].Cases, ExecutionCase{
		ID: "service", Kind: "go", Module: "bin", Arguments: []string{"./fixture"},
		EventFile: "service.jsonl", StartServices: []string{"fixture", "fixture"},
	})
	if err := ValidateExecutionManifest(manifest); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("duplicate service error = %v", err)
	}
}

func TestExecutionManifestRequiresBoundedCaseTimeouts(t *testing.T) {
	manifest := testExecutionManifest()
	manifest.CaseTimeoutSeconds = 0
	if err := ValidateExecutionManifest(manifest); err == nil || !strings.Contains(err.Error(), "case timeout") {
		t.Fatalf("case timeout error = %v", err)
	}
	manifest = testExecutionManifest()
	manifest.Suites[0].Cases[1].TimeoutSeconds = 3601
	if err := ValidateExecutionManifest(manifest); err == nil || !strings.Contains(err.Error(), "invalid timeout") {
		t.Fatalf("case override error = %v", err)
	}
}

func testExecutionManifest() ExecutionManifest {
	return ExecutionManifest{SchemaVersion: 1, PlatformID: "server", OS: "windows-server-2022", Architecture: "amd64", CaseTimeoutSeconds: 900, GoPackageTimeoutSeconds: 600, ProgressIntervalSeconds: 30, Suites: []ExecutionSuite{
		{ID: "ordinary", Identity: "standard", Privilege: "ordinary", Cases: []ExecutionCase{{ID: "identity", Kind: "identity"}, {ID: "go", Kind: "go", Module: "bin", Arguments: []string{"./..."}, EventFile: "go.jsonl"}}},
		{ID: "privileged", Identity: "elevated", Privilege: "privileged", Serialized: true, Cases: []ExecutionCase{{ID: "elevated", Kind: "identity"}, {ID: "serial", Kind: "serialization"}}},
	}}
}
