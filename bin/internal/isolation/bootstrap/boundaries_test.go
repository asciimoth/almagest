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
	command := exec.Command("go", "list", "-deps", "-f", "{{.ImportPath}}", //nolint:gosec // Fixed tool and package list.
		"./cmd/almagest-network-role", "./cmd/almagest-config-writer-role")
	command.Dir = moduleRoot
	output, err := command.Output()
	if err != nil {
		t.Fatalf("list role dependencies: %v", err)
	}
	forbidden := []string{
		"github.com/asciimoth/almagest/lib",
		"github.com/asciimoth/almagest/bin/almagestd",
		"go.etcd.io/bbolt",
		"archive/zip",
		"github.com/asciimoth/configer",
		"github.com/asciimoth/sysnet-",
	}
	scanner := bufio.NewScanner(strings.NewReader(string(output)))
	for scanner.Scan() {
		dependency := scanner.Text()
		for _, prefix := range forbidden {
			if dependency == prefix || strings.HasPrefix(dependency, prefix+"/") {
				t.Errorf("privileged role imports forbidden dependency %s", dependency)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
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
