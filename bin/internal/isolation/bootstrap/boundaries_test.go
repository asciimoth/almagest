package bootstrap

import (
	"bufio"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestPrivilegedRoleDependencyBoundary(t *testing.T) {
	moduleRoot := filepath.Clean(filepath.Join(packageDirectory(t), "..", "..", ".."))
	// Check build-tag-selected imports even when the suite runs on another OS.
	for _, target := range []struct{ os, arch string }{{"linux", "amd64"}, {"windows", "amd64"}, {"windows", "arm64"}} {
		t.Run(target.os+"/"+target.arch, func(t *testing.T) {
			checkRoleDependencies(t, moduleRoot, target.os, target.arch)
		})
	}
}

func checkRoleDependencies(t *testing.T, moduleRoot, targetOS, targetArch string) {
	t.Helper()
	entries := []struct {
		path   string
		native bool
	}{
		{"./cmd/almagest-network-role", true},
		{"./cmd/almagest-config-writer-role", false},
		{"./internal/isolation/bootstrap", false},
	}
	if targetOS == "linux" {
		entries = append(entries, struct {
			path   string
			native bool
		}{"./internal/nativebackend", true})
		entries = append(entries, struct {
			path   string
			native bool
		}{"./internal/isolation/networkrole", true})
	}
	for _, entry := range entries {
		t.Run(entry.path, func(t *testing.T) {
			command := exec.Command("go", "list", "-deps", "-f", "{{.ImportPath}}", entry.path) //nolint:gosec // Closed package list.
			command.Dir = moduleRoot
			command.Env = append(os.Environ(), "GOOS="+targetOS, "GOARCH="+targetArch, "CGO_ENABLED=0")
			output, err := command.CombinedOutput()
			if err != nil {
				t.Fatalf("list role dependencies: %v\n%s", err, output)
			}
			scanner := bufio.NewScanner(strings.NewReader(string(output)))
			for scanner.Scan() {
				if dependency := scanner.Text(); forbiddenRoleDependency(dependency, entry.native) {
					t.Errorf("privileged role imports forbidden dependency %s", dependency)
				}
			}
			if err := scanner.Err(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func forbiddenRoleDependency(dependency string, native bool) bool {
	forbidden := []string{
		"github.com/asciimoth/almagest/lib",
		"github.com/asciimoth/almagest/bin/almagestd",
		"go.etcd.io/bbolt",
		"archive/zip",
		"github.com/asciimoth/configer",
	}
	for _, prefix := range forbidden {
		if dependency == prefix || strings.HasPrefix(dependency, prefix+"/") {
			return true
		}
	}
	return !native && strings.HasPrefix(dependency, "github.com/asciimoth/sysnet-")
}

func TestRoleDependencyClassification(t *testing.T) {
	for _, native := range []bool{false, true} {
		for _, dependency := range []string{"github.com/asciimoth/almagest/lib", "github.com/asciimoth/almagest/lib/nodes", "archive/zip", "go.etcd.io/bbolt"} {
			if !forbiddenRoleDependency(dependency, native) {
				t.Errorf("accepted runtime dependency %s", dependency)
			}
		}
		for _, dependency := range []string{"github.com/asciimoth/sysnet-linux", "github.com/asciimoth/sysnet-linux/routing", "github.com/asciimoth/sysnet-windows"} {
			if forbiddenRoleDependency(dependency, native) == !native {
				continue
			}
			t.Errorf("incorrect native dependency classification: %s native=%v", dependency, native)
		}
		if forbiddenRoleDependency("github.com/asciimoth/almagest/bin/internal/snapshotlimits", native) {
			t.Fatal("rejected dependency-free snapshot limits")
		}
	}
}

func TestDaemonDoesNotImplicitlyLoadEnvironmentFiles(t *testing.T) {
	root := filepath.Clean(filepath.Join(packageDirectory(t), "..", "..", "..", ".."))
	mainData, err := os.ReadFile(filepath.Join(root, "bin", "almagestd", "main.go"))
	if err != nil {
		t.Fatal(err)
	}
	moduleData, err := os.ReadFile(filepath.Join(root, "bin", "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	for _, data := range [][]byte{mainData, moduleData} {
		if strings.Contains(string(data), "godotenv") {
			t.Fatal("trusted startup still references implicit environment-file loading")
		}
	}
}

func packageDirectory(t *testing.T) string {
	t.Helper()
	directory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return directory
}
