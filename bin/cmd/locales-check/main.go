// Command locales-check validates the repository localized content bundles.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/asciimoth/pasta/pasta"
	"github.com/asciimoth/pasta/pasta/std"

	"github.com/asciimoth/almagest/bin/almagestd/localizedcontent"
	amnesianodes "github.com/asciimoth/almagest/lib/amnesia"
	dnsnodes "github.com/asciimoth/almagest/lib/dns"
	httpnodes "github.com/asciimoth/almagest/lib/http"
	networknodes "github.com/asciimoth/almagest/lib/network"
	socksnodes "github.com/asciimoth/almagest/lib/socks"
	tailscalenodes "github.com/asciimoth/almagest/lib/tailscale"
	tlsnodes "github.com/asciimoth/almagest/lib/tls"
	tornodes "github.com/asciimoth/almagest/lib/tor"
	triggernodes "github.com/asciimoth/almagest/lib/trigger"
	tunnodes "github.com/asciimoth/almagest/lib/tun"
	wireguardnodes "github.com/asciimoth/almagest/lib/wireguard"
)

func main() {
	root, err := repositoryRoot()
	check(err)
	classes := productionClasses()
	check(validate(root, classes))
}

func productionClasses() []pasta.NodeClass {
	classes := amnesianodes.Classes(nil)
	classes = append(classes, dnsnodes.Classes(nil)...)
	classes = append(classes, httpnodes.Classes(nil)...)
	classes = append(classes, networknodes.Classes(nil)...)
	classes = append(classes, socksnodes.Classes(nil)...)
	classes = append(classes, tailscalenodes.Classes(nil)...)
	classes = append(classes, tlsnodes.Classes(nil)...)
	classes = append(classes, tunnodes.Classes(nil)...)
	classes = append(classes, triggernodes.TimeTriggerClass{}, wireguardnodes.WireGuardClass{}, wireguardnodes.WGConfigClass{}, tornodes.TorDriverClass{})
	classes = append(classes, std.StdClasses()...)
	sort.Slice(classes, func(i, j int) bool { return classes[i].ClassName() < classes[j].ClassName() })
	return classes
}

func repositoryRoot() (string, error) {
	directory, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(directory, "AGENTS.md")); err == nil {
			return directory, nil
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			return "", fmt.Errorf("repository root was not found")
		}
		directory = parent
	}
}

func validate(root string, classes []pasta.NodeClass) error {
	locales := filepath.Join(root, "locales")
	if err := localizedcontent.ValidateRoot(locales); err != nil {
		return err
	}
	var englishPanel map[string]string
	var loremPanel map[string]string
	var englishNodes map[string]string
	var englishMenus any
	var loremMenus any
	entries, err := os.ReadDir(locales)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			return fmt.Errorf("unexpected locale-root file %s", entry.Name())
		}
		data, err := os.ReadFile(filepath.Join(locales, entry.Name(), "manifest.json"))
		if err != nil {
			return err
		}
		var manifest struct {
			Format          int    `json:"format"`
			Locale          string `json:"locale"`
			Name            string `json:"name"`
			DevelopmentOnly bool   `json:"development_only"`
		}
		decoder := json.NewDecoder(strings.NewReader(string(data)))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&manifest); err != nil {
			return fmt.Errorf("%s manifest: %w", entry.Name(), err)
		}
		if manifest.Format != 1 || manifest.Locale != entry.Name() || manifest.Name == "" {
			return fmt.Errorf("invalid %s manifest", entry.Name())
		}
		if entry.Name() == "lorem" && !manifest.DevelopmentOnly {
			return fmt.Errorf("lorem must be development-only")
		}
		for _, name := range []string{"panel.json", "menus.json", "nodes.json"} {
			data, err := os.ReadFile(filepath.Join(locales, entry.Name(), name))
			if err != nil {
				return err
			}
			var value any
			if err := json.Unmarshal(data, &value); err != nil {
				return fmt.Errorf("%s/%s: %w", entry.Name(), name, err)
			}
			if entry.Name() == "en" && name == "panel.json" {
				if err := json.Unmarshal(data, &englishPanel); err != nil {
					return err
				}
			}
			if entry.Name() == "lorem" && name == "panel.json" {
				if err := json.Unmarshal(data, &loremPanel); err != nil {
					return err
				}
			}
			if entry.Name() == "en" && name == "menus.json" {
				englishMenus = value
			}
			if entry.Name() == "lorem" && name == "menus.json" {
				loremMenus = value
			}
			if entry.Name() == "en" && name == "nodes.json" {
				if err := json.Unmarshal(data, &englishNodes); err != nil {
					return err
				}
			}
		}
	}
	if err := validatePanelKeys(root, englishPanel); err != nil {
		return err
	}
	if err := validateFixtureCoverage(englishPanel, loremPanel, "panel"); err != nil {
		return err
	}
	if err := validateFixtureCoverage(englishMenus, loremMenus, "menus"); err != nil {
		return err
	}
	if err := validateMenuClasses(locales, classes); err != nil {
		return err
	}
	for _, class := range classes {
		path := filepath.FromSlash(class.ClassName())
		if englishNodes[class.ClassName()] == "" {
			return fmt.Errorf("nodes.json is missing %s", class.ClassName())
		}
		for _, file := range []string{filepath.Join("docs", "pages", "nodes", path+".md")} {
			data, err := os.ReadFile(filepath.Join(locales, "en", file))
			if err != nil {
				return fmt.Errorf("%s: %w", file, err)
			}
			if len(data) == 0 || len(data) > 64<<10 {
				return fmt.Errorf("invalid node file %s", file)
			}
		}
	}
	if englishNodes["almagest/Command"] == "" {
		return fmt.Errorf("nodes.json is missing almagest/Command")
	}
	for _, file := range []string{filepath.Join("docs", "pages", "nodes", "almagest", "Command.md")} {
		if _, err := os.Stat(filepath.Join(locales, "en", file)); err != nil {
			return fmt.Errorf("%s: %w", file, err)
		}
	}
	return nil
}

