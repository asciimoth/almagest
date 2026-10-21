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
	foundEnglish := false
	localeCount := 0
	for _, entry := range entries {
		if entry.Name() == "README.md" {
			if entry.Type()&os.ModeSymlink != 0 || !entry.Type().IsRegular() {
				return fmt.Errorf("README.md is not a regular file")
			}
			continue
		}
		localeCount++
		if localeCount > maxLocales {
			return fmt.Errorf("locale root has more than %d locales", maxLocales)
		}
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
		case "manifest.json", "panel.json", "menus.json", "nodes.json":
			if entry.Type()&os.ModeSymlink != 0 || !entry.Type().IsRegular() {
				return fmt.Errorf("%s is not a regular file", entry.Name())
			}
		case "docs":
			if entry.Type()&os.ModeSymlink != 0 || !entry.IsDir() {
				return fmt.Errorf("docs is not a directory")
			}
			if err := validateDocsDirectory(reader, locale); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unexpected file %s", entry.Name())
		}
	}
	var nodes map[string]string
	if err := reader.readJSON(filepath.Join(locale, "nodes.json"), maxCatalog, &nodes); err != nil {
		return fmt.Errorf("nodes.json: %w", err)
	}
	if err := validateNodes(nodes); err != nil {
		return fmt.Errorf("nodes.json: %w", err)
	}
	return nil
}

func validateDocsDirectory(reader *Reader, locale string) error {
	var index docIndex
	if err := reader.readJSON(filepath.Join(locale, "docs", "index.json"), maxCatalog, &index); err != nil {
		return fmt.Errorf("docs/index.json: %w", err)
	}
	if err := validateDocsIndex(index); err != nil {
		return fmt.Errorf("docs/index.json: %w", err)
	}
	root := filepath.Join(reader.root, locale, "docs", "pages")
	if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("documentation path is a symbolic link: %s", path)
		}
		if entry.IsDir() {
			return nil
		}
		if !entry.Type().IsRegular() || !strings.HasSuffix(entry.Name(), ".md") {
			return fmt.Errorf("invalid documentation file %s", path)
		}
		relative, relErr := filepath.Rel(root, path)
		if relErr != nil || !validDocFile(filepath.ToSlash(relative)) {
			return fmt.Errorf("invalid documentation file %s", path)
		}
		value, readErr := reader.readText(filepath.Join(locale, "docs", "pages", relative), maxNodeDocument)
		if readErr != nil || value == "" {
			if readErr == nil {
				readErr = fmt.Errorf("empty text")
			}
			return fmt.Errorf("invalid documentation file %s: %w", relative, readErr)
		}
		return nil
	}); err != nil {
		return err
	}
	entries, err := os.ReadDir(filepath.Join(reader.root, locale, "docs"))
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.Name() != "index.json" && entry.Name() != "pages" {
			return fmt.Errorf("unexpected docs entry %s", entry.Name())
		}
	}
	return nil
}

func validateDocsIndex(index docIndex) error {
	ids := map[string]struct{}{}
	pageCount := 0
	defaultPages := 0
	var validateTopics func([]docTopic) error
	validateTopics = func(topics []docTopic) error {
		for _, topic := range topics {
			if !validDocID(topic.ID) || strings.TrimSpace(topic.Name) == "" || len(topic.Name) > maxValue || !validDocOrder(topic.Order) {
				return fmt.Errorf("invalid topic %q", topic.ID)
			}
			if _, exists := ids[topic.ID]; exists {
				return fmt.Errorf("duplicate documentation ID %q", topic.ID)
			}
			ids[topic.ID] = struct{}{}
			if err := validateTopics(topic.Topics); err != nil {
				return err
			}
			for _, page := range topic.Pages {
				pageCount++
				if pageCount > maxDocsPages || !validDocID(page.ID) || strings.TrimSpace(page.Name) == "" || len(page.Name) > maxValue || !validDocFile(page.File) || !validDocOrder(page.Order) {
					return fmt.Errorf("invalid page %q", page.ID)
				}
				if _, exists := ids[page.ID]; exists {
					return fmt.Errorf("duplicate documentation ID %q", page.ID)
				}
				ids[page.ID] = struct{}{}
				if page.Default {
					defaultPages++
					if defaultPages > 1 {
						return fmt.Errorf("more than one default documentation page")
					}
				}
			}
		}
		return nil
	}
	if len(index.Topics) == 0 {
		return fmt.Errorf("documentation topics are empty")
	}
	return validateTopics(index.Topics)
}

func validDocOrder(order *int) bool {
	return order == nil || *order >= 0 && *order <= maxDocsPages
}
