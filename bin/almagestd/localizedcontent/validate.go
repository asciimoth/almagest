package localizedcontent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ValidateRoot validates all bundle files below root with the runtime parser.
func ValidateRoot(root string) error {
	reader, err := New(root, nil)
	if err != nil {
		return err
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	if len(entries) > maxLocales {
		return fmt.Errorf("locale root has more than %d entries", maxLocales)
	}
	foundEnglish := false
	for _, entry := range entries {
		canonical, valid := CanonicalLocale(entry.Name())
		if !valid || canonical != entry.Name() || entry.Type()&os.ModeSymlink != 0 || !entry.IsDir() {
			return fmt.Errorf("invalid locale directory %q", entry.Name())
		}
		if entry.Name() == "en" {
			foundEnglish = true
		}
		if err := validateLocaleDirectory(reader, entry.Name()); err != nil {
			return fmt.Errorf("locale %s: %w", entry.Name(), err)
		}
	}
	if !foundEnglish {
		return fmt.Errorf("english locale is missing")
	}
	return nil
}

func validateLocaleDirectory(reader *Reader, locale string) error {
	var value manifest
	if err := reader.readJSON(filepath.Join(locale, "manifest.json"), maxCatalog, &value); err != nil {
		return fmt.Errorf("manifest.json: %w", err)
	}
	if value.Format != 1 || value.Locale != locale || strings.TrimSpace(value.Name) == "" || len(value.Name) > maxValue {
		return fmt.Errorf("invalid manifest")
	}
	if locale == "lorem" && !value.DevelopmentOnly {
		return fmt.Errorf("lorem must be development-only")
	}

	var panel map[string]string
	if err := reader.readJSON(filepath.Join(locale, "panel.json"), maxCatalog, &panel); err != nil {
		return fmt.Errorf("panel.json: %w", err)
	}
	if err := validatePanel(panel); err != nil {
		return fmt.Errorf("panel.json: %w", err)
	}
	var menus menuCatalog
	if err := reader.readJSON(filepath.Join(locale, "menus.json"), maxCatalog, &menus); err != nil {
		return fmt.Errorf("menus.json: %w", err)
	}
	if err := validateMenus(menus); err != nil {
		return fmt.Errorf("menus.json: %w", err)
	}

	entries, err := os.ReadDir(filepath.Join(reader.root, locale))
	if err != nil {
		return err
	}
	for _, entry := range entries {
		switch entry.Name() {
		case "manifest.json", "panel.json", "menus.json":
			if entry.Type()&os.ModeSymlink != 0 || !entry.Type().IsRegular() {
				return fmt.Errorf("%s is not a regular file", entry.Name())
			}
		case "nodes":
			if entry.Type()&os.ModeSymlink != 0 || !entry.IsDir() {
				return fmt.Errorf("nodes is not a directory")
			}
			if err := validateNodeDirectory(reader, locale); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unexpected file %s", entry.Name())
		}
	}
	return nil
}

func validateNodeDirectory(reader *Reader, locale string) error {
	root := filepath.Join(reader.root, locale, "nodes")
	namespaces, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	for _, namespace := range namespaces {
		if !classSegment.MatchString(namespace.Name()) || namespace.Type()&os.ModeSymlink != 0 || !namespace.IsDir() {
			return fmt.Errorf("invalid node namespace %q", namespace.Name())
		}
		files, err := os.ReadDir(filepath.Join(root, namespace.Name()))
		if err != nil {
			return err
		}
		for _, file := range files {
			if file.Type()&os.ModeSymlink != 0 || !file.Type().IsRegular() {
				return fmt.Errorf("invalid node file %s/%s", namespace.Name(), file.Name())
			}
			class, suffix, valid := splitNodeFile(file.Name())
			if !valid || !classSegment.MatchString(class) {
				return fmt.Errorf("invalid node file name %s/%s", namespace.Name(), file.Name())
			}
			limit := int64(maxNodeDocument)
			if suffix == ".short.txt" {
				limit = maxValue
			}
			value, err := reader.readText(filepath.Join(locale, "nodes", namespace.Name(), file.Name()), limit)
			if err != nil || value == "" {
				if err == nil {
					err = fmt.Errorf("empty text")
				}
				return fmt.Errorf("invalid node file %s/%s: %w", namespace.Name(), file.Name(), err)
			}
		}
	}
	return nil
}

func splitNodeFile(name string) (string, string, bool) {
	for _, suffix := range []string{".short.txt", ".md"} {
		if strings.HasSuffix(name, suffix) {
			class := strings.TrimSuffix(name, suffix)
			return class, suffix, class != ""
		}
	}
	return "", "", false
}
