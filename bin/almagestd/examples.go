package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/asciimoth/almagest/bin/almagestd/localizedcontent"
	"github.com/asciimoth/almagest/bin/api"
	bolt "go.etcd.io/bbolt"
)

const (
	exampleManifestName     = "manifest.json"
	exampleFormatVersion    = 1
	exampleIDMaxBytes       = 96
	exampleCommentMaxBytes  = 4096
	exampleMaxCount         = 256
	exampleManifestMaxBytes = 64 << 10
)

type exampleTranslation struct {
	Name    string `json:"name,omitempty"`
	Comment string `json:"comment,omitempty"`
}

type exampleManifest struct {
	Format       int                           `json:"format"`
	Name         string                        `json:"name"`
	Comment      string                        `json:"comment"`
	Translations map[string]exampleTranslation `json:"translations,omitempty"`
}

type exampleStore struct {
	root *os.Root
}

func resolveExamplesRoot(defaultRoot string) (string, error) {
	if override, set := os.LookupEnv("ALMAGEST_EXAMPLES_DIR"); set {
		if !filepath.IsAbs(override) || filepath.Clean(override) != override {
			return "", errors.New("ALMAGEST_EXAMPLES_DIR must be an absolute clean path")
		}
		return override, nil
	}
	return defaultRoot, nil
}

func openExampleStore(path string) (*exampleStore, error) {
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, err
	}
	return &exampleStore{root: root}, nil
}

func (s *exampleStore) Close() error {
	if s == nil || s.root == nil {
		return nil
	}
	return s.root.Close()
}

func (s *exampleStore) List(locale string) ([]api.SnapshotInfo, error) {
	if s == nil || s.root == nil {
		return []api.SnapshotInfo{}, nil
	}
	root, err := s.root.Open(".")
	if err != nil {
		return nil, fmt.Errorf("open examples root: %w", err)
	}
	entries, readErr := root.ReadDir(exampleMaxCount + 1)
	closeErr := root.Close()
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		return nil, fmt.Errorf("list examples: %w", readErr)
	}
	if closeErr != nil {
		return nil, closeErr
	}
	if len(entries) > exampleMaxCount {
		return nil, fmt.Errorf("examples root has more than %d entries", exampleMaxCount)
	}
	slices.SortFunc(entries, func(a, b os.DirEntry) int { return strings.Compare(a.Name(), b.Name()) })
	result := make([]api.SnapshotInfo, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() || validateExampleID(entry.Name()) != nil {
			continue
		}
		manifest, err := s.readManifest(entry.Name())
		if err != nil {
			return nil, err
		}
		name, comment := localizedExampleMetadata(manifest, locale)
		result = append(result, api.SnapshotInfo{Example: entry.Name(), Name: name, Comment: comment})
	}
	return result, nil
}

func localizedExampleMetadata(manifest exampleManifest, locale string) (string, string) {
	translation, ok := manifest.Translations[locale]
	if !ok {
		if dash := strings.IndexByte(locale, '-'); dash > 0 {
			translation, ok = manifest.Translations[locale[:dash]]
		}
	}
	if !ok {
		return manifest.Name, manifest.Comment
	}
	name, comment := translation.Name, translation.Comment
	if name == "" {
		name = manifest.Name
	}
	if comment == "" {
		comment = manifest.Comment
	}
	return name, comment
}

func (s *exampleStore) readManifest(id string) (exampleManifest, error) {
	if err := validateExampleID(id); err != nil {
		return exampleManifest{}, err
	}
	directory, err := s.root.Lstat(id)
	if err != nil {
		return exampleManifest{}, fmt.Errorf("inspect example %q: %w", id, err)
	}
	if !directory.IsDir() {
		return exampleManifest{}, fmt.Errorf("%w: example %q is not a directory", errSnapshotInvalid, id)
	}
	data, err := readExampleFile(s.root, filepath.ToSlash(filepath.Join(id, exampleManifestName)), exampleManifestMaxBytes)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return exampleManifest{}, fmt.Errorf("%w: read example %q manifest: %v", errSnapshotInvalid, id, err)
		}
		return exampleManifest{}, fmt.Errorf("read example %q manifest: %w", id, err)
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	var manifest exampleManifest
	if err := decoder.Decode(&manifest); err != nil {
		return exampleManifest{}, fmt.Errorf("%w: decode example %q manifest: %v", errSnapshotInvalid, id, err)
	}
	if decoder.Decode(new(any)) != io.EOF || manifest.Format != exampleFormatVersion {
		return exampleManifest{}, fmt.Errorf("%w: invalid example %q manifest", errSnapshotInvalid, id)
	}
	if err := validateExampleText(manifest.Name, snapshotNameMaxBytes, false); err != nil {
		return exampleManifest{}, fmt.Errorf("%w: example %q name: %v", errSnapshotInvalid, id, err)
	}
	if err := validateExampleText(manifest.Comment, exampleCommentMaxBytes, false); err != nil {
		return exampleManifest{}, fmt.Errorf("%w: example %q comment: %v", errSnapshotInvalid, id, err)
	}
	for locale, translation := range manifest.Translations {
		if canonical, ok := localizedcontent.CanonicalLocale(locale); !ok || canonical != locale {
			return exampleManifest{}, fmt.Errorf("%w: example %q has invalid locale %q", errSnapshotInvalid, id, locale)
		}
		if err := validateExampleText(translation.Name, snapshotNameMaxBytes, true); err != nil {
			return exampleManifest{}, fmt.Errorf("%w: example %q translated name: %v", errSnapshotInvalid, id, err)
		}
		if err := validateExampleText(translation.Comment, exampleCommentMaxBytes, true); err != nil {
			return exampleManifest{}, fmt.Errorf("%w: example %q translated comment: %v", errSnapshotInvalid, id, err)
		}
	}
	return manifest, nil
}

