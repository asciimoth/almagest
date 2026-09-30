package main

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

const daemonCleanupTimeout = 30 * time.Second

// daemonLifecycle owns every resource acquired after file storage setup. A
// successful engine build transfers system ownership to the engine. If the
// engine close reports an error, the direct system close is one deliberate
// recovery attempt, not a second normal close.
type daemonLifecycle struct {
	once sync.Once
	err  error

	cancel         context.CancelFunc
	stopAdmin      func() error
	waitBackground func()
	flushConfig    func() error
	closeEngine    func() error
	closeSystem    func() error
	closeRunner    func()
	closeNodeNames func()
	closeStorage   func() error
	cleanupFiles   func() error
	timeout        time.Duration
}

func (d *daemonLifecycle) Close() error {
	d.once.Do(func() { d.err = d.close() })
	return d.err
}

func (d *daemonLifecycle) close() error {
	timeout := d.timeout
	if timeout <= 0 {
		timeout = daemonCleanupTimeout
	}
	if d.cancel != nil {
		d.cancel()
	}
	var result error
	if d.stopAdmin != nil {
		result = errors.Join(result, d.runStep(timeout, "stop admin API", d.stopAdmin))
	}

	waitErr := d.runStep(timeout, "wait for background tasks", func() error {
		if d.waitBackground != nil {
			d.waitBackground()
		}
		return nil
	})
	result = errors.Join(result, waitErr)
	if errors.Is(waitErr, context.DeadlineExceeded) {
		return result
	}
	engineErr := d.runStep(timeout, "close engine", d.closeEngine)
	result = errors.Join(result, engineErr)
	if errors.Is(engineErr, context.DeadlineExceeded) {
		return result
	}
	if d.closeEngine == nil {
		systemErr := d.runStep(timeout, "close system", d.closeSystem)
		result = errors.Join(result, systemErr)
		if errors.Is(systemErr, context.DeadlineExceeded) {
			return result
		}
	} else if engineErr != nil && d.closeSystem != nil {
		// sysnet-windows keeps recovery-required shutdown retryable. Use a new
		// deadline and retain the first close error even when recovery succeeds.
		result = errors.Join(result, d.runStep(timeout, "recover system close", d.closeSystem))
	}
	result = errors.Join(result, d.runStep(timeout, "flush config", d.flushConfig))
	result = errors.Join(result, d.runStep(timeout, "close command runner", func() error {
		if d.closeRunner != nil {
			d.closeRunner()
		}
		return nil
	}))
	if d.closeNodeNames != nil {
		d.closeNodeNames()
	}
	result = errors.Join(result, d.runStep(timeout, "close storage", d.closeStorage))
	result = errors.Join(result, d.runStep(timeout, "clean up file storage", d.cleanupFiles))
	return result
}

func (d *daemonLifecycle) runStep(timeout time.Duration, name string, run func() error) error {
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
