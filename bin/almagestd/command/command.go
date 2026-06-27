// Package command runs configured daemon callback commands.
package command

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"text/template"
	"time"
)

const (
	// DefaultQueueSize is the runner channel size used for callback commands.
	DefaultQueueSize = 64

	// DefaultSyncTimeout is used when a synchronous command does not configure
	// an explicit Timeout.
	DefaultSyncTimeout = 10 * time.Second

	maxLoggedCommandLen = 512
)

// Logger is the daemon logger surface used by Runner. The almagestd logger
// stores these entries in the API-visible log cache.
type Logger interface {
	Debugf(format string, args ...any)
	Infof(format string, args ...any)
	Warnf(format string, args ...any)
	Errf(format string, args ...any)
}

// Dispatcher accepts commands for asynchronous execution.
type Dispatcher interface {
	Submit(cmd Command, values any) bool
}

// Command describes one executable callback command.
//
// Command is an argv array where element 0 is the executable and remaining
// elements are arguments. Args may contain text/template templates. Env values
// are added to the daemon process environment. UID and GID, when set, request
// that the child process drop to those credentials before exec.
//
// Sync controls runner scheduling: synchronous commands are executed by the
// runner loop one at a time, while background commands are started in their own
// goroutine. Command output is discarded; Runner logs only command identity and
// process status.
type Command struct {
	Name    string            `json:"Name,omitempty"`
	Command []string          `json:"Command"`
	Env     map[string]string `json:"Env,omitempty"`
	UID     *uint32           `json:"UID,omitempty"`
	GID     *uint32           `json:"GID,omitempty"`
	Sync    bool              `json:"Sync,omitempty"`
	Timeout time.Duration     `json:"Timeout,omitempty"`
}

// Configured reports whether c contains an argv array to execute.
func (c Command) Configured() bool {
	return len(c.Command) > 0
}

// Render applies text/template rendering to c.Command.
//
// If a non-empty argv element renders to an empty string, Render drops that
// element. This lets configs write optional arguments as templates without
// leaving empty placeholder arguments behind. Elements that were empty before
// rendering are preserved.
func Render(c Command, values any) (Command, error) {
	out := c
	out.Command = make([]string, 0, len(c.Command))
	for _, arg := range c.Command {
		rendered, err := renderString(arg, values)
		if err != nil {
			return c, err
		}
		if arg != "" && rendered == "" {
			continue
		}
		out.Command = append(out.Command, rendered)
	}
	return out, nil
}

func renderString(value string, values any) (string, error) {
	if !strings.Contains(value, "{{") {
		return value, nil
	}
	tmpl, err := template.New("arg").Option("missingkey=error").Parse(value)
	if err != nil {
		return "", fmt.Errorf("parse command template %q: %w", value, err)
	}
	var out bytes.Buffer
	if err := tmpl.Execute(&out, values); err != nil {
		return "", fmt.Errorf("render command template %q: %w", value, err)
	}
	return out.String(), nil
}

// Runner executes callback commands received through its queue.
type Runner struct {
	logger Logger
	queue  chan request

	startOnce sync.Once
	closeOnce sync.Once
	wg        sync.WaitGroup

	ctx     context.Context
	cancel  context.CancelFunc
	started atomic.Bool
	closed  atomic.Bool
}

type request struct {
	cmd    Command
	values any
}

// NewRunner creates a command runner. Call Start once before submitting
// commands.
func NewRunner(logger Logger) *Runner {
	return &Runner{
		logger: logger,
		queue:  make(chan request, DefaultQueueSize),
	}
}

// Start launches the runner loop. The loop stops when ctx is canceled or Close
// is called.
func (r *Runner) Start(ctx context.Context) {
	if ctx == nil {
		ctx = context.Background()
	}
	r.startOnce.Do(func() {
		r.ctx, r.cancel = context.WithCancel(ctx)
		r.started.Store(true)
		r.wg.Go(r.loop)
	})
}

// Close cancels in-flight commands and waits for the runner to stop.
func (r *Runner) Close() {
	r.closeOnce.Do(func() {
		r.closed.Store(true)
		if r.cancel != nil {
			r.cancel()
		}
	})
	r.wg.Wait()
}

// Submit queues cmd for execution with values used for argv templates. It
// returns false when the runner is not running or has already stopped.
func (r *Runner) Submit(cmd Command, values any) bool {
	if !r.started.Load() || r.closed.Load() {
		return false
	}
	select {
	case r.queue <- request{cmd: cmd, values: values}:
		return true
	case <-r.ctx.Done():
		return false
	}
}

