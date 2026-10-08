package main

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/asciimoth/almagest/bin/api"
	"github.com/asciimoth/almagest/bin/internal/durablefile"
	almagest "github.com/asciimoth/almagest/lib"
	"github.com/asciimoth/almagest/lib/logging"
	"github.com/asciimoth/configer/hujson"
	"github.com/asciimoth/gonnect/tlspolicy"
)

const (
	snapshotDirectoryName  = "snapshots"
	snapshotArchiveVersion = 1
	snapshotManifestName   = "manifest.json"
	snapshotConfigName     = "config.hujson"
	snapshotFilesPrefix    = "files/"

	// The JSON API keeps an archive and its base64 representation in memory.
	// Keep compressed archives small enough for that transport, and separately
	// bound extracted data so a highly compressed ZIP cannot exhaust storage.
	snapshotExtractedMaxBytes uint64 = 256 << 20
	snapshotManifestMaxBytes  uint64 = 64 << 10
	snapshotConfigMaxBytes    uint64 = 8 << 20
	snapshotNameMaxBytes             = 256
)

// snapshotExcludedStoragePaths is the explicit list of file-storage-relative
// paths that snapshots never contain. A directory entry excludes its complete
// subtree. Add future daemon-local transient paths here.
var snapshotExcludedStoragePaths = []string{
	snapshotDirectoryName,
}

var (
	errSnapshotNotFound = errors.New("snapshot was not found")
	errSnapshotInvalid  = errors.New("snapshot is invalid")
	errSnapshotTooLarge = errors.New("snapshot exceeds the size limit")
)

type snapshotManifest struct {
	Version  int              `json:"version"`
	Snapshot api.SnapshotInfo `json:"snapshot"`
}

type snapshotConfigFileLocator interface {
	ConfigFilePath() string
	ProtectedConfigFilePath() string
}

type snapshotService struct {
	storagePath string
	autosaver   *configAutosaver
	engine      almagest.Engine
	manager     *bboltKVManager
	config      *Config
	readOnly    bool
	logger      logging.Logger
	tlsFetcher  tlspolicy.AuthorityFetcher

	mu     sync.Mutex
	now    func() time.Time
	random io.Reader

	// dynamicExclusions protects config source files that happen to be inside
	// file storage. The sanitized regular config has its own archive entry, and
	// protected config must never enter an archive.
	dynamicExclusions map[string]struct{}
}

type loadedSnapshot struct {
	info       api.SnapshotInfo
	config     *hujson.Config
	configData []byte
	filesPath  string
	cleanup    func()
}

// startupStorageRecovery is a private copy of the storage state that existed
// before --snapshot started to mutate it. Config source files and the snapshot
// store are excluded because restore must keep those host-local paths intact.
type startupStorageRecovery struct {
	filesPath       string
	databasePresent bool
	cleanup         func()
}

type storageSnapshotIndex struct {
	dnsCaches   []string
	secrets     []string
	tlsCerts    []string
	tlsPolicies []string
}

func newSnapshotService(
	storagePath string,
	autosaver *configAutosaver,
	engine almagest.Engine,
	manager *bboltKVManager,
	config *Config,
	configLocator snapshotConfigFileLocator,
	readOnly bool,
	logger logging.Logger,
	tlsFetcher tlspolicy.AuthorityFetcher,
) *snapshotService {
	service := &snapshotService{
		storagePath:       storagePath,
		autosaver:         autosaver,
		engine:            engine,
		manager:           manager,
		config:            config,
		readOnly:          readOnly,
		logger:            logger,
		tlsFetcher:        tlsFetcher,
		now:               time.Now,
		random:            rand.Reader,
		dynamicExclusions: map[string]struct{}{},
	}
	if configLocator != nil {
		service.addDynamicExclusion(configLocator.ConfigFilePath())
		service.addDynamicExclusion(configLocator.ProtectedConfigFilePath())
	}
	return service
}

func (s *snapshotService) addDynamicExclusion(path string) {
	relative, ok := pathWithinStorage(s.storagePath, path)
	if !ok {
		// Config and storage paths can use different symlink aliases for the
		// same directory. Resolve both existing paths before deciding that a
		// config source is outside storage.
		resolvedStorage, storageErr := filepath.EvalSymlinks(s.storagePath)
		resolvedPath, pathErr := filepath.EvalSymlinks(path)
		if storageErr == nil && pathErr == nil {
			relative, ok = pathWithinStorage(resolvedStorage, resolvedPath)
		}
	}
	if ok {
		s.dynamicExclusions[relative] = struct{}{}
	}
}

func pathWithinStorage(root, path string) (string, bool) {
	if root == "" || path == "" {
		return "", false
	}
	relative, err := filepath.Rel(root, path)
	if err != nil || relative == "." || filepath.IsAbs(relative) || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", false
	}
	return filepath.Clean(relative), true
}

// restoreStartupSnapshot validates and restores an archive before bbolt and
// the engine are opened. Protected config source files and the snapshot store
// remain host-local, as they do during a live restore.
func restoreStartupSnapshot(
	ctx context.Context,
	storagePath string,
	archivePath string,
	stdin io.Reader,
	locator any,
) ([]byte, api.SnapshotInfo, error) {
	if storagePath == "" {
		return nil, api.SnapshotInfo{}, fmt.Errorf("snapshot storage is unavailable")
	}
	directory := filepath.Join(storagePath, snapshotDirectoryName)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return nil, api.SnapshotInfo{}, fmt.Errorf("create snapshots directory: %w", err)
	}

	var source io.ReadCloser
	if archivePath == "-" {
		if stdin == nil {
			return nil, api.SnapshotInfo{}, fmt.Errorf("snapshot archive path - requires stdin")
		}
		source = io.NopCloser(stdin)
	} else {
		file, err := os.Open(archivePath)
		if err != nil {
			return nil, api.SnapshotInfo{}, fmt.Errorf("open startup snapshot archive %s: %w", archivePath, err)
		}
		source = file
	}
	defer source.Close() //nolint:errcheck

	temporary, err := os.CreateTemp(directory, ".startup-*.tmp")
	if err != nil {
		return nil, api.SnapshotInfo{}, fmt.Errorf("create startup snapshot copy: %w", err)
	}
	temporaryPath := temporary.Name()
	defer removeFileIfPresent(temporaryPath)
	_, copyErr := copySnapshotArchive(temporary, source)
	closeSourceErr := source.Close()
	if copyErr != nil {
		_ = temporary.Close()
		return nil, api.SnapshotInfo{}, fmt.Errorf("read startup snapshot archive: %w", copyErr)
	}
	if closeSourceErr != nil {
		_ = temporary.Close()
		return nil, api.SnapshotInfo{}, fmt.Errorf("close startup snapshot archive: %w", closeSourceErr)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return nil, api.SnapshotInfo{}, fmt.Errorf("sync startup snapshot archive: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return nil, api.SnapshotInfo{}, fmt.Errorf("close startup snapshot archive: %w", err)
	}

	service := &snapshotService{
		storagePath:       storagePath,
		dynamicExclusions: map[string]struct{}{},
	}
	if configLocator, ok := locator.(snapshotConfigFileLocator); ok {
		service.addDynamicExclusion(configLocator.ConfigFilePath())
		service.addDynamicExclusion(configLocator.ProtectedConfigFilePath())
	}
	loaded, err := service.loadArchive(ctx, temporaryPath, "")
	if err != nil {
		return nil, api.SnapshotInfo{}, err
	}
	defer loaded.cleanup()
	recovery, err := service.captureStartupRecovery()
	if err != nil {
		return nil, api.SnapshotInfo{}, fmt.Errorf("capture pre-restore startup state: %w", err)
	}
	defer recovery.cleanup()
	if err := service.applyStartupStorage(loaded.filesPath); err != nil {
		if rollbackErr := service.restoreStartupRecovery(recovery); rollbackErr != nil {
			return nil, api.SnapshotInfo{}, fmt.Errorf("restore startup snapshot: %w; rollback failed: %v", err, rollbackErr)
		}
		return nil, api.SnapshotInfo{}, fmt.Errorf("restore startup snapshot: %w", err)
	}
	return bytes.Clone(loaded.configData), loaded.info, nil
}

