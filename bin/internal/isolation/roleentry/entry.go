// Package roleentry contains the minimal process entry boundary for privileged
// isolated roles. It intentionally imports no daemon runtime, node, panel,
// archive, database, native backend, or general config package.
package roleentry

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"

	"github.com/asciimoth/almagest/bin/internal/isolation/protocol"
)

const BootstrapFDEnvironment = "ALMAGEST_BOOTSTRAP_FD"

var ErrStandaloneRole = errors.New("isolated role requires an inherited bootstrap channel")

// OpenBootstrap accepts only an inherited non-file descriptor selected by the
// trusted launcher. A role flag or a named endpoint is not sufficient.
func OpenBootstrap(role protocol.Role, arguments []string, getenv func(string) string) (io.ReadWriteCloser, error) {
	if !role.Valid() || role == protocol.RoleRuntime || getenv == nil || len(arguments) != 0 {
		return nil, fmt.Errorf("%w: invalid role entry", ErrStandaloneRole)
	}
	value := getenv(BootstrapFDEnvironment)
	fd, err := strconv.ParseUint(value, 10, 32)
	if err != nil || fd <= 2 {
		return nil, fmt.Errorf("%w: missing bootstrap descriptor", ErrStandaloneRole)
	}
	file := os.NewFile(uintptr(fd), "isolation-bootstrap")
	if file == nil {
		return nil, fmt.Errorf("%w: invalid bootstrap descriptor", ErrStandaloneRole)
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("%w: inspect bootstrap descriptor: %v", ErrStandaloneRole, err)
	}
	if info.Mode().IsRegular() || info.IsDir() || info.Mode()&os.ModeCharDevice != 0 {
		_ = file.Close()
		return nil, fmt.Errorf("%w: bootstrap descriptor has an unsafe type", ErrStandaloneRole)
	}
	return file, nil
}
