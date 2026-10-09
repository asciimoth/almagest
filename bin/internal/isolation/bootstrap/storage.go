package bootstrap

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/asciimoth/almagest/bin/internal/durablefile"
)

var (
	ErrUnsafeLayout        = errors.New("unsafe isolation storage layout")
	ErrMigrationRequired   = errors.New("storage migration is required")
	ErrMigrationIncomplete = errors.New("storage migration is incomplete")
)

const migrationVersion = 1

// StorageLayout separates runtime-writable application data from the broker's
// trusted recovery journal. Backup and State are operator-controlled siblings;
// none can be below Runtime or Legacy.
type StorageLayout struct {
	Legacy  string
	Runtime string
	Broker  string
	Backup  string
	State   string
}

// Validate checks path relationships and trusted parents. It never creates,
// changes ownership, or follows a link.
func (l StorageLayout) Validate() error {
	paths := []string{l.Legacy, l.Runtime, l.Broker, l.Backup, l.State}
	for _, path := range paths {
		if !fixedAbsolutePath(path) {
			return fmt.Errorf("%w: paths must be absolute, clean, and contain no parent traversal: %q", ErrUnsafeLayout, path)
		}
	}
	for i, left := range paths {
		for j, right := range paths {
			if i == j {
				continue
			}
			if sameOrBelow(left, right) {
				return fmt.Errorf("%w: path %q overlaps %q", ErrUnsafeLayout, left, right)
			}
		}
	}
	parents := map[string]struct{}{}
	for _, path := range paths {
		parents[filepath.Dir(path)] = struct{}{}
	}
	for parent := range parents {
		if err := validateStorageParent(parent); err != nil {
			return fmt.Errorf("%w: parent %s: %v", ErrUnsafeLayout, parent, err)
		}
	}
	return nil
}

func sameOrBelow(parent, child string) bool {
	relative, err := filepath.Rel(parent, child)
	return err == nil && (relative == "." || relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)))
}

// RejectRuntimePaths rejects trusted inputs that R could replace. Call this
// for protected config, role executables, libraries, and broker journals after
// resolving their exact absolute paths.
func (l StorageLayout) RejectRuntimePaths(paths ...string) error {
	if err := l.Validate(); err != nil {
		return err
	}
	for _, path := range paths {
		if !fixedAbsolutePath(path) {
			return fmt.Errorf("%w: trusted input path is not absolute and clean: %q", ErrUnsafeLayout, path)
		}
		if sameOrBelow(l.Runtime, path) || sameOrBelow(l.Legacy, path) {
			return fmt.Errorf("%w: trusted input %s is inside runtime-writable storage", ErrUnsafeLayout, path)
		}
	}
	return nil
}

type migrationPhase string

const (
	phaseStarted          migrationPhase = "started"
	phaseBackupCreated    migrationPhase = "backup_created"
	phaseRuntimeInstalled migrationPhase = "runtime_installed"
	phaseBrokerCreated    migrationPhase = "broker_created"
	phaseComplete         migrationPhase = "complete"
)

type migrationRecord struct {
	Version int            `json:"version"`
	Layout  StorageLayout  `json:"layout"`
	Phase   migrationPhase `json:"phase"`
}

// Migrator performs a stopped-daemon migration. The zero value is ready.
// phaseHook is used only for deterministic crash simulation in package tests.
type Migrator struct {
	phaseHook func(migrationPhase) error
}

