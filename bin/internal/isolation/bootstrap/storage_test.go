package bootstrap

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func newStorageLayout(t *testing.T) StorageLayout {
	t.Helper()
	root, err := os.MkdirTemp(".", ".bootstrap-storage-test-")
	if err != nil {
		t.Fatal(err)
	}
	root, err = filepath.Abs(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	hardenTestStorageRoot(t, root)
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	return StorageLayout{
		Legacy: filepath.Join(root, "legacy"), Runtime: filepath.Join(root, "runtime"),
		Broker: filepath.Join(root, "broker"), Backup: filepath.Join(root, "legacy.backup"),
		State: filepath.Join(root, "migration.state"),
	}
}

func seedLegacyStorage(t *testing.T, layout StorageLayout) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(layout.Legacy, "snapshots"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(layout.Legacy, "alm.db"), []byte("database"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(layout.Legacy, "snapshots", "one.zip"), []byte("archive"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestStorageMigrationCreatesSeparatedRuntimeBrokerAndBackup(t *testing.T) {
	layout := newStorageLayout(t)
	seedLegacyStorage(t, layout)
	if err := (Migrator{}).Migrate(layout); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		filepath.Join(layout.Runtime, "alm.db"), filepath.Join(layout.Runtime, "snapshots", "one.zip"),
		filepath.Join(layout.Backup, "alm.db"), layout.Broker,
	} {
		if _, err := os.Lstat(path); err != nil {
			t.Errorf("migrated path %s: %v", path, err)
		}
	}
	if _, err := os.Lstat(layout.Legacy); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("legacy path still exists: %v", err)
	}
	if _, err := os.Lstat(layout.State); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("migration state still exists: %v", err)
	}
	if err := os.WriteFile(filepath.Join(layout.Runtime, "runtime-only"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(layout.Backup, "runtime-only")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("backup changed with runtime: %v", err)
	}
}

func TestStorageMigrationRollbackRestoresLegacy(t *testing.T) {
	layout := newStorageLayout(t)
	seedLegacyStorage(t, layout)
	if err := (Migrator{}).Migrate(layout); err != nil {
		t.Fatal(err)
	}
	if err := (Migrator{}).Rollback(layout); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(layout.Legacy, "alm.db"))
	if err != nil || string(data) != "database" {
		t.Fatalf("restored legacy data = %q, %v", data, err)
	}
	for _, path := range []string{layout.Runtime, layout.Broker, layout.Backup, layout.State} {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("rollback retained %s: %v", path, err)
		}
	}
}

func TestStorageMigrationFailureRollsBackBeforeReturning(t *testing.T) {
	for _, phase := range []migrationPhase{phaseStarted, phaseBackupCreated, phaseRuntimeInstalled, phaseBrokerCreated} {
		t.Run(string(phase), func(t *testing.T) {
			layout := newStorageLayout(t)
			seedLegacyStorage(t, layout)
			failure := errors.New("injected migration failure")
			migrator := Migrator{phaseHook: func(current migrationPhase) error {
				if current == phase {
					return failure
				}
				return nil
			}}
			if err := migrator.Migrate(layout); !errors.Is(err, failure) {
				t.Fatalf("Migrate error = %v", err)
			}
			if data, err := os.ReadFile(filepath.Join(layout.Legacy, "alm.db")); err != nil || string(data) != "database" {
				t.Fatalf("legacy after failure = %q, %v", data, err)
			}
			for _, path := range []string{layout.Runtime, layout.Broker, layout.Backup, layout.State} {
				if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
					t.Errorf("failure retained %s: %v", path, err)
				}
			}
		})
	}
}

func TestStorageMigrationRecoversInterruptedRecords(t *testing.T) {
	t.Run("backup-created", func(t *testing.T) {
		layout := newStorageLayout(t)
		seedLegacyStorage(t, layout)
		if err := os.Rename(layout.Legacy, layout.Backup); err != nil {
			t.Fatal(err)
		}
		record := migrationRecord{Version: migrationVersion, Layout: layout, Phase: phaseBackupCreated}
		if err := writeMigrationRecord(record); err != nil {
			t.Fatal(err)
		}
		if err := (Migrator{}).Recover(layout); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(filepath.Join(layout.Legacy, "alm.db")); err != nil {
			t.Fatalf("recovered legacy: %v", err)
		}
	})
	t.Run("complete", func(t *testing.T) {
		layout := newStorageLayout(t)
		seedLegacyStorage(t, layout)
		migrator := Migrator{phaseHook: func(phase migrationPhase) error {
			if phase == phaseComplete {
				return errors.New("simulated process loss")
			}
			return nil
		}}
		if err := migrator.Migrate(layout); err == nil {
			t.Fatal("Migrate succeeded despite simulated process loss")
		}
		if err := (Migrator{}).Recover(layout); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(filepath.Join(layout.Runtime, "alm.db")); err != nil {
			t.Fatalf("completed runtime was rolled back: %v", err)
		}
	})
}

