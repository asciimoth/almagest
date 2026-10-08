package main

import (
	"testing"
	"time"
)

func TestRuntimeMutationCoordinatorExcludesRestore(t *testing.T) {
	var coordinator runtimeMutationCoordinator
	releaseRead := coordinator.Acquire(false)

	acquired := make(chan func(), 1)
	go func() {
		acquired <- coordinator.Acquire(true)
	}()

	select {
	case release := <-acquired:
		release()
		t.Fatal("exclusive mutation overlapped a state reader")
	case <-time.After(20 * time.Millisecond):
	}

	releaseRead()
	select {
	case release := <-acquired:
		release()
	case <-time.After(time.Second):
		t.Fatal("exclusive mutation did not start after the state reader finished")
	}
}

func TestRuntimeMutationCoordinatorAllowsConcurrentReaders(t *testing.T) {
	var coordinator runtimeMutationCoordinator
	releaseFirst := coordinator.Acquire(false)

	acquired := make(chan func(), 1)
	go func() {
		acquired <- coordinator.Acquire(false)
	}()

	select {
	case releaseSecond := <-acquired:
		releaseSecond()
	case <-time.After(time.Second):
		t.Fatal("state readers did not run concurrently")
	}
	releaseFirst()
}
