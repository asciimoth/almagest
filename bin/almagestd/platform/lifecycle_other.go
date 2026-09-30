//go:build !windows

package platform

import (
	"context"
	"os"
	"os/signal"
	"syscall"
)

var notifyLifecycleContext = signal.NotifyContext

// RunLifecycle runs the daemon as a console process.
func RunLifecycle(run LifecycleRun) error {
	ctx, cancel := notifyLifecycleContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer cancel()
	return run(ctx, func() {})
}
