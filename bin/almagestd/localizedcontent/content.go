// Package localizedcontent reads daemon presentation bundles.
package localizedcontent

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"unicode/utf8"
)

const (
	maxNodeDocument = 64 << 10
	maxCatalog      = 1 << 20
	maxValue        = 4 << 10
	maxLocales      = 128
	maxKeys         = 10_000
	maxDocsPages    = 10_000
)

var (
	localePattern = regexp.MustCompile(`^[A-Za-z0-9]+(?:-[A-Za-z0-9]+)*$`)
	classSegment  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)
	catalogKey    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)
	docIDPattern  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:/-]*$`)
)

// Source identifies the fallback level that supplied a value.
type Source string

const (
	SourceRequested Source = "requested"
	SourceEnglish   Source = "en"
	SourceBuiltin   Source = "builtin"
	SourceMissing   Source = "missing"
)

// Locale is one valid discovered bundle.
type Locale struct {
	Locale string `json:"locale"`
	Name   string `json:"name"`
}

// DocPage is one localized documentation page entry.
type DocPage struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Folded  bool   `json:"folded"`
	Order   *int   `json:"order,omitempty"`
	Default bool   `json:"default,omitempty"`
}

// DocTopic is one localized documentation topic entry.
type DocTopic struct {
	ID     string     `json:"id"`
	Name   string     `json:"name"`
	Folded bool       `json:"folded"`
	Order  *int       `json:"order,omitempty"`
	Topics []DocTopic `json:"topics"`
	Pages  []DocPage  `json:"pages"`
}

type docIndex struct {
	Topics []docTopic `json:"topics"`
}

type docTopic struct {
	ID        string     `json:"id"`
	Name      string     `json:"name"`
	Folded    *bool      `json:"folded,omitempty"`
	Order     *int       `json:"order,omitempty"`
	NodePages bool       `json:"node_pages,omitempty"`
	Topics    []docTopic `json:"topics,omitempty"`
	Pages     []docPage  `json:"pages,omitempty"`
}

type docPage struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	File    string `json:"file"`
	Folded  *bool  `json:"folded,omitempty"`
	Order   *int   `json:"order,omitempty"`
	Default bool   `json:"default,omitempty"`
}

type manifest struct {
	Format          int    `json:"format"`
	Locale          string `json:"locale"`
	Name            string `json:"name"`
	DevelopmentOnly bool   `json:"development_only"`
}

type cachedCatalog[T any] struct {
	identity string
	value    T
	valid    bool
}

// Reader reads one fixed locale root. A nil Reader uses built-in strings.
type Reader struct {
	root     string
	warn     func(string, ...any)
	mu       sync.Mutex
	panels   map[string]cachedCatalog[map[string]string]
	nodes    map[string]cachedCatalog[map[string]string]
	menus    map[string]cachedCatalog[menuCatalog]
	revision atomic.Uint64
}

// New opens a reader for an absolute, clean directory path.
func New(root string, warn func(string, ...any)) (*Reader, error) {
	if !filepath.IsAbs(root) || filepath.Clean(root) != root {
		return nil, fmt.Errorf("locale root must be an absolute clean path")
	}
	info, err := os.Lstat(root)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return nil, fmt.Errorf("locale root is not a directory")
	}
	return &Reader{
		root: root, warn: warn,
		panels: map[string]cachedCatalog[map[string]string]{},
		nodes:  map[string]cachedCatalog[map[string]string]{},
		menus:  map[string]cachedCatalog[menuCatalog]{},
	}, nil
}

// ValidLocale reports whether name follows the restricted locale grammar.
func ValidLocale(name string) bool {
	return name != "" && len(name) <= 35 && localePattern.MatchString(name)
}

// CanonicalLocale applies the bundle name casing rules.
func CanonicalLocale(name string) (string, bool) {
	if !ValidLocale(name) {
		return "", false
	}
	parts := strings.Split(name, "-")
	parts[0] = strings.ToLower(parts[0])
	for i := 1; i < len(parts); i++ {
		switch len(parts[i]) {
		case 2:
			parts[i] = strings.ToUpper(parts[i])
		case 4:
			parts[i] = strings.ToUpper(parts[i][:1]) + strings.ToLower(parts[i][1:])
		default:
			parts[i] = strings.ToLower(parts[i])
		}
	}
	return strings.Join(parts, "-"), true
}

// Revision changes after a valid catalog reload.
func (r *Reader) Revision() uint64 {
	if r == nil {
		return 0
	}
	return r.revision.Load()
}

// AvailableLocales returns valid manifests below the fixed root.
func (r *Reader) AvailableLocales() []Locale {
	if r == nil {
		return []Locale{{Locale: "en", Name: "English"}}
	}
	entries, err := os.ReadDir(r.root)
	if err != nil {
		r.warning("cannot read locale root: %v", err)
		return []Locale{{Locale: "en", Name: "English"}}
	}
	locales := make([]Locale, 0, min(len(entries), maxLocales))
	for _, entry := range entries {
		if len(locales) == maxLocales || !entry.IsDir() {
			continue
		}
		canonical, ok := CanonicalLocale(entry.Name())
		if !ok || canonical != entry.Name() {
			continue
		}
		var value manifest
		if err := r.readJSON(filepath.Join(entry.Name(), "manifest.json"), maxCatalog, &value); err != nil ||
			value.Format != 1 || value.Locale != entry.Name() || value.Name == "" || len(value.Name) > maxValue || !utf8.ValidString(value.Name) {
			continue
		}
		locales = append(locales, Locale{Locale: value.Locale, Name: value.Name})
	}
	slices.SortFunc(locales, func(a, b Locale) int { return strings.Compare(a.Locale, b.Locale) })
	if !slices.ContainsFunc(locales, func(value Locale) bool { return value.Locale == "en" }) {
		locales = append([]Locale{{Locale: "en", Name: "English"}}, locales...)
	}
	return locales
}

// EffectiveLocale returns requested when it is available and otherwise en.
func (r *Reader) EffectiveLocale(requested string) string {
	canonical, ok := CanonicalLocale(requested)
	if !ok {
		return "en"
	}
	if slices.ContainsFunc(r.AvailableLocales(), func(value Locale) bool { return value.Locale == canonical }) {
		return canonical
	}
	return "en"
}

// Panel resolves the panel catalog per key. English is the base catalog.
func (r *Reader) Panel(locale string) map[string]string {
	result := map[string]string{}
	if r == nil {
		return result
	}
	if english, ok := r.panelCatalog("en"); ok {
		for key, value := range english {
			result[key] = value
		}
	}
	canonical, ok := CanonicalLocale(locale)
	if ok && canonical != "en" {
		if requested, valid := r.panelCatalog(canonical); valid {
			for key, value := range requested {
				result[key] = value
			}
		}
	}
	return result
}

// NodeShort resolves one required short description.
func (r *Reader) NodeShort(locale, class, builtin string) (string, Source) {
	if r != nil && validClass(class) {
		canonical, ok := CanonicalLocale(locale)
		if ok && canonical != "en" {
			if catalog, valid := r.nodeCatalog(canonical); valid && catalog[class] != "" {
				return catalog[class], SourceRequested
			}
		}
		if catalog, valid := r.nodeCatalog("en"); valid && catalog[class] != "" {
			return catalog[class], SourceEnglish
		}
	}
	if builtin != "" {
		return builtin, SourceBuiltin
	}
	return "", SourceMissing
}

// NodeLong resolves one required Markdown description.
func (r *Reader) NodeLong(locale, class, builtin string) (string, Source) {
	return r.docText(locale, "nodes/"+class+".md", builtin)
}

// DocsTree returns the localized documentation tree. Node pages are generated
// from the classes available in the current runtime.
func (r *Reader) DocsTree(locale string, classes []string, builtinNames ...map[string]string) ([]DocTopic, string) {
	if r == nil {
		return nil, "en"
	}
	effective := r.EffectiveLocale(locale)
	index, ok := r.docsIndex(effective)
	if !ok && effective != "en" {
		effective = "en"
		index, ok = r.docsIndex(effective)
	}
	if !ok {
		return nil, effective
	}
	classes = slices.Clone(classes)
	return buildDocTopics(index.Topics, classes, func(class string) string {
		parts := strings.Split(class, "/")
		builtin := parts[len(parts)-1]
		if len(builtinNames) != 0 && builtinNames[0][class] != "" {
			builtin = builtinNames[0][class]
		}
		name, _ := r.NodeShort(effective, class, builtin)
		return name
	}), effective
}

// DocPage resolves one documentation page by its stable ID.
func (r *Reader) DocPage(locale, id string, builtinContent ...string) (DocPage, string, string, bool) {
	if r == nil || !validDocID(id) {
		return DocPage{}, "", "en", false
	}
	effective := r.EffectiveLocale(locale)
	index, ok := r.docsIndex(effective)
	if !ok && effective != "en" {
		effective = "en"
		index, ok = r.docsIndex(effective)
	}
	if !ok {
		return DocPage{}, "", effective, false
	}
	page, file, found := findDocPage(index.Topics, id)
	if !found {
		return DocPage{}, "", effective, false
	}
	if class, isNode := strings.CutPrefix(id, "node/"); isNode {
		parts := strings.Split(class, "/")
		page.Name, _ = r.NodeShort(effective, class, parts[len(parts)-1])
	}
	builtin := ""
	if len(builtinContent) != 0 {
		builtin = builtinContent[0]
	}
	content, source := r.docText(effective, file, builtin)
	return page, content, effective, source != SourceMissing
}

func (r *Reader) docsIndex(locale string) (docIndex, bool) {
	var value docIndex
	if err := r.readJSON(filepath.Join(locale, "docs", "index.json"), maxCatalog, &value); err != nil {
		return docIndex{}, false
	}
	if err := validateDocsIndex(value); err != nil {
		r.warning("ignoring invalid %s/docs/index.json: %v", locale, err)
		return docIndex{}, false
	}
	return value, true
}

func (r *Reader) docText(locale, file, builtin string) (string, Source) {
	if r != nil && validDocFile(file) {
		canonical, ok := CanonicalLocale(locale)
		if ok && canonical != "en" {
			if value, err := r.readText(filepath.Join(canonical, "docs", "pages", filepath.FromSlash(file)), maxNodeDocument); err == nil && value != "" {
				return value, SourceRequested
			}
		}
		if value, err := r.readText(filepath.Join("en", "docs", "pages", filepath.FromSlash(file)), maxNodeDocument); err == nil && value != "" {
			return value, SourceEnglish
		}
	}
	if builtin != "" {
		return builtin, SourceBuiltin
	}
	return "", SourceMissing
}

func buildDocTopics(values []docTopic, classes []string, nodeName func(string) string) []DocTopic {
	values = slices.Clone(values)
	slices.SortStableFunc(values, func(a, b docTopic) int { return compareDocOrder(a.Order, b.Order) })
	result := make([]DocTopic, 0, len(values))
	for _, value := range values {
		topic := DocTopic{ID: value.ID, Name: value.Name, Folded: folded(value.Folded), Order: value.Order, Pages: make([]DocPage, 0, len(value.Pages))}
		topic.Topics = buildDocTopics(value.Topics, classes, nodeName)
		pages := slices.Clone(value.Pages)
		slices.SortStableFunc(pages, func(a, b docPage) int { return compareDocOrder(a.Order, b.Order) })
		for _, page := range pages {
			topic.Pages = append(topic.Pages, DocPage{ID: page.ID, Name: page.Name, Folded: folded(page.Folded), Order: page.Order, Default: page.Default})
		}
		if value.NodePages {
			for _, class := range classes {
				if validClass(class) {
					topic.Pages = append(topic.Pages, DocPage{ID: "node/" + class, Name: nodeName(class), Folded: true})
				}
			}
		}
		result = append(result, topic)
	}
	return result
}

func findDocPage(topics []docTopic, id string) (DocPage, string, bool) {
	if strings.HasPrefix(id, "node/") && validClass(strings.TrimPrefix(id, "node/")) {
		class := strings.TrimPrefix(id, "node/")
		for _, topic := range topics {
			if containsNodePages(topic) {
				parts := strings.Split(class, "/")
				return DocPage{ID: id, Name: parts[1], Folded: true}, "nodes/" + class + ".md", true
			}
		}
	}
	for _, topic := range topics {
		for _, page := range topic.Pages {
			if page.ID == id {
				return DocPage{ID: page.ID, Name: page.Name, Folded: folded(page.Folded), Order: page.Order, Default: page.Default}, page.File, true
			}
		}
		if page, file, ok := findDocPage(topic.Topics, id); ok {
			return page, file, true
		}
	}
	return DocPage{}, "", false
}

func compareDocOrder(a, b *int) int {
	if a == nil && b == nil {
		return 0
	}
	if a == nil {
		return 1
	}
	if b == nil {
		return -1
	}
	if *a < *b {
		return -1
	}
	if *a > *b {
		return 1
	}
	return 0
}

func containsNodePages(topic docTopic) bool {
	if topic.NodePages {
		return true
	}
	for _, child := range topic.Topics {
		if containsNodePages(child) {
			return true
		}
	}
	return false
}

func folded(value *bool) bool {
	return value == nil || *value
}

func validDocID(value string) bool {
	return value != "" && len(value) <= 256 && !strings.Contains(value, "..") && docIDPattern.MatchString(value)
}

func validDocFile(value string) bool {
	if value == "" || !strings.HasSuffix(value, ".md") || filepath.IsAbs(value) || filepath.Clean(value) != filepath.FromSlash(value) {
		return false
	}
	for _, part := range strings.Split(value, "/") {
		if part == "" || part == "." || part == ".." || !classSegment.MatchString(strings.TrimSuffix(part, ".md")) {
			return false
		}
	}
	return true
}

func validClass(class string) bool {
	parts := strings.Split(class, "/")
	return len(parts) == 2 && classSegment.MatchString(parts[0]) && classSegment.MatchString(parts[1])
}

func (r *Reader) panelCatalog(locale string) (map[string]string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return loadCatalog(r, locale, "panel.json", r.panels, validatePanel)
}

func (r *Reader) nodeCatalog(locale string) (map[string]string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return loadCatalog(r, locale, "nodes.json", r.nodes, validateNodes)
}

func (r *Reader) menu(locale string) (menuCatalog, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return loadCatalog(r, locale, "menus.json", r.menus, validateMenus)
}

func loadCatalog[T any](r *Reader, locale, name string, cache map[string]cachedCatalog[T], validate func(T) error) (T, bool) {
	var zero T
	relative := filepath.Join(locale, name)
	identity, err := r.fileIdentity(relative)
	if err != nil {
		return zero, false
	}
	old := cache[locale]
	if old.identity == identity {
		return old.value, old.valid
	}
	var value T
	if err := r.readJSON(relative, maxCatalog, &value); err != nil || validate(value) != nil {
		if err == nil {
			err = validate(value)
		}
		r.warning("ignoring invalid %s: %v", relative, err)
		old.identity = identity
		cache[locale] = old
		return old.value, old.valid
	}
	cache[locale] = cachedCatalog[T]{identity: identity, value: value, valid: true}
	r.revision.Add(1)
	return value, true
}

func (r *Reader) fileIdentity(relative string) (string, error) {
	file, err := r.open(relative)
	if err != nil {
		return "", err
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("not a regular file")
	}
	return fmt.Sprintf("%d:%d", info.ModTime().UnixNano(), info.Size()), nil
}

func (r *Reader) readText(relative string, limit int64) (string, error) {
	data, err := r.read(relative, limit)
	if err != nil {
		return "", err
	}
	if !utf8.Valid(data) {
		return "", fmt.Errorf("invalid UTF-8")
	}
	value := strings.TrimSuffix(string(data), "\n")
	if strings.HasSuffix(value, "\r") || strings.Contains(value, "\x00") {
		return "", fmt.Errorf("invalid text")
	}
	return value, nil
}

func (r *Reader) readJSON(relative string, limit int64, out any) error {
	data, err := r.read(relative, limit)
	if err != nil {
		return err
	}
	if !utf8.Valid(data) {
		return fmt.Errorf("invalid UTF-8")
	}
	if err := rejectDuplicateKeys(data); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(out); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return fmt.Errorf("trailing JSON data")
	}
	return nil
}

func (r *Reader) read(relative string, limit int64) ([]byte, error) {
	file, err := r.open(relative)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("not a regular file")
	}
	if info.Size() > limit {
		return nil, fmt.Errorf("file exceeds %d bytes", limit)
	}
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("file exceeds %d bytes", limit)
	}
	return data, nil
}

func (r *Reader) open(relative string) (*os.File, error) {
	if relative == "" || filepath.IsAbs(relative) || filepath.Clean(relative) != relative {
		return nil, fmt.Errorf("unsafe relative path")
	}
	for _, part := range strings.Split(relative, string(filepath.Separator)) {
		if part == "" || part == "." || part == ".." {
			return nil, fmt.Errorf("unsafe path component")
		}
	}
	return openRelative(r.root, relative)
}

func (r *Reader) warning(format string, args ...any) {
	if r != nil && r.warn != nil {
		r.warn(format, args...)
	}
}

func validatePanel(catalog map[string]string) error {
	if len(catalog) > maxKeys {
		return fmt.Errorf("catalog has too many keys")
	}
	for key, value := range catalog {
		if !catalogKey.MatchString(key) || value == "" || len(value) > maxValue || !utf8.ValidString(value) {
			return fmt.Errorf("invalid panel value %q", key)
		}
	}
	return nil
}

func validateNodes(catalog map[string]string) error {
	if len(catalog) > maxKeys {
		return fmt.Errorf("catalog has too many keys")
	}
	for class, value := range catalog {
		if !validClass(class) || value == "" || len(value) > maxValue || !utf8.ValidString(value) || strings.ContainsAny(value, "\r\n") {
			return fmt.Errorf("invalid node value %q", class)
		}
	}
	return nil
}

func rejectDuplicateKeys(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	var walk func() error
	walk = func() error {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		delim, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		switch delim {
		case '{':
			seen := map[string]struct{}{}
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					return err
				}
				key := keyToken.(string)
				if _, exists := seen[key]; exists {
					return fmt.Errorf("duplicate JSON key %q", key)
				}
				seen[key] = struct{}{}
				if err := walk(); err != nil {
					return err
				}
			}
			_, err = decoder.Token()
			return err
		case '[':
			for decoder.More() {
				if err := walk(); err != nil {
					return err
				}
			}
			_, err = decoder.Token()
			return err
		default:
			return fmt.Errorf("unexpected JSON delimiter")
		}
	}
	if err := walk(); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return fmt.Errorf("trailing JSON data")
	}
	return nil
}

// ResolveRoot returns the configured root. A caller can treat an absent root
// as optional while it continues to reject malformed or unsafe paths.
func ResolveRoot(defaultRoot string) (string, error) {
	if override, set := os.LookupEnv("ALMAGEST_LOCALES_DIR"); set {
		if !filepath.IsAbs(override) || filepath.Clean(override) != override {
			return "", fmt.Errorf("ALMAGEST_LOCALES_DIR must be an absolute clean path")
		}
		return override, nil
	}
	return defaultRoot, nil
}
