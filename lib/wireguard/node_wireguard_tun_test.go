package wireguardnodes

import (
	"bytes"
	"sync"
	"testing"
	"time"

	"github.com/asciimoth/gonnect/tun"
)

func TestContentCeilingTunKeepsLegacyUnlimitedBehavior(t *testing.T) {
	source, peer := tun.Pipe(1, 1500, 0, 0)
	defer func() { _ = source.Close() }()
	defer func() { _ = peer.Close() }()

	if got := NewContentCeilingTun(source, 0); got != source {
		t.Fatal("zero ceiling wrapped the source TUN")
	}
	if got := NewContentCeilingTun(source, -1); got != source {
		t.Fatal("negative ceiling wrapped the source TUN")
	}
}

func TestContentCeilingTunDropsOversizePackets(t *testing.T) {
	source, peer := tun.Pipe(2, 1500, 0, 0)
	defer func() { _ = source.Close() }()
	defer func() { _ = peer.Close() }()
	limited := NewContentCeilingTun(source, 1280)
	if mtu, err := limited.MTU(); err != nil || mtu != 1280 {
		t.Fatalf("MTU() = %d, %v; want 1280, nil", mtu, err)
	}

	oversize := bytes.Repeat([]byte{0xaa}, 1400)
	accepted := bytes.Repeat([]byte{0xbb}, 1200)
	writeResult := make(chan error, 1)
	go func() {
		_, err := peer.Write([][]byte{oversize, accepted}, peer.MWO())
		writeResult <- err
	}()

	bufs := [][]byte{make([]byte, 1500), make([]byte, 1500)}
	sizes := make([]int, len(bufs))
	n, err := limited.Read(bufs, sizes, limited.MRO())
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	if n != 1 || sizes[0] != len(accepted) || !bytes.Equal(bufs[0][:sizes[0]], accepted) {
		t.Fatalf("Read() returned n=%d sizes=%v, want only the 1200-byte packet", n, sizes)
	}
	select {
	case err := <-writeResult:
		if err != nil {
			t.Fatalf("Write() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Write() did not complete")
	}
}

func TestWireGuardManagedTunConcurrentMTUUpdates(t *testing.T) {
	source, peer := tun.Pipe(1, 1500, 0, 0)
	defer func() { _ = source.Close() }()
	defer func() { _ = peer.Close() }()
	managed := &wireGuardManagedTun{Tun: source, events: make(chan tun.Event, 1)}

	start := make(chan struct{})
	var workers sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		worker := worker
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			for iteration := 0; iteration < 500; iteration++ {
				if worker%2 == 0 {
					managed.SetMTU(1200 + ((worker + iteration) % 200))
					continue
				}
				_, _ = managed.MTU()
			}
		}()
	}
	close(start)
	workers.Wait()

	managed.SetMTU(1280)
	if mtu, err := managed.MTU(); err != nil || mtu != 1280 {
		t.Fatalf("final MTU() = %d, %v; want 1280, nil", mtu, err)
	}
	select {
	case event := <-managed.Events():
		if event&tun.EventMTUUpdate == 0 {
			t.Fatalf("event = %v, want EventMTUUpdate", event)
		}
	default:
		t.Fatal("SetMTU() did not emit an MTU update event")
	}
}
