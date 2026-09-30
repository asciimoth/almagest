//go:build windows

package main

// Snapshot restore must work for an ordinary user. Windows symbolic-link
// creation can require a privilege that such a user does not have.
func snapshotSymlinkRestoreSupported() bool { return false }
