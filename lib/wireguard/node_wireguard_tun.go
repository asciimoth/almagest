package wireguardnodes

import (
	"sync"
	"sync/atomic"

	"github.com/asciimoth/gonnect/tun"
)

// contentCeilingTun limits the plaintext packet size seen by a WireGuard
// device without changing the source TUN MTU or lifecycle.
type contentCeilingTun struct {
	tun.Tun
	mtu int
}

// NewContentCeilingTun returns a TUN view that reports mtu as an additional
// ceiling and discards larger packets read from an asymmetric joiner. A
// non-positive mtu leaves the source unrestricted.
func NewContentCeilingTun(source tun.Tun, mtu int) tun.Tun {
	if source == nil || mtu <= 0 {
		return source
	}
	return &contentCeilingTun{Tun: source, mtu: mtu}
}

func (t *contentCeilingTun) MTU() (int, error) {
	mtu, err := t.Tun.MTU()
	if err != nil {
		return 0, err
	}
	if mtu <= 0 || t.mtu < mtu {
		return t.mtu, nil
	}
	return mtu, nil
}

func (t *contentCeilingTun) Read(bufs [][]byte, sizes []int, offset int) (int, error) {
	for {
		n, err := t.Tun.Read(bufs, sizes, offset)
		if n <= 0 || err != nil {
			return n, err
		}
		accepted := compactPacketsToMTU(bufs, sizes, offset, n, t.mtu)
		if accepted > 0 {
			return accepted, nil
		}
	}
}

// wireGuardManagedTun prevents a TUN provider's lifecycle from changing the
// shared WireGuard device state. The node owns device up/down transitions. MTU
// changes still pass to wgo.
type wireGuardManagedTun struct {
	tun.Tun
	events chan tun.Event
	mtu    atomic.Int64

	eventsMu sync.Mutex
	closed   bool
}

func (t *wireGuardManagedTun) Events() <-chan tun.Event {
	return t.events
}

func (t *wireGuardManagedTun) MTU() (int, error) {
	mtu, err := t.Tun.MTU()
	if err != nil {
		return 0, err
	}
	if ceiling := int(t.mtu.Load()); ceiling > 0 && (mtu <= 0 || ceiling < mtu) {
		mtu = ceiling
	}
	return mtu, nil
}

func (t *wireGuardManagedTun) Read(bufs [][]byte, sizes []int, offset int) (int, error) {
	for {
		n, err := t.Tun.Read(bufs, sizes, offset)
		if n <= 0 || err != nil {
			return n, err
		}
		ceiling := int(t.mtu.Load())
		if ceiling <= 0 {
			return n, nil
		}
		accepted := compactPacketsToMTU(bufs, sizes, offset, n, ceiling)
		if accepted > 0 {
			return accepted, nil
		}
	}
}

func compactPacketsToMTU(bufs [][]byte, sizes []int, offset, count, mtu int) int {
	accepted := 0
	for index := range count {
		if sizes[index] > mtu {
			continue
		}
		if accepted != index {
			copy(bufs[accepted][offset:], bufs[index][offset:offset+sizes[index]])
			sizes[accepted] = sizes[index]
		}
		accepted++
	}
	return accepted
}

// SetMTU changes the independent WireGuard plaintext ceiling. It leaves the
// graph TUN and its wrappers unchanged.
func (t *wireGuardManagedTun) SetMTU(mtu int) {
	if t == nil || t.mtu.Swap(int64(mtu)) == int64(mtu) {
		return
	}
	t.emit(tun.EventMTUUpdate)
}

func (t *wireGuardManagedTun) emit(event tun.Event) {
	t.eventsMu.Lock()
	defer t.eventsMu.Unlock()
	if t.closed {
		return
	}
	select {
	case t.events <- event:
	default:
	}
}

func (n *wireGuardNode) newManagedTun(source tun.Tun) (tun.Tun, error) {
	events := make(chan tun.Event, 2)
	managed := &wireGuardManagedTun{Tun: source, events: events}
	managed.mtu.Store(n.transportContentMTU.Load())
	run := func() {
		defer func() {
			managed.eventsMu.Lock()
			managed.closed = true
			close(events)
			managed.eventsMu.Unlock()
		}()
		for event := range source.Events() {
			event &^= tun.EventUp | tun.EventDown
			if event == 0 {
				continue
			}
			managed.emit(event)
		}
	}
	if n.WorkerSpawner == nil {
		go run()
		return managed, nil
	}
	if err := n.SpawnWorker("WireGuard TUN event filter", run); err != nil {
		return nil, err
	}
	return managed, nil
}
