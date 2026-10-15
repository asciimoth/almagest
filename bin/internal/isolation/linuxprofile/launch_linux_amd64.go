//go:build linux && amd64

package linuxprofile

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"syscall"
)

// Inputs are held objects opened and trust-checked by bootstrap. The launcher
// independently verifies object types, ownership, ELF format, and limits.
// Launcher and Executable must be root-owned static ELF files. Each role needs
// a distinct identity, private directory and independently limited cgroup.
// The launcher enters this already provisioned cgroup atomically at process
// creation. This API never writes cgroup limits or obtains missing authority.
type Inputs struct {
	Launcher    *os.File
	Control     *os.File
	Executable  *os.File
	Directory   *os.File
	Cgroup      *os.File
	Code        *os.File // required only by linux-user-v1 for one exclusive role copy
	Command     *os.File // required only for the fixed-command helper
	Data        *os.File // optional separate N/R data lane
	Diagnostics *os.File // optional separate logging lane
	Admin       *os.File // optional separate N/R admin lane
	Config      *os.File // optional fixed R/C control lane
	Commands    *os.File // optional fixed R/H control lane
	Callbacks   *os.File // optional fixed N/H callback lane
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
	profile := p.Profile
	if profile == "" {
		profile = ProfileFull
	}
	if ctx == nil || in.Launcher == nil || in.Control == nil || in.Executable == nil || in.Directory == nil ||
		(profile == ProfileFull && (in.Cgroup == nil || in.Code != nil) ||
			profile == ProfileUser && (in.Cgroup != nil || in.Code == nil)) ||
		(p.Role == Command) != (in.Command != nil) ||
		(p.Role == Writer || p.Role == Command) && (in.Data != nil || in.Admin != nil || in.Config != nil || in.Commands != nil) ||
		p.Role != Command && in.Callbacks != nil {
		return nil, fmt.Errorf("%w: incomplete inherited objects", ErrUnavailable)
	}
	// Descriptor 8 is used only to enter the native launcher. It is closed by
	// close_range before the role executable starts.
	cmd := exec.CommandContext(ctx, "/proc/self/fd/8", args...)
	cmd.Env = []string{}
	cmd.Dir = "/"
	// CLONE_INTO_CGROUP puts the first child instruction under the role's
	// resource limits. Moving the bootstrap process or attaching a running child
	// would either affect another role or leave an unrestricted startup window.
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
	if profile == ProfileFull {
		cmd.SysProcAttr.UseCgroupFD = true
		cmd.SysProcAttr.CgroupFD = int(in.Cgroup.Fd())
	}
	resource := in.Cgroup
	if profile == ProfileUser {
		resource = in.Code
	}
	cmd.ExtraFiles = []*os.File{in.Control, in.Executable, in.Directory, resource, in.Command, in.Launcher, in.Data, in.Diagnostics, in.Admin, in.Config, in.Commands, in.Callbacks}
	return cmd, nil
}
