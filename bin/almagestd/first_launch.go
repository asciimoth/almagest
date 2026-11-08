package main

import (
	"context"

	"github.com/kataras/golog"
)

const firstLaunchExample = "001-hello-network"

// loadFirstLaunchExample runs before the admin API accepts requests.
func loadFirstLaunchExample(ctx context.Context, snapshots *snapshotService, manager *bboltKVManager) error {
	if manager.flagsSnapshot().LaunchedBefore {
		return nil
	}
	if nodes, ok := currentWorkspaceNodes(snapshots.engine); ok && len(nodes) == 0 {
		result, err := snapshots.restore.RestoreExample(ctx, firstLaunchExample, "en")
		if err != nil {
			golog.Warnf("Cannot load first-launch example: %v", err)
		} else if result.ConfigWriteError != "" {
			golog.Warnf("First-launch example config could not be written: %s", result.ConfigWriteError)
		}
	}
	return manager.markLaunched(ctx)
}
