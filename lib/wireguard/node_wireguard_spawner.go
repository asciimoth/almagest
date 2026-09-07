package wireguardnodes

import (
	"strings"
	"sync"

	"github.com/asciimoth/gonnect"
)

const wireGuardHandshakeWorkerPrefix = "wgo: handshake worker "

// wireGuardDeviceWorkerSpawner lets one long-lived wgo handshake consumer run
// at a time. Other wgo workers still run in parallel through the base spawner.
// Waiting handshake consumers start and exit in turn when wgo closes their
// shared queue, so its device shutdown wait groups retain their normal shape.
type wireGuardDeviceWorkerSpawner struct {
	base        gonnect.Spawner
	handshakeMu sync.Mutex
}

func newWireGuardDeviceWorkerSpawner(base gonnect.Spawner) gonnect.Spawner {
	if base == nil {
		return nil
	}
	return &wireGuardDeviceWorkerSpawner{base: base}
}

func (s *wireGuardDeviceWorkerSpawner) Spawn(worker func(), name string) (uint64, error) {
	return s.base.Spawn(s.wrap(worker, name), name)
}

func (s *wireGuardDeviceWorkerSpawner) SpawnWg(worker func(), wg *sync.WaitGroup, name string) (uint64, error) {
	return s.base.SpawnWg(s.wrap(worker, name), wg, name)
}

func (s *wireGuardDeviceWorkerSpawner) wrap(worker func(), name string) func() {
	if !strings.HasPrefix(name, wireGuardHandshakeWorkerPrefix) {
		return worker
	}
	return func() {
		s.handshakeMu.Lock()
		defer s.handshakeMu.Unlock()
		worker()
	}
}
