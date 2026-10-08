// Package command runs daemon commands through a protected startup allowlist.
//
// The runner deliberately does not expose any API that accepts argv from
// runtime callers. Protected config defines the complete executable command
// list, and runtime code can only submit a named Request with bounded slot
// values. Keep that boundary intact when adding new admin or platform features:
// accepting a raw command from an owner-controlled channel would turn command
// runner access into arbitrary code execution.
package command

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
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

// Dispatcher accepts legacy direct-mode allowlist command requests for
// asynchronous execution. New callers must use the source-specific daemon
// service contracts so node and backend callback authority stay distinct.
type Dispatcher interface {
	Submit(req Request) bool
}

type requestSource uint8

const (
	requestSourceLegacy requestSource = iota
	requestSourceNode
	requestSourceBackendCallback
)

// Runner executes protected allowlist commands received through its queue.
type Runner struct {
	logger   Logger
	registry *registry
	queue    chan request

	startOnce sync.Once
	closeOnce sync.Once
	wg        sync.WaitGroup

	ctx     context.Context
	cancel  context.CancelFunc
	started atomic.Bool
	closed  atomic.Bool
}

type request struct {
	req    Request
	source requestSource
}

// NewRunner creates a command runner from protected startup config. Call Start
// once before submitting requests.
func NewRunner(logger Logger, cfg Config) (*Runner, error) {
	registry, err := newRegistry(cfg)
	if err != nil {
		return nil, err
	}
	return &Runner{
		logger:   logger,
		registry: registry,
		queue:    make(chan request, DefaultQueueSize),
	}, nil
}

// Start launches the runner loop. The loop stops when ctx is canceled or Close
// is called.
func (r *Runner) Start(ctx context.Context) {
	if ctx == nil {
		ctx = context.Background()
	}
	r.startOnce.Do(func() {
		if r.closed.Load() {
			return
		}
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

// Submit queues an allowlist command request for execution. It
// returns false when the runner is not running or has already stopped.
func (r *Runner) Submit(req Request) bool {
	return r.submit(req, requestSourceLegacy)
}

// SubmitNodeCommand queues a command-node request. AllowNodes is checked by
// the runner when it resolves the request, not only by the node catalog.
func (r *Runner) SubmitNodeCommand(req Request) bool {
	return r.submit(req, requestSourceNode)
}

// SubmitBackendCallback queues a trusted backend callback request. Backend
// callbacks use protected callback configuration and do not require AllowNodes.
func (r *Runner) SubmitBackendCallback(req Request) bool {
	return r.submit(req, requestSourceBackendCallback)
}

func (r *Runner) submit(req Request, source requestSource) bool {
	if !r.started.Load() || r.closed.Load() {
		return false
	}
	select {
	case r.queue <- request{req: req, source: source}:
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
	cmd, err := r.registry.resolveFor(req.req, req.source)
	if err != nil {
		r.logErr("command %s skipped: %v", requestLabel(req.req), err)
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

func (r *Runner) runSync(parent context.Context, cmd resolvedCommand) {
	if parent == nil {
		parent = context.Background()
	}
	timeout := cmd.Timeout
	if timeout <= 0 {
		timeout = DefaultSyncTimeout
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	r.runCommand(ctx, cmd)
}

func (r *Runner) runBackground(parent context.Context, cmd resolvedCommand) {
	if parent == nil {
		parent = context.Background()
	}
	r.runCommand(parent, cmd)
}

func (r *Runner) runCommand(ctx context.Context, cfg resolvedCommand) {
	label := cfg.label()
	env, err := commandEnv(cfg.Env)
	if err != nil {
		r.logErr("command %s skipped: %v", label, err)
		return
	}

	if err := validateProcessConfig(cfg); err != nil {
		r.logErr("command %s skipped: %v", label, err)
		return
	}

	start := time.Now()
	r.logInfo("command %s started", label)
	// Keep Stdout and Stderr nil. os/exec connects nil streams directly to the
	// null device. An io.Discard writer would use pipes that a descendant can
	// keep open after the command process is killed.
	err = executeCommand(ctx, cfg, env)
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

func (c resolvedCommand) validate() error {
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

func requestLabel(req Request) string {
	label := strings.TrimSpace(req.Label)
	if label != "" {
		return quoteLogLabel(label)
	}
	name := strings.TrimSpace(req.Name)
	if name != "" {
		return quoteLogLabel(name)
	}
	return "<unnamed>"
}

func (c resolvedCommand) label() string {
	label := strings.TrimSpace(c.Label)
	name := strings.TrimSpace(c.Name)
	if label != "" && name != "" && label != name {
		return quoteLogLabel(label) + " via " + quoteLogLabel(name)
	}
	if label != "" {
		return quoteLogLabel(label)
	}
	if name != "" {
		return quoteLogLabel(name)
	}
	return "<unnamed>"
}

func quoteLogLabel(value string) string {
	if len(value) > maxLoggedCommandLen {
		value = value[:maxLoggedCommandLen] + "..."
	}
	return strconv.Quote(value)
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
