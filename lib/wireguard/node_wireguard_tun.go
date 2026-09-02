package wireguardnodes

import "github.com/asciimoth/gonnect/tun"

// wireGuardManagedTun prevents a TUN provider's lifecycle from changing the
// shared WireGuard device state. The node owns device up/down transitions. MTU
// changes still pass to wgo.
type wireGuardManagedTun struct {
	tun.Tun
	events <-chan tun.Event
}

func (t *wireGuardManagedTun) Events() <-chan tun.Event {
	return t.events
}

func (n *wireGuardNode) newManagedTun(source tun.Tun) (tun.Tun, error) {
	events := make(chan tun.Event, 1)
	managed := &wireGuardManagedTun{Tun: source, events: events}
	run := func() {
		defer close(events)
		for event := range source.Events() {
			event &^= tun.EventUp | tun.EventDown
			if event == 0 {
				continue
			}
			select {
			case events <- event:
			default:
			}
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
