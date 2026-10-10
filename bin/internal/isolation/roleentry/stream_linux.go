//go:build linux

package roleentry

import (
	"fmt"
	"net"
	"os"

	"github.com/asciimoth/almagest/bin/internal/isolation/transport"
)

func bindBootstrapStream(file *os.File) (transport.Stream, error) {
	connection, err := net.FileConn(file)
	_ = file.Close()
	if err != nil {
		return nil, fmt.Errorf("%w: bind bootstrap socket: %v", ErrStandaloneRole, err)
	}
	stream, ok := connection.(*net.UnixConn)
	if !ok {
		_ = connection.Close()
		return nil, fmt.Errorf("%w: bootstrap is not a Unix stream", ErrStandaloneRole)
	}
	return stream, nil
}