// Create writes one self-contained zip archive under file-storage/snapshots.
// The config entry is sanitized independently from the file walk.
func (s *snapshotService) Create(ctx context.Context, name string) (api.SnapshotInfo, error) {
	if s == nil {
		return api.SnapshotInfo{}, fmt.Errorf("snapshot service is not ready")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ready(); err != nil {
		return api.SnapshotInfo{}, err
	}
	if err := validateSnapshotName(name); err != nil {
		return api.SnapshotInfo{}, err
	}

	s.autosaver.saveMu.Lock()
	defer s.autosaver.saveMu.Unlock()
	configData, err := s.autosaver.currentConfigBytes()
	if err != nil {
		return api.SnapshotInfo{}, fmt.Errorf("export snapshot config: %w", err)
	}
	configData, err = configOnlyBytes(configData)
	if err != nil {
		return api.SnapshotInfo{}, fmt.Errorf("sanitize snapshot config: %w", err)
	}

	uuid, err := newSnapshotUUID(s.random)
	if err != nil {
		return api.SnapshotInfo{}, err
	}
	info := api.SnapshotInfo{UUID: uuid, Timestamp: s.now().UnixNano(), Name: name}
	directory := filepath.Join(s.storagePath, snapshotDirectoryName)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return api.SnapshotInfo{}, fmt.Errorf("create snapshots directory: %w", err)
	}
	temporary, err := os.CreateTemp(directory, ".create-*.tmp")
	if err != nil {
		return api.SnapshotInfo{}, fmt.Errorf("create snapshot archive: %w", err)
	}
	temporaryPath := temporary.Name()
	if err := temporary.Close(); err != nil {
		removeFileIfPresent(temporaryPath)
		return api.SnapshotInfo{}, err
	}
	defer removeFileIfPresent(temporaryPath)

	if err := s.writeArchive(ctx, temporaryPath, info, configData); err != nil {
		return api.SnapshotInfo{}, err
	}
	if err := validateSnapshotArchiveSize(temporaryPath); err != nil {
		return api.SnapshotInfo{}, err
	}
	finalPath := filepath.Join(directory, uuid+".zip")
	// A hard-link publication is atomic and cannot replace an archive if the
	// generated UUID already exists. Both paths are in the same directory.
	if err := os.Link(temporaryPath, finalPath); errors.Is(err, fs.ErrExist) {
		return api.SnapshotInfo{}, fmt.Errorf("%w: generated UUID already exists", errSnapshotInvalid)
	} else if err != nil {
		return api.SnapshotInfo{}, fmt.Errorf("publish snapshot archive: %w", err)
	}
	return info, nil
}

