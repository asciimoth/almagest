package relay

import "github.com/asciimoth/almagest/bin/internal/isolation/protocol"

// Go's Windows UDP message APIs do not provide the Linux cmsg interface.
// Do not reinterpret native bytes from another platform.
func ParseAncillary(raw []byte, outbound bool) (*protocol.PacketMetadata, error) {
	if len(raw) != 0 {
		return nil, protocol.ErrCapabilityUnsupported
	}
	return &protocol.PacketMetadata{}, nil
}
func MarshalAncillary(meta *protocol.PacketMetadata, ipv6, outbound bool) ([]byte, error) {
	if meta != nil && meta.DNS != nil {
		return nil, protocol.ErrInvalidContract
	}
	if meta != nil && (meta.Source != nil && outbound || meta.Destination != nil && !outbound || meta.InterfaceIndex != nil || meta.HopLimit != nil || meta.TrafficClass != nil) {
		return nil, protocol.ErrCapabilityUnsupported
	}
	return nil, nil
}
