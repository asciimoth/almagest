package dnsnodes

import (
	"encoding/binary"
	"net/netip"
	"testing"
)

func TestUDPIPPacketRoundTripIPv4AndIPv6(t *testing.T) {
	tests := []struct {
		name        string
		source      netip.Addr
		destination netip.Addr
	}{
		{name: "IPv4", source: netip.MustParseAddr("192.0.2.1"), destination: netip.MustParseAddr("198.51.100.2")},
		{name: "IPv6", source: netip.MustParseAddr("2001:db8::1"), destination: netip.MustParseAddr("2001:db8::2")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			packet, err := buildUDPIPPacket(test.source, test.destination, 1234, 53, []byte("dns"))
			if err != nil {
				t.Fatal(err)
			}
			got, ok := parseUDPIPPacket(packet)
			if !ok || got.source != test.source || got.destination != test.destination || got.sourcePort != 1234 || got.destinationPort != 53 || string(got.payload) != "dns" {
				t.Fatalf("parsed packet = %+v, %t", got, ok)
			}
		})
	}
}

func TestBuildUDPIPPacketValidation(t *testing.T) {
	v4 := netip.MustParseAddr("192.0.2.1")
	v6 := netip.MustParseAddr("2001:db8::1")
	for _, test := range []struct {
		name        string
		source      netip.Addr
		destination netip.Addr
		payload     []byte
	}{
		{name: "invalid source", destination: v4},
		{name: "mixed families", source: v4, destination: v6},
		{name: "UDP too large", source: v6, destination: v6, payload: make([]byte, 1<<16)},
		{name: "IPv4 too large", source: v4, destination: v4, payload: make([]byte, (1<<16)-20-8)},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := buildUDPIPPacket(test.source, test.destination, 1, 2, test.payload); err == nil {
				t.Fatal("buildUDPIPPacket succeeded")
			}
		})
	}
}

func TestParseUDPIPv4RejectsMalformedPackets(t *testing.T) {
	base, err := buildUDPIPPacket(netip.MustParseAddr("192.0.2.1"), netip.MustParseAddr("198.51.100.2"), 1, 2, []byte("x"))
	if err != nil {
		t.Fatal(err)
	}
	mutations := []func([]byte) []byte{
		func([]byte) []byte { return nil },
		func(packet []byte) []byte { packet[0] = 0x30; return packet },
		func(packet []byte) []byte { return packet[:19] },
		func(packet []byte) []byte { packet[0] = 0x44; return packet },
		func(packet []byte) []byte { binary.BigEndian.PutUint16(packet[2:4], 20); return packet },
		func(packet []byte) []byte {
			binary.BigEndian.PutUint16(packet[2:4], uint16(len(packet)+1))
			return packet
		},
		func(packet []byte) []byte { packet[6] = 0x20; return packet },
		func(packet []byte) []byte { packet[9] = 6; return packet },
		func(packet []byte) []byte { packet[10] ^= 1; return packet },
		func(packet []byte) []byte { binary.BigEndian.PutUint16(packet[24:26], 7); return packet },
		func(packet []byte) []byte {
			binary.BigEndian.PutUint16(packet[24:26], uint16(len(packet)))
			return packet
		},
		func(packet []byte) []byte { packet[26] ^= 1; return packet },
	}
	for index, mutate := range mutations {
		packet := mutate(append([]byte(nil), base...))
		if _, ok := parseUDPIPPacket(packet); ok {
			t.Fatalf("mutation %d was accepted", index)
		}
	}
}

func TestParseUDPIPv6ExtensionHeadersAndFailures(t *testing.T) {
	base, err := buildUDPIPPacket(netip.MustParseAddr("2001:db8::1"), netip.MustParseAddr("2001:db8::2"), 1, 2, []byte("x"))
	if err != nil {
		t.Fatal(err)
	}
	withExtension := make([]byte, len(base)+8)
	copy(withExtension[:40], base[:40])
	withExtension[6] = 0
	withExtension[40] = ipProtocolUDP
	copy(withExtension[48:], base[40:])
	binary.BigEndian.PutUint16(withExtension[4:6], uint16(len(withExtension)-40))
	udp := withExtension[48:]
	binary.BigEndian.PutUint16(udp[6:8], 0)
	binary.BigEndian.PutUint16(udp[6:8], nonZeroPacketChecksum(ipv6UDPPseudoHeader(withExtension[8:24], withExtension[24:40], udp), udp))
	if _, ok := parseUDPIPv6Packet(withExtension); !ok {
		t.Fatal("IPv6 destination-options packet was rejected")
	}

	mutations := []func([]byte) []byte{
		func(packet []byte) []byte { return packet[:39] },
		func(packet []byte) []byte { packet[0] = 0x40; return packet },
		func(packet []byte) []byte { binary.BigEndian.PutUint16(packet[4:6], 7); return packet },
		func(packet []byte) []byte {
			binary.BigEndian.PutUint16(packet[4:6], uint16(len(packet)))
			return packet
		},
		func(packet []byte) []byte { packet[6] = 44; return packet },
		func(packet []byte) []byte { packet[6] = 250; return packet },
		func(packet []byte) []byte { packet[6] = 0; return packet[:41] },
		func(packet []byte) []byte { packet[6] = 0; packet[41] = 255; return packet },
		func(packet []byte) []byte { binary.BigEndian.PutUint16(packet[44:46], 7); return packet },
		func(packet []byte) []byte { packet[46] = 0; packet[47] = 0; return packet },
	}
	for index, mutate := range mutations {
		packet := mutate(append([]byte(nil), base...))
		if _, ok := parseUDPIPv6Packet(packet); ok {
			t.Fatalf("mutation %d was accepted", index)
		}
	}
}

func TestUDPChecksumEdgeCases(t *testing.T) {
	if _, ok := parseUDPSegment(make([]byte, 7)); ok {
		t.Fatal("short UDP segment was accepted")
	}
	segment := make([]byte, 8)
	binary.BigEndian.PutUint16(segment[4:6], 8)
	if _, ok := parseUDPSegment(segment); !ok {
		t.Fatal("minimum UDP segment was rejected")
	}
	if !validUDPChecksumIPv4(make([]byte, 4), make([]byte, 4), segment) {
		t.Fatal("zero IPv4 UDP checksum was rejected")
	}
	if validUDPChecksumIPv6(make([]byte, 16), make([]byte, 16), segment) {
		t.Fatal("zero IPv6 UDP checksum was accepted")
	}
	if checksum := packetChecksum([]byte{1}, []byte{2, 3, 4}); checksum == 0 {
		t.Fatal("unexpected zero checksum")
	}
}
