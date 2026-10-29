// Command package-locales copies production locale bundles to a package tree.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

type manifest struct {
	Format          int    `json:"format"`
	Locale          string `json:"locale"`
	Name            string `json:"name"`
	DevelopmentOnly bool   `json:"development_only"`
}

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: package-locales SOURCE DESTINATION")
		os.Exit(2)
	}
	if err := copyProductionLocales(os.Args[1], os.Args[2]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func copyProductionLocales(source, destination string) error {
	source, err := filepath.Abs(source)
	if err != nil {
		return err
	}
	destination, err = filepath.Abs(destination)
	if err != nil {
		return err
	}
	if source == destination || filepath.Dir(destination) == destination {
		return fmt.Errorf("unsafe locale destination: %s", destination)
	}
	entries, err := os.ReadDir(source)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		return err
	}
	temporary, err := os.MkdirTemp(filepath.Dir(destination), ".almagest-locales-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(temporary)
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		bundle := filepath.Join(source, entry.Name())
		data, err := os.ReadFile(filepath.Join(bundle, "manifest.json"))
		if err != nil {
			return err
		}
		var value manifest
		if err := json.Unmarshal(data, &value); err != nil {
			return fmt.Errorf("%s manifest: %w", entry.Name(), err)
		}
		if value.Format != 1 || value.Locale != entry.Name() || value.Name == "" {
			return fmt.Errorf("invalid %s manifest", entry.Name())
		}
		if value.DevelopmentOnly {
			continue
		}
		if err := os.CopyFS(filepath.Join(temporary, entry.Name()), os.DirFS(bundle)); err != nil {
			return err
		}
	}
	if err := os.RemoveAll(destination); err != nil {
		return err
	}
	return os.Rename(temporary, destination)
}
