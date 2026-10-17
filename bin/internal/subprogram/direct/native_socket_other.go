//go:build !linux || !amd64

package direct

import (
	"context"
	"os"

	sandbox "github.com/asciimoth/gobox"
)

func (s *instance) nativeSocket(context.Context, string, string, string) (*os.File, error) {
	return nil, sandbox.ErrUnavailable
}
