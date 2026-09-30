//go:build !windows

package main

func snapshotSymlinkRestoreSupported() bool { return true }
