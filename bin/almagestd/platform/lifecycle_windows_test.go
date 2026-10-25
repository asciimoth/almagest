//go:build windows

package platform

import (
	"context"
	"errors"
	"os"
	"strings"
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
				result:           make(chan error, 1),
				progressInterval: time.Hour,
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

func TestWindowsServiceHandlerReportsPendingProgress(t *testing.T) {
	allowReady := make(chan struct{})
	handler := &windowsServiceHandler{
		result:           make(chan error, 1),
		progressInterval: 5 * time.Millisecond,
		run: func(ctx context.Context, ready func()) error {
			<-allowReady
			ready()
			<-ctx.Done()
			return nil
		},
	}
	requests := make(chan svc.ChangeRequest, 1)
	statuses := make(chan svc.Status, 16)
	done := make(chan uint32, 1)
	go func() {
		_, code := handler.Execute(nil, requests, statuses)
		done <- code
	}()

	first := readWindowsServiceStatus(t, statuses)
	second := readWindowsServiceStatus(t, statuses)
	if first.State != svc.StartPending || first.CheckPoint != 1 || first.WaitHint == 0 {
		t.Fatalf("initial service status = %+v, want StartPending checkpoint 1 with a wait hint", first)
	}
	if second.State != svc.StartPending || second.CheckPoint <= first.CheckPoint || second.WaitHint != first.WaitHint {
		t.Fatalf("next service status = %+v, want an increased StartPending checkpoint", second)
	}

	close(allowReady)
	for current := readWindowsServiceStatus(t, statuses); current.State != svc.Running; current = readWindowsServiceStatus(t, statuses) {
	}
	requests <- svc.ChangeRequest{Cmd: svc.Stop}
	stopPending := readWindowsServiceStatus(t, statuses)
	if stopPending.State != svc.StopPending || stopPending.CheckPoint != 1 || stopPending.WaitHint == 0 {
		t.Fatalf("stop service status = %+v, want StopPending checkpoint 1 with a wait hint", stopPending)
	}
	if code := <-done; code != 0 {
		t.Fatalf("service exit code = %d, want 0", code)
	}
}

func TestWindowsServiceHandlerBoundsShutdownAndReportsProgress(t *testing.T) {
	release := make(chan struct{})
	handler := &windowsServiceHandler{
		result:           make(chan error, 1),
		progressInterval: 5 * time.Millisecond,
		shutdownTimeout:  30 * time.Millisecond,
		run: func(_ context.Context, ready func()) error {
			ready()
			<-release
			return nil
		},
	}
	requests := make(chan svc.ChangeRequest, 1)
	statuses := make(chan svc.Status, 32)
	done := make(chan uint32, 1)
	go func() {
		_, code := handler.Execute(nil, requests, statuses)
		done <- code
	}()

	for status := readWindowsServiceStatus(t, statuses); status.State != svc.Running; status = readWindowsServiceStatus(t, statuses) {
	}
	requests <- svc.ChangeRequest{Cmd: svc.Shutdown}
	first := readWindowsServiceStatus(t, statuses)
	second := readWindowsServiceStatus(t, statuses)
	if first.State != svc.StopPending || first.CheckPoint != 1 || first.WaitHint == 0 {
		t.Fatalf("initial shutdown status = %+v, want StopPending checkpoint 1 with a wait hint", first)
	}
	if second.State != svc.StopPending || second.CheckPoint <= first.CheckPoint {
		t.Fatalf("next shutdown status = %+v, want an increased StopPending checkpoint", second)
	}
	select {
	case code := <-done:
		if code == 0 {
			t.Fatal("bounded shutdown returned a successful exit code")
		}
	case <-time.After(time.Second):
		t.Fatal("service shutdown did not honor its deadline")
	}
	close(release)
	if err := <-handler.result; err == nil || !strings.Contains(err.Error(), "shutdown exceeded") {
		t.Fatalf("service result = %v, want shutdown timeout", err)
	}
}

func TestWindowsServiceHandlerPreservesCleanupFailure(t *testing.T) {
	cleanupFailure := errors.New("cleanup failed")
	handler := &windowsServiceHandler{
		result:           make(chan error, 1),
		progressInterval: time.Hour,
		run: func(ctx context.Context, ready func()) error {
			ready()
			<-ctx.Done()
			return cleanupFailure
		},
	}
	requests := make(chan svc.ChangeRequest, 1)
	statuses := make(chan svc.Status, 4)
	done := make(chan uint32, 1)
	go func() {
		_, code := handler.Execute(nil, requests, statuses)
		done <- code
	}()
	for status := readWindowsServiceStatus(t, statuses); status.State != svc.Running; status = readWindowsServiceStatus(t, statuses) {
	}
	requests <- svc.ChangeRequest{Cmd: svc.Stop}
	if code := <-done; code == 0 {
		t.Fatal("cleanup failure returned a successful exit code")
	}
	if err := <-handler.result; !errors.Is(err, cleanupFailure) {
		t.Fatalf("service result = %v, want cleanup failure", err)
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

func readWindowsServiceStatus(t *testing.T, statuses <-chan svc.Status) svc.Status {
	t.Helper()
	select {
	case status := <-statuses:
		return status
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for a Windows service status")
		return svc.Status{}
	}
}
