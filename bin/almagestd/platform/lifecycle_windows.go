//go:build windows

package platform

import (
	"context"
	"errors"
	"os"
	"os/signal"
	"sync"

	"golang.org/x/sys/windows/svc"
)

const windowsServiceName = "almagestd"

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
	run    LifecycleRun
	result chan error
	once   sync.Once
}

func (h *windowsServiceHandler) Execute(
	_ []string,
	requests <-chan svc.ChangeRequest,
	status chan<- svc.Status,
) (bool, uint32) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	status <- svc.Status{State: svc.StartPending}
	done := make(chan error, 1)
	ready := make(chan struct{})
	var readyOnce sync.Once
	go func() {
		done <- h.run(ctx, func() { readyOnce.Do(func() { close(ready) }) })
	}()
	currentStatus := svc.Status{State: svc.StartPending}

	for {
		select {
		case <-ready:
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
				status <- svc.Status{State: svc.StopPending}
				cancel()
				err := <-done
				h.publish(err)
				if err != nil {
					return false, 1
				}
				return false, 0
			}
		}
	}
}

func (h *windowsServiceHandler) publish(err error) {
	h.once.Do(func() { h.result <- err })
}
