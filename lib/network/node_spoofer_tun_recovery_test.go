package networknodes

import (
	"bytes"
	"encoding/binary"
	"testing"
	"time"

	"github.com/asciimoth/almagest/lib/nodekit"
	gonnectspoofer "github.com/asciimoth/gonnect-netstack/spoofer"
	gonnecttun "github.com/asciimoth/gonnect/tun"
	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
)

func TestSpooferTunPipelineRecoversFromMixedMTUCapacityErrors(t *testing.T) {
	defaultInput, defaultPeer := gonnecttun.Pipe(1, 1420, 0, 0)
	secondaryInput, secondaryPeer := gonnecttun.Pipe(1, 4096, 0, 0)
	joiner := gonnecttun.NewJoiner(nil, nil)

	// TunOutputCache creates its owning root before all graph inputs are ready.
	// Its per-link output and Spoofer then add nested detached wrappers.
	root := gonnecttun.Detach(joiner, nil, nil)
	output := gonnecttun.Detach(root, nil, nil)
	if err := joiner.AttachDefault(defaultInput); err != nil {
		t.Fatalf("AttachDefault: %v", err)
	}
	if err := joiner.AttachSecondary(secondaryInput); err != nil {
		t.Fatalf("AttachSecondary: %v", err)
	}
	stackTun := gonnecttun.Detach(output, nil, nil)
	failures := make(chan error, 1)
	observed := nodekit.NewTunFailureObserver(stackTun, func(err error) {
		failures <- err
	})
	endpoint := gonnectspoofer.NewTunEndpoint(observed, 8)
	dispatcher := &spooferRecoveryDispatcher{packets: make(chan []byte, 8)}
	endpoint.Attach(dispatcher)

	t.Cleanup(func() {
		endpoint.Close()
		_ = output.Close()
		_ = root.Close()
		_ = joiner.Close()
		_ = defaultPeer.Close()
		_ = secondaryPeer.Close()
		endpoint.Wait()
		stackTun.Wait()
		output.Wait()
		root.Wait()
	})

	if mtu, err := observed.MTU(); err != nil || mtu != 1420 {
		t.Fatalf("observed MTU = %d, %v; want 1420, nil", mtu, err)
	}

	// Each invalid packet forces one new endpoint capacity. It cannot reach the
	// dispatcher even if a later dependency version preserves it. The following
	// valid packet proves that the same endpoint recovered at that capacity.
	for _, test := range []struct {
		size   int
		marker byte
	}{
		{size: 1500, marker: 0x51},
		{size: 3000, marker: 0x72},
	} {
		sacrificial := spooferRecoveryIPv4Packet(test.size, test.marker)
		sacrificial[0] = 0
		writeSpooferRecoveryPacket(t, secondaryPeer, sacrificial)

		packet := spooferRecoveryIPv4Packet(test.size, test.marker)
		writeSpooferRecoveryPacket(t, secondaryPeer, packet)
		assertSpooferRecoveryPacket(t, dispatcher.packets, packet)
	}

	// A smaller follow-up proves that growth does not make later normal packets
	// unreadable and that the endpoint remains active after repeated recovery.
	followUp := spooferRecoveryIPv4Packet(64, 0x93)
	writeSpooferRecoveryPacket(t, secondaryPeer, followUp)
	assertSpooferRecoveryPacket(t, dispatcher.packets, followUp)

	select {
	case err := <-failures:
		t.Fatalf("recoverable capacity error marked the TUN as failed: %v", err)
	default:
	}
}

type spooferRecoveryDispatcher struct {
	packets chan []byte
}

func (d *spooferRecoveryDispatcher) DeliverNetworkPacket(
	_ tcpip.NetworkProtocolNumber,
	packet *stack.PacketBuffer,
) {
	buf := packet.ToBuffer()
	defer buf.Release()
	d.packets <- buf.Flatten()
}

func (*spooferRecoveryDispatcher) DeliverLinkPacket(
	tcpip.NetworkProtocolNumber,
	*stack.PacketBuffer,
) {
}

func spooferRecoveryIPv4Packet(size int, marker byte) []byte {
	packet := bytes.Repeat([]byte{marker}, size)
	packet[0] = 0x45
	binary.BigEndian.PutUint16(packet[2:4], uint16(size))
	packet[8] = 64
	packet[9] = 1
	copy(packet[12:16], []byte{192, 0, 2, 1})
	copy(packet[16:20], []byte{192, 0, 2, 2})
	return packet
}

func writeSpooferRecoveryPacket(t *testing.T, peer gonnecttun.Tun, packet []byte) {
	t.Helper()
	done := make(chan error, 1)
	go func() {
		_, err := peer.Write([][]byte{packet}, 0)
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Write(%d bytes): %v", len(packet), err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("Write(%d bytes) did not complete", len(packet))
	}
}

func assertSpooferRecoveryPacket(t *testing.T, packets <-chan []byte, want []byte) {
	t.Helper()
	select {
	case got := <-packets:
		if !bytes.Equal(got, want) {
			t.Fatalf("delivered packet differs: size=%d want_size=%d", len(got), len(want))
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("endpoint did not deliver recovered %d-byte packet", len(want))
	}
}
