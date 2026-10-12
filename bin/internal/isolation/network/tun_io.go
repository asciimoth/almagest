package network

import (
	"context"
	"io"
	"os"
	"time"

	"github.com/asciimoth/almagest/bin/internal/isolation/protocol"
)

type tunReadResult struct {
	buffers [][]byte
	sizes   []int
	offset  int
	mtu     int
	n       int
	err     error
}

// Native TUN devices have no deadline API. Keep one native read in flight and
// let a canceled channel request stop waiting for it. A later request consumes
// that read result instead of starting a second reader on the device.
func (t *systemTun) SetReadDeadline(deadline time.Time) error {
	t.ioMu.Lock()
	t.readDeadline = deadline
	if t.readWake != nil {
		close(t.readWake)
	}
	t.readWake = make(chan struct{})
	t.ioMu.Unlock()
	return nil
}
func (t *systemTun) SetWriteDeadline(deadline time.Time) error {
	t.ioMu.Lock()
	t.writeDeadline = deadline
	t.ioMu.Unlock()
	return nil
}
func (t *systemTun) ReadData(p []byte) (int, *protocol.PacketMetadata, error) {
	if len(t.pendingPackets) == 0 {
		result, err := t.readNative()
		if err != nil {
			return 0, nil, err
		}
		if result.n < 0 || result.n > len(result.buffers) {
			t.clearPackets()
			return 0, nil, protocol.ErrInvalidContract
		}
		for i := 0; i < result.n; i++ {
			if result.sizes[i] < 1 || result.sizes[i] > result.mtu {
				t.clearPackets()
				return 0, nil, protocol.ErrInvalidContract
			}
			t.pendingPackets = append(t.pendingPackets, result.buffers[i][result.offset:result.offset+result.sizes[i]])
		}
		t.pendingReadError = result.err
		if result.n == 0 {
			t.clearPackets()
			return 0, nil, result.err
		}
	}
	packet := t.pendingPackets[0]
	t.pendingPackets[0] = nil
	t.pendingPackets = t.pendingPackets[1:]
	n := copy(p, packet)
	meta := &protocol.PacketMetadata{
		OriginalLength: uint32(len(packet)),
		Truncated:      n < len(packet),
		BatchRemaining: uint32(len(t.pendingPackets)),
	}
	var err error
	if len(t.pendingPackets) == 0 {
		err = t.pendingReadError
		t.pendingReadError = nil
		if t.releaseData != nil {
			t.releaseData()
			t.releaseData = nil
		}
	}
	return n, meta, err
}

func (t *systemTun) readNative() (tunReadResult, error) {
	t.ioMu.Lock()
	result := t.readResult
	deadline := t.readDeadline
	t.ioMu.Unlock()
	if result == nil && !deadline.IsZero() && time.Now().After(deadline) {
		return tunReadResult{}, os.ErrDeadlineExceeded
	}
	if result == nil {
		batch := t.device.BatchSize()
		_, mtu, err := t.interfaceState(context.Background())
		if err != nil {
			return tunReadResult{}, err
		}
		offset := t.device.MRO()
		if batch < 1 || batch > 256 || mtu < 1 || mtu > protocol.MaxPacketRecord || offset < 0 || offset > 4096 {
			return tunReadResult{}, protocol.ErrCapabilityUnsupported
		}
		recordSize := mtu + offset
		batch = min(batch, protocol.MaxNativePacketBatchBytes/recordSize)
		if batch < 1 {
			return tunReadResult{}, protocol.ErrCapabilityUnsupported
		}
		if t.reserveData != nil {
			release, reserveErr := t.reserveData(batch * recordSize)
			if reserveErr != nil {
				return tunReadResult{}, reserveErr
			}
			t.releaseData = release
		}
		buffers := make([][]byte, batch)
		sizes := make([]int, batch)
		for i := range buffers {
			buffers[i] = make([]byte, mtu+offset)
		}
		result = make(chan tunReadResult, 1)
		t.ioMu.Lock()
		t.readResult = result
		t.ioMu.Unlock()
		go func() {
			n, readErr := t.device.Read(buffers, sizes, offset)
			result <- tunReadResult{buffers: buffers, sizes: sizes, offset: offset, mtu: mtu, n: n, err: readErr}
		}()
	}

	for {
		t.ioMu.Lock()
		deadline := t.readDeadline
		wake := t.readWake
		if wake == nil {
			wake = make(chan struct{})
			t.readWake = wake
		}
		t.ioMu.Unlock()

		var timer *time.Timer
		var expired <-chan time.Time
		if !deadline.IsZero() {
			remaining := time.Until(deadline)
			if remaining <= 0 {
				return tunReadResult{}, os.ErrDeadlineExceeded
			}
			timer = time.NewTimer(remaining)
			expired = timer.C
		}
		select {
		case value := <-result:
			if timer != nil {
				timer.Stop()
			}
			t.ioMu.Lock()
			if t.readResult == result {
				t.readResult = nil
			}
			t.ioMu.Unlock()
			return value, nil
		case <-wake:
			if timer != nil {
				timer.Stop()
			}
		case <-expired:
			return tunReadResult{}, os.ErrDeadlineExceeded
		}
	}
}
func (t *systemTun) WriteData(p []byte, meta *protocol.PacketMetadata) (int, error) {
	if meta == nil || len(p) == 0 || meta.Source != nil || meta.Destination != nil || meta.InterfaceIndex != nil || meta.HopLimit != nil || meta.TrafficClass != nil || meta.DNS != nil {
		return 0, protocol.ErrInvalidContract
	}
	t.ioMu.Lock()
	deadline := t.writeDeadline
	t.ioMu.Unlock()
	if !deadline.IsZero() && time.Now().After(deadline) {
		return 0, os.ErrDeadlineExceeded
	}
	_, mtu, err := t.interfaceState(context.Background())
	if err != nil {
		return 0, err
	}
	if len(p) > mtu {
		return 0, io.ErrShortBuffer
	}
	offset := t.device.MWO()
	if offset < 0 || offset > 4096 {
		return 0, protocol.ErrCapabilityUnsupported
	}
	packet := make([]byte, offset+len(p))
	copy(packet[offset:], p)
	n, err := t.device.Write([][]byte{packet}, offset)
	if n == 1 {
		return len(p), err
	}
	if n != 0 {
		return 0, protocol.ErrInvalidContract
	}
	return 0, err
}

func (t *systemTun) clearPackets() {
	t.pendingPackets = nil
	t.pendingReadError = nil
	if t.releaseData != nil {
		t.releaseData()
		t.releaseData = nil
	}
}