// List validates snapshot manifests and returns newest snapshots first.
func (s *snapshotService) List() ([]api.SnapshotInfo, error) {
	if s == nil {
		return nil, fmt.Errorf("snapshot storage is unavailable")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.storagePath == "" {
		return nil, fmt.Errorf("snapshot storage is unavailable")
	}
	directory := filepath.Join(s.storagePath, snapshotDirectoryName)
	entries, err := os.ReadDir(directory)
	if errors.Is(err, os.ErrNotExist) {
		return []api.SnapshotInfo{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("list snapshot archives: %w", err)
	}
	result := make([]api.SnapshotInfo, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".zip" {
			continue
		}
		uuid, err := normalizeSnapshotUUID(strings.TrimSuffix(entry.Name(), ".zip"))
		if err != nil {
			continue
		}
		manifest, err := readSnapshotManifest(filepath.Join(directory, entry.Name()))
		if err != nil {
			return nil, err
		}
		if manifest.Snapshot.UUID != uuid {
			return nil, fmt.Errorf("%w: archive %s UUID does not match its manifest", errSnapshotInvalid, entry.Name())
		}
		result = append(result, manifest.Snapshot)
	}
	slices.SortFunc(result, func(left, right api.SnapshotInfo) int {
		if left.Timestamp != right.Timestamp {
			if left.Timestamp > right.Timestamp {
				return -1
			}
			return 1
		}
		return strings.Compare(left.UUID, right.UUID)
	})
	return result, nil
}

// Delete removes one snapshot archive by UUID.
func (s *snapshotService) Delete(uuid string) error {
	if s == nil {
		return fmt.Errorf("snapshot storage is unavailable")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.storagePath == "" {
		return fmt.Errorf("snapshot storage is unavailable")
	}
	uuid, err := normalizeSnapshotUUID(uuid)
	if err != nil {
		return err
	}
	path := filepath.Join(s.storagePath, snapshotDirectoryName, uuid+".zip")
	if err := os.Remove(path); errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("%w: %s", errSnapshotNotFound, uuid)
	} else if err != nil {
		return fmt.Errorf("delete snapshot archive: %w", err)
	}
	return nil
}

// Export reads one validated snapshot archive from the store. The caller gets
// a private byte slice, so later rename or delete operations cannot change the
// response while it is being sent.
func (s *snapshotService) Export(uuid string) (api.SnapshotsExportResponse, error) {
	if s == nil {
		return api.SnapshotsExportResponse{}, fmt.Errorf("snapshot storage is unavailable")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.storagePath == "" {
		return api.SnapshotsExportResponse{}, fmt.Errorf("snapshot storage is unavailable")
	}
	uuid, err := normalizeSnapshotUUID(uuid)
	if err != nil {
		return api.SnapshotsExportResponse{}, err
	}
	path := filepath.Join(s.storagePath, snapshotDirectoryName, uuid+".zip")
	if err := validateSnapshotArchiveSize(path); err != nil {
		return api.SnapshotsExportResponse{}, err
	}
	manifest, err := readSnapshotManifest(path)
	if err != nil {
		return api.SnapshotsExportResponse{}, err
	}
	if manifest.Snapshot.UUID != uuid {
		return api.SnapshotsExportResponse{}, fmt.Errorf("%w: archive UUID does not match its manifest", errSnapshotInvalid)
	}
	archive, err := os.ReadFile(path)
	if err != nil {
		return api.SnapshotsExportResponse{}, fmt.Errorf("read snapshot archive: %w", err)
	}
	return api.SnapshotsExportResponse{Snapshot: manifest.Snapshot, Archive: archive}, nil
}

// Import validates an exported archive and publishes it in the snapshot store.
// Existing UUIDs are never replaced. An optional name changes only the copied
// manifest and does not modify the caller's archive.
func (s *snapshotService) Import(ctx context.Context, archive io.Reader, name *string) (api.SnapshotInfo, error) {
	if s == nil {
		return api.SnapshotInfo{}, fmt.Errorf("snapshot storage is unavailable")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.storagePath == "" {
		return api.SnapshotInfo{}, fmt.Errorf("snapshot storage is unavailable")
	}
	if archive == nil {
		return api.SnapshotInfo{}, fmt.Errorf("%w: archive is missing", errSnapshotInvalid)
	}
	if name != nil {
		if err := validateSnapshotName(*name); err != nil {
			return api.SnapshotInfo{}, err
		}
	}
	directory := filepath.Join(s.storagePath, snapshotDirectoryName)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return api.SnapshotInfo{}, fmt.Errorf("create snapshots directory: %w", err)
	}
	temporary, err := os.CreateTemp(directory, ".import-*.tmp")
	if err != nil {
		return api.SnapshotInfo{}, fmt.Errorf("create imported snapshot archive: %w", err)
	}
	temporaryPath := temporary.Name()
	defer removeFileIfPresent(temporaryPath)
	if _, err := copySnapshotArchive(temporary, archive); err != nil {
		_ = temporary.Close()
		return api.SnapshotInfo{}, fmt.Errorf("read imported snapshot archive: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return api.SnapshotInfo{}, fmt.Errorf("sync imported snapshot archive: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return api.SnapshotInfo{}, fmt.Errorf("close imported snapshot archive: %w", err)
	}

	loaded, err := s.loadArchive(ctx, temporaryPath, "")
	if err != nil {
		return api.SnapshotInfo{}, err
	}
	info := loaded.info
	loaded.cleanup()
	if name != nil && info.Name != *name {
		info, err = rewriteSnapshotName(temporaryPath, info.UUID, *name)
		if err != nil {
			return api.SnapshotInfo{}, err
		}
	}
	finalPath := filepath.Join(directory, info.UUID+".zip")
	if err := os.Link(temporaryPath, finalPath); errors.Is(err, fs.ErrExist) {
		return api.SnapshotInfo{}, fmt.Errorf("%w: snapshot UUID %s already exists", errSnapshotInvalid, info.UUID)
	} else if err != nil {
		return api.SnapshotInfo{}, fmt.Errorf("publish imported snapshot archive: %w", err)
	}
	return info, nil
}

// Rename updates the display name in one snapshot manifest. The archive is
// replaced atomically after all unchanged entries are copied in raw form.
func (s *snapshotService) Rename(uuid, name string) (api.SnapshotInfo, error) {
	if s == nil {
		return api.SnapshotInfo{}, fmt.Errorf("snapshot storage is unavailable")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.storagePath == "" {
		return api.SnapshotInfo{}, fmt.Errorf("snapshot storage is unavailable")
	}
	if err := validateSnapshotName(name); err != nil {
		return api.SnapshotInfo{}, err
	}
	uuid, err := normalizeSnapshotUUID(uuid)
	if err != nil {
		return api.SnapshotInfo{}, err
	}
	directory := filepath.Join(s.storagePath, snapshotDirectoryName)
	path := filepath.Join(directory, uuid+".zip")
	return rewriteSnapshotName(path, uuid, name)
}

// rewriteSnapshotName replaces only the manifest entry and raw-copies all
// other ZIP entries. The replacement is atomic because its temporary file is
// in the same directory as the source archive.
func rewriteSnapshotName(path, expectedUUID, name string) (api.SnapshotInfo, error) {
	if err := validateSnapshotName(name); err != nil {
		return api.SnapshotInfo{}, err
	}
	manifest, err := readSnapshotManifest(path)
	if err != nil {
		return api.SnapshotInfo{}, err
	}
	if manifest.Snapshot.UUID != expectedUUID {
		return api.SnapshotInfo{}, fmt.Errorf("%w: archive UUID does not match its manifest", errSnapshotInvalid)
	}
	manifest.Snapshot.Name = name

	source, err := zip.OpenReader(path)
	if err != nil {
		return api.SnapshotInfo{}, fmt.Errorf("%w: open archive: %v", errSnapshotInvalid, err)
	}
	defer source.Close() //nolint:errcheck
	temporary, err := os.CreateTemp(filepath.Dir(path), ".rename-*.tmp")
	if err != nil {
		return api.SnapshotInfo{}, fmt.Errorf("create renamed snapshot archive: %w", err)
	}
	temporaryPath := temporary.Name()
	defer removeFileIfPresent(temporaryPath)
	zw := zip.NewWriter(temporary)
	fail := func(cause error) (api.SnapshotInfo, error) {
		_ = zw.Close()
		_ = temporary.Close()
		return api.SnapshotInfo{}, cause
	}
	manifestData, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return fail(err)
	}
	manifestWriter, err := zw.Create(snapshotManifestName)
	if err != nil {
		return fail(err)
	}
	if _, err := manifestWriter.Write(manifestData); err != nil {
		return fail(err)
	}
	for _, file := range source.File {
		if file.Name == snapshotManifestName {
			continue
		}
		if err := zw.Copy(file); err != nil {
			return fail(fmt.Errorf("copy snapshot entry %s: %w", file.Name, err))
		}
	}
	if err := zw.Close(); err != nil {
		_ = temporary.Close()
		return api.SnapshotInfo{}, fmt.Errorf("finalize renamed snapshot archive: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return api.SnapshotInfo{}, err
	}
	if err := validateSnapshotArchiveSize(temporaryPath); err != nil {
		return api.SnapshotInfo{}, err
	}
	if err := source.Close(); err != nil {
		return api.SnapshotInfo{}, fmt.Errorf("close source snapshot archive: %w", err)
	}
	if err := durablefile.Replace(path, temporaryPath); err != nil {
		return api.SnapshotInfo{}, fmt.Errorf("replace renamed snapshot archive: %w", err)
	}
	return manifest.Snapshot, nil
}

// Restore replaces persistent storage and reconstructs every graph node from
// the snapshot config. Failure triggers a best-effort rollback from an internal
// archive captured immediately before the restore.
func (s *snapshotService) Restore(ctx context.Context, uuid string) (api.SnapshotsRestoreResponse, error) {
	if s == nil {
		return api.SnapshotsRestoreResponse{}, fmt.Errorf("snapshot service is not ready")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ready(); err != nil {
		return api.SnapshotsRestoreResponse{}, err
	}
	uuid, err := normalizeSnapshotUUID(uuid)
	if err != nil {
		return api.SnapshotsRestoreResponse{}, err
	}
	desired, err := s.loadArchive(ctx, filepath.Join(s.storagePath, snapshotDirectoryName, uuid+".zip"), uuid)
	if err != nil {
		return api.SnapshotsRestoreResponse{}, err
	}
	defer desired.cleanup()

	s.autosaver.saveMu.Lock()
	defer s.autosaver.saveMu.Unlock()
	recovery, err := s.captureRecoveryArchive(ctx)
	if err != nil {
		return api.SnapshotsRestoreResponse{}, fmt.Errorf("capture pre-restore state: %w", err)
	}
	defer recovery.cleanup()

	// Keep the previous graph visible while restore tears down and reconstructs
	// runtime nodes. Publish always runs, including after a successful rollback,
	// so subscribers receive one authoritative view of the state that remains.
	notifications := s.engine.BeginNotificationTransaction()
	defer notifications.Publish()

	before := s.storageIndex()
	if err := s.applyLoadedSnapshot(ctx, desired); err != nil {
		if rollbackErr := s.applyLoadedSnapshot(ctx, recovery); rollbackErr != nil {
			return api.SnapshotsRestoreResponse{}, fmt.Errorf("restore snapshot: %w; rollback failed: %v", err, rollbackErr)
		}
		return api.SnapshotsRestoreResponse{}, fmt.Errorf("restore snapshot: %w", err)
	}
	after := s.storageIndex()
	s.notifyStorageReset(before, after)

	response := api.SnapshotsRestoreResponse{Snapshot: desired.info}
	if !s.readOnly && s.autosaver.persistence != nil {
		if err := s.autosaver.replaceConfigLocked(desired.configData); err != nil {
			response.ConfigWriteError = err.Error()
			if s.logger != nil {
				s.logger.Errf("snapshot restore config write failed; restored config remains active in memory: %v", err)
			}
		} else {
			response.ConfigWritten = true
		}
	}
	return response, nil
}

func (s *snapshotService) ready() error {
	if s == nil || s.storagePath == "" || s.autosaver == nil || s.engine == nil || s.manager == nil || s.config == nil {
		return fmt.Errorf("snapshot service is not ready")
	}
	return nil
}

func (s *snapshotService) captureRecoveryArchive(ctx context.Context) (*loadedSnapshot, error) {
	configData, err := s.autosaver.currentConfigBytes()
	if err != nil {
		return nil, err
	}
	configData, err = configOnlyBytes(configData)
	if err != nil {
		return nil, err
	}
	directory := filepath.Join(s.storagePath, snapshotDirectoryName)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return nil, err
	}
	file, err := os.CreateTemp(directory, ".rollback-*.tmp")
	if err != nil {
		return nil, err
	}
	path := file.Name()
	if err := file.Close(); err != nil {
		removeFileIfPresent(path)
		return nil, err
	}
	info := api.SnapshotInfo{UUID: "00000000-0000-4000-8000-000000000000", Timestamp: s.now().UnixNano()}
	if err := s.writeArchive(ctx, path, info, configData); err != nil {
		removeFileIfPresent(path)
		return nil, err
	}
	loaded, err := s.loadArchive(ctx, path, info.UUID)
	removeFileIfPresent(path)
	return loaded, err
}

func (s *snapshotService) writeArchive(ctx context.Context, archivePath string, info api.SnapshotInfo, configData []byte) error {
	databaseCopy, err := os.CreateTemp(filepath.Join(s.storagePath, snapshotDirectoryName), ".database-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary database snapshot: %w", err)
	}
	databasePath := databaseCopy.Name()
	if err := databaseCopy.Close(); err != nil {
		removeFileIfPresent(databasePath)
		return err
	}
	removeFileIfPresent(databasePath)
	defer removeFileIfPresent(databasePath)
	if err := s.manager.copyBboltDatabase(databasePath); err != nil {
		return err
	}

	archive, err := os.OpenFile(archivePath, os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("open snapshot archive: %w", err)
	}
	zw := zip.NewWriter(archive)
	fail := func(cause error) error {
		_ = zw.Close()
		_ = archive.Close()
		return cause
	}
	manifestData, err := json.MarshalIndent(snapshotManifest{Version: snapshotArchiveVersion, Snapshot: info}, "", "  ")
	if err != nil {
		return fail(err)
	}
	manifestData = append(manifestData, '\n')
	if uint64(len(manifestData)) > snapshotManifestMaxBytes {
		return fail(fmt.Errorf("%w: manifest exceeds %d bytes", errSnapshotTooLarge, snapshotManifestMaxBytes))
	}
	if uint64(len(configData)) > snapshotConfigMaxBytes {
		return fail(fmt.Errorf("%w: config exceeds %d bytes", errSnapshotTooLarge, snapshotConfigMaxBytes))
	}
	if err := writeZipBytes(zw, snapshotManifestName, manifestData, 0o600, s.now()); err != nil {
		return fail(err)
	}
	if err := writeZipBytes(zw, snapshotConfigName, configData, 0o600, s.now()); err != nil {
		return fail(err)
	}
	if err := s.writeStorageFiles(ctx, zw, databasePath); err != nil {
		return fail(err)
	}
	if err := zw.Close(); err != nil {
		_ = archive.Close()
		return fmt.Errorf("finish snapshot archive: %w", err)
	}
	if err := archive.Sync(); err != nil {
		_ = archive.Close()
		return fmt.Errorf("sync snapshot archive: %w", err)
	}
	if err := archive.Close(); err != nil {
		return fmt.Errorf("close snapshot archive: %w", err)
	}
	return nil
}

func (s *snapshotService) writeStorageFiles(ctx context.Context, zw *zip.Writer, databasePath string) error {
	var storedBytes uint64
	return filepath.WalkDir(s.storagePath, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if path == s.storagePath {
			return nil
		}
		relative, err := filepath.Rel(s.storagePath, path)
		if err != nil {
			return err
		}
		relative = filepath.Clean(relative)
		if s.storagePathExcluded(relative) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		archiveSourcePath := path
		if relative == kvDBFileName {
			archiveSourcePath = databasePath
		}
		if entry.Type()&os.ModeSymlink != 0 {
			if err := validateStorageSymlink(s.storagePath, path, s); err != nil {
				return err
			}
		}
		if err := reserveSnapshotStorageBytes(archiveSourcePath, &storedBytes); err != nil {
			return err
		}
		return writeZipFile(zw, snapshotFilesPrefix+filepath.ToSlash(relative), archiveSourcePath)
	})
}

func reserveSnapshotStorageBytes(path string, total *uint64) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.IsDir() {
		return nil
	}
	size := uint64(max(info.Size(), 0))
	if size > snapshotExtractedMaxBytes-*total {
		return fmt.Errorf("%w: stored files exceed %d bytes", errSnapshotTooLarge, snapshotExtractedMaxBytes)
	}
	*total += size
	return nil
}

func (s *snapshotService) storagePathExcluded(relative string) bool {
	relative = filepath.Clean(relative)
	for _, excluded := range snapshotExcludedStoragePaths {
		excluded = filepath.Clean(excluded)
		if relative == excluded || strings.HasPrefix(relative, excluded+string(filepath.Separator)) {
			return true
		}
	}
	_, excluded := s.dynamicExclusions[relative]
	return excluded
}

// storagePathContainsDynamicExclusion reports whether relative is an ancestor
// of an active config source. Such directories cannot be removed as a unit;
// mirroring must enter them and preserve the excluded descendant.
func (s *snapshotService) storagePathContainsDynamicExclusion(relative string) bool {
	relative = filepath.Clean(relative)
	for excluded := range s.dynamicExclusions {
		if relative == "." || excluded == relative || strings.HasPrefix(excluded, relative+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

func writeZipBytes(zw *zip.Writer, name string, data []byte, mode fs.FileMode, modified time.Time) error {
	header := &zip.FileHeader{Name: name, Method: zip.Deflate, Modified: modified}
	header.SetMode(mode)
	writer, err := zw.CreateHeader(header)
	if err != nil {
		return err
	}
	_, err = writer.Write(data)
	return err
}

func writeZipFile(zw *zip.Writer, name, path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	header, err := zip.FileInfoHeader(info)
	if err != nil {
		return err
	}
	header.Name = name
	if info.IsDir() {
		header.Name += "/"
		header.Method = zip.Store
		_, err = zw.CreateHeader(header)
		return err
	}
	if info.Mode().IsRegular() {
		header.Method = zip.Deflate
		writer, err := zw.CreateHeader(header)
		if err != nil {
			return err
		}
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(writer, file)
		closeErr := file.Close()
		if copyErr != nil {
			return copyErr
		}
		return closeErr
	}
	if info.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(path)
		if err != nil {
			return err
		}
		header.Method = zip.Store
		writer, err := zw.CreateHeader(header)
		if err != nil {
			return err
		}
		_, err = io.WriteString(writer, target)
		return err
	}
	return fmt.Errorf("snapshot file %s has unsupported mode %s", path, info.Mode())
}

func readSnapshotManifest(path string) (snapshotManifest, error) {
	if err := validateSnapshotArchiveSize(path); err != nil {
		return snapshotManifest{}, err
	}
	reader, err := zip.OpenReader(path)
	if errors.Is(err, os.ErrNotExist) {
		return snapshotManifest{}, errSnapshotNotFound
	}
	if err != nil {
		return snapshotManifest{}, fmt.Errorf("%w: open archive %s: %v", errSnapshotInvalid, filepath.Base(path), err)
	}
	defer reader.Close() //nolint:errcheck
	for _, file := range reader.File {
		if file.Name != snapshotManifestName {
			continue
		}
		data, err := readZipEntry(file, snapshotManifestMaxBytes)
		if err != nil {
			return snapshotManifest{}, err
		}
		var manifest snapshotManifest
		if err := json.Unmarshal(data, &manifest); err != nil {
			return snapshotManifest{}, fmt.Errorf("%w: decode manifest: %v", errSnapshotInvalid, err)
		}
		if manifest.Version != snapshotArchiveVersion {
			return snapshotManifest{}, fmt.Errorf("%w: unsupported archive version %d", errSnapshotInvalid, manifest.Version)
		}
		if _, err := normalizeSnapshotUUID(manifest.Snapshot.UUID); err != nil || manifest.Snapshot.Timestamp <= 0 {
			return snapshotManifest{}, fmt.Errorf("%w: invalid manifest metadata", errSnapshotInvalid)
		}
		if err := validateSnapshotName(manifest.Snapshot.Name); err != nil {
			return snapshotManifest{}, err
		}
		return manifest, nil
	}
	return snapshotManifest{}, fmt.Errorf("%w: manifest is missing", errSnapshotInvalid)
}

func (s *snapshotService) loadArchive(ctx context.Context, path, expectedUUID string) (*loadedSnapshot, error) {
	if err := validateSnapshotArchiveSize(path); err != nil {
		return nil, err
	}
	reader, err := zip.OpenReader(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, errSnapshotNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("%w: open archive: %v", errSnapshotInvalid, err)
	}
	defer reader.Close() //nolint:errcheck
	stage, err := os.MkdirTemp(filepath.Join(s.storagePath, snapshotDirectoryName), ".restore-*")
	if err != nil {
		return nil, fmt.Errorf("create restore staging directory: %w", err)
	}
	loaded := &loadedSnapshot{filesPath: filepath.Join(stage, "files"), cleanup: func() { _ = os.RemoveAll(stage) }}
	fail := func(cause error) (*loadedSnapshot, error) {
		loaded.cleanup()
		return nil, cause
	}
	seen := snapshotEntrySet{paths: map[string]string{}, entries: map[string]string{}}
	var manifestFound, configFound bool
	var extractedBytes uint64
	for _, file := range reader.File {
		if err := ctx.Err(); err != nil {
			return fail(err)
		}
		name, err := cleanSnapshotEntryName(file.Name)
		if err != nil {
			return fail(err)
		}
		if err := seen.register(name); err != nil {
			return fail(err)
		}
		switch {
		case name == snapshotManifestName:
			data, err := readZipEntry(file, snapshotManifestMaxBytes)
			if err != nil {
				return fail(err)
			}
			var manifest snapshotManifest
			if err := json.Unmarshal(data, &manifest); err != nil {
				return fail(fmt.Errorf("%w: decode manifest: %v", errSnapshotInvalid, err))
			}
			if manifest.Version != snapshotArchiveVersion || manifest.Snapshot.Timestamp <= 0 {
				return fail(fmt.Errorf("%w: manifest metadata does not match archive", errSnapshotInvalid))
			}
			uuid, err := normalizeSnapshotUUID(manifest.Snapshot.UUID)
			if err != nil || (expectedUUID != "" && uuid != expectedUUID) {
				return fail(fmt.Errorf("%w: manifest metadata does not match archive", errSnapshotInvalid))
			}
			manifest.Snapshot.UUID = uuid
			if err := validateSnapshotName(manifest.Snapshot.Name); err != nil {
				return fail(err)
			}
			loaded.info = manifest.Snapshot
			manifestFound = true
		case name == snapshotConfigName:
			data, err := readZipEntry(file, snapshotConfigMaxBytes)
			if err != nil {
				return fail(err)
			}
			data, err = configOnlyBytes(data)
			if err != nil {
				return fail(fmt.Errorf("%w: parse config: %v", errSnapshotInvalid, err))
			}
			config, err := parseEditableConfig(data)
			if err != nil {
				return fail(fmt.Errorf("%w: parse config: %v", errSnapshotInvalid, err))
			}
			loaded.config = config
			loaded.configData = data
			configFound = true
		case strings.HasPrefix(name, snapshotFilesPrefix):
			relative := strings.TrimPrefix(name, snapshotFilesPrefix)
			if relative == "" {
				continue
			}
			if file.UncompressedSize64 > snapshotExtractedMaxBytes-extractedBytes {
				return fail(fmt.Errorf("%w: extracted files exceed %d bytes", errSnapshotTooLarge, snapshotExtractedMaxBytes))
			}
			extractedBytes += file.UncompressedSize64
			if err := extractSnapshotFile(file, loaded.filesPath, relative); err != nil {
				return fail(err)
			}
		default:
			return fail(fmt.Errorf("%w: unknown archive entry %q", errSnapshotInvalid, name))
		}
	}
	if !manifestFound || !configFound {
		return fail(fmt.Errorf("%w: archive metadata or config is missing", errSnapshotInvalid))
	}
	if err := validateSnapshotSymlinks(loaded.filesPath, s); err != nil {
		return fail(err)
	}
	databasePath := filepath.Join(loaded.filesPath, kvDBFileName)
	if info, err := os.Lstat(databasePath); err != nil || !info.Mode().IsRegular() {
		return fail(fmt.Errorf("%w: bbolt database is missing", errSnapshotInvalid))
	}
	if err := validateBboltDatabase(databasePath); err != nil {
		return fail(fmt.Errorf("%w: %v", errSnapshotInvalid, err))
	}
	return loaded, nil
}

func cleanSnapshotEntryName(name string) (string, error) {
	name = strings.TrimSuffix(name, "/")
	if name == "" || strings.Contains(name, "\\") || strings.HasPrefix(name, "/") {
		return "", fmt.Errorf("%w: unsafe archive entry %q", errSnapshotInvalid, name)
	}
	cleaned := filepath.ToSlash(filepath.Clean(filepath.FromSlash(name)))
	if cleaned != name || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", fmt.Errorf("%w: unsafe archive entry %q", errSnapshotInvalid, name)
	}
	for _, component := range strings.Split(cleaned, "/") {
		if err := validateSnapshotPathComponent(component); err != nil {
			return "", fmt.Errorf("%w: unsafe archive entry %q: %v", errSnapshotInvalid, name, err)
		}
	}
	return cleaned, nil
}

// snapshotEntryAlias uses Windows path identity on every host. This prevents
// an archive accepted on Linux from selecting two destinations on Windows.
func snapshotEntryAlias(name string) string {
	return strings.ToLower(name)
}

type snapshotEntrySet struct {
	paths   map[string]string
	entries map[string]string
}

func (s snapshotEntrySet) register(name string) error {
	parts := strings.Split(name, "/")
	for i := range parts {
		path := strings.Join(parts[:i+1], "/")
		alias := snapshotEntryAlias(path)
		previous, exists := s.paths[alias]
		if exists && previous != path {
			return fmt.Errorf("%w: archive paths %q and %q differ only by a Windows alias", errSnapshotInvalid, previous, path)
		}
		if !exists {
			s.paths[alias] = path
		}
	}
	alias := snapshotEntryAlias(name)
	if previous, exists := s.entries[alias]; exists {
		return fmt.Errorf("%w: duplicate archive entries %q and %q", errSnapshotInvalid, previous, name)
	}
	s.entries[alias] = name
	return nil
}

func validateSnapshotPathComponent(component string) error {
	if component == "" || strings.ContainsRune(component, ':') {
		return errors.New("empty components and alternate streams are not permitted")
	}
	if strings.HasSuffix(component, ".") || strings.HasSuffix(component, " ") {
		return errors.New("trailing dots and spaces are not permitted")
	}
	base := component
	if dot := strings.IndexByte(base, '.'); dot >= 0 {
		base = base[:dot]
	}
	switch strings.ToUpper(base) {
	case "CON", "PRN", "AUX", "NUL", "CONIN$", "CONOUT$",
		"COM1", "COM2", "COM3", "COM4", "COM5", "COM6", "COM7", "COM8", "COM9",
		"LPT1", "LPT2", "LPT3", "LPT4", "LPT5", "LPT6", "LPT7", "LPT8", "LPT9":
		return errors.New("reserved Windows device names are not permitted")
	}
	return nil
}

func readZipEntry(file *zip.File, limit uint64) ([]byte, error) {
	if file.UncompressedSize64 > limit {
		return nil, fmt.Errorf("%w: archive entry %q exceeds %d bytes", errSnapshotTooLarge, file.Name, limit)
	}
	reader, err := file.Open()
	if err != nil {
		return nil, err
	}
	data, readErr := io.ReadAll(io.LimitReader(reader, int64(limit)+1))
	closeErr := reader.Close()
	if uint64(len(data)) > limit {
		return nil, fmt.Errorf("%w: archive entry %q exceeds %d bytes", errSnapshotTooLarge, file.Name, limit)
	}
	if readErr != nil {
		return nil, readErr
	}
	return data, closeErr
}

func extractSnapshotFile(file *zip.File, root, relative string) error {
	relative = filepath.FromSlash(relative)
	destination := filepath.Join(root, relative)
	if _, ok := pathWithinStorage(root, destination); !ok {
		return fmt.Errorf("%w: unsafe storage entry %q", errSnapshotInvalid, relative)
	}
	if err := rejectSymlinkExtractionParents(root, filepath.Dir(relative)); err != nil {
		return err
	}
	mode := file.Mode()
	if mode.IsDir() {
		return os.MkdirAll(destination, mode.Perm())
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return err
	}
	if mode&os.ModeSymlink != 0 {
		if !snapshotSymlinkRestoreSupported() {
			return fmt.Errorf("%w: symbolic-link entries are not supported on this platform", errSnapshotInvalid)
		}
		data, err := readZipEntry(file, snapshotExtractedMaxBytes)
		if err != nil {
			return err
		}
		return os.Symlink(string(data), destination)
	}
	if !mode.IsRegular() {
		return fmt.Errorf("%w: unsupported storage entry mode %s", errSnapshotInvalid, mode)
	}
	reader, err := file.Open()
	if err != nil {
		return err
	}
	destinationFile, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode.Perm())
	if err != nil {
		_ = reader.Close()
		return err
	}
	_, copyErr := io.Copy(destinationFile, reader)
	closeDestinationErr := destinationFile.Close()
	closeReaderErr := reader.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeDestinationErr != nil {
		return closeDestinationErr
	}
	return closeReaderErr
}

// rejectSymlinkExtractionParents prevents a prior archive symlink entry from
// redirecting a later file or directory outside the private staging tree.
func rejectSymlinkExtractionParents(root, relativeParent string) error {
	if relativeParent == "." || relativeParent == "" {
		return nil
	}
	current := root
	for _, part := range strings.Split(relativeParent, string(filepath.Separator)) {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		switch {
		case errors.Is(err, os.ErrNotExist):
			return nil
		case err != nil:
			return err
		case info.Mode()&os.ModeSymlink != 0:
			return fmt.Errorf("%w: storage entry has symlink parent %q", errSnapshotInvalid, relativeParent)
		case !info.IsDir():
			return fmt.Errorf("%w: storage entry has non-directory parent %q", errSnapshotInvalid, relativeParent)
		}
	}
	return nil
}

// validateSnapshotSymlinks checks the completed staging tree before any live
// state changes. EvalSymlinks follows complete chains, so this rejects absolute
// targets, escaping relative targets, dangling links, and chains that leave the
// staging root. A target that maps to a host-local config exclusion is also
// rejected because that path is intentionally different in live storage.
func validateSnapshotSymlinks(root string, service *snapshotService) error {
	return filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return fmt.Errorf("%w: inspect staged symlink: %v", errSnapshotInvalid, walkErr)
		}
		if entry.Type()&os.ModeSymlink == 0 {
			return nil
		}
		return validateStorageSymlink(root, path, service)
	})
}

