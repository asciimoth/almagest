//go:build !linux

package direct

import (
	"os"

	"github.com/asciimoth/almagest/lib/sandbox"
)

func duplicateFile(*os.File) (*os.File, error) { return nil, sandbox.ErrUnavailable }
