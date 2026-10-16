//go:build !linux || !amd64

package direct

import (
	"context"
	"os"

	"github.com/asciimoth/almagest/lib/sandbox"
)

func (s *instance) nativeSocket(context.Context, string, string, string) (*os.File, error) {
	return nil, sandbox.ErrUnavailable
}
