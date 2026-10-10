package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

type restoreEpochRecorder struct {
	mu     sync.Mutex
	errors []error
	calls  int
}

func (r *restoreEpochRecorder) ReleaseGraphEpoch(context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	if len(r.errors) == 0 {
		return nil
	}
	err := r.errors[0]
	r.errors = r.errors[1:]
	return err
}

func TestRuntimeMutationCoordinatorExcludesRestore(t *testing.T) {
	var coordinator runtimeMutationCoordinator
	releaseRead, err := coordinator.Acquire(false, false)
	if err != nil {
		t.Fatal(err)
	}

	acquired := make(chan func(), 1)
	go func() {
		release, _ := coordinator.Acquire(true, true)
		acquired <- release
	}()

	select {
	case release := <-acquired:
		release()
		t.Fatal("exclusive mutation overlapped a state reader")
	case <-time.After(20 * time.Millisecond):
	}

	releaseRead()
	select {
	case release := <-acquired:
		release()
	case <-time.After(time.Second):
		t.Fatal("exclusive mutation did not start after the state reader finished")
	}
}

func TestRuntimeMutationCoordinatorBlocksMutationsAfterFailedRollback(t *testing.T) {
	var coordinator runtimeMutationCoordinator
	release, err := coordinator.Acquire(true, true)
	if err != nil {
		t.Fatal(err)
	}
	coordinator.requireRecovery(errors.New("rollback uncertain"))
	release()

	if _, err := coordinator.Acquire(false, true); !errors.Is(err, errRestoreRecoveryRequired) {
		t.Fatalf("mutation error = %v, want recovery required", err)
	}
	release, err = coordinator.Acquire(false, false)
	if err != nil {
		t.Fatalf("read-only request was blocked: %v", err)
	}
	release()
}

func TestGraphEpochCoordinatorAdvancesOnlyAfterDrain(t *testing.T) {
	injected := errors.New("lost release reply")
	recorder := &restoreEpochRecorder{errors: []error{injected, nil}}
	coordinator := graphEpochCoordinator{releaser: recorder}
	if err := coordinator.invalidateAndDrain(context.Background()); !errors.Is(err, injected) {
		t.Fatalf("first drain error = %v, want %v", err, injected)
	}
	if epoch := coordinator.current(); epoch != 0 {
		t.Fatalf("epoch after failed drain = %d, want 0", epoch)
	}
	if err := coordinator.invalidateAndDrain(context.Background()); err != nil {
		t.Fatal(err)
	}
	if epoch := coordinator.current(); epoch != 1 {
		t.Fatalf("epoch after committed drain = %d, want 1", epoch)
	}
}

func TestRestoreDrainsDesiredAndRollbackEpochs(t *testing.T) {
	service, _, engine, _, storagePath := newSnapshotTestService(t, false)
	marker := filepath.Join(storagePath, "epoch-marker")
	if err := os.WriteFile(marker, []byte("snapshot"), 0o600); err != nil {
		t.Fatal(err)
	}
	snapshot, err := service.Create(context.Background(), "epoch rollback")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(marker, []byte("before"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := service.autosaver.Update(`{"nodes":{}}`, ""); err != nil {
		t.Fatal(err)
	}
	recorder := &restoreEpochRecorder{}
	service.restore.setEpochReleaser(recorder)
	service.restore.hooks.afterDataApplied = func() error { return errors.New("injected rebuild failure") }

	if _, err := service.Restore(context.Background(), snapshot.UUID); err == nil {
		t.Fatal("restore succeeded after injected failure")
	}
	if recorder.calls != 2 || service.restore.epoch.current() != 2 {
		t.Fatalf("epoch releases = %d, epoch = %d; want 2, 2", recorder.calls, service.restore.epoch.current())
	}
	if data, err := os.ReadFile(marker); err != nil || string(data) != "before" {
		t.Fatalf("marker after rollback = %q, err=%v", data, err)
	}
	if _, ok := engine.NodeByName("main", "saved-node"); ok {
		t.Fatal("rollback did not restore the pre-restore empty graph")
	}
	journalPath, recoveryPath := restoreStatePaths(storagePath)
	for _, path := range []string{journalPath, recoveryPath} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("restore state %s remains after rollback: %v", path, err)
		}
	}
}

func TestRestoreFailedRollbackLatchesRecoveryRequired(t *testing.T) {
	service, _, _, _, _ := newSnapshotTestService(t, false)
	snapshot, err := service.Create(context.Background(), "failed rollback")
	if err != nil {
		t.Fatal(err)
	}
	recorder := &restoreEpochRecorder{errors: []error{errors.New("first drain failed"), errors.New("rollback drain failed")}}
	service.restore.setEpochReleaser(recorder)
	var mutations runtimeMutationCoordinator
	service.requireRecovery = mutations.requireRecovery

	if _, err := service.Restore(context.Background(), snapshot.UUID); err == nil {
		t.Fatalf("restore error = %v, want failed rollback", err)
	}
	if _, err := mutations.Acquire(false, true); !errors.Is(err, errRestoreRecoveryRequired) {
		t.Fatalf("later mutation error = %v, want recovery required", err)
	}
}

func TestRuntimeMutationCoordinatorAllowsConcurrentReaders(t *testing.T) {
	var coordinator runtimeMutationCoordinator
	releaseFirst, err := coordinator.Acquire(false, false)
	if err != nil {
		t.Fatal(err)
	}

	acquired := make(chan func(), 1)
	go func() {
		release, _ := coordinator.Acquire(false, false)
		acquired <- release
	}()

	select {
	case releaseSecond := <-acquired:
		releaseSecond()
	case <-time.After(time.Second):
		t.Fatal("state readers did not run concurrently")
	}
	releaseFirst()
}
