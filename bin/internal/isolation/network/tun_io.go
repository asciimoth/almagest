package network

import (
	"context"
	"io"
	"time"

	"github.com/asciimoth/almagest/bin/internal/isolation/protocol"
)

// TUN has no deadline API. Session shutdown closes the owned device to wake
// native I/O. There is only one reader and writer per resource.
func (t *systemTun) SetReadDeadline(deadline time.Time) error {
	if !deadline.IsZero() {
		return protocol.ErrCapabilityUnsupported
	}
	return nil
}
func (t *systemTun) SetWriteDeadline(deadline time.Time) error {
	if !deadline.IsZero() {
		return protocol.ErrCapabilityUnsupported
	}
	return nil
}
func (t *systemTun) ReadData(p []byte) (int, *protocol.PacketMetadata, error) {
	if len(t.pendingPackets) == 0 {
		batch := t.device.BatchSize()
		_, mtu, err := t.interfaceState(context.Background())
		if err != nil {
			return 0, nil, err
		}
		offset := t.device.MRO()
		if batch < 1 || batch > 64 || mtu < 1 || mtu > protocol.MaxPacketRecord || offset < 0 || offset > 4096 || batch*(mtu+offset) > 256*1024 {
			return 0, nil, protocol.ErrCapabilityUnsupported
		}
		if t.reserveData != nil {
			release, reserveErr := t.reserveData(batch * (mtu + offset))
			if reserveErr != nil {
				return 0, nil, reserveErr
			}
			t.releaseData = release
		}
		buffers := make([][]byte, batch)
		sizes := make([]int, batch)
		for i := range buffers {
			buffers[i] = make([]byte, mtu+offset)
		}
		n, err := t.device.Read(buffers, sizes, offset)
		if n < 0 || n > batch {
			t.clearPackets()
			return 0, nil, protocol.ErrInvalidContract
		}
		for i := 0; i < n; i++ {
			if sizes[i] < 1 || sizes[i] > mtu {
				t.clearPackets()
				return 0, nil, protocol.ErrInvalidContract
			}
			t.pendingPackets = append(t.pendingPackets, buffers[i][offset:offset+sizes[i]])
		}
		t.pendingReadError = err
		if n == 0 {
			t.clearPackets()
			return 0, nil, err
		}
	}
	packet := t.pendingPackets[0]
	t.pendingPackets[0] = nil
	t.pendingPackets = t.pendingPackets[1:]
	n := copy(p, packet)
	meta := &protocol.PacketMetadata{OriginalLength: uint32(len(packet)), Truncated: n < len(packet)}
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
func (t *systemTun) WriteData(p []byte, meta *protocol.PacketMetadata) (int, error) {
	if meta == nil || len(p) == 0 || meta.Source != nil || meta.Destination != nil || meta.InterfaceIndex != nil || meta.HopLimit != nil || meta.TrafficClass != nil || meta.DNS != nil {
		return 0, protocol.ErrInvalidContract
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
