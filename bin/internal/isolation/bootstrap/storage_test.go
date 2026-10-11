package bootstrap

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
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
	if record, err := readMigrationRecord(layout.State); err != nil || record.Phase != phaseComplete {
		t.Errorf("completed rollback receipt missing: %+v, %v", record, err)
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
		identity, err := migrationDirectoryIdentity(layout.Backup)
		if err != nil {
			t.Fatal(err)
		}
		record := migrationRecord{Version: migrationVersion, Layout: layout, Phase: phaseBackupCreated, LegacyIdentity: identity}
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
		{"staging-is-backup", func(l *StorageLayout) { l.Backup = l.Runtime + ".migrating" }},
		{"staging-contains-legacy", func(l *StorageLayout) { l.Legacy = filepath.Join(l.Runtime+".migrating", "legacy") }},
		{"temporary-state-is-broker", func(l *StorageLayout) { l.Broker = l.State + ".tmp" }},
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
	for _, occupied := range []string{"runtime", "broker", "backup", "state", "staging", "temporary-state"} {
		t.Run(occupied, func(t *testing.T) {
			layout := newStorageLayout(t)
			seedLegacyStorage(t, layout)
			path := map[string]string{"runtime": layout.Runtime, "broker": layout.Broker, "backup": layout.Backup, "state": layout.State, "staging": layout.Runtime + ".migrating", "temporary-state": layout.State + ".tmp"}[occupied]
			if occupied == "state" || occupied == "temporary-state" {
				if err := os.WriteFile(path, []byte("foreign"), 0o600); err != nil {
					t.Fatal(err)
				}
			} else if err := os.Mkdir(path, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := (Migrator{}).Migrate(layout); err == nil {
				t.Fatal("Migrate adopted occupied destination")
			}
			if _, err := os.Lstat(path); err != nil {
				t.Fatalf("occupied destination was removed: %v", err)
			}
			if data, err := os.ReadFile(filepath.Join(layout.Legacy, "alm.db")); err != nil || string(data) != "database" {
				t.Fatalf("legacy changed on rejected migration: %q, %v", data, err)
			}
		})
	}
}

func TestStorageMigrationRejectsDifferentRecordedLayout(t *testing.T) {
	for _, operation := range []string{"recover", "rollback"} {
		t.Run(operation, func(t *testing.T) {
			requested := newStorageLayout(t)
			foreign := newStorageLayout(t)
			seedLegacyStorage(t, foreign)
			if err := os.Rename(foreign.Legacy, foreign.Backup); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(foreign.Runtime, 0o700); err != nil {
				t.Fatal(err)
			}
			record := migrationRecord{Version: migrationVersion, Layout: foreign, Phase: phaseRuntimeInstalled, LegacyIdentity: "foreign-backup", RuntimeIdentity: "foreign-runtime"}
			data, err := json.Marshal(record)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(requested.State, data, 0o600); err != nil {
				t.Fatal(err)
			}
			if operation == "recover" {
				err = (Migrator{}).Recover(requested)
			} else {
				err = (Migrator{}).Rollback(requested)
			}
			if !errors.Is(err, ErrUnsafeLayout) {
				t.Fatalf("different layout accepted: %v", err)
			}
			for _, path := range []string{foreign.Runtime, foreign.Backup, requested.State} {
				if _, err := os.Lstat(path); err != nil {
					t.Fatalf("rejected rollback changed %s: %v", path, err)
				}
			}
		})
	}
}

func TestStorageRollbackChecksBackupBeforeRemovingOutputs(t *testing.T) {
	for _, state := range []string{"missing", "file", "legacy-exists"} {
		t.Run(state, func(t *testing.T) {
			layout := newStorageLayout(t)
			for _, path := range []string{layout.Runtime, layout.Broker, layout.Runtime + ".migrating"} {
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(path, "keep"), []byte("keep"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			switch state {
			case "file":
				if err := os.WriteFile(layout.Backup, []byte("foreign"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "legacy-exists":
				seedLegacyStorage(t, layout)
				if err := os.Mkdir(layout.Backup, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			record := migrationRecord{Version: migrationVersion, Layout: layout, Phase: phaseComplete, LegacyIdentity: "original", RuntimeIdentity: "runtime", BrokerIdentity: "broker"}
			if err := writeMigrationRecord(record); err != nil {
				t.Fatal(err)
			}
			if err := (Migrator{}).Rollback(layout); err == nil {
				t.Fatal("rollback accepted unsafe backup state")
			}
			for _, path := range []string{layout.Runtime, layout.Broker, layout.Runtime + ".migrating"} {
				if data, err := os.ReadFile(filepath.Join(path, "keep")); err != nil || string(data) != "keep" {
					t.Fatalf("rollback removed output before checking backup: %q, %v", data, err)
				}
			}
		})
	}
}

func TestMigrationRecordRejectsAmbiguousAndUnknownInput(t *testing.T) {
	for _, data := range []string{
		`{"version":2,"layout":{},"phase":"unknown","legacy_identity":"id"}`,
		`{"version":2,"version":2,"layout":{},"phase":"started","legacy_identity":"id"}`,
		`{"version":2,"layout":{"Legacy":"a","Legacy":"b"},"phase":"started","legacy_identity":"id"}`,
		`{"version":2,"layout":{},"phase":"started","legacy_identity":"id"} {}`,
		`{"version":2,"layout":{},"phase":"started","extra":true,"legacy_identity":"id"}`,
		`{"version":2,"layout":{},"phase":"started","legacy_identity":"id"} garbage`,
		`{"version":2,"layout":{},"phase":"started"}`,
		`{"version":1,"layout":{},"phase":"started","legacy_identity":"id"}`,
		`{"version":2,"Version":2,"layout":{},"phase":"started","legacy_identity":"id"}`,
		`{"version":2,"layout":{"Legacy":"a","legacy":"b"},"phase":"started","legacy_identity":"id"}`,
		`null`,
	} {
		if _, err := decodeMigrationRecord([]byte(data)); err == nil {
			t.Errorf("accepted %s", data)
		}
	}
}

func TestStorageRollbackRequiresReceiptAndOriginalDirectories(t *testing.T) {
	for _, replaced := range []string{"receipt", "runtime", "broker", "backup"} {
		t.Run(replaced, func(t *testing.T) {
			layout := newStorageLayout(t)
			seedLegacyStorage(t, layout)
			if err := (Migrator{}).Migrate(layout); err != nil {
				t.Fatal(err)
			}
			path := map[string]string{"receipt": layout.State, "runtime": layout.Runtime, "broker": layout.Broker, "backup": layout.Backup}[replaced]
			if err := os.Rename(path, path+".saved"); err != nil {
				t.Fatal(err)
			}
			if replaced != "receipt" {
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(path, "foreign"), []byte("foreign"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			for _, operation := range []func(StorageLayout) error{(Migrator{}).Rollback, (Migrator{}).Recover} {
				if err := operation(layout); err == nil {
					t.Fatal("accepted unproved migration objects")
				}
			}
			for _, retained := range []string{layout.Runtime, layout.Broker, layout.Backup, path + ".saved"} {
				if _, err := os.Lstat(retained); err != nil {
					t.Fatalf("rejected recovery removed %s: %v", retained, err)
				}
			}
		})
	}
}

func TestStorageRecoveryRejectsUnrecordedOutput(t *testing.T) {
	layout := newStorageLayout(t)
	seedLegacyStorage(t, layout)
	identity, err := migrationDirectoryIdentity(layout.Legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(layout.Legacy, layout.Backup); err != nil {
		t.Fatal(err)
	}
	staging := layout.Runtime + ".migrating"
	if err := os.Mkdir(staging, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := writeMigrationRecord(migrationRecord{Version: migrationVersion, Layout: layout, Phase: phaseBackupCreated, LegacyIdentity: identity}); err != nil {
		t.Fatal(err)
	}
	if err := (Migrator{}).Recover(layout); !errors.Is(err, ErrMigrationIncomplete) {
		t.Fatalf("unrecorded output recovery error = %v", err)
	}
	for _, path := range []string{staging, layout.Backup, layout.State} {
		if _, err := os.Lstat(path); err != nil {
			t.Fatalf("uncertain recovery changed %s: %v", path, err)
		}
	}
}

func TestStorageRollbackPreservesBrokerRecoveryState(t *testing.T) {
	layout := newStorageLayout(t)
	seedLegacyStorage(t, layout)
	if err := (Migrator{}).Migrate(layout); err != nil {
		t.Fatal(err)
	}
	journal := filepath.Join(layout.Broker, "recovery.json")
	if err := os.WriteFile(journal, []byte("unresolved host ownership"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := (Migrator{}).Rollback(layout); !errors.Is(err, ErrMigrationIncomplete) {
		t.Fatalf("rollback with broker recovery state = %v", err)
	}
	if data, err := os.ReadFile(journal); err != nil || string(data) != "unresolved host ownership" {
		t.Fatalf("broker recovery state was lost: %q, %v", data, err)
	}
	for _, path := range []string{layout.Runtime, layout.Backup, layout.State} {
		if _, err := os.Lstat(path); err != nil {
			t.Fatalf("blocked rollback removed %s: %v", path, err)
		}
	}
}

func TestStorageRecoveryFindsRecordedStagingAfterInterruptedRename(t *testing.T) {
	layout := newStorageLayout(t)
	seedLegacyStorage(t, layout)
	if err := (Migrator{}).Migrate(layout); err != nil {
		t.Fatal(err)
	}
	record, err := readMigrationRecord(layout.State)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(layout.Runtime, layout.Runtime+".migrating"); err != nil {
		t.Fatal(err)
	}
	record.Phase = phaseBackupCreated
	if err := writeMigrationRecord(record); err != nil {
		t.Fatal(err)
	}
	if err := (Migrator{}).Recover(layout); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(filepath.Join(layout.Legacy, "alm.db")); err != nil || string(data) != "database" {
		t.Fatalf("recovered legacy data = %q, %v", data, err)
	}
}

func TestStorageMigrationProcessCrash(t *testing.T) {
	const crashExit = 73
	if phase := os.Getenv("ALMAGEST_MIGRATION_CRASH_PHASE"); phase != "" {
		var layout StorageLayout
		if err := json.Unmarshal([]byte(os.Getenv("ALMAGEST_MIGRATION_CRASH_LAYOUT")), &layout); err != nil {
			t.Fatal(err)
		}
		migrator := Migrator{phaseHook: func(current migrationPhase) error {
			if string(current) == phase {
				os.Exit(crashExit)
			}
			return nil
		}}
		if err := migrator.Migrate(layout); err != nil {
			t.Fatal(err)
		}
		t.Fatal("migration did not reach the crash point")
	}
	for _, phase := range []migrationPhase{phaseStarted, phaseBackupCreated, phaseRuntimeInstalled, phaseBrokerCreated, phaseComplete} {
		t.Run(string(phase), func(t *testing.T) {
			layout := newStorageLayout(t)
			seedLegacyStorage(t, layout)
			encoded, err := json.Marshal(layout)
			if err != nil {
				t.Fatal(err)
			}
			command := exec.Command(os.Args[0], "-test.run=^TestStorageMigrationProcessCrash$", "-test.timeout=30s") //nolint:gosec // Re-executes this test binary with fixed arguments.
			command.Env = append(os.Environ(), "ALMAGEST_MIGRATION_CRASH_PHASE="+string(phase), "ALMAGEST_MIGRATION_CRASH_LAYOUT="+string(encoded))
			output, err := command.CombinedOutput()
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != crashExit {
				t.Fatalf("crash child: %v\n%s", err, output)
			}
			if err := (Migrator{}).Recover(layout); err != nil {
				t.Fatalf("recover after process death: %v", err)
			}
			dataPath := filepath.Join(layout.Legacy, "alm.db")
			if phase == phaseComplete {
				dataPath = filepath.Join(layout.Runtime, "alm.db")
				if err := (Migrator{}).Recover(layout); err != nil {
					t.Fatalf("repeat completed recovery: %v", err)
				}
			} else {
				for _, path := range []string{layout.Runtime, layout.Runtime + ".migrating", layout.Broker, layout.State} {
					if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
						t.Fatalf("incomplete migration output remains: %s: %v", path, err)
					}
				}
			}
			if data, err := os.ReadFile(dataPath); err != nil || string(data) != "database" {
				t.Fatalf("recovered data: %q, %v", data, err)
			}
		})
	}
}

func FuzzMigrationRecord(f *testing.F) {
	f.Add([]byte(`{"version":2,"layout":{},"phase":"started","legacy_identity":"id"}`))
	f.Add([]byte(`{"version":1,"version":2,"phase":"complete"}`))
	f.Add([]byte(`null`))
	f.Fuzz(func(t *testing.T, data []byte) {
		record, err := decodeMigrationRecord(data)
		if err != nil {
			return
		}
		encoded, err := json.Marshal(record)
		if err != nil {
			t.Fatal(err)
		}
		roundTrip, err := decodeMigrationRecord(encoded)
		if err != nil || roundTrip != record {
			t.Fatalf("accepted record does not round trip: %+v, %v", roundTrip, err)
		}
	})
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
