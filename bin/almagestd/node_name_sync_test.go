package main

import (
	"sync"
	"testing"

	"github.com/asciimoth/pasta/pasta"
)

func TestSecretNodeNameSyncConcurrentSnapshotAndUpdate(t *testing.T) {
	syncer := &secretNodeNameSync{
		nodes: map[uint64]string{1: "stable"},
	}
	runConcurrentNodeNameSyncTest(t, syncer.reconcileSnapshot, syncer.renameNode)
}

func TestTLSNodeNameSyncConcurrentSnapshotAndUpdate(t *testing.T) {
	syncer := &tlsNodeNameSync{
		nodes: map[uint64]string{1: "stable"},
	}
	runConcurrentNodeNameSyncTest(t, syncer.reconcileSnapshot, syncer.renameNode)
}

func runConcurrentNodeNameSyncTest(
	t *testing.T,
	reconcile func(map[uint64]pasta.NodeSnapshot),
	update func(uint64, string),
) {
	t.Helper()

	const (
		workers    = 12
		iterations = 500
	)
	snapshot := map[uint64]pasta.NodeSnapshot{
		1: {Name: "stable"},
	}
	start := make(chan struct{})
	var wg sync.WaitGroup
	for worker := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for range iterations {
				if worker%2 == 0 {
					reconcile(snapshot)
					continue
				}
				update(1, "stable")
			}
		}()
	}
	close(start)
	wg.Wait()
}
