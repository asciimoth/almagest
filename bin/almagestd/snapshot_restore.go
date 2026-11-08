package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/asciimoth/almagest/bin/api"
	"github.com/asciimoth/configer/hujson"
)

type runtimeMutationCoordinator struct {
	mu               sync.RWMutex
	recoveryRequired error
}

func (c *runtimeMutationCoordinator) Acquire(exclusive, mutation bool) (func(), error) {
	if exclusive {
		c.mu.Lock()
		if mutation && c.recoveryRequired != nil {
			err := c.recoveryRequired
			c.mu.Unlock()
			return nil, err
		}
		return c.mu.Unlock, nil
	}
	c.mu.RLock()
	if mutation && c.recoveryRequired != nil {
		err := c.recoveryRequired
		c.mu.RUnlock()
		return nil, err
	}
	return c.mu.RUnlock, nil
}

// requireRecovery is called while the exclusive mutation gate is held.
func (c *runtimeMutationCoordinator) requireRecovery(cause error) {
	if cause != nil && c.recoveryRequired == nil {
		c.recoveryRequired = fmt.Errorf("%w: %v", errRestoreRecoveryRequired, cause)
	}
}

func (c *runtimeMutationCoordinator) setInitialRecoveryRequired(cause error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.requireRecovery(cause)
}

type graphEpochReleaser interface {
	ReleaseGraphEpoch(context.Context) error
}

type graphEpochCoordinator struct {
	mu       sync.Mutex
	epoch    uint64
	releaser graphEpochReleaser
}

func (c *graphEpochCoordinator) invalidateAndDrain(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.releaser != nil {
		if err := c.releaser.ReleaseGraphEpoch(ctx); err != nil {
			return err
		}
	}
	c.epoch++
	return nil
}

func (c *graphEpochCoordinator) current() uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.epoch
}

type snapshotRestoreHooks struct {
	afterPrepared    func() error
	afterDataApplied func() error
	afterCommitted   func() error
}

var errSimulatedRestoreProcessDeath = errors.New("simulated process death during snapshot restore")

type snapshotRestoreCoordinator struct {
	service *snapshotService
	epoch   graphEpochCoordinator
	hooks   snapshotRestoreHooks
}

func newSnapshotRestoreCoordinator(service *snapshotService) *snapshotRestoreCoordinator {
	return &snapshotRestoreCoordinator{service: service}
}

func (c *snapshotRestoreCoordinator) setEpochReleaser(candidate any) {
	if releaser, ok := candidate.(graphEpochReleaser); ok {
		c.epoch.releaser = releaser
	}
}

func (c *snapshotRestoreCoordinator) Restore(ctx context.Context, uuid string) (api.SnapshotsRestoreResponse, error) {
	return c.restore(ctx, func(s *snapshotService) (*loadedSnapshot, error) {
		uuid, err := normalizeSnapshotUUID(uuid)
		if err != nil {
			return nil, err
		}
		return s.loadArchive(ctx, filepath.Join(s.storagePath, snapshotDirectoryName, uuid+".zip"), uuid)
	})
}

func (c *snapshotRestoreCoordinator) RestoreExample(ctx context.Context, id, locale string) (api.SnapshotsRestoreResponse, error) {
	if err := validateExampleID(id); err != nil {
		return api.SnapshotsRestoreResponse{}, err
	}
	return c.restore(ctx, func(s *snapshotService) (*loadedSnapshot, error) {
		return s.loadExample(ctx, id, locale)
	})
}

