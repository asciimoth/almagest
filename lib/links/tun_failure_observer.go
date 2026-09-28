package links

import (
	"errors"
	"sync"

	"github.com/asciimoth/gonnect/tun"
)

// TunFailureObserver reports the first terminal read, write, or event-stream
// failure from a TUN. Closing the observer intentionally does not report a
// failure. Recoverable I/O errors remain available to the caller but do not
// mark the observed TUN as failed.
type TunFailureObserver struct {
	tun.Tun

	reportFailureOnce sync.Once
	eventsOnce        sync.Once
	closeOnce         sync.Once
	done              chan struct{}
	events            chan tun.Event
	report            func(error)
}

// NewTunFailureObserver wraps source and calls report for its first runtime
// failure. It returns nil when source is nil.
func NewTunFailureObserver(source tun.Tun, report func(error)) *TunFailureObserver {
	if source == nil {
		return nil
	}
	return &TunFailureObserver{
		Tun:    source,
		done:   make(chan struct{}),
		report: report,
	}
}

// TunFailureObserverSource returns the direct source wrapped by an observer.
func TunFailureObserverSource(t tun.Tun) tun.Tun {
	if observed, ok := t.(*TunFailureObserver); ok {
		return observed.Tun
	}
	return t
}

// GetWrapped implements gonnect.Wrapper.
func (t *TunFailureObserver) GetWrapped() any {
	if t == nil {
		return nil
	}
	return t.Tun
}

func (t *TunFailureObserver) Read(bufs [][]byte, sizes []int, offset int) (int, error) {
	count, err := t.Tun.Read(bufs, sizes, offset)
	t.reportOnce(err)
	return count, err
}

func (t *TunFailureObserver) Write(bufs [][]byte, offset int) (int, error) {
	count, err := t.Tun.Write(bufs, offset)
	t.reportOnce(err)
	return count, err
}

func (t *TunFailureObserver) Events() <-chan tun.Event {
	t.eventsOnce.Do(func() {
		t.events = make(chan tun.Event, 8)
		go t.forwardEvents()
	})
	return t.events
}

func (t *TunFailureObserver) Close() error {
	if t == nil {
		return nil
	}
	t.closeOnce.Do(func() { close(t.done) })
	return t.Tun.Close()
}

func (t *TunFailureObserver) forwardEvents() {
	defer close(t.events)
	events := t.Tun.Events()
	for {
		select {
		case <-t.done:
			return
		case event, ok := <-events:
			if !ok {
				t.reportOnce(errors.New("TUN input event stream closed"))
				return
			}
			if event == tun.EventDown {
				_, err := t.MTU()
				if errors.Is(err, tun.ErrDetachedTunFailed) {
					t.reportOnce(err)
				}
			}
			select {
			case <-t.done:
				return
			case t.events <- event:
			}
		}
	}
}

func (t *TunFailureObserver) reportOnce(err error) {
	if t == nil || !tun.IsTunTermError(err) || t.report == nil {
		return
	}
	select {
	case <-t.done:
		return
	default:
	}
	t.reportFailureOnce.Do(func() { t.report(err) })
}
