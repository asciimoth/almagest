package dnsnodes

import (
	"encoding/binary"
	"errors"
	"net/netip"
)

const (
	ipProtocolUDP = 17
	dnsUDPPort    = 53
)

type udpIPPacket struct {
	source          netip.Addr
	destination     netip.Addr
	sourcePort      uint16
	destinationPort uint16
	payload         []byte
}

// parseUDPIPPacket accepts complete, unfragmented IPv4 and IPv6 UDP packets.
// The returned payload aliases packet and is valid only while packet is valid.
func parseUDPIPPacket(packet []byte) (udpIPPacket, bool) {
	if len(packet) == 0 {
		return udpIPPacket{}, false
	}
	switch packet[0] >> 4 {
	case 4:
		return parseUDPIPv4Packet(packet)
	case 6:
		return parseUDPIPv6Packet(packet)
	default:
		return udpIPPacket{}, false
	}
}

func parseUDPIPv4Packet(packet []byte) (udpIPPacket, bool) {
	if len(packet) < 20 || packet[0]>>4 != 4 {
		return udpIPPacket{}, false
	}
	headerLen := int(packet[0]&0x0f) * 4
	if headerLen < 20 || headerLen > len(packet) {
		return udpIPPacket{}, false
	}
	totalLen := int(binary.BigEndian.Uint16(packet[2:4]))
	if totalLen < headerLen+8 || totalLen > len(packet) {
		return udpIPPacket{}, false
	}
	// Reject fragments. DNS responses can be retried over TCP by a downstream
	// client when an upstream server sets the DNS truncated bit.
	if binary.BigEndian.Uint16(packet[6:8])&0x3fff != 0 || packet[9] != ipProtocolUDP {
		return udpIPPacket{}, false
	}
	if packetChecksum(packet[:headerLen]) != 0 {
		return udpIPPacket{}, false
	}
	udpBytes := packet[headerLen:totalLen]
	udp, ok := parseUDPSegment(udpBytes)
	if !ok {
		return udpIPPacket{}, false
	}
	udpLen := int(binary.BigEndian.Uint16(udpBytes[4:6]))
	if !validUDPChecksumIPv4(packet[12:16], packet[16:20], udpBytes[:udpLen]) {
		return udpIPPacket{}, false
	}
	var source, destination [4]byte
	copy(source[:], packet[12:16])
	copy(destination[:], packet[16:20])
	udp.source = netip.AddrFrom4(source)
	udp.destination = netip.AddrFrom4(destination)
	return udp, true
}

func parseUDPIPv6Packet(packet []byte) (udpIPPacket, bool) {
	if len(packet) < 40 || packet[0]>>4 != 6 {
		return udpIPPacket{}, false
	}
	totalLen := 40 + int(binary.BigEndian.Uint16(packet[4:6]))
	if totalLen < 48 || totalLen > len(packet) {
		return udpIPPacket{}, false
	}
	next := packet[6]
	offset := 40
	for next != ipProtocolUDP {
		var headerLen int
		switch next {
		case 0, 43, 60, 135, 139, 140: // Standard 8-byte-unit extension headers.
			if offset+2 > totalLen {
				return udpIPPacket{}, false
			}
			headerLen = (int(packet[offset+1]) + 1) * 8
		case 51: // Authentication header uses 4-byte units, plus two units.
			if offset+2 > totalLen {
				return udpIPPacket{}, false
			}
			headerLen = (int(packet[offset+1]) + 2) * 4
		case 44: // Fragmented DNS is intentionally not reassembled here.
			return udpIPPacket{}, false
		default:
			return udpIPPacket{}, false
		}
		if headerLen < 8 || offset+headerLen > totalLen {
			return udpIPPacket{}, false
		}
		next = packet[offset]
		offset += headerLen
	}
	if offset+8 > totalLen {
		return udpIPPacket{}, false
	}
	udpBytes := packet[offset:totalLen]
	udp, ok := parseUDPSegment(udpBytes)
	if !ok {
		return udpIPPacket{}, false
	}
	udpLen := int(binary.BigEndian.Uint16(udpBytes[4:6]))
	if !validUDPChecksumIPv6(packet[8:24], packet[24:40], udpBytes[:udpLen]) {
		return udpIPPacket{}, false
	}
	var source, destination [16]byte
	copy(source[:], packet[8:24])
	copy(destination[:], packet[24:40])
	udp.source = netip.AddrFrom16(source)
	udp.destination = netip.AddrFrom16(destination)
	return udp, true
}

func parseUDPSegment(segment []byte) (udpIPPacket, bool) {
	if len(segment) < 8 {
		return udpIPPacket{}, false
	}
	length := int(binary.BigEndian.Uint16(segment[4:6]))
	if length < 8 || length > len(segment) {
		return udpIPPacket{}, false
	}
	return udpIPPacket{
		sourcePort:      binary.BigEndian.Uint16(segment[0:2]),
		destinationPort: binary.BigEndian.Uint16(segment[2:4]),
		payload:         segment[8:length],
	}, true
}

