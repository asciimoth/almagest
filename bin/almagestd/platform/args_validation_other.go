//go:build !windows

package platform

import "github.com/asciimoth/almagest/bin/almagestd/args"

// ValidateArgs checks platform-specific command-line restrictions.
func ValidateArgs(*args.Args) error {
	return nil
}
