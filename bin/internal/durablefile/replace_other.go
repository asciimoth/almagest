//go:build !windows

package durablefile

import "os"

// Replace atomically replaces destination with replacement. Both paths must be
// on the same filesystem.
func Replace(destination, replacement string) error {
	return os.Rename(replacement, destination)
}