func TestStorageLayoutRejectsPathAttacksAndUnsupportedTrees(t *testing.T) {
	layout := newStorageLayout(t)
	seedLegacyStorage(t, layout)
	tests := []struct {
		name   string
		mutate func(*StorageLayout)
	}{
		{"relative", func(l *StorageLayout) { l.Broker = "broker" }},
		{"parent-traversal", func(l *StorageLayout) {
			l.Broker = filepath.Dir(l.Broker) + string(filepath.Separator) + "x" + string(filepath.Separator) + ".." + string(filepath.Separator) + "broker"
		}},
		{"runtime-contains-broker", func(l *StorageLayout) { l.Broker = filepath.Join(l.Runtime, "broker") }},
		{"journal-inside-runtime", func(l *StorageLayout) { l.State = filepath.Join(l.Runtime, "state") }},
		{"same-backup", func(l *StorageLayout) { l.Backup = l.Legacy }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := layout
			test.mutate(&candidate)
			if err := candidate.Validate(); !errors.Is(err, ErrUnsafeLayout) {
				t.Fatalf("Validate error = %v", err)
			}
		})
	}
	if runtime.GOOS != "windows" {
		link := filepath.Join(layout.Legacy, "escape")
		if err := os.Symlink(filepath.Dir(layout.Legacy), link); err != nil {
			t.Fatal(err)
		}
		if err := (Migrator{}).Migrate(layout); !errors.Is(err, ErrUnsafeLayout) {
			t.Fatalf("symlink migration error = %v", err)
		}
	}
}

func TestStorageMigrationRejectsHardLinkedFilesAndRollsBack(t *testing.T) {
	layout := newStorageLayout(t)
	seedLegacyStorage(t, layout)
	if err := os.Link(filepath.Join(layout.Legacy, "alm.db"), filepath.Join(layout.Legacy, "alias.db")); err != nil {
		t.Skipf("hard links are unavailable: %v", err)
	}
	if err := (Migrator{}).Migrate(layout); err == nil {
		t.Fatal("Migrate accepted hard-linked legacy data")
	}
	if _, err := os.Stat(filepath.Join(layout.Legacy, "alm.db")); err != nil {
		t.Fatalf("migration failure did not restore legacy data: %v", err)
	}
}

func TestStorageLayoutRejectsTrustedInputsInsideRuntimeAuthority(t *testing.T) {
	layout := newStorageLayout(t)
	for _, path := range []string{
		filepath.Join(layout.Runtime, "protected.hujson"),
		filepath.Join(layout.Legacy, "almagest-network-role"),
	} {
		if err := layout.RejectRuntimePaths(path); !errors.Is(err, ErrUnsafeLayout) {
			t.Errorf("RejectRuntimePaths(%q) error = %v", path, err)
		}
	}
	trusted := filepath.Join(filepath.Dir(layout.Runtime), "protected.hujson")
	if err := layout.RejectRuntimePaths(trusted); err != nil {
		t.Fatalf("trusted sibling rejected: %v", err)
	}
}

func TestStorageMigrationDoesNotAdoptExistingDestinationsOrRecords(t *testing.T) {
	for _, occupied := range []string{"runtime", "broker", "backup", "state"} {
		t.Run(occupied, func(t *testing.T) {
			layout := newStorageLayout(t)
			seedLegacyStorage(t, layout)
			path := map[string]string{"runtime": layout.Runtime, "broker": layout.Broker, "backup": layout.Backup, "state": layout.State}[occupied]
			if occupied == "state" {
				if err := os.WriteFile(path, []byte("foreign"), 0o600); err != nil {
					t.Fatal(err)
				}
			} else if err := os.Mkdir(path, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := (Migrator{}).Migrate(layout); err == nil {
				t.Fatal("Migrate adopted occupied destination")
			}
		})
	}
}

func FuzzStorageLayoutValidation(f *testing.F) {
	f.Add("legacy", "runtime", "broker", "backup", "state")
	f.Add("../legacy", "runtime", "broker", "backup", "state")
	f.Add("same", "same", "broker", "backup", "state")
	f.Fuzz(func(t *testing.T, legacy, runtimePath, broker, backup, state string) {
		root := string(filepath.Separator) + "fixed"
		toPath := func(value string) string {
			if strings.HasPrefix(value, string(filepath.Separator)) {
				return value
			}
			return filepath.Join(root, value)
		}
		layout := StorageLayout{
			Legacy: toPath(legacy), Runtime: toPath(runtimePath), Broker: toPath(broker),
			Backup: toPath(backup), State: toPath(state),
		}
		_ = layout.Validate()
	})
}
