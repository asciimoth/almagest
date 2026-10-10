package main

import (
	"path/filepath"
	"strings"
	"testing"
)

func FuzzSnapshotEntryName(f *testing.F) {
	for _, seed := range []string{
		"manifest.json",
		"files/kv.db",
		"../escape",
		"files/C:/stream",
		"files/nested/CON.txt",
		"files/alias./value",
		"files\\windows",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, input string) {
		cleaned, err := cleanSnapshotEntryName(input)
		if err != nil {
			return
		}
		if (cleaned != input && cleaned+"/" != input) || len(cleaned) > snapshotEntryNameMaxBytes {
			t.Fatalf("accepted non-canonical name %q as %q", input, cleaned)
		}
		if filepath.IsAbs(cleaned) || strings.Contains(cleaned, "\\") {
			t.Fatalf("accepted unsafe path %q", cleaned)
		}
		parts := strings.Split(cleaned, "/")
		if len(parts) > snapshotPathMaxDepth {
			t.Fatalf("accepted %d path components", len(parts))
		}
		for _, part := range parts {
			if part == "" || part == "." || part == ".." || len(part) > snapshotPathComponentMaxBytes {
				t.Fatalf("accepted unsafe component %q in %q", part, cleaned)
			}
		}
	})
}

func FuzzSnapshotEntryAliases(f *testing.F) {
	f.Add("files/value", "files/VALUE")
	f.Add("files/one", "files/two")
	f.Add("files/a", "files/a/child")
	f.Fuzz(func(t *testing.T, first, second string) {
		first, firstErr := cleanSnapshotEntryName(first)
		second, secondErr := cleanSnapshotEntryName(second)
		if firstErr != nil || secondErr != nil {
			return
		}
		set := snapshotEntrySet{paths: map[string]string{}, entries: map[string]string{}}
		if err := set.register(first); err != nil {
			return
		}
		err := set.register(second)
		if first == second && err == nil {
			t.Fatalf("accepted duplicate entry %q", first)
		}
		firstParts := strings.Split(first, "/")
		secondParts := strings.Split(second, "/")
		for index := 0; index < min(len(firstParts), len(secondParts)); index++ {
			firstPath := strings.Join(firstParts[:index+1], "/")
			secondPath := strings.Join(secondParts[:index+1], "/")
			if firstPath != secondPath && snapshotEntryAlias(firstPath) == snapshotEntryAlias(secondPath) && err == nil {
				t.Fatalf("accepted aliased paths %q and %q", firstPath, secondPath)
			}
		}
	})
}
