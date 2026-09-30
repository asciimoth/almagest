//go:build !windows

package platform

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestRunLifecycleCancelsConsoleRun(t *testing.T) {
	original := notifyLifecycleContext
	t.Cleanup(func() { notifyLifecycleContext = original })
	stop := make(chan struct{})
	notifyLifecycleContext = func(parent context.Context, _ ...os.Signal) (context.Context, context.CancelFunc) {
		ctx, cancel := context.WithCancel(parent)
		go func() {
			<-stop
			cancel()
		}()
		return ctx, cancel
	}

	started := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- RunLifecycle(func(ctx context.Context, ready func()) error {
			close(started)
			ready()
			<-ctx.Done()
			return nil
		})
	}()
	<-started
	close(stop)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("RunLifecycle() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("RunLifecycle did not return after console stop")
	}
}
