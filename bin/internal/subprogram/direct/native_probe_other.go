//go:build !linux || !amd64

package direct

import "github.com/asciimoth/almagest/lib/sandbox"

func nativePrerequisites() error { return sandbox.ErrUnavailable }
