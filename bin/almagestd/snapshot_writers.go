package main

import (
	"context"
	"sync"
)

// storageWriteBarrier lets normal storage writers run concurrently and lets a
// snapshot wait until all of them finish. Once a snapshot starts waiting, new
// writers wait too. This gives snapshot creation a finite, coherent cut of the
// live storage state instead of relying only on the admin mutation gate.
type storageWriteBarrier struct {
	mu              sync.Mutex
	changed         chan struct{}
	writers         uint64
	snapshotWaiters uint64
	snapshotActive  bool
}

func newStorageWriteBarrier() *storageWriteBarrier {
	return &storageWriteBarrier{changed: make(chan struct{})}
}

func (b *storageWriteBarrier) beginWrite(ctx context.Context) (func(), error) {
	if b == nil {
		return func() {}, nil
	}
	for {
		b.mu.Lock()
		if b.snapshotWaiters == 0 && !b.snapshotActive {
			b.writers++
			b.mu.Unlock()
			return b.endWrite, nil
		}
		changed := b.changed
		b.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-changed:
		}
	}
}

func (b *storageWriteBarrier) endWrite() {
	b.mu.Lock()
	b.writers--
	b.broadcastLocked()
	b.mu.Unlock()
}

func (b *storageWriteBarrier) quiesce(ctx context.Context) (func(), error) {
	if b == nil {
		return func() {}, nil
	}
	b.mu.Lock()
	b.snapshotWaiters++
	b.broadcastLocked()
	b.mu.Unlock()

	for {
		b.mu.Lock()
		if b.writers == 0 && !b.snapshotActive {
			b.snapshotWaiters--
			b.snapshotActive = true
			b.mu.Unlock()
			return b.resume, nil
		}
		changed := b.changed
		b.mu.Unlock()
		select {
		case <-ctx.Done():
			b.mu.Lock()
			b.snapshotWaiters--
			b.broadcastLocked()
			b.mu.Unlock()
			return nil, ctx.Err()
		case <-changed:
		}
	}
}

func (b *storageWriteBarrier) resume() {
	b.mu.Lock()
	b.snapshotActive = false
	b.broadcastLocked()
	b.mu.Unlock()
}

func (b *storageWriteBarrier) broadcastLocked() {
	close(b.changed)
	b.changed = make(chan struct{})
}
