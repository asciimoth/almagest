package main

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"

	"github.com/asciimoth/almagest/bin/api"
)

// runtimeMutationCoordinator owns the admin-side gate for runtime state. A
// snapshot mutation gets exclusive access. Other requests get shared access,
// so a live restore cannot expose a partially replaced graph or data store.
type runtimeMutationCoordinator struct {
	mu sync.RWMutex
}

// Acquire enters the mutation gate and returns its matching release function.
func (c *runtimeMutationCoordinator) Acquire(exclusive bool) func() {
	if exclusive {
		c.mu.Lock()
		return c.mu.Unlock
	}
	c.mu.RLock()
	return c.mu.RUnlock
}

// snapshotRestoreCoordinator owns the live restore transaction. Archive
// parsing and storage operations remain in snapshotService, while this type
// makes the runtime mutation, rollback, notification, and persistence order
// explicit. It is an in-process coordinator and does not define an IPC API.
type snapshotRestoreCoordinator struct {
	service *snapshotService
}

func newSnapshotRestoreCoordinator(service *snapshotService) *snapshotRestoreCoordinator {
	return &snapshotRestoreCoordinator{service: service}
}

// Restore replaces persistent storage and reconstructs every graph node from
// the snapshot config. A synchronous failure restores the captured runtime
// state before the final workspace and worker views are published.
func (c *snapshotRestoreCoordinator) Restore(ctx context.Context, uuid string) (api.SnapshotsRestoreResponse, error) {
	if c == nil || c.service == nil {
		return api.SnapshotsRestoreResponse{}, fmt.Errorf("snapshot service is not ready")
	}
	s := c.service
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

	// The autosaver lock is the runtime mutation gate for graph/config changes.
	// Hold it through rollback and the distinct post-restore config write.
	s.autosaver.saveMu.Lock()
	defer s.autosaver.saveMu.Unlock()
	recovery, err := s.captureRecoveryArchive(ctx)
	if err != nil {
		return api.SnapshotsRestoreResponse{}, fmt.Errorf("capture pre-restore state: %w", err)
	}
	defer recovery.cleanup()

	// Keep the previous graph visible while restore tears down and reconstructs
	// runtime nodes. Publish also runs after rollback, so worker and workspace
	// subscribers receive one authoritative view of the state that remains.
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
