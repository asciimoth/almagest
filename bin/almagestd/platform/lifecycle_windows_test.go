//go:build windows

package platform

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"golang.org/x/sys/windows/svc"
)

func TestWindowsConsoleLifecycleCancelsRun(t *testing.T) {
	originalServiceStatus := isWindowsService
	originalNotify := notifyLifecycleContext
	t.Cleanup(func() {
		isWindowsService = originalServiceStatus
		notifyLifecycleContext = originalNotify
	})
	isWindowsService = func() (bool, error) { return false, nil }
	stop := make(chan struct{})
	notifyLifecycleContext = func(parent context.Context, _ ...os.Signal) (context.Context, context.CancelFunc) {
		ctx, cancel := context.WithCancel(parent)
		go func() { <-stop; cancel() }()
		return ctx, cancel
	}
	done := make(chan error, 1)
	go func() {
		done <- RunLifecycle(func(ctx context.Context, ready func()) error {
			ready()
			<-ctx.Done()
			return nil
		})
	}()
	close(stop)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("RunLifecycle() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("console lifecycle did not stop")
	}
}

func TestWindowsServiceHandlerStopsForSCMControls(t *testing.T) {
	for _, test := range []struct {
		name    string
		command svc.Cmd
	}{
		{name: "stop", command: svc.Stop},
		{name: "shutdown", command: svc.Shutdown},
	} {
		t.Run(test.name, func(t *testing.T) {
			started := make(chan struct{})
			handler := &windowsServiceHandler{
				result: make(chan error, 1),
				run: func(ctx context.Context, ready func()) error {
					close(started)
					ready()
					<-ctx.Done()
					return nil
				},
			}
			requests := make(chan svc.ChangeRequest, 1)
			statuses := make(chan svc.Status, 4)
			done := make(chan uint32, 1)
			go func() {
				_, code := handler.Execute(nil, requests, statuses)
				done <- code
			}()
			<-started
			requests <- svc.ChangeRequest{Cmd: test.command}
			select {
			case code := <-done:
				if code != 0 {
					t.Fatalf("service exit code = %d, want 0", code)
				}
			case <-time.After(time.Second):
				t.Fatal("service handler did not return after SCM control")
			}
		})
	}
}

func TestWindowsServiceHandlerDoesNotReportRunningAfterStartupFailure(t *testing.T) {
	handler := &windowsServiceHandler{
		result: make(chan error, 1),
		run: func(context.Context, func()) error {
			return errors.New("startup failed")
		},
	}
	statuses := make(chan svc.Status, 4)
	_, code := handler.Execute(nil, make(chan svc.ChangeRequest), statuses)
	if code == 0 {
		t.Fatal("service startup failure returned a successful exit code")
	}
	close(statuses)
	for status := range statuses {
		if status.State == svc.Running {
			t.Fatal("service reported Running after startup failure")
		}
	}
}