// validateStorageSymlink permits only complete relative links that stay in the
// snapshotted part of file storage. This rule applies to locally created and
// imported archives, so a snapshot is never published in an unrestorable form.
func validateStorageSymlink(root, path string, service *snapshotService) error {
	if service != nil {
		if relative, ok := pathWithinStorage(root, path); ok && service.storagePathExcluded(relative) {
			return fmt.Errorf("%w: symlink %q occupies an excluded storage path", errSnapshotInvalid, path)
		}
	}
	target, err := os.Readlink(path)
	if err != nil {
		return fmt.Errorf("%w: read storage symlink %q: %v", errSnapshotInvalid, path, err)
	}
	if filepath.IsAbs(target) {
		return fmt.Errorf("%w: symlink %q has an absolute target", errSnapshotInvalid, path)
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return fmt.Errorf("%w: symlink %q is dangling or invalid: %v", errSnapshotInvalid, path, err)
	}
	resolved = filepath.Clean(resolved)
	if resolved == filepath.Clean(root) {
		return nil
	}
	relative, ok := pathWithinStorage(root, resolved)
	if !ok {
		return fmt.Errorf("%w: symlink %q resolves outside file storage", errSnapshotInvalid, path)
	}
	if service != nil && service.storagePathExcluded(relative) {
		return fmt.Errorf("%w: symlink %q resolves through an excluded storage path", errSnapshotInvalid, path)
	}
	return nil
}

