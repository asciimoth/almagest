//go:build !linux

package direct

import (
	"os"

	sandbox "github.com/asciimoth/gobox"
)

func duplicateFile(*os.File) (*os.File, error) { return nil, sandbox.ErrUnavailable }