func validateExampleID(id string) error {
	if id == "" || len(id) > exampleIDMaxBytes || !utf8.ValidString(id) {
		return fmt.Errorf("%w: invalid example identifier", errSnapshotInvalid)
	}
	for index, r := range id {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || index > 0 && (r == '-' || r == '_') {
			continue
		}
		return fmt.Errorf("%w: invalid example identifier", errSnapshotInvalid)
	}
	if err := validateSnapshotPathComponent(id); err != nil {
		return fmt.Errorf("%w: invalid example identifier: %v", errSnapshotInvalid, err)
	}
	return nil
}

func validateExampleText(value string, limit int, optional bool) error {
	if optional && value == "" {
		return nil
	}
	if strings.TrimSpace(value) == "" || len(value) > limit || !utf8.ValidString(value) || strings.ContainsRune(value, 0) {
		return errors.New("text is empty, invalid, or too long")
	}
	return nil
}

func readExampleFile(root *os.Root, name string, limit uint64) ([]byte, error) {
	info, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() < 0 || uint64(info.Size()) > limit {
		return nil, fmt.Errorf("file is not a bounded regular file")
	}
	file, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	defer file.Close() //nolint:errcheck
	info, err = file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() < 0 || uint64(info.Size()) > limit {
		return nil, fmt.Errorf("file is not a bounded regular file")
	}
	data, err := io.ReadAll(io.LimitReader(file, int64(limit)+1))
	if err != nil || uint64(len(data)) > limit {
		return nil, errors.Join(errSnapshotTooLarge, err)
	}
	return data, nil
}

func (s *snapshotService) loadExample(ctx context.Context, id, locale string) (*loadedSnapshot, error) {
	if s.examples == nil {
		return nil, errSnapshotNotFound
	}
	manifest, err := s.examples.readManifest(id)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, errSnapshotNotFound
		}
		return nil, err
	}
	configData, err := readExampleFile(s.examples.root, filepath.ToSlash(filepath.Join(id, snapshotConfigName)), snapshotConfigMaxBytes)
	if err != nil {
		return nil, fmt.Errorf("%w: read example %q config: %v", errSnapshotInvalid, id, err)
	}
	config, err := parseEditableConfig(configData)
	if err != nil {
		return nil, fmt.Errorf("%w: parse example %q config: %v", errSnapshotInvalid, id, err)
	}
	stagingRoot := filepath.Join(s.storagePath, snapshotDirectoryName)
	if err := os.MkdirAll(stagingRoot, 0o700); err != nil {
		return nil, err
	}
	stage, err := os.MkdirTemp(stagingRoot, ".example-*")
	if err != nil {
		return nil, err
	}
	name, comment := localizedExampleMetadata(manifest, locale)
	loaded := &loadedSnapshot{
		info: api.SnapshotInfo{Example: id, Name: name, Comment: comment}, restoreID: exampleRestoreUUID(id),
		config: config, configData: configData, filesPath: filepath.Join(stage, "files"), cleanup: func() { _ = os.RemoveAll(stage) },
	}
	fail := func(cause error) (*loadedSnapshot, error) { loaded.cleanup(); return nil, cause }
	if err := os.MkdirAll(loaded.filesPath, 0o700); err != nil {
		return fail(err)
	}
	if err := copyExampleFiles(ctx, s, s.examples.root, filepath.ToSlash(filepath.Join(id, "files")), loaded.filesPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fail(err)
	}
	databasePath := filepath.Join(loaded.filesPath, kvDBFileName)
	if _, err := os.Stat(databasePath); errors.Is(err, os.ErrNotExist) {
		database, openErr := bolt.Open(databasePath, 0o600, nil)
		if openErr != nil {
			return fail(openErr)
		}
		if closeErr := database.Close(); closeErr != nil {
			return fail(closeErr)
		}
	} else if err != nil {
		return fail(err)
	}
	if err := validateBboltDatabase(databasePath); err != nil {
		return fail(fmt.Errorf("%w: example database: %v", errSnapshotInvalid, err))
	}
	if _, err := readFlags(filepath.Join(loaded.filesPath, flagsFileName)); err != nil {
		return fail(fmt.Errorf("%w: example flags file: %v", errSnapshotInvalid, err))
	}
	var stagedBytes uint64
	if err := filepath.WalkDir(loaded.filesPath, func(path string, _ os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		return reserveSnapshotStorageBytes(path, &stagedBytes)
	}); err != nil {
		return fail(err)
	}
	return loaded, nil
}