// Migrate preserves Legacy as Backup, installs a separate Runtime copy, and
// creates an empty broker-private directory. Existing destinations are never
// adopted. An error rolls back completed steps when possible.
func (m Migrator) Migrate(layout StorageLayout) (err error) {
	if err := layout.Validate(); err != nil {
		return err
	}
	if _, err := os.Lstat(layout.State); err == nil {
		return fmt.Errorf("%w: run Recover or Rollback", ErrMigrationIncomplete)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := validateLegacyTree(layout.Legacy); err != nil {
		return err
	}
	for _, path := range []string{layout.Runtime, layout.Broker, layout.Backup} {
		if _, statErr := os.Lstat(path); statErr == nil {
			return fmt.Errorf("%w: destination already exists: %s", ErrUnsafeLayout, path)
		} else if !errors.Is(statErr, os.ErrNotExist) {
			return statErr
		}
	}
	record := migrationRecord{Version: migrationVersion, Layout: layout, Phase: phaseStarted}
	if err := writeMigrationRecord(record); err != nil {
		return err
	}
	rollback := true
	defer func() {
		if err != nil && rollback {
			if rollbackErr := rollbackRecord(record); rollbackErr != nil {
				err = errors.Join(err, fmt.Errorf("automatic migration rollback: %w", rollbackErr))
			}
		}
	}()
	if err = m.afterPhase(phaseStarted); err != nil {
		return err
	}
	if err = os.Rename(layout.Legacy, layout.Backup); err != nil {
		return fmt.Errorf("create legacy backup: %w", err)
	}
	record.Phase = phaseBackupCreated
	if err = writeMigrationRecord(record); err != nil {
		return err
	}
	if err = m.afterPhase(record.Phase); err != nil {
		return err
	}
	staging := layout.Runtime + ".migrating"
	if _, statErr := os.Lstat(staging); statErr == nil {
		return fmt.Errorf("%w: staging path already exists: %s", ErrUnsafeLayout, staging)
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return statErr
	}
	if err = copyStorageTree(layout.Backup, staging); err != nil {
		return fmt.Errorf("copy runtime storage: %w", err)
	}
	if err = os.Rename(staging, layout.Runtime); err != nil {
		_ = os.RemoveAll(staging)
		return fmt.Errorf("install runtime storage: %w", err)
	}
	record.Phase = phaseRuntimeInstalled
	if err = writeMigrationRecord(record); err != nil {
		return err
	}
	if err = m.afterPhase(record.Phase); err != nil {
		return err
	}
	if err = os.Mkdir(layout.Broker, 0o700); err != nil {
		return fmt.Errorf("create broker journal directory: %w", err)
	}
	record.Phase = phaseBrokerCreated
	if err = writeMigrationRecord(record); err != nil {
		return err
	}
	if err = m.afterPhase(record.Phase); err != nil {
		return err
	}
	record.Phase = phaseComplete
	if err = writeMigrationRecord(record); err != nil {
		return err
	}
	if err = m.afterPhase(record.Phase); err != nil {
		rollback = false
		return err
	}
	rollback = false
	if err = os.Remove(layout.State); err != nil {
		return fmt.Errorf("remove completed migration record: %w", err)
	}
	return nil
}

func (m Migrator) afterPhase(phase migrationPhase) error {
	if m.phaseHook != nil {
		return m.phaseHook(phase)
	}
	return nil
}

// Recover handles a durable interrupted phase. Completed migrations are kept;
// incomplete migrations are rolled back to the legacy layout.
func (Migrator) Recover(layout StorageLayout) error {
	record, err := readMigrationRecord(layout.State)
	if err != nil {
		return err
	}
	if record.Layout != layout {
		return fmt.Errorf("%w: migration record names a different layout", ErrUnsafeLayout)
	}
	if record.Phase == phaseComplete {
		return os.Remove(layout.State)
	}
	return rollbackRecord(record)
}

// Rollback restores Backup as Legacy and removes only migration-created
// Runtime and Broker trees. It does not follow entries inside those trees.
func (Migrator) Rollback(layout StorageLayout) error {
	if err := layout.Validate(); err != nil {
		return err
	}
	record := migrationRecord{Version: migrationVersion, Layout: layout, Phase: phaseComplete}
	if stored, err := readMigrationRecord(layout.State); err == nil {
		record = stored
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return rollbackRecord(record)
}

func rollbackRecord(record migrationRecord) error {
	if record.Version != migrationVersion {
		return fmt.Errorf("%w: migration record version %d", ErrUnsafeLayout, record.Version)
	}
	layout := record.Layout
	if err := layout.Validate(); err != nil {
		return err
	}
	var joined error
	for _, path := range []string{layout.Runtime + ".migrating", layout.Runtime, layout.Broker} {
		if err := removeMigrationTree(path); err != nil {
			joined = errors.Join(joined, err)
		}
	}
	if _, err := os.Lstat(layout.Backup); err == nil {
		if _, legacyErr := os.Lstat(layout.Legacy); legacyErr == nil {
			joined = errors.Join(joined, fmt.Errorf("%w: both legacy and backup exist", ErrUnsafeLayout))
		} else if !errors.Is(legacyErr, os.ErrNotExist) {
			joined = errors.Join(joined, legacyErr)
		} else if renameErr := os.Rename(layout.Backup, layout.Legacy); renameErr != nil {
			joined = errors.Join(joined, fmt.Errorf("restore legacy backup: %w", renameErr))
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		joined = errors.Join(joined, err)
	}
	if joined == nil {
		if err := os.Remove(layout.State); err != nil && !errors.Is(err, os.ErrNotExist) {
			joined = err
		}
	}
	return joined
}

func removeMigrationTree(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%w: migration output is not a directory: %s", ErrUnsafeLayout, path)
	}
	return os.RemoveAll(path)
}

func validateLegacyTree(root string) error {
	info, err := os.Lstat(root)
	if err != nil {
		return fmt.Errorf("%w: legacy storage: %v", ErrUnsafeLayout, err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%w: legacy storage is not a directory", ErrUnsafeLayout)
	}
	return filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if entry.IsDir() || info.Mode().IsRegular() {
			return nil
		}
		return fmt.Errorf("%w: legacy storage contains unsupported entry %s", ErrUnsafeLayout, path)
	})
}

func copyStorageTree(source, destination string) error {
	if err := os.Mkdir(destination, 0o700); err != nil {
		return err
	}
	failed := true
	defer func() {
		if failed {
			_ = os.RemoveAll(destination)
		}
	}()
	err := filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == source {
			return nil
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		target := filepath.Join(destination, relative)
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return os.Mkdir(target, info.Mode().Perm()&0o700)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("unsupported storage entry %s", path)
		}
		return copyStorageFile(path, target, info.Mode().Perm()&0o600)
	})
	if err != nil {
		return err
	}
	failed = false
	return nil
}