func (s *snapshotService) applyLoadedSnapshot(ctx context.Context, loaded *loadedSnapshot) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	empty, err := parseEditableConfig([]byte(`{"nodes":{}}`))
	if err != nil {
		return err
	}
	if _, err := s.engine.ReconcileConfig(empty); err != nil {
		return fmt.Errorf("stop current engine graph: %w", err)
	}
	if err := s.restoreStorageFiles(loaded.filesPath); err != nil {
		return err
	}
	if err := s.manager.replaceBboltDatabase(filepath.Join(loaded.filesPath, kvDBFileName)); err != nil {
		return err
	}
	if err := s.restoreImmutableState(ctx, loaded.config); err != nil {
		return err
	}
	s.config.mu.Lock()
	s.config.Main = loaded.config
	s.config.mu.Unlock()
	s.autosaver.noteRestoredConfig()
	if _, err := s.engine.ReconcileConfig(loaded.config); err != nil {
		return fmt.Errorf("rebuild engine graph: %w", err)
	}
	activeNames := configuredNodeNameSet(loaded.config)
	if err := s.manager.reconcileSecretNodeReferences(activeNames); err != nil {
		return fmt.Errorf("reconcile restored secret references: %w", err)
	}
	if err := s.manager.reconcileKVOwnerEntries(activeNames); err != nil {
		return fmt.Errorf("reconcile restored KV ownership: %w", err)
	}
	return nil
}

