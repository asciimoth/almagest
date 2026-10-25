//go:build windows

package platform

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"sync"
	"time"

	"golang.org/x/sys/windows/svc"
)

const windowsServiceName = "almagestd"

const (
	windowsServiceProgressInterval = 2 * time.Second
	windowsServiceStartWaitHint    = 30 * time.Second
	windowsServiceStopWaitHint     = 30 * time.Second
	windowsServiceShutdownTimeout  = 2 * time.Minute
)

var (
	isWindowsService       = svc.IsWindowsService
	runWindowsService      = svc.Run
	notifyLifecycleContext = signal.NotifyContext
)

// RunLifecycle runs the daemon in the SCM dispatcher when the process was
// started as a service. Service registration remains external to almagestd.
func RunLifecycle(run LifecycleRun) error {
	service, err := isWindowsService()
	if err != nil {
		return err
	}
	if !service {
		ctx, cancel := notifyLifecycleContext(context.Background(), os.Interrupt)
		defer cancel()
		return run(ctx, func() {})
	}

	handler := &windowsServiceHandler{run: run, result: make(chan error, 1)}
	dispatchErr := runWindowsService(windowsServiceName, handler)
	select {
	case runErr := <-handler.result:
		return errors.Join(runErr, dispatchErr)
	default:
		return dispatchErr
	}
}

type windowsServiceHandler struct {
	run              LifecycleRun
	result           chan error
	once             sync.Once
	progressInterval time.Duration
	shutdownTimeout  time.Duration
}

func (h *windowsServiceHandler) Execute(
	_ []string,
	requests <-chan svc.ChangeRequest,
	status chan<- svc.Status,
) (bool, uint32) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	progressInterval := h.progressInterval
	if progressInterval <= 0 {
		progressInterval = windowsServiceProgressInterval
	}
	shutdownTimeout := h.shutdownTimeout
	if shutdownTimeout <= 0 {
		shutdownTimeout = windowsServiceShutdownTimeout
	}
	progress := time.NewTicker(progressInterval)
	defer progress.Stop()

	startCheckpoint := uint32(1)
	currentStatus := pendingWindowsServiceStatus(svc.StartPending, startCheckpoint, windowsServiceStartWaitHint)
	status <- currentStatus
	done := make(chan error, 1)
	ready := make(chan struct{})
	var readyOnce sync.Once
	go func() {
		done <- h.run(ctx, func() { readyOnce.Do(func() { close(ready) }) })
	}()
	var shutdown <-chan time.Time
	stopping := false

	for {
		select {
		case <-ready:
			if stopping {
				ready = nil
				continue
			}
			currentStatus = svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}
			status <- currentStatus
			ready = nil
		case err := <-done:
			h.publish(err)
			if err != nil {
				return false, 1
			}
			return false, 0
		case request := <-requests:
			switch request.Cmd {
			case svc.Interrogate:
				status <- currentStatus
			case svc.Stop, svc.Shutdown:
				if stopping {
					continue
				}
				stopping = true
				ready = nil
				currentStatus = pendingWindowsServiceStatus(svc.StopPending, 1, windowsServiceStopWaitHint)
				status <- currentStatus
				cancel()
				shutdown = time.After(shutdownTimeout)
			}
		case <-progress.C:
			switch currentStatus.State {
			case svc.StartPending:
				startCheckpoint++
				currentStatus = pendingWindowsServiceStatus(svc.StartPending, startCheckpoint, windowsServiceStartWaitHint)
				status <- currentStatus
			case svc.StopPending:
				currentStatus.CheckPoint++
				status <- currentStatus
			}
		case <-shutdown:
			err := fmt.Errorf("Windows service shutdown exceeded %s", shutdownTimeout)
			h.publish(err)
			return false, 1
		}
	}
}

func pendingWindowsServiceStatus(state svc.State, checkpoint uint32, waitHint time.Duration) svc.Status {
	return svc.Status{State: state, CheckPoint: checkpoint, WaitHint: uint32(waitHint / time.Millisecond)}
}

func (h *windowsServiceHandler) publish(err error) {
	h.once.Do(func() { h.result <- err })
}
