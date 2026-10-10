//go:build !linux

package roleentry

import (
	"fmt"
	"os"

	"github.com/asciimoth/almagest/bin/internal/isolation/transport"
)

func bindBootstrapStream(file *os.File) (transport.Stream, error) {
	stream, ok := any(file).(transport.Stream)
	if !ok {
		_ = file.Close()
		return nil, fmt.Errorf("%w: bootstrap descriptor has no deadline support", ErrStandaloneRole)
	}
	return stream, nil
}
