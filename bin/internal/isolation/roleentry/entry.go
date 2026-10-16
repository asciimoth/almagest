// Package roleentry contains the minimal process entry boundary for privileged
// isolated roles. It intentionally imports no daemon runtime, node, panel,
// archive, database, native backend, or general config package.
package roleentry

import (
	"errors"
	"fmt"
	"os"
	"strconv"

	"github.com/asciimoth/almagest/bin/internal/isolation/protocol"
	"github.com/asciimoth/almagest/bin/internal/isolation/transport"
)

const BootstrapFDEnvironment = "ALMAGEST_BOOTSTRAP_FD"

type Lane string

const (
	LaneBootstrap   Lane = BootstrapFDEnvironment
	LaneData        Lane = "ALMAGEST_DATA_FD"
	LaneDiagnostics Lane = "ALMAGEST_DIAGNOSTICS_FD"
	LaneAdmin       Lane = "ALMAGEST_ADMIN_FD"
	LaneConfig      Lane = "ALMAGEST_CONFIG_FD"
	LaneCommand     Lane = "ALMAGEST_COMMAND_FD"
	LaneCallback    Lane = "ALMAGEST_CALLBACK_FD"
	LaneSandbox     Lane = "ALMAGEST_SANDBOX_FD"
	LaneSandboxData Lane = "ALMAGEST_SANDBOX_DATA_FD"
)

var ErrStandaloneRole = errors.New("isolated role requires an inherited bootstrap channel")

// OpenBootstrap accepts only an inherited non-file descriptor selected by the
// trusted launcher. A role flag or a named endpoint is not sufficient.
func OpenBootstrap(role protocol.Role, arguments []string, getenv func(string) string) (transport.Stream, error) {
	if !role.Valid() || role == protocol.RoleRuntime || getenv == nil || len(arguments) != 0 {
		return nil, fmt.Errorf("%w: invalid role entry", ErrStandaloneRole)
	}
	return OpenLane(LaneBootstrap, getenv)
}

// OpenLane opens one fixed inherited role channel. The role executable selects
// the lane in code; no command-line value can select a descriptor or handle.
func OpenLane(lane Lane, getenv func(string) string) (transport.Stream, error) {
	file, err := OpenLaneFile(lane, getenv)
	if err != nil {
		return nil, err
	}
	return bindBootstrapStream(file)
}

// OpenLaneFile returns one inherited channel before peer binding. The caller
// must bind it to trusted launch identity with transport.BindSocket or close
// it. It is used only for direct role-to-role lanes whose peer starts later.
func OpenLaneFile(lane Lane, getenv func(string) string) (*os.File, error) {
	if getenv == nil || !lane.Valid() {
		return nil, fmt.Errorf("%w: invalid role lane", ErrStandaloneRole)
	}
	value := getenv(string(lane))
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

func (l Lane) Valid() bool {
	switch l {
	case LaneBootstrap, LaneData, LaneDiagnostics, LaneAdmin, LaneConfig, LaneCommand, LaneCallback, LaneSandbox, LaneSandboxData:
		return true
	default:
		return false
	}
}