func (c *snapshotRestoreCoordinator) restore(
	ctx context.Context,
	load func(*snapshotService) (*loadedSnapshot, error),
) (api.SnapshotsRestoreResponse, error) {
	if c == nil || c.service == nil {
		return api.SnapshotsRestoreResponse{}, fmt.Errorf("snapshot service is not ready")
	}
	s := c.service
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ready(); err != nil {
		return api.SnapshotsRestoreResponse{}, err
	}
	if _, err := readRestoreJournal(s.storagePath); err == nil {
		return api.SnapshotsRestoreResponse{}, errRestoreRecoveryRequired
	} else if !errors.Is(err, os.ErrNotExist) {
		return api.SnapshotsRestoreResponse{}, fmt.Errorf("%w: existing restore journal: %v", errRestoreRecoveryRequired, err)
	}
	desired, err := load(s)
	if err != nil {
		return api.SnapshotsRestoreResponse{}, err
	}
	defer desired.cleanup()

	s.autosaver.saveMu.Lock()
	defer s.autosaver.saveMu.Unlock()
	resumeWriters, err := s.manager.writeBarrier.quiesce(ctx)
	if err != nil {
		return api.SnapshotsRestoreResponse{}, fmt.Errorf("wait for storage writers: %w", err)
	}
	defer resumeWriters()

	recovery, journal, err := s.prepareRestoreRecovery(ctx, desired)
	if err != nil {
		return api.SnapshotsRestoreResponse{}, fmt.Errorf("capture pre-restore state: %w", err)
	}
	defer recovery.cleanup()
	if c.hooks.afterPrepared != nil {
		if err := c.hooks.afterPrepared(); err != nil {
			return api.SnapshotsRestoreResponse{}, err
		}
	}

	notifications := s.engine.BeginNotificationTransaction()
	defer notifications.Publish()
	before := s.storageIndex()
	if err := c.apply(ctx, desired, &journal, resumeWriters); err != nil {
		if errors.Is(err, errSimulatedRestoreProcessDeath) {
			return api.SnapshotsRestoreResponse{}, err
		}
		return api.SnapshotsRestoreResponse{}, c.rollback(ctx, recovery, err, resumeWriters)
	}
	journal.State = restoreStateCommitted
	if err := writeRestoreJournal(s.storagePath, journal); err != nil {
		return api.SnapshotsRestoreResponse{}, c.rollback(ctx, recovery, fmt.Errorf("commit restore journal: %w", err), resumeWriters)
	}
	if c.hooks.afterCommitted != nil {
		if err := c.hooks.afterCommitted(); err != nil {
			return api.SnapshotsRestoreResponse{}, err
		}
	}
	after := s.storageIndex()
	s.notifyStorageReset(before, after)

	response := api.SnapshotsRestoreResponse{Snapshot: desired.info}
	if !s.readOnly && s.autosaver.persistence != nil {
		if err := s.autosaver.replaceConfigLocked(desired.configData); err != nil {
			response.ConfigWriteError = err.Error()
			if cleanupErr := removeCommittedRecovery(s.storagePath); cleanupErr != nil {
				if s.requireRecovery != nil {
					s.requireRecovery(fmt.Errorf("remove committed recovery copy: %w", cleanupErr))
				}
				if s.logger != nil {
					s.logger.Errf("snapshot restore recovery-copy cleanup failed: %v", cleanupErr)
				}
			}
			if s.logger != nil {
				s.logger.Errf("snapshot restore config write failed; restored config remains active in memory: %v", err)
			}
			return response, nil
		}
		response.ConfigWritten = true
	}
	if err := clearRestoreState(s.storagePath, true); err != nil {
		if s.requireRecovery != nil {
			s.requireRecovery(fmt.Errorf("clear committed restore state: %w", err))
		}
		if s.logger != nil {
			s.logger.Errf("snapshot restore committed-state cleanup failed: %v", err)
		}
	}
	return response, nil
}

func (c *snapshotRestoreCoordinator) apply(
	ctx context.Context,
	desired *loadedSnapshot,
	journal *restoreJournal,
	resumeWriters func(),
) error {
	s := c.service
	if err := s.stopGraph(); err != nil {
		return err
	}
	if err := c.epoch.invalidateAndDrain(ctx); err != nil {
		return fmt.Errorf("release old graph epoch: %w", err)
	}
	if err := s.applyLoadedSnapshotData(ctx, desired, false); err != nil {
		return err
	}
	journal.State = restoreStateDataApplied
	if err := writeRestoreJournal(s.storagePath, *journal); err != nil {
		return fmt.Errorf("record applied restore data: %w", err)
	}
	if c.hooks.afterDataApplied != nil {
		if err := c.hooks.afterDataApplied(); err != nil {
			return err
		}
	}
	// Graph construction can start node-owned storage writes. The old graph is
	// gone and the snapshot data is installed, so admit only work created by
	// the new graph from this point.
	resumeWriters()
	return s.rebuildGraph(desired.config)
}

func (c *snapshotRestoreCoordinator) rollback(
	ctx context.Context,
	recovery *loadedSnapshot,
	cause error,
	resumeInitialWriters func(),
) error {
	s := c.service
	resumeInitialWriters()
	rollbackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	rollbackErr := s.stopGraph()
	if drainErr := c.epoch.invalidateAndDrain(rollbackCtx); drainErr != nil {
		rollbackErr = errors.Join(rollbackErr, fmt.Errorf("release failed graph epoch: %w", drainErr))
	}
	var resumeWriters func()
	if rollbackErr == nil {
		resumeWriters, rollbackErr = s.manager.writeBarrier.quiesce(rollbackCtx)
		if resumeWriters != nil {
			defer resumeWriters()
		}
	}
	if rollbackErr == nil {
		rollbackErr = s.applyLoadedSnapshotData(rollbackCtx, recovery, true)
	}
	if rollbackErr == nil {
		resumeWriters()
		rollbackErr = s.rebuildGraph(recovery.config)
	}
	if rollbackErr == nil {
		rollbackErr = clearRestoreState(s.storagePath, false)
	}
	if rollbackErr != nil {
		failure := fmt.Errorf("restore snapshot: %w; rollback failed: %v", cause, rollbackErr)
		if s.requireRecovery != nil {
			s.requireRecovery(failure)
		}
		return failure
	}
	return fmt.Errorf("restore snapshot: %w", cause)
}

func (s *snapshotService) stopGraph() error {
	empty, err := parseEditableConfig([]byte(`{"nodes":{}}`))
	if err != nil {
		return err
	}
	if _, err := s.engine.ReconcileConfig(empty); err != nil {
		return fmt.Errorf("stop current engine graph: %w", err)
	}
	return nil
}

func (s *snapshotService) rebuildGraph(config *hujson.Config) error {
	if _, err := s.engine.ReconcileConfig(config); err != nil {
		return fmt.Errorf("rebuild engine graph: %w", err)
	}
	return nil
}
