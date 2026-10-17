//go:build !linux || !amd64

package direct

import sandbox "github.com/asciimoth/gobox"

func nativePrerequisites() error { return sandbox.ErrUnavailable }