func (r *Runner) loop() {
	for {
		select {
		case <-r.ctx.Done():
			return
		case req := <-r.queue:
			r.handle(req)
		}
	}
}

func (r *Runner) handle(req request) {
	cmd, err := Render(req.cmd, req.values)
	if err != nil {
		r.logErr("command %s skipped: %v", req.cmd.label(), err)
		return
	}
	if err := cmd.validate(); err != nil {
		r.logErr("command %s skipped: %v", cmd.label(), err)
		return
	}
	if cmd.Sync {
		r.runSync(r.ctx, cmd)
		return
	}
	r.wg.Go(func() {
		r.runBackground(r.ctx, cmd)
	})
}

func (r *Runner) runSync(parent context.Context, cmd Command) {
	timeout := cmd.Timeout
	if timeout <= 0 {
		timeout = DefaultSyncTimeout
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	r.runCommand(ctx, cmd)
}

func (r *Runner) runBackground(parent context.Context, cmd Command) {
	r.runCommand(parent, cmd)
}

func (r *Runner) runCommand(ctx context.Context, cfg Command) {
	label := cfg.label()
	env, err := commandEnv(cfg.Env)
	if err != nil {
		r.logErr("command %s skipped: %v", label, err)
		return
	}

	cmd := exec.CommandContext(ctx, cfg.Command[0], cfg.Command[1:]...)
	cmd.Env = env
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if err := setCommandCredential(cmd, cfg); err != nil {
		r.logErr("command %s skipped: %v", label, err)
		return
	}

	start := time.Now()
	r.logInfo("command %s started", label)
	err = cmd.Run()
	elapsed := time.Since(start)
	if err == nil {
		r.logInfo("command %s finished: status=success duration=%s", label, elapsed)
		return
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		r.logWarn("command %s finished: status=timeout duration=%s", label, elapsed)
		return
	}
	if errors.Is(ctx.Err(), context.Canceled) {
		r.logWarn("command %s finished: status=canceled duration=%s", label, elapsed)
		return
	}

	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		r.logWarn(
			"command %s finished: status=exit-code-%d duration=%s",
			label,
			exitErr.ExitCode(),
			elapsed,
		)
		return
	}
	r.logErr("command %s failed: %v duration=%s", label, err, elapsed)
}

func (c Command) validate() error {
	if len(c.Command) == 0 {
		return errors.New("empty argv")
	}
	if strings.TrimSpace(c.Command[0]) == "" {
		return errors.New("empty executable")
	}
	if c.Timeout < 0 {
		return errors.New("negative timeout")
	}
	return nil
}

func commandEnv(env map[string]string) ([]string, error) {
	out := os.Environ()
	if len(env) == 0 {
		return out, nil
	}
	keys := make([]string, 0, len(env))
	for key := range env {
		if key == "" || strings.Contains(key, "=") {
			return nil, fmt.Errorf("invalid environment variable name %q", key)
		}
		keys = append(keys, key)
	}
	slices.Sort(keys)
	for _, key := range keys {
		out = append(out, key+"="+env[key])
	}
	return out, nil
}

func (c Command) label() string {
	name := strings.TrimSpace(c.Name)
	if name != "" {
		return strconv.Quote(name)
	}
	if len(c.Command) == 0 {
		return "<empty>"
	}
	label := shellQuote(c.Command)
	if len(label) > maxLoggedCommandLen {
		label = label[:maxLoggedCommandLen] + "..."
	}
	return label
}

func shellQuote(args []string) string {
	quoted := make([]string, 0, len(args))
	for _, arg := range args {
		quoted = append(quoted, shellQuoteArg(arg))
	}
	return strings.Join(quoted, " ")
}

func shellQuoteArg(arg string) string {
	if arg == "" {
		return "''"
	}
	if strings.ContainsAny(arg, " \t\n'\"\\$`") {
		return strconv.Quote(arg)
	}
	return arg
}

func (r *Runner) logInfo(format string, args ...any) {
	if r.logger != nil {
		r.logger.Infof(format, args...)
	}
}

func (r *Runner) logWarn(format string, args ...any) {
	if r.logger != nil {
		r.logger.Warnf(format, args...)
	}
}

func (r *Runner) logErr(format string, args ...any) {
	if r.logger != nil {
		r.logger.Errf(format, args...)
	}
}
