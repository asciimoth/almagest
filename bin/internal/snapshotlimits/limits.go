// Package snapshotlimits shares existing snapshot transfer limits without
// importing archive parsers, the daemon API, or runtime engine code.
package snapshotlimits

const (
	ArchiveBytes    int64 = 32 << 20
	APIMessageBytes int64 = ((ArchiveBytes + 2) / 3 * 4) + (1 << 20)
)