func validateFixtureCoverage(english, fixture any, path string) error {
	switch value := english.(type) {
	case map[string]string:
		translated, ok := fixture.(map[string]string)
		if !ok {
			return fmt.Errorf("lorem %s fixture has the wrong type", path)
		}
		for key := range value {
			if translated[key] == "" {
				return fmt.Errorf("lorem fixture is missing %s.%s", path, key)
			}
		}
	case map[string]any:
		translated, ok := fixture.(map[string]any)
		if !ok {
			return fmt.Errorf("lorem %s fixture has the wrong type", path)
		}
		for key, child := range value {
			translatedChild, exists := translated[key]
			if !exists {
				return fmt.Errorf("lorem fixture is missing %s.%s", path, key)
			}
			if err := validateFixtureCoverage(child, translatedChild, path+"."+key); err != nil {
				return err
			}
		}
	case string:
		if translated, ok := fixture.(string); !ok || translated == "" {
			return fmt.Errorf("lorem fixture is missing %s", path)
		}
	}
	return nil
}

func validateMenuClasses(locales string, classes []pasta.NodeClass) error {
	known := make(map[string]struct{}, len(classes)+1)
	for _, class := range classes {
		known[class.ClassName()] = struct{}{}
	}
	known["almagest/Command"] = struct{}{}
	data, err := os.ReadFile(filepath.Join(locales, "en", "menus.json"))
	if err != nil {
		return err
	}
	var catalog map[string]json.RawMessage
	if err := json.Unmarshal(data, &catalog); err != nil {
		return err
	}
	for class := range catalog {
		if _, exists := known[class]; !exists {
			return fmt.Errorf("english menu catalog refers to unknown class %s", class)
		}
	}
	return nil
}

func validatePanelKeys(root string, catalog map[string]string) error {
	patterns := []*regexp.Regexp{
		regexp.MustCompile(`data-i18n(?:-aria-label|-placeholder|-title)?="([A-Za-z0-9_.-]+)"`),
		regexp.MustCompile(`(?:localizedText|\bt)\(\s*"([A-Za-z0-9_.-]+)"`),
	}
	paths := []string{filepath.Join(root, "panel", "index.html")}
	sources, err := filepath.Glob(filepath.Join(root, "panel", "src", "*.ts"))
	if err != nil {
		return err
	}
	paths = append(paths, sources...)
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, pattern := range patterns {
			for _, match := range pattern.FindAllSubmatch(data, -1) {
				key := string(match[1])
				if catalog[key] == "" {
					return fmt.Errorf("english panel catalog is missing %s", key)
				}
			}
		}
	}
	return nil
}

func check(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "locales-check:", err)
		os.Exit(1)
	}
}