func copyStorageFile(source, destination string, mode fs.FileMode) error {
	input, err := openMigrationSource(source)
	if err != nil {
		return err
	}
	defer input.Close() //nolint:errcheck
	output, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	remove := true
	defer func() {
		_ = output.Close()
		if remove {
			_ = os.Remove(destination)
		}
	}()
	if _, err := io.Copy(output, input); err != nil {
		return err
	}
	if err := output.Sync(); err != nil {
		return err
	}
	if err := output.Close(); err != nil {
		return err
	}
	remove = false
	return nil
}

func writeMigrationRecord(record migrationRecord) error {
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	temporary := record.Layout.State + ".tmp"
	file, err := os.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		return fmt.Errorf("%w: temporary migration record exists", ErrMigrationIncomplete)
	}
	if err != nil {
		return err
	}
	remove := true
	defer func() {
		_ = file.Close()
		if remove {
			_ = os.Remove(temporary)
		}
	}()
	if _, err := file.Write(data); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := durablefile.Replace(record.Layout.State, temporary); err != nil {
		return err
	}
	remove = false
	return nil
}

func readMigrationRecord(path string) (migrationRecord, error) {
	file, err := openTrustedFile(path)
	if err != nil {
		return migrationRecord{}, err
	}
	defer file.Close() //nolint:errcheck
	data, err := readBounded(file, 16*1024)
	if err != nil {
		return migrationRecord{}, err
	}
	var record migrationRecord
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&record); err != nil {
		return migrationRecord{}, fmt.Errorf("%w: invalid migration record: %v", ErrUnsafeLayout, err)
	}
	if record.Version != migrationVersion {
		return migrationRecord{}, fmt.Errorf("%w: migration record version %d", ErrUnsafeLayout, record.Version)
	}
	return record, nil
}