func copyExampleFiles(ctx context.Context, service *snapshotService, root *os.Root, source, destination string) error {
	info, err := root.Lstat(source)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("%w: example files root is not a directory", errSnapshotInvalid)
	}
	directory, err := root.Open(source)
	if err != nil {
		return err
	}
	defer directory.Close() //nolint:errcheck
	entries := snapshotEntrySet{paths: map[string]string{}, entries: map[string]string{}}
	var storedBytes uint64
	var walk func(string, string, int) error
	walk = func(relative, target string, depth int) error {
		if depth > snapshotPathMaxDepth {
			return errSnapshotTooLarge
		}
		path := source
		if relative != "" {
			path += "/" + relative
		}
		current, err := root.Open(path)
		if err != nil {
			return err
		}
		children, readErr := current.ReadDir(snapshotEntryMaxCount + 1)
		closeErr := current.Close()
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return readErr
		}
		if closeErr != nil {
			return closeErr
		}
		for _, child := range children {
			if err := ctx.Err(); err != nil {
				return err
			}
			childRelative := child.Name()
			if relative != "" {
				childRelative = relative + "/" + childRelative
			}
			if service.storagePathExcluded(filepath.FromSlash(childRelative)) {
				return fmt.Errorf("%w: example contains excluded storage path %q", errSnapshotInvalid, childRelative)
			}
			cleaned, err := cleanSnapshotEntryName(snapshotFilesPrefix + childRelative)
			if err != nil {
				return err
			}
			if err := entries.register(cleaned); err != nil {
				return err
			}
			info, err := child.Info()
			if err != nil {
				return err
			}
			childTarget := filepath.Join(target, filepath.FromSlash(child.Name()))
			switch {
			case info.IsDir():
				if err := os.Mkdir(childTarget, 0o700); err != nil {
					return err
				}
				if err := walk(childRelative, childTarget, depth+1); err != nil {
					return err
				}
			case info.Mode().IsRegular():
				if info.Size() < 0 || uint64(info.Size()) > snapshotExtractedMaxBytes-storedBytes {
					return errSnapshotTooLarge
				}
				storedBytes += uint64(info.Size())
				if err := copyExampleFile(root, source+"/"+childRelative, childTarget, info.Size()); err != nil {
					return err
				}
			default:
				return fmt.Errorf("%w: example file %q has unsupported mode %s", errSnapshotInvalid, childRelative, info.Mode())
			}
		}
		return nil
	}
	return walk("", destination, 0)
}

func copyExampleFile(root *os.Root, source, destination string, expectedSize int64) error {
	input, err := root.Open(source)
	if err != nil {
		return err
	}
	output, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		_ = input.Close()
		return err
	}
	written, copyErr := io.Copy(output, io.LimitReader(input, expectedSize+1))
	closeOutputErr := output.Close()
	closeInputErr := input.Close()
	if copyErr != nil {
		return copyErr
	}
	if written != expectedSize {
		return fmt.Errorf("%w: example file size changed while it was copied", errSnapshotInvalid)
	}
	return errors.Join(closeOutputErr, closeInputErr)
}

func exampleRestoreUUID(id string) string {
	digest := sha256.Sum256([]byte("almagest-example:" + id))
	digest[6] = digest[6]&0x0f | 0x50
	digest[8] = digest[8]&0x3f | 0x80
	value := hex.EncodeToString(digest[:16])
	return value[:8] + "-" + value[8:12] + "-" + value[12:16] + "-" + value[16:20] + "-" + value[20:]
}