func buildUDPIPPacket(
	source, destination netip.Addr,
	sourcePort, destinationPort uint16,
	payload []byte,
) ([]byte, error) {
	source = source.Unmap()
	destination = destination.Unmap()
	if !source.IsValid() || !destination.IsValid() || source.Is4() != destination.Is4() {
		return nil, errors.New("dns tunnel: source and destination address families do not match")
	}
	udpLen := 8 + len(payload)
	if udpLen > 1<<16-1 {
		return nil, errors.New("dns tunnel: UDP packet is too large")
	}
	if source.Is4() {
		if 20+udpLen > 1<<16-1 {
			return nil, errors.New("dns tunnel: IPv4 packet is too large")
		}
		return buildUDPIPv4Packet(source, destination, sourcePort, destinationPort, payload, udpLen), nil
	}
	return buildUDPIPv6Packet(source, destination, sourcePort, destinationPort, payload, udpLen), nil
}

func buildUDPIPv4Packet(
	source, destination netip.Addr,
	sourcePort, destinationPort uint16,
	payload []byte,
	udpLen int,
) []byte {
	totalLen := 20 + udpLen
	packet := make([]byte, totalLen)
	packet[0] = 0x45
	// totalLen is bounded by the UDP length and the fixed IPv4 header.
	binary.BigEndian.PutUint16(packet[2:4], uint16(totalLen)) //nolint:gosec
	packet[8] = 64
	packet[9] = ipProtocolUDP
	sourceBytes := source.As4()
	destinationBytes := destination.As4()
	copy(packet[12:16], sourceBytes[:])
	copy(packet[16:20], destinationBytes[:])
	binary.BigEndian.PutUint16(packet[10:12], packetChecksum(packet[:20]))
	udp := packet[20:]
	writeUDPSegment(udp, sourcePort, destinationPort, payload)
	binary.BigEndian.PutUint16(udp[6:8], nonZeroPacketChecksum(
		ipv4UDPPseudoHeader(packet[12:16], packet[16:20], udp), udp,
	))
	return packet
}

func buildUDPIPv6Packet(
	source, destination netip.Addr,
	sourcePort, destinationPort uint16,
	payload []byte,
	udpLen int,
) []byte {
	packet := make([]byte, 40+udpLen)
	packet[0] = 0x60
	// udpLen is bounded before this function is called.
	binary.BigEndian.PutUint16(packet[4:6], uint16(udpLen)) //nolint:gosec
	packet[6] = ipProtocolUDP
	packet[7] = 64
	sourceBytes := source.As16()
	destinationBytes := destination.As16()
	copy(packet[8:24], sourceBytes[:])
	copy(packet[24:40], destinationBytes[:])
	udp := packet[40:]
	writeUDPSegment(udp, sourcePort, destinationPort, payload)
	binary.BigEndian.PutUint16(udp[6:8], nonZeroPacketChecksum(
		ipv6UDPPseudoHeader(packet[8:24], packet[24:40], udp), udp,
	))
	return packet
}

func writeUDPSegment(segment []byte, sourcePort, destinationPort uint16, payload []byte) {
	binary.BigEndian.PutUint16(segment[0:2], sourcePort)
	binary.BigEndian.PutUint16(segment[2:4], destinationPort)
	// The segment length is bounded before allocation.
	binary.BigEndian.PutUint16(segment[4:6], uint16(len(segment))) //nolint:gosec
	copy(segment[8:], payload)
}

func validUDPChecksumIPv4(source, destination, segment []byte) bool {
	if len(segment) < 8 {
		return false
	}
	if binary.BigEndian.Uint16(segment[6:8]) == 0 {
		return true
	}
	return packetChecksum(ipv4UDPPseudoHeader(source, destination, segment), segment) == 0
}

func validUDPChecksumIPv6(source, destination, segment []byte) bool {
	if len(segment) < 8 || binary.BigEndian.Uint16(segment[6:8]) == 0 {
		return false
	}
	return packetChecksum(ipv6UDPPseudoHeader(source, destination, segment), segment) == 0
}

func ipv4UDPPseudoHeader(source, destination, segment []byte) []byte {
	header := make([]byte, 12)
	copy(header[0:4], source)
	copy(header[4:8], destination)
	header[9] = ipProtocolUDP
	// UDP lengths are 16-bit values by definition.
	binary.BigEndian.PutUint16(header[10:12], uint16(len(segment))) //nolint:gosec
	return header
}

func ipv6UDPPseudoHeader(source, destination, segment []byte) []byte {
	header := make([]byte, 40)
	copy(header[0:16], source)
	copy(header[16:32], destination)
	// A UDP segment is at most 65535 bytes here.
	binary.BigEndian.PutUint32(header[32:36], uint32(len(segment))) //nolint:gosec
	header[39] = ipProtocolUDP
	return header
}

func nonZeroPacketChecksum(parts ...[]byte) uint16 {
	value := packetChecksum(parts...)
	if value == 0 {
		return 0xffff
	}
	return value
}

func packetChecksum(parts ...[]byte) uint16 {
	var sum uint32
	for _, part := range parts {
		for len(part) >= 2 {
			sum += uint32(binary.BigEndian.Uint16(part[:2]))
			part = part[2:]
		}
		if len(part) == 1 {
			sum += uint32(part[0]) << 8
		}
	}
	for sum>>16 != 0 {
		sum = (sum & 0xffff) + (sum >> 16)
	}
	return ^uint16(sum) //nolint:gosec // sum is folded to 16 bits above.
}
