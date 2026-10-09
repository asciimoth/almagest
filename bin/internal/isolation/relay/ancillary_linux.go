package relay

import (
	"encoding/binary"
	"net/netip"

	"github.com/asciimoth/almagest/bin/internal/isolation/protocol"
	"golang.org/x/sys/unix"
)

// ParseAncillary translates a closed Linux cmsg set into typed fields. Unknown
// controls fail explicitly; no raw control buffer is forwarded over IPC.
func ParseAncillary(raw []byte, outbound bool) (*protocol.PacketMetadata, error) {
	meta := &protocol.PacketMetadata{}
	if len(raw) > 4096 {
		return nil, protocol.ErrLimitExceeded
	}
	messages, err := unix.ParseSocketControlMessage(raw)
	if err != nil {
		return nil, protocol.ErrInvalidContract
	}
	seen := make(map[[2]int32]bool)
	for _, m := range messages {
		key := [2]int32{m.Header.Level, m.Header.Type}
		if seen[key] {
			return nil, protocol.ErrInvalidContract
		}
		seen[key] = true
		switch {
		case m.Header.Level == unix.IPPROTO_IP && m.Header.Type == unix.IP_PKTINFO:
			if len(m.Data) != 12 {
				return nil, protocol.ErrInvalidContract
			}
			index := binary.NativeEndian.Uint32(m.Data)
			meta.InterfaceIndex = &index
			offset := 8
			if outbound {
				offset = 4
			}
			addr := netip.AddrFrom4([4]byte(m.Data[offset : offset+4]))
			ep := netip.AddrPortFrom(addr, 0)
			if outbound {
				meta.Source = &ep
			} else {
				meta.Destination = &ep
			}
		case m.Header.Level == unix.IPPROTO_IPV6 && m.Header.Type == unix.IPV6_PKTINFO:
			if len(m.Data) != 20 {
				return nil, protocol.ErrInvalidContract
			}
			index := binary.NativeEndian.Uint32(m.Data[16:])
			meta.InterfaceIndex = &index
			addr := netip.AddrFrom16([16]byte(m.Data[:16]))
			ep := netip.AddrPortFrom(addr, 0)
			if outbound {
				meta.Source = &ep
			} else {
				meta.Destination = &ep
			}
		case m.Header.Level == unix.IPPROTO_IP && m.Header.Type == unix.IP_TTL || m.Header.Level == unix.IPPROTO_IPV6 && m.Header.Type == unix.IPV6_HOPLIMIT:
			if len(m.Data) != 4 {
				return nil, protocol.ErrInvalidContract
			}
			v := binary.NativeEndian.Uint32(m.Data)
			if v > 255 {
				return nil, protocol.ErrInvalidContract
			}
			n := uint8(v)
			meta.HopLimit = &n
		case m.Header.Level == unix.IPPROTO_IP && m.Header.Type == unix.IP_TOS || m.Header.Level == unix.IPPROTO_IPV6 && m.Header.Type == unix.IPV6_TCLASS:
			var v uint32
			if len(m.Data) == 1 {
				v = uint32(m.Data[0])
			} else if len(m.Data) == 4 {
				v = binary.NativeEndian.Uint32(m.Data)
			} else {
				return nil, protocol.ErrInvalidContract
			}
			if v > 255 {
				return nil, protocol.ErrInvalidContract
			}
			n := uint8(v)
			meta.TrafficClass = &n
		default:
			return nil, protocol.ErrCapabilityUnsupported
		}
	}
	return meta, nil
}
func cmsg(level, kind int32, payload []byte) []byte {
	out := make([]byte, unix.CmsgSpace(len(payload)))
	header := unix.CmsgLen(0)
	// Linux amd64 and arm64 use a 64-bit size_t. Other Linux architectures
	// use a four-byte cmsg_len followed by the same two int32 fields.
	if header == 16 {
		binary.NativeEndian.PutUint64(out, uint64(unix.CmsgLen(len(payload))))
	} else {
		binary.NativeEndian.PutUint32(out, uint32(unix.CmsgLen(len(payload))))
	}
	binary.NativeEndian.PutUint32(out[header-8:], uint32(level))
	binary.NativeEndian.PutUint32(out[header-4:], uint32(kind))
	copy(out[header:], payload)
	return out
}
func MarshalAncillary(meta *protocol.PacketMetadata, ipv6, outbound bool) ([]byte, error) {
	if meta == nil {
		return nil, nil
	}
	var out []byte
	endpoint := meta.Destination
	if outbound {
		endpoint = meta.Source
	}
	if endpoint != nil || meta.InterfaceIndex != nil {
		size := 12
		level, kind := int32(unix.IPPROTO_IP), int32(unix.IP_PKTINFO)
		if ipv6 {
			size = 20
			level = unix.IPPROTO_IPV6
			kind = unix.IPV6_PKTINFO
		}
		p := make([]byte, size)
		if meta.InterfaceIndex != nil {
			offset := 0
			if ipv6 {
				offset = 16
			}
			binary.NativeEndian.PutUint32(p[offset:], *meta.InterfaceIndex)
		}
		if endpoint != nil {
			addr := endpoint.Addr()
			if ipv6 {
				if !addr.Is6() {
					return nil, protocol.ErrInvalidContract
				}
				copy(p, addr.AsSlice())
			} else {
				if !addr.Is4() {
					return nil, protocol.ErrInvalidContract
				}
				offset := 8
				if outbound {
					offset = 4
				}
				copy(p[offset:], addr.AsSlice())
			}
		}
		out = append(out, cmsg(level, kind, p)...)
	}
	for _, entry := range []struct {
		value  *uint8
		v4, v6 int32
	}{{meta.HopLimit, unix.IP_TTL, unix.IPV6_HOPLIMIT}, {meta.TrafficClass, unix.IP_TOS, unix.IPV6_TCLASS}} {
		if entry.value == nil {
			continue
		}
		p := make([]byte, 4)
		binary.NativeEndian.PutUint32(p, uint32(*entry.value))
		level, kind := int32(unix.IPPROTO_IP), entry.v4
		if ipv6 {
			level = unix.IPPROTO_IPV6
			kind = entry.v6
		}
		out = append(out, cmsg(level, kind, p)...)
	}
	return out, nil
}
