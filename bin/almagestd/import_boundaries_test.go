package main

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// TestDirectModeExtractedPackageImportBoundaries keeps the direct service
// implementation separate from process launch and wire protocols. These are
// source import checks so that they also cover files for other operating
// systems.
func TestDirectModeExtractedPackageImportBoundaries(t *testing.T) {
	repository := daemonRepositoryRoot(t)
	targets := []string{
		filepath.Join(repository, "bin", "almagestd", "daemonservices"),
		filepath.Join(repository, "bin", "almagestd", "internal", "daemonruntime"),
	}
	forbiddenExact := map[string]string{
		"encoding/gob": "wire serialization",
		"net/rpc":      "RPC transport",
		"os/exec":      "process launch",
	}
	const isolationImport = "github.com/asciimoth/almagest/bin/internal/isolation"
	for _, root := range targets {
		err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() || filepath.Ext(path) != ".go" || strings.HasSuffix(path, "_test.go") {
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
				if reason, forbidden := forbiddenExact[importPath]; forbidden {
					t.Errorf("%s imports %s package %s", path, reason, importPath)
				}
				if importPath == "github.com/asciimoth/almagest/bin/api" ||
					importPath == isolationImport ||
					strings.HasPrefix(importPath, isolationImport+"/") ||
					strings.HasPrefix(importPath, "google.golang.org/grpc") ||
					strings.HasPrefix(importPath, "google.golang.org/protobuf") {
					t.Errorf("%s imports wire or isolation package %s", path, importPath)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}

func daemonRepositoryRoot(t *testing.T) string {
	t.Helper()
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate import-boundary test source")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(source), "..", ".."))
}