func (s *snapshotService) restoreImmutableState(ctx context.Context, restored *hujson.Config) error {
	immutableSecrets, err := s.config.Protected.ImmutableSecrets()
	if err != nil {
		return err
	}
	osTLS, err := loadOSTLSData(ctx, s.tlsFetcher)
	if err != nil {
		return err
	}
	protectedTLS, err := s.config.Protected.ImmutableTLS()
	if err != nil {
		return err
	}
	builtinCA, err := s.config.Protected.TLSBuiltinCA()
	if err != nil {
		return err
	}
	if builtinCA == nil {
		temporaryConfig := &Config{Main: restored}
		options, err := temporaryConfig.TLSBuiltinCAOptions()
		if err != nil {
			return err
		}
		builtinCA, err = loadOrCreateTLSBuiltinCA(s.storagePath, options, s.now())
		if err != nil {
			return err
		}
	}
	s.manager.replaceImmutableStorageState(
		immutableSecrets,
		mergeImmutableTLSData(osTLS, protectedTLS),
		builtinCA,
	)
	return nil
}

func (s *snapshotService) restoreStorageFiles(sourceRoot string) error {
	return s.mirrorStorageDirectory(sourceRoot, s.storagePath, "", false)
}

// captureStartupRecovery copies the current closed storage tree before
// --snapshot changes it. The copy lives below snapshots/, which both the source
// walk and later restore operations exclude.
func (s *snapshotService) captureStartupRecovery() (*startupStorageRecovery, error) {
	stage, err := os.MkdirTemp(filepath.Join(s.storagePath, snapshotDirectoryName), ".startup-recovery-*")
	if err != nil {
		return nil, err
	}
	recovery := &startupStorageRecovery{
		filesPath: filepath.Join(stage, "files"),
		cleanup:   func() { _ = os.RemoveAll(stage) },
	}
	if err := os.MkdirAll(recovery.filesPath, 0o700); err != nil {
		recovery.cleanup()
		return nil, err
	}
	if _, err := os.Lstat(filepath.Join(s.storagePath, kvDBFileName)); err == nil {
		recovery.databasePresent = true
	} else if !errors.Is(err, os.ErrNotExist) {
		recovery.cleanup()
		return nil, err
	}
	if err := s.mirrorStorageDirectory(s.storagePath, recovery.filesPath, "", true); err != nil {
		recovery.cleanup()
		return nil, err
	}
	return recovery, nil
}

