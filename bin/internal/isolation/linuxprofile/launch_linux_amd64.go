//go:build linux && amd64

package linuxprofile

import (
	"context"
	"fmt"
	"os"
	"os/exec"
)

// Inputs are held objects opened and trust-checked by bootstrap. The launcher
// independently verifies object types, ownership, ELF format, and limits.
// Launcher and Executable must be root-owned static ELF files. Each role needs
// a distinct identity, private directory and independently limited cgroup.
// Bootstrap must put the launcher in that cgroup BEFORE it starts; this API
// never writes a cgroup or obtains missing authority.
type Inputs struct {
	Launcher    *os.File
	Control     *os.File
	Executable  *os.File
	Directory   *os.File
	Cgroup      *os.File
	Command     *os.File // required only for the fixed-command helper
	Tun         *os.File // optional /dev/net/tun, only for N
	Data        *os.File // optional separate N/R data lane
	Diagnostics *os.File // optional separate logging lane
	Admin       *os.File // optional separate N/R admin lane
}

// CommandContext prepares a launch through a held executable descriptor, with
// no PATH lookup, caller environment or unsafe post-fork Go callback. The
// caller owns all input files and must attach pipe/Unix-stream stdout/stderr.
// Close the parent's child-channel copy after Start so peer loss produces EOF.
func CommandContext(ctx context.Context, p Plan, in Inputs) (*exec.Cmd, error) {
	args, err := p.Arguments()
	if err != nil {
		return nil, err
	}
	if ctx == nil || in.Launcher == nil || in.Control == nil || in.Executable == nil || in.Directory == nil || in.Cgroup == nil ||
		(p.Role == Command) != (in.Command != nil) || in.Tun != nil && p.Role != Network ||
		(p.Role == Writer || p.Role == Command) && (in.Data != nil || in.Admin != nil) {
		return nil, fmt.Errorf("%w: incomplete inherited objects", ErrUnavailable)
	}
	// Descriptor 8 is used only to enter the native launcher. It is closed by
	// close_range before the role executable starts.
	cmd := exec.CommandContext(ctx, "/proc/self/fd/8", args...)
	cmd.Env = []string{}
	cmd.Dir = "/"
	cmd.ExtraFiles = []*os.File{in.Control, in.Executable, in.Directory, in.Cgroup, in.Command, in.Launcher, in.Data, in.Diagnostics, in.Admin}
	if in.Tun != nil {
		cmd.ExtraFiles[4] = in.Tun
	}
	return cmd, nil
}
