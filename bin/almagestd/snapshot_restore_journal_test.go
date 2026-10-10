package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInterruptedRestoreRollsBackPreparedAndDataAppliedStages(t *testing.T) {
	for _, test := range []struct {
		name string
		hook func(*snapshotRestoreCoordinator)
	}{
		{
			name: "prepared",
			hook: func(coordinator *snapshotRestoreCoordinator) {
				coordinator.hooks.afterPrepared = func() error { return errSimulatedRestoreProcessDeath }
			},
		},
		{
			name: "data applied",
			hook: func(coordinator *snapshotRestoreCoordinator) {
				coordinator.hooks.afterDataApplied = func() error { return errSimulatedRestoreProcessDeath }
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			service, manager, engine, writer, storagePath := newSnapshotTestService(t, false)
			marker := filepath.Join(storagePath, "crash-marker")
			if err := os.WriteFile(marker, []byte("snapshot"), 0o600); err != nil {
				t.Fatal(err)
			}
			snapshot, err := service.Create(context.Background(), "crash recovery")
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(marker, []byte("before"), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := service.autosaver.Update(`{"nodes":{}}`, ""); err != nil {
				t.Fatal(err)
			}
			regular, err := os.ReadFile(writer.configPath)
			if err != nil {
				t.Fatal(err)
			}
			test.hook(service.restore)
			if _, err := service.Restore(context.Background(), snapshot.UUID); !errors.Is(err, errSimulatedRestoreProcessDeath) {
				t.Fatalf("restore error = %v, want simulated process death", err)
			}
			_ = engine.Close()
			_ = manager.Close()

			result, err := recoverInterruptedRestore(context.Background(), storagePath, regular, writer)
			if err != nil {
				t.Fatal(err)
			}
			if !result.RolledBack || result.UnresolvedConfigWrite {
				t.Fatalf("recovery result = %#v", result)
			}
			if data, err := os.ReadFile(marker); err != nil || string(data) != "before" {
				t.Fatalf("marker after crash recovery = %q, err=%v", data, err)
			}
			journalPath, recoveryPath := restoreStatePaths(storagePath)
			for _, path := range []string{journalPath, recoveryPath} {
				if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("restore state %s remains: %v", path, err)
				}
			}
		})
	}
}

