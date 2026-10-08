// Package daemonruntime owns direct daemon application lifetime and shutdown
// order. It contains no process, transport, RPC, or serialization boundary.
package daemonruntime

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

const defaultCleanupTimeout = 30 * time.Second

// Application owns services acquired while the direct daemon is constructed.
// Setters transfer ownership to Application. Close releases each service once
// in dependency order, including after partial construction.
type Application struct {
	once sync.Once
	err  error

	cancel         context.CancelFunc
	stopAdmin      func() error
	waitBackground func()
	flushConfig    func() error
	closeEngine    func() error
	closeNetwork   func() error
	closeCommands  func()
	closeNodeNames func()
	closeStorage   func() error
	cleanupFiles   func() error
	closeLogging   func() error
	timeout        time.Duration
}

// Constructor builds and runs the direct application. It transfers each
// acquired service to app as soon as construction succeeds.
type Constructor func(ctx context.Context, app *Application) error

// Run creates the application context, invokes its constructor, and closes all
// acquired services before it returns. Construction and close errors are both
// retained.
func Run(parent context.Context, construct Constructor) (result error) {
	ctx, cancel := context.WithCancel(parent)
	app := New(cancel)
	defer func() { result = errors.Join(result, app.Close()) }()
	return construct(ctx, app)
}

// New constructs an application owner for a direct daemon run.
func New(cancel context.CancelFunc) *Application {
	return &Application{cancel: cancel}
}

// Cancel requests application shutdown without closing services in the caller.
func (a *Application) Cancel() {
	if a.cancel != nil {
		a.cancel()
	}
}

func (a *Application) OwnAdmin(close func() error)       { a.stopAdmin = close }
func (a *Application) OwnBackground(wait func())         { a.waitBackground = wait }
func (a *Application) OwnConfigFlush(flush func() error) { a.flushConfig = flush }
func (a *Application) OwnEngine(close func() error)      { a.closeEngine = close }
func (a *Application) OwnHostNetwork(close func() error) { a.closeNetwork = close }
func (a *Application) OwnCommands(close func())          { a.closeCommands = close }
func (a *Application) OwnNodeNames(close func())         { a.closeNodeNames = close }
func (a *Application) OwnStorage(close func() error)     { a.closeStorage = close }
func (a *Application) OwnFiles(cleanup func() error)     { a.cleanupFiles = cleanup }
func (a *Application) OwnLogging(close func() error)     { a.closeLogging = close }

// Close stops and releases the complete or partially constructed application.
func (a *Application) Close() error {
	a.once.Do(func() { a.err = a.close() })
	return a.err
}

func (a *Application) close() error {
	timeout := a.timeout
	if timeout <= 0 {
		timeout = defaultCleanupTimeout
	}
	a.Cancel()
	var result error
	result = errors.Join(result, a.runStep(timeout, "stop admin API", a.stopAdmin))
	waitErr := a.runStep(timeout, "wait for background tasks", func() error {
		if a.waitBackground != nil {
			a.waitBackground()
		}
		return nil
	})
	result = errors.Join(result, waitErr)
	if errors.Is(waitErr, context.DeadlineExceeded) {
		return result
	}
	engineErr := a.runStep(timeout, "close engine", a.closeEngine)
	result = errors.Join(result, engineErr)
	if errors.Is(engineErr, context.DeadlineExceeded) {
		return result
	}
	if a.closeEngine == nil {
		result = errors.Join(result, a.runStep(timeout, "close host network", a.closeNetwork))
	} else if engineErr != nil && a.closeNetwork != nil {
		result = errors.Join(result, a.runStep(timeout, "recover host network close", a.closeNetwork))
	}
	result = errors.Join(result, a.runStep(timeout, "flush config", a.flushConfig))
	result = errors.Join(result, a.runStep(timeout, "close allowed commands", func() error {
		if a.closeCommands != nil {
			a.closeCommands()
		}
		return nil
	}))
	if a.closeNodeNames != nil {
		a.closeNodeNames()
	}
	result = errors.Join(result, a.runStep(timeout, "close storage", a.closeStorage))
	result = errors.Join(result, a.runStep(timeout, "clean up file storage", a.cleanupFiles))
	result = errors.Join(result, a.runStep(timeout, "close logging", a.closeLogging))
	return result
}

func (a *Application) runStep(timeout time.Duration, name string, run func() error) error {
	if run == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- run() }()
	select {
	case err := <-done:
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		return nil
	case <-ctx.Done():
		return fmt.Errorf("%s: %w", name, ctx.Err())
	}
}
