package windowssupport

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckedInContracts(t *testing.T) {
	matrix, manifest := loadCheckedInContracts(t)
	if err := ValidateContracts(matrix, manifest); err != nil {
		t.Fatalf("ValidateContracts: %v", err)
	}

	for _, platform := range matrix.Platforms {
		if platform.Status != "unavailable" {
			t.Errorf("platform %q status = %q, want unavailable before native evidence", platform.ID, platform.Status)
		}
	}
}

func TestQualificationEvidenceFixtures(t *testing.T) {
	matrix, manifest := loadCheckedInContracts(t)
	tests := []struct {
		name     string
		wantText string
	}{
		{name: "absent", wantText: "required test \"native::execution-identity-standard-user\" is absent"},
		{name: "skipped", wantText: "outcome is \"skipped\", want passed"},
		{name: "wrong-platform", wantText: "evidence platform is windows-11/amd64"},
		{name: "cross-build", wantText: "evidence is not from native execution"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join("testdata", "evidence", test.name+".json"))
			if err != nil {
				t.Fatal(err)
			}
			evidence, err := DecodeQualificationEvidence(data)
			if err != nil {
				t.Fatalf("DecodeQualificationEvidence: %v", err)
			}
			err = ValidateQualificationEvidence(matrix, manifest, evidence)
			if err == nil || !strings.Contains(err.Error(), test.wantText) {
				t.Fatalf("ValidateQualificationEvidence error = %v, want text %q", err, test.wantText)
			}
		})
	}
}

func TestQualificationEvidenceAcceptsAllRequiredNativeTests(t *testing.T) {
	matrix, manifest := loadCheckedInContracts(t)
	platform := matrix.Platforms[0]
	evidence := QualificationEvidence{
		SchemaVersion:   contractSchemaVersion,
		PlatformID:      platform.ID,
		OS:              platform.OS,
		Architecture:    platform.Architecture,
		NativeExecution: true,
		Suites:          make(map[string]SuiteEvidence, len(platform.RequiredSuites)),
	}
	suites := make(map[string]TestSuite, len(manifest.Suites))
	for _, suite := range manifest.Suites {
		suites[suite.ID] = suite
	}
	for _, suiteID := range platform.RequiredSuites {
		tests := make(map[string]string, len(suites[suiteID].RequiredTests))
		for _, testID := range suites[suiteID].RequiredTests {
			tests[testID] = "passed"
		}
		evidence.Suites[suiteID] = SuiteEvidence{Outcome: "passed", Tests: tests}
	}
	if err := ValidateQualificationEvidence(matrix, manifest, evidence); err != nil {
		t.Fatalf("ValidateQualificationEvidence: %v", err)
	}
}

func TestContractsRejectCrossBuildAsRequiredNativeSuite(t *testing.T) {
	matrix, manifest := loadCheckedInContracts(t)
	matrix.Platforms[0].RequiredSuites = []string{"windows-cross-build"}
	if err := ValidateContracts(matrix, manifest); err == nil || !strings.Contains(err.Error(), "uses non-native suite") {
		t.Fatalf("ValidateContracts error = %v, want non-native suite error", err)
	}
}

func loadCheckedInContracts(t *testing.T) (SupportMatrix, TestManifest) {
	t.Helper()
	root := filepath.Clean(filepath.Join("..", "..", ".."))
	matrixData, err := os.ReadFile(filepath.Join(root, "docs", "windows", "support-matrix.json"))
	if err != nil {
		t.Fatal(err)
	}
	manifestData, err := os.ReadFile(filepath.Join(root, "docs", "windows", "test-manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	matrix, err := DecodeSupportMatrix(matrixData)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := DecodeTestManifest(manifestData)
	if err != nil {
		t.Fatal(err)
	}
	return matrix, manifest
}