func TestInterruptedCommittedRestoreKeepsNewStorageAndReportsConfigMismatch(t *testing.T) {
	service, manager, engine, writer, storagePath := newSnapshotTestService(t, false)
	marker := filepath.Join(storagePath, "committed-marker")
	if err := os.WriteFile(marker, []byte("restored"), 0o600); err != nil {
		t.Fatal(err)
	}
	snapshot, err := service.Create(context.Background(), "committed crash")
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := service.loadArchive(context.Background(), filepath.Join(storagePath, snapshotDirectoryName, snapshot.UUID+".zip"), snapshot.UUID)
	if err != nil {
		t.Fatal(err)
	}
	restoredConfig := append([]byte(nil), loaded.configData...)
	loaded.cleanup()
	if err := os.WriteFile(marker, []byte("before"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := service.autosaver.Update(`{"nodes":{}}`, ""); err != nil {
		t.Fatal(err)
	}
	regular, err := os.ReadFile(writer.configPath)
	if err != nil {
		t.Fatal(err)
	}
	service.restore.hooks.afterCommitted = func() error { return errSimulatedRestoreProcessDeath }
	if _, err := service.Restore(context.Background(), snapshot.UUID); !errors.Is(err, errSimulatedRestoreProcessDeath) {
		t.Fatalf("restore error = %v, want simulated process death", err)
	}
	_ = engine.Close()
	_ = manager.Close()

	result, err := recoverInterruptedRestore(context.Background(), storagePath, regular, writer)
	if err != nil {
		t.Fatal(err)
	}
	if result.RolledBack || !result.UnresolvedConfigWrite {
		t.Fatalf("recovery result = %#v", result)
	}
	if data, err := os.ReadFile(marker); err != nil || string(data) != "restored" {
		t.Fatalf("committed marker = %q, err=%v", data, err)
	}
	_, recoveryPath := restoreStatePaths(storagePath)
	if _, err := os.Stat(recoveryPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("committed recovery copy remains: %v", err)
	}
	if _, err := recoverInterruptedRestore(context.Background(), storagePath, restoredConfig, writer); err != nil {
		t.Fatal(err)
	}
	journalPath, _ := restoreStatePaths(storagePath)
	if _, err := os.Stat(journalPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("resolved commit journal remains: %v", err)
	}
}

func TestRestoreRecoveryCopyPublicationFailureDoesNotChangeRuntime(t *testing.T) {
	service, _, engine, _, storagePath := newSnapshotTestService(t, false)
	snapshot, err := service.Create(context.Background(), "publication failure")
	if err != nil {
		t.Fatal(err)
	}
	_, recoveryPath := restoreStatePaths(storagePath)
	if err := os.Mkdir(recoveryPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Restore(context.Background(), snapshot.UUID); err == nil {
		t.Fatal("restore succeeded when the durable recovery target was unavailable")
	}
	if _, ok := engine.NodeByName("main", "saved-node"); !ok {
		t.Fatal("runtime graph changed before recovery publication committed")
	}
}

func TestRestoreJournalWriteFailureRollsBackAtAppliedAndCommitTransitions(t *testing.T) {
	for _, state := range []restoreJournalState{restoreStateDataApplied, restoreStateCommitted} {
		t.Run(string(state), func(t *testing.T) {
			service, _, engine, _, storagePath := newSnapshotTestService(t, false)
			marker := filepath.Join(storagePath, "journal-failure-marker")
			if err := os.WriteFile(marker, []byte("snapshot"), 0o600); err != nil {
				t.Fatal(err)
			}
			snapshot, err := service.Create(context.Background(), "journal write failure")
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(marker, []byte("before"), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := service.autosaver.Update(`{"nodes":{}}`, ""); err != nil {
				t.Fatal(err)
			}
			journalPath, _ := restoreStatePaths(storagePath)
			sabotage := func() error {
				if err := os.Remove(journalPath); err != nil {
					return err
				}
				return os.Mkdir(journalPath, 0o700)
			}
			if state == restoreStateDataApplied {
				service.restore.hooks.afterPrepared = sabotage
			} else {
				service.restore.hooks.afterDataApplied = sabotage
			}
			if _, err := service.Restore(context.Background(), snapshot.UUID); err == nil {
				t.Fatal("restore succeeded after journal publication failure")
			}
			if data, err := os.ReadFile(marker); err != nil || string(data) != "before" {
				t.Fatalf("marker after journal failure rollback = %q, err=%v", data, err)
			}
			if _, ok := engine.NodeByName("main", "saved-node"); ok {
				t.Fatal("journal failure did not restore the prior graph")
			}
		})
	}
}

func TestRestoreCancellationStillCompletesRollback(t *testing.T) {
	service, _, engine, _, storagePath := newSnapshotTestService(t, false)
	marker := filepath.Join(storagePath, "cancel-marker")
	if err := os.WriteFile(marker, []byte("snapshot"), 0o600); err != nil {
		t.Fatal(err)
	}
	snapshot, err := service.Create(context.Background(), "canceled restore")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(marker, []byte("before"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := service.autosaver.Update(`{"nodes":{}}`, ""); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	service.restore.hooks.afterDataApplied = func() error {
		cancel()
		return ctx.Err()
	}
	if _, err := service.Restore(ctx, snapshot.UUID); !errors.Is(err, context.Canceled) {
		t.Fatalf("restore error = %v, want canceled", err)
	}
	if data, err := os.ReadFile(marker); err != nil || string(data) != "before" {
		t.Fatalf("marker after canceled rollback = %q, err=%v", data, err)
	}
	if _, ok := engine.NodeByName("main", "saved-node"); ok {
		t.Fatal("canceled restore did not rebuild the prior graph")
	}
}

func TestRestoreJournalRejectsHostAuthorityAndMalformedState(t *testing.T) {
	valid := `{"version":1,"state":"prepared","snapshot_uuid":"11111111-1111-4111-8111-111111111111","recovery_sha256":"` +
		strings.Repeat("0", 64) + `","restored_config_sha256":"` + strings.Repeat("1", 64) + `"}`
	if _, err := decodeRestoreJournal([]byte(valid)); err != nil {
		t.Fatalf("valid journal: %v", err)
	}
	for _, data := range []string{
		``,
		`{"version":1}`,
		strings.Replace(valid, `"prepared"`, `"unknown"`, 1),
		strings.TrimSuffix(valid, "}") + `,"resource_id":"forged"}`,
		valid + valid,
	} {
		if _, err := decodeRestoreJournal([]byte(data)); !errors.Is(err, errRestoreRecoveryRequired) {
			t.Fatalf("decode journal %q error = %v, want recovery required", data, err)
		}
	}
}

func FuzzRestoreJournalDecoder(f *testing.F) {
	f.Add([]byte(`{"version":1,"state":"committed"}`))
	f.Add([]byte("\x00\xff{}"))
	f.Add(make([]byte, restoreJournalMaxSize+1))
	f.Fuzz(func(t *testing.T, data []byte) {
		journal, err := decodeRestoreJournal(data)
		if err == nil {
			if err := journal.validate(); err != nil {
				t.Fatalf("decoder accepted invalid journal: %#v: %v", journal, err)
			}
		}
	})
}
