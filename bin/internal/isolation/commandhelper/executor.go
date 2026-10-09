package commandhelper

import (
	"context"
	"errors"
	"io"
	"os/exec"
	"strings"
	"sync"
)

// Execution contains only values resolved from one validated protected
// profile. It has no request-controlled executable, environment, or directory.
type Execution struct {
	Sandbox          string
	Executable       string
	Arguments        []string
	Environment      []string
	WorkingDirectory string
	OutputLimit      int
}

// ExecutionResult distinguishes a failure to start from an interrupted
// command whose external side effects can be unknown.
type ExecutionResult struct {
	Started  bool
	ExitCode int32
	Err      error
}

// Launcher resolves protected sandbox IDs and executes commands inside them.
// Validate must reject a profile when the launcher cannot apply its complete
// restriction set. Production launchers are added with the platform profiles;
// this component never silently substitutes an unrestricted launcher.
type Launcher interface {
	Validate(Profile) error
	Run(context.Context, Execution) ExecutionResult
}

// ProcessLauncher is useful for component qualification inside an already
// restricted test process. It accepts only sandbox IDs selected at construction.
// Production composition must use a launcher that installs the platform H
// restrictions before it starts the command.
type ProcessLauncher struct{ sandboxes map[string]struct{} }

// NewProcessLauncher creates a launcher with a closed sandbox-ID set.
func NewProcessLauncher(sandboxes ...string) *ProcessLauncher {
	allowed := make(map[string]struct{}, len(sandboxes))
	for _, sandbox := range sandboxes {
		allowed[sandbox] = struct{}{}
	}
	return &ProcessLauncher{sandboxes: allowed}
}

func (l *ProcessLauncher) Validate(profile Profile) error {
	if l == nil {
		return errors.New("nil process launcher")
	}
	if _, ok := l.sandboxes[profile.Sandbox]; !ok {
		return errors.New("sandbox is not installed")
	}
	return nil
}

func (l *ProcessLauncher) Run(ctx context.Context, execution Execution) ExecutionResult {
	if l == nil {
		return ExecutionResult{Err: errors.New("nil process launcher")}
	}
	if _, ok := l.sandboxes[execution.Sandbox]; !ok {
		return ExecutionResult{Err: errors.New("sandbox is not installed")}
	}
	commandContext, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	cmd := exec.CommandContext(commandContext, execution.Executable, execution.Arguments...)
	cmd.Env = append([]string(nil), execution.Environment...)
	cmd.Dir = execution.WorkingDirectory
	// An EOF pipe has the same input semantics as /dev/null and also works in
	// a role filesystem that deliberately has no host device directory.
	cmd.Stdin = strings.NewReader("")
	cmd.WaitDelay = commandWaitDelay
	configureCommand(cmd)
	output := &boundedOutput{remaining: execution.OutputLimit, cancel: cancel}
	cmd.Stdout, cmd.Stderr = output, output
	if err := cmd.Start(); err != nil {
		return ExecutionResult{Err: err}
	}
	configureStartedCommand(cmd)
	err := cmd.Wait()
	if cause := context.Cause(commandContext); cause != nil {
		err = cause
	}
	exitCode := int32(0)
	if cmd.ProcessState != nil {
		exitCode = int32(cmd.ProcessState.ExitCode())
	}
	return ExecutionResult{Started: true, ExitCode: exitCode, Err: err}
}

type boundedOutput struct {
	mu        sync.Mutex
	remaining int
	cancel    context.CancelCauseFunc
	limited   bool
}

func (w *boundedOutput) Write(value []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.limited {
		return len(value), nil
	}
	if len(value) <= w.remaining {
		w.remaining -= len(value)
		return len(value), nil
	}
	w.remaining = 0
	w.limited = true
	w.cancel(errOutputLimit)
	// Report the input as consumed. Cancellation stops the process; returning a
	// short write could make os/exec replace the more precise limit cause.
	return len(value), nil
}

var _ io.Writer = (*boundedOutput)(nil)
