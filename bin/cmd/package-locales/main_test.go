package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCopyProductionLocales(t *testing.T) {
	source := t.TempDir()
	for name, developmentOnly := range map[string]bool{"en": false, "lorem": true} {
		directory := filepath.Join(source, name)
		if err := os.Mkdir(directory, 0o755); err != nil {
			t.Fatal(err)
		}
		manifest := `{"format":1,"locale":"` + name + `","name":"` + name + `","development_only":false}`
		if developmentOnly {
			manifest = `{"format":1,"locale":"` + name + `","name":"` + name + `","development_only":true}`
		}
		if err := os.WriteFile(filepath.Join(directory, "manifest.json"), []byte(manifest), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(directory, "panel.json"), []byte(name), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	destination := filepath.Join(t.TempDir(), "locales")
	if err := copyProductionLocales(source, destination); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(destination, "en", "panel.json")); err != nil {
		t.Fatalf("production locale was not copied: %v", err)
	}
	if _, err := os.Stat(filepath.Join(destination, "lorem")); !os.IsNotExist(err) {
		t.Fatalf("development locale was copied: %v", err)
	}
}

func TestCopyProductionLocalesReplacesDestination(t *testing.T) {
	source := t.TempDir()
	directory := filepath.Join(source, "en")
	if err := os.Mkdir(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := `{"format":1,"locale":"en","name":"English","development_only":false}`
	if err := os.WriteFile(filepath.Join(directory, "manifest.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	destination := t.TempDir()
	if err := os.WriteFile(filepath.Join(destination, "stale"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := copyProductionLocales(source, destination); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(destination, "stale")); !os.IsNotExist(err) {
		t.Fatalf("stale destination file was retained: %v", err)
	}
}
