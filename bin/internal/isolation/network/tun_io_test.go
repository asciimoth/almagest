package network

import (
	"errors"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/asciimoth/gonnect/tun"
)

type blockingReadTun struct {
	started chan struct{}
	packet  chan []byte
	reads   atomic.Int32
	batch   int
}

func (t *blockingReadTun) Read(buffers [][]byte, sizes []int, offset int) (int, error) {
	t.reads.Add(1)
	t.started <- struct{}{}
	packet := <-t.packet
	copy(buffers[0][offset:], packet)
	sizes[0] = len(packet)
	return 1, nil
}
func (*blockingReadTun) Write([][]byte, int) (int, error) { return 0, nil }
func (*blockingReadTun) Close() error                     { return nil }
func (*blockingReadTun) MTU() (int, error)                { return 1400, nil }
func (*blockingReadTun) Name() (string, error)            { return "test", nil }
func (*blockingReadTun) Events() <-chan tun.Event         { return nil }
func (*blockingReadTun) File() *os.File                   { return nil }
func (t *blockingReadTun) BatchSize() int                 { return t.batch }
func (*blockingReadTun) MWO() int                         { return 0 }
func (*blockingReadTun) MRO() int                         { return 0 }
func (*blockingReadTun) IsNative() bool                   { return false }

func TestSystemTunCanceledReadIsReused(t *testing.T) {
	// Linux native TUN uses a batch size of 128 when IFF_VNET_HDR is active.
	device := &blockingReadTun{started: make(chan struct{}, 2), packet: make(chan []byte), batch: 128}
	owned := &systemTun{device: device}
	if err := owned.SetReadDeadline(time.Now().Add(20 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := owned.ReadData(make([]byte, 1400)); !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("first read error = %v, want deadline", err)
	}
	if got := device.reads.Load(); got != 1 {
		t.Fatalf("native read count = %d, want 1", got)
	}
	if err := owned.SetReadDeadline(time.Time{}); err != nil {
		t.Fatal(err)
	}

	result := make(chan error, 1)
	go func() {
		buffer := make([]byte, 1400)
		n, _, err := owned.ReadData(buffer)
		if err == nil && (n != 4 || string(buffer[:n]) != "ping") {
			err = errors.New("unexpected packet")
		}
		result <- err
	}()
	device.packet <- []byte("ping")
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("second read did not consume the in-flight native read")
	}
	if got := device.reads.Load(); got != 1 {
		t.Fatalf("native read count = %d, want 1", got)
	}
}
