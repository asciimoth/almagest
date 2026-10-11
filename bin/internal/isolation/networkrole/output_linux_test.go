//go:build linux && amd64

package networkrole

import (
	"bytes"
	"errors"
	"sync"
	"testing"
	"time"
)

type blockedRoleOutput struct{ release chan struct{} }

func (b blockedRoleOutput) Write(input []byte) (int, error) {
	<-b.release
	return len(input), nil
}

func TestBoundedRoleOutputNeverWaitsForOperatorSink(t *testing.T) {
	release := make(chan struct{})
	output := newBoundedRoleOutput(blockedRoleOutput{release: release})
	start := time.Now()
	for range roleOutputQueueDepth * 2 {
		if _, err := output.Write(bytes.Repeat([]byte("x"), roleOutputChunkBytes+1)); err != nil {
			t.Fatal(err)
		}
	}
	if time.Since(start) > time.Second || output.DroppedBytes() == 0 {
		t.Fatalf("write blocked or did not account for drops: elapsed=%v dropped=%d", time.Since(start), output.DroppedBytes())
	}
	if err := output.Close(); !errors.Is(err, errRoleOutputIncomplete) {
		t.Fatalf("blocked close = %v", err)
	}
	close(release)
}

func TestBoundedRoleOutputConcurrentClose(t *testing.T) {
	var sink bytes.Buffer
	output := newBoundedRoleOutput(&sink)
	var writers sync.WaitGroup
	for range 16 {
		writers.Go(func() {
			for range 100 {
				_, _ = output.Write([]byte("record\n"))
			}
		})
	}
	writers.Wait()
	if err := output.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := output.Write([]byte("after close")); err != nil || output.DroppedBytes() == 0 {
		t.Fatalf("closed write = %v, dropped=%d", err, output.DroppedBytes())
	}
}
