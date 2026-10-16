//go:build linux && amd64

package networkrole

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/asciimoth/almagest/bin/internal/isolation/linuxprofile"
	"github.com/asciimoth/almagest/bin/internal/isolation/protocol"
	"github.com/asciimoth/almagest/bin/internal/isolation/roleentry"
	"github.com/asciimoth/almagest/bin/internal/isolation/supervision"
	"github.com/asciimoth/almagest/bin/internal/isolation/transport"
)

type sandboxRoleProcess struct {
	command   *exec.Cmd
	bootstrap transport.Stream
	control   *os.File
	data      *os.File
	done      chan struct{}
	mu        sync.Mutex
	err       error
	ready     chan supervision.ReadyResult
}

type wrappedRoleOutput struct{ io.Writer }

func startSandboxRole(ctx context.Context, executable, cgroup *os.File, profile linuxprofile.Profile, limits linuxprofile.Limits, output io.Writer) (_ *sandboxRoleProcess, err error) {
	if profile == "" {
		profile = linuxprofile.ProfileFull
	}
	if ctx == nil || executable == nil || limits.Validate() != nil || profile == linuxprofile.ProfileFull && cgroup == nil || profile == linuxprofile.ProfileUser && cgroup != nil {
		return nil, supervision.ErrInvalidComposition
	}
	bootstrapParent, bootstrapChild, err := transport.SocketPair()
	if err != nil {
		return nil, err
	}
	runtimeControl, supervisorControl, err := transport.SocketPair()
	if err != nil {
		_ = bootstrapParent.Close()
		_ = bootstrapChild.Close()
		return nil, err
	}
	runtimeData, supervisorData, err := transport.SocketPair()
	if err != nil {
		for _, file := range []*os.File{bootstrapParent, bootstrapChild, runtimeControl, supervisorControl} {
			_ = file.Close()
		}
		return nil, err
	}
	files := []*os.File{bootstrapParent, bootstrapChild, runtimeControl, supervisorControl, runtimeData, supervisorData}
	failed := true
	defer func() {
		if failed {
			for _, file := range files {
				_ = file.Close()
			}
		}
	}()
	cmd := exec.CommandContext(context.WithoutCancel(ctx), "/proc/self/fd/6")
	cmd.Args = []string{"almagest-sandbox-supervisor"}
	cmd.Env = []string{
		"ALMAGEST_ROLE=" + string(protocol.RoleSandboxSupervisor),
		string(roleentry.LaneBootstrap) + "=3",
		string(roleentry.LaneSandbox) + "=4",
		string(roleentry.LaneSandboxData) + "=5",
		"ALMAGEST_SANDBOX_NOFILE=" + strconv.FormatUint(limits.Descriptors, 10),
		"ALMAGEST_SANDBOX_FSIZE=" + strconv.FormatUint(limits.FileBytes, 10),
		"GOMAXPROCS=2", "GODEBUG=netdns=go",
	}
	cmd.Dir = "/"
	cmd.ExtraFiles = []*os.File{bootstrapChild, supervisorControl, supervisorData, executable}
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL, Setpgid: true}
	if profile == linuxprofile.ProfileFull {
		cmd.SysProcAttr.UseCgroupFD = true
		cmd.SysProcAttr.CgroupFD = int(cgroup.Fd())
	}
	if output == nil {
		output = io.Discard
	}
	cmd.Stdout, cmd.Stderr = wrappedRoleOutput{output}, wrappedRoleOutput{output}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	for _, child := range []*os.File{bootstrapChild, supervisorControl, supervisorData} {
		_ = child.Close()
	}
	peer := transport.LinuxPeer{PID: int32(cmd.Process.Pid), UID: uint32(os.Geteuid()), GID: uint32(os.Getegid())}
	bootstrapStream, err := transport.BindSocket(bootstrapParent, peer)
	if err != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		_ = cmd.Wait()
		return nil, err
	}
	process := &sandboxRoleProcess{command: cmd, bootstrap: bootstrapStream, control: runtimeControl, data: runtimeData,
		done: make(chan struct{}), ready: make(chan supervision.ReadyResult, 1)}
	go func() {
		waitErr := cmd.Wait()
		process.mu.Lock()
		process.err = waitErr
		process.mu.Unlock()
		close(process.done)
	}()
	failed = false
	return process, nil
}

func (p *sandboxRoleProcess) runtimePeer(profile linuxprofile.Profile) roleentry.RuntimePeer {
	peer := roleentry.RuntimePeer{PID: int32(p.command.Process.Pid), UID: uint32(os.Geteuid()), GID: uint32(os.Getegid())}
	if profile == linuxprofile.ProfileFull {
		peer.PID = 0
		peer.OutsidePIDNamespace = true
	}
	return peer
}

func (p *sandboxRoleProcess) supervised(ready roleentry.Ready) (supervision.Process, error) {
	p.ready <- supervision.ReadyResult{Role: ready.Role, Version: ready.Version, PolicyDigest: ready.PolicyDigest, RestrictionsApplied: ready.RestrictionsApplied}
	return supervision.NewStartedProcess(protocol.RoleSandboxSupervisor, p.ready, p.wait, p.stop, p.kill)
}

func (p *sandboxRoleProcess) wait() error {
	<-p.done
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.err
}

func (p *sandboxRoleProcess) stop(ctx context.Context) error {
	if err := transport.WriteFrame(p.bootstrap, struct{}{}); err != nil && !errors.Is(err, os.ErrClosed) {
		return err
	}
	_ = p.bootstrap.Close()
	select {
	case <-p.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (p *sandboxRoleProcess) kill() error {
	select {
	case <-p.done:
		return nil
	default:
	}
	if err := syscall.Kill(-p.command.Process.Pid, syscall.SIGKILL); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return fmt.Errorf("kill sandbox supervisor process group %s: %w", strconv.Itoa(p.command.Process.Pid), err)
	}
	return nil
}

func (p *sandboxRoleProcess) close() error {
	_ = p.bootstrap.Close()
	_ = p.control.Close()
	_ = p.data.Close()
	_ = p.kill()
	select {
	case <-p.done:
		return nil
	case <-time.After(5 * time.Second):
		return supervision.ErrShutdownIncomplete
	}
}