func (s *snapshotService) applyStartupStorage(sourceRoot string) error {
	if err := s.restoreStorageFiles(sourceRoot); err != nil {
		return fmt.Errorf("restore files: %w", err)
	}
	// At startup no bbolt handle exists, so publish the validated database file
	// directly after all other files have been mirrored.
	if err := mirrorStorageEntry(sourceRoot, s.storagePath, kvDBFileName, s, true); err != nil {
		return fmt.Errorf("restore database: %w", err)
	}
	return nil
}

func (s *snapshotService) restoreStartupRecovery(recovery *startupStorageRecovery) error {
	if recovery == nil {
		return errors.New("startup recovery state is unavailable")
	}
	if err := s.restoreStorageFiles(recovery.filesPath); err != nil {
		return fmt.Errorf("restore previous files: %w", err)
	}
	databasePath := filepath.Join(s.storagePath, kvDBFileName)
	if !recovery.databasePresent {
		if err := os.RemoveAll(databasePath); err != nil {
			return fmt.Errorf("remove restored database: %w", err)
		}
		return nil
	}
	if err := mirrorStorageEntry(recovery.filesPath, s.storagePath, kvDBFileName, s, true); err != nil {
		return fmt.Errorf("restore previous database: %w", err)
	}
	return nil
}

func (s *snapshotService) mirrorStorageDirectory(sourceRoot, destinationRoot, relative string, includeDatabase bool) error {
	sourcePath := filepath.Join(sourceRoot, relative)
	destinationPath := filepath.Join(destinationRoot, relative)
	sourceEntries, err := os.ReadDir(sourcePath)
	if errors.Is(err, os.ErrNotExist) {
		sourceEntries = nil
	} else if err != nil {
		return err
	}
	destinationEntries, err := os.ReadDir(destinationPath)
	if err != nil {
		return err
	}
	sourceByName := make(map[string]fs.DirEntry, len(sourceEntries))
	for _, entry := range sourceEntries {
		sourceByName[entry.Name()] = entry
	}
	for _, entry := range destinationEntries {
		childRelative := filepath.Join(relative, entry.Name())
		if s.storagePathExcluded(childRelative) || (!includeDatabase && childRelative == kvDBFileName) {
			continue
		}
		if _, found := sourceByName[entry.Name()]; !found {
			if s.storagePathContainsDynamicExclusion(childRelative) {
				if entry.IsDir() {
					if err := s.mirrorStorageDirectory(sourceRoot, destinationRoot, childRelative, includeDatabase); err != nil {
						return err
					}
				}
				continue
			}
			if err := os.RemoveAll(filepath.Join(destinationRoot, childRelative)); err != nil {
				return fmt.Errorf("remove storage path %s: %w", childRelative, err)
			}
		}
	}
	for _, entry := range sourceEntries {
		childRelative := filepath.Join(relative, entry.Name())
		if s.storagePathExcluded(childRelative) || (!includeDatabase && childRelative == kvDBFileName) {
			continue
		}
		if err := mirrorStorageEntry(sourceRoot, destinationRoot, childRelative, s, includeDatabase); err != nil {
			return err
		}
	}
	return nil
}

