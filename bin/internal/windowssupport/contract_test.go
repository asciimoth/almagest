package windowssupport

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	sysnetwindows "github.com/asciimoth/sysnet-windows"
	"golang.org/x/mod/modfile"
)

var _ = sysnetwindows.New

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

func TestCheckedInDeploymentContract(t *testing.T) {
	contract := loadCheckedInDeploymentContract(t)
	if err := ValidateDeploymentContract(contract); err != nil {
		t.Fatalf("ValidateDeploymentContract: %v", err)
	}
}

func TestCheckedInExecutionManifest(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(repositoryRoot(), "dev", "winvm", "test-suites.json"))
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := DecodeExecutionManifest(data)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateExecutionManifest(manifest); err != nil {
		t.Fatalf("ValidateExecutionManifest: %v", err)
	}
	if manifest.PlatformID != "windows-server-2022-amd64" || manifest.OS != "windows-server-2022" || manifest.Architecture != "amd64" {
		t.Fatalf("execution platform = %s %s/%s", manifest.PlatformID, manifest.OS, manifest.Architecture)
	}
}

func TestDeploymentContractRejectsBoundaryViolations(t *testing.T) {
	tests := []struct {
		name     string
		mutate   func(*DeploymentContract)
		wantText string
	}{
		{
			name: "unpinned-driver",
			mutate: func(contract *DeploymentContract) {
				contract.SplitDriver.Architectures["amd64"].Files["mullvad-split-tunnel.sys"] = strings.Repeat("0", 64)
			},
			wantText: "does not match its pinned SHA-256",
		},
		{
			name: "daemon-installs",
			mutate: func(contract *DeploymentContract) {
				contract.Provisioning.DaemonInstalls = true
			},
			wantText: "daemon must not provision",
		},
		{
			name: "ordinary-networking-depends-on-split",
			mutate: func(contract *DeploymentContract) {
				contract.Degradation.MustRemainAvailable = []string{"regular-tun"}
			},
			wantText: "split-driver independent features",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			contract := loadCheckedInDeploymentContract(t)
			test.mutate(&contract)
			err := ValidateDeploymentContract(contract)
			if err == nil || !strings.Contains(err.Error(), test.wantText) {
				t.Fatalf("ValidateDeploymentContract error = %v, want text %q", err, test.wantText)
			}
		})
	}
}

func TestWindowsBackendDependencyBoundary(t *testing.T) {
	root := repositoryRoot()
	data, err := os.ReadFile(filepath.Join(root, "bin", "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	file, err := modfile.Parse("bin/go.mod", data, nil)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, requirement := range file.Require {
		if requirement.Mod.Path == windowsBackendModule {
			found = true
			if requirement.Mod.Version != windowsBackendVersion || requirement.Indirect {
				t.Errorf("Windows backend requirement = %s (indirect %t), want direct %s", requirement.Mod.Version, requirement.Indirect, windowsBackendVersion)
			}
		}
	}
	if !found {
		t.Errorf("bin/go.mod does not require %s", windowsBackendModule)
	}
	allowedLocalReplacements := map[string]string{
		"github.com/asciimoth/almagest/certcat": "../certcat",
		"github.com/asciimoth/almagest/lib":     "../lib",
	}
	for _, replacement := range file.Replace {
		allowedPath, internal := allowedLocalReplacements[replacement.Old.Path]
		localReplacement := replacement.New.Version == ""
		if replacement.Old.Path == windowsBackendModule || (localReplacement && (!internal || replacement.New.Path != allowedPath)) {
			t.Errorf("bin/go.mod has external Windows dependency replacement: %s => %s", replacement.Old.Path, replacement.New.Path)
		}
	}
}

func TestLibraryImportBoundary(t *testing.T) {
	root := filepath.Join(repositoryRoot(), "lib")
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || filepath.Ext(path) != ".go" {
			return nil
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		for _, spec := range file.Imports {
			importPath, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				return err
			}
			backendImport := importPath == windowsBackendModule || strings.HasPrefix(importPath, windowsBackendModule+"/")
			daemonImport := importPath == "github.com/asciimoth/almagest/bin" || strings.HasPrefix(importPath, "github.com/asciimoth/almagest/bin/")
			if backendImport || daemonImport {
				t.Errorf("%s imports forbidden package %s", path, importPath)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
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
	root := repositoryRoot()
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

func loadCheckedInDeploymentContract(t *testing.T) DeploymentContract {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(repositoryRoot(), "docs", "windows", "deployment-contract.json"))
	if err != nil {
		t.Fatal(err)
	}
	contract, err := DecodeDeploymentContract(data)
	if err != nil {
		t.Fatal(err)
	}
	return contract
}

func repositoryRoot() string {
	return filepath.Clean(filepath.Join("..", "..", ".."))
}
