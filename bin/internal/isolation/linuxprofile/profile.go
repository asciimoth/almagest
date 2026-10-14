// Package linuxprofile defines the fixed Linux role-launch contract used by
// the production Linux isolated daemon. Only trusted bootstrap may build a Plan.
package linuxprofile

import (
	"errors"
	"fmt"
	"strconv"
)

type Role string

const (
	Runtime Role = "runtime"
	Network Role = "network" // Trusted backend cgroup only; never a launcher role.
	Writer  Role = "writer"
	Command Role = "command"
)

var ErrUnavailable = errors.New("linux isolation profile unavailable; use explicit --no-isolation")

// Limits are hard ceilings, not reservations. Each role also needs its own
// externally provisioned cgroup v2 with bounded memory, CPU, and tasks.
type Limits struct {
	Descriptors uint64
	FileBytes   uint64
	MemoryBytes uint64
	Tasks       uint64
	CPUQuota    uint64 // microseconds per 100000 microseconds
}

func DefaultLimits() Limits {
	return Limits{Descriptors: 65536, FileBytes: 1 << 40, MemoryBytes: 8 << 30, Tasks: 4096, CPUQuota: 6400000}
}

// Plan fixes one static Linux/amd64 executable, identity and role. Descriptor
// inputs are supplied separately: connected control at 3, executable at 4,
// private storage/target directory at 5, and the current cgroup at 6. Command
// additionally receives one fixed static command executable at 7.
// None of these values may come from regular config or a runtime request.
type Plan struct {
	Role         Role
	UID, GID     uint32
	Capabilities uint64 // Reserved launcher ABI field; must be zero.
	Limits       Limits
}

func (p Plan) Validate() error {
	if p.Role != Runtime && p.Role != Writer && p.Role != Command {
		return fmt.Errorf("%w: unknown role", ErrUnavailable)
	}
	if p.UID == 0 || p.GID == 0 || p.UID == ^uint32(0) || p.GID == ^uint32(0) {
		return fmt.Errorf("%w: provision separate non-root role identities", ErrUnavailable)
	}
	if p.Capabilities != 0 {
		return fmt.Errorf("%w: capability expansion", ErrUnavailable)
	}
	return p.Limits.Validate()
}

// Validate checks the resource ranges accepted by the native launcher ABI.
func (l Limits) Validate() error {
	if l.Descriptors < 32 || l.Descriptors > 65536 || l.FileBytes < 1<<20 || l.FileBytes > 1<<40 ||
		l.MemoryBytes < 32<<20 || l.MemoryBytes > 1<<40 || l.Tasks < 8 || l.Tasks > 4096 ||
		l.CPUQuota < 1000 || l.CPUQuota > 6400000 {
		return fmt.Errorf("%w: invalid resource ceilings", ErrUnavailable)
	}
	return nil
}

// Arguments is the versioned native launcher ABI. There are no path or
// environment arguments. The native side independently checks every field.
func (p Plan) Arguments() ([]string, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	args := []string{"v1", string(p.Role)}
	for _, n := range []uint64{uint64(p.UID), uint64(p.GID), p.Capabilities, p.Limits.Descriptors,
		p.Limits.FileBytes, p.Limits.MemoryBytes, p.Limits.Tasks, p.Limits.CPUQuota} {
		args = append(args, strconv.FormatUint(n, 10))
	}
	return args, nil
}
