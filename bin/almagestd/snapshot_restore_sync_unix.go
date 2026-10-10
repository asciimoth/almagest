//go:build !windows

package main

import "os"

func syncSnapshotDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close() //nolint:errcheck
	return directory.Sync()
}
