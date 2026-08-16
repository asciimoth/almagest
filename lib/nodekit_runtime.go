package almagest

import (
	"context"

	"github.com/asciimoth/gonnect/sysnet"
	"github.com/asciimoth/pasta/pasta"

	"github.com/asciimoth/almagest/lib/nodekit"
)

var _ nodekit.Runtime = (*engine)(nil)

func (e *engine) Context() context.Context {
	if e == nil {
		return context.Background()
	}
	return e.ctx
}

func (e *engine) System() sysnet.System {
	if e == nil {
		return nil
	}
	return e.sys
}

func (e *engine) WorkerSpawner(node uint64) nodekit.WorkerSpawner {
	if e == nil || e.workers == nil {
		return nil
	}
	return e.workers.spawner(node)
}

func (e *engine) ObserveNodeSnapshot(node uint64, snapshot pasta.NodeSnapshot) {
	if e == nil || e.workers == nil || node == 0 {
		return
	}
	e.workers.observeNodeSnapshot(node, snapshot)
}
