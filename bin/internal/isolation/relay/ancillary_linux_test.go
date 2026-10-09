package relay

import (
	"bytes"
	"net/netip"
	"testing"

	"github.com/asciimoth/almagest/bin/internal/isolation/protocol"
	"golang.org/x/sys/unix"
)

func TestTypedAncillary(t *testing.T) {
	for _, address := range []string{"192.0.2.1:0", "[2001:db8::1]:0"} {
		endpoint := netip.MustParseAddrPort(address)
		index := uint32(2)
		hop, traffic := uint8(42), uint8(17)
		for _, outbound := range []bool{false, true} {
			meta := &protocol.PacketMetadata{InterfaceIndex: &index, HopLimit: &hop, TrafficClass: &traffic}
			if outbound {
				meta.Source = &endpoint
			} else {
				meta.Destination = &endpoint
			}
			raw, err := MarshalAncillary(meta, endpoint.Addr().Is6(), outbound)
			if err != nil {
				t.Fatal(err)
			}
			got, err := ParseAncillary(raw, outbound)
			if err != nil {
				t.Fatal(err)
			}
			again, err := MarshalAncillary(got, endpoint.Addr().Is6(), outbound)
			if err != nil || !bytes.Equal(raw, again) {
				t.Fatalf("ancillary roundtrip %v", err)
			}
			if _, err = ParseAncillary(append(raw, raw...), outbound); err == nil {
				t.Fatal("duplicate ancillary accepted")
			}
		}
	}
	if _, err := ParseAncillary(unix.UnixRights(0), true); err == nil {
		t.Fatal("descriptor ancillary accepted")
	}
}