func mirrorStorageEntry(
	sourceRoot, destinationRoot, relative string,
	service *snapshotService,
	includeDatabase bool,
) error {
	sourcePath := filepath.Join(sourceRoot, relative)
	destinationPath := filepath.Join(destinationRoot, relative)
	info, err := os.Lstat(sourcePath)
	if err != nil {
		return err
	}
	destinationInfo, destinationErr := os.Lstat(destinationPath)
	if destinationErr != nil && !errors.Is(destinationErr, os.ErrNotExist) {
		return destinationErr
	}
	sameKind := destinationErr == nil &&
		info.IsDir() == destinationInfo.IsDir() &&
		(info.Mode()&os.ModeSymlink != 0) == (destinationInfo.Mode()&os.ModeSymlink != 0) &&
		info.Mode().IsRegular() == destinationInfo.Mode().IsRegular()
	if service.storagePathContainsDynamicExclusion(relative) &&
		(!info.IsDir() || (destinationErr == nil && !destinationInfo.IsDir())) {
		return fmt.Errorf("restore storage path %s would replace a directory that contains an active config source", relative)
	}
	if !sameKind && destinationErr == nil {
		if err := os.RemoveAll(destinationPath); err != nil {
			return err
		}
	}
	if info.IsDir() {
		if err := os.MkdirAll(destinationPath, info.Mode().Perm()); err != nil {
			return err
		}
		if err := service.mirrorStorageDirectory(sourceRoot, destinationRoot, relative, includeDatabase); err != nil {
			return err
		}
		return os.Chmod(destinationPath, info.Mode().Perm())
	}
	if info.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(sourcePath)
		if err != nil {
			return err
		}
		if destinationErr == nil {
			if err := os.Remove(destinationPath); err != nil {
				return err
			}
		}
		return os.Symlink(target, destinationPath)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("restore storage path %s has unsupported mode %s", relative, info.Mode())
	}
	if err := os.MkdirAll(filepath.Dir(destinationPath), 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(destinationPath), ".restore-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer removeFileIfPresent(temporaryPath)
	source, err := os.Open(sourcePath)
	if err != nil {
		_ = temporary.Close()
		return err
	}
	_, copyErr := io.Copy(temporary, source)
	closeSourceErr := source.Close()
	if chmodErr := temporary.Chmod(info.Mode().Perm()); copyErr == nil {
		copyErr = chmodErr
	}
	closeTemporaryErr := temporary.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeSourceErr != nil {
		return closeSourceErr
	}
	if closeTemporaryErr != nil {
		return closeTemporaryErr
	}
	if destinationErr == nil && destinationInfo.IsDir() {
		if err := os.RemoveAll(destinationPath); err != nil {
			return err
		}
	}
	return durablefile.Replace(destinationPath, temporaryPath)
}

func (s *snapshotService) storageIndex() storageSnapshotIndex {
	var index storageSnapshotIndex
	if dns, err := s.manager.dnsCachesSnapshot(); err == nil {
		for _, cache := range dns.Caches {
			index.dnsCaches = append(index.dnsCaches, cache.Name)
		}
	}
	if secrets, err := s.manager.secretsSnapshot(); err == nil {
		for _, secret := range secrets.Secrets {
			index.secrets = append(index.secrets, secret.Key)
		}
	}
	if tlsSnapshot, err := s.manager.tlsSnapshot(); err == nil {
		for _, cert := range tlsSnapshot.Certs {
			index.tlsCerts = append(index.tlsCerts, cert.Name)
		}
		for _, policy := range tlsSnapshot.Policies {
			index.tlsPolicies = append(index.tlsPolicies, policy.Name)
		}
	}
	return index
}

func (s *snapshotService) notifyStorageReset(before, after storageSnapshotIndex) {
	s.manager.notifyChange()
	for _, name := range before.dnsCaches {
		s.manager.notifyDNSChange(dnsCacheChange{kind: dnsCacheChangeCacheDelete, cache: name})
	}
	for _, name := range after.dnsCaches {
		s.manager.notifyDNSChange(dnsCacheChange{kind: dnsCacheChangeCacheUpsert, cache: name})
	}
	for _, name := range unionStrings(before.secrets, after.secrets) {
		kind := secretChangeDelete
		if slices.Contains(after.secrets, name) {
			kind = secretChangeUpsert
		}
		s.manager.notifySecretChange(secretChange{kind: kind, key: name})
	}
	for _, name := range unionStrings(before.tlsCerts, after.tlsCerts) {
		kind := tlsChangeCertDelete
		if slices.Contains(after.tlsCerts, name) {
			kind = tlsChangeCertUpsert
		}
		s.manager.notifyTLSChange(tlsChange{kind: kind, name: name})
	}
	for _, name := range unionStrings(before.tlsPolicies, after.tlsPolicies) {
		kind := tlsChangePolicyDelete
		if slices.Contains(after.tlsPolicies, name) {
			kind = tlsChangePolicyUpsert
		}
		s.manager.notifyTLSChange(tlsChange{kind: kind, name: name})
	}
	s.manager.notifyTLSChange(tlsChange{kind: tlsChangeBuiltinCA, name: tlsBuiltinCACertName})
}

func unionStrings(left, right []string) []string {
	values := make(map[string]struct{}, len(left)+len(right))
	for _, value := range left {
		values[value] = struct{}{}
	}
	for _, value := range right {
		values[value] = struct{}{}
	}
	result := make([]string, 0, len(values))
	for value := range values {
		result = append(result, value)
	}
	slices.Sort(result)
	return result
}

func newSnapshotUUID(random io.Reader) (string, error) {
	var raw [16]byte
	if _, err := io.ReadFull(random, raw[:]); err != nil {
		return "", fmt.Errorf("generate snapshot UUID: %w", err)
	}
	raw[6] = (raw[6] & 0x0f) | 0x40
	raw[8] = (raw[8] & 0x3f) | 0x80
	encoded := hex.EncodeToString(raw[:])
	return encoded[0:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:32], nil
}

func normalizeSnapshotUUID(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if len(value) != 36 || value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' {
		return "", fmt.Errorf("%w: malformed UUID", errSnapshotInvalid)
	}
	raw := strings.ReplaceAll(value, "-", "")
	if _, err := hex.DecodeString(raw); err != nil {
		return "", fmt.Errorf("%w: malformed UUID", errSnapshotInvalid)
	}
	return value, nil
}

func validateSnapshotName(name string) error {
	if !utf8.ValidString(name) {
		return fmt.Errorf("%w: snapshot name is not valid UTF-8", errSnapshotInvalid)
	}
	if len(name) > snapshotNameMaxBytes {
		return fmt.Errorf("%w: snapshot name exceeds %d bytes", errSnapshotTooLarge, snapshotNameMaxBytes)
	}
	return nil
}

func validateSnapshotArchiveSize(path string) error {
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return errSnapshotNotFound
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%w: archive is not a regular file", errSnapshotInvalid)
	}
	if info.Size() > api.SnapshotArchiveMaxBytes {
		return fmt.Errorf("%w: compressed archive exceeds %d bytes", errSnapshotTooLarge, api.SnapshotArchiveMaxBytes)
	}
	return nil
}

// copySnapshotArchive bounds a stream before JSON-decoded imports or startup
// stdin can fill the filesystem with an arbitrarily large temporary archive.
func copySnapshotArchive(destination io.Writer, source io.Reader) (int64, error) {
	limited := &io.LimitedReader{R: source, N: api.SnapshotArchiveMaxBytes + 1}
	written, err := io.Copy(destination, limited)
	if err != nil {
		return written, err
	}
	if written > api.SnapshotArchiveMaxBytes {
		return written, fmt.Errorf("%w: compressed archive exceeds %d bytes", errSnapshotTooLarge, api.SnapshotArchiveMaxBytes)
	}
	return written, nil
}
