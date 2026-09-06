package tailscalenodes

import (
	"net"
	"net/netip"
	"testing"

	"github.com/asciimoth/gonnect"
)

func TestTailscaleUDPConnUnmapsIPv4MappedSource(t *testing.T) {
	want := netip.MustParseAddrPort("192.0.2.10:41641")
	base := &mappedSourceUDPConn{
		addr: netip.MustParseAddrPort("[::ffff:192.0.2.10]:41641"),
	}
	conn := &tailscaleUDPConn{UDPConn: base}

	_, got, err := conn.ReadFromUDPAddrPort(nil)
	if err != nil {
		t.Fatalf("read source: %v", err)
	}
	if got != want || got.Addr().Is6() {
		t.Fatalf("normalized source = %v, want %v", got, want)
	}
}

func TestUnmapUDPAddrPreservesMetadataAndDoesNotAliasIP(t *testing.T) {
	original := &net.UDPAddr{
		IP:   net.ParseIP("::ffff:192.0.2.10"),
		Port: 41641,
		Zone: "test-zone",
	}
	got := unmapUDPAddr(original)
	if got == original || !got.IP.Equal(net.IPv4(192, 0, 2, 10)) || got.Port != original.Port || got.Zone != original.Zone {
		t.Fatalf("unmapped UDP address = %#v", got)
	}
	got.IP[0] = 198
	if original.IP.To4()[0] != 192 {
		t.Fatal("unmapped IP aliases the source address")
	}
}

type mappedSourceUDPConn struct {
	gonnect.UDPConn
	addr netip.AddrPort
}

func (c *mappedSourceUDPConn) ReadFromUDPAddrPort([]byte) (int, netip.AddrPort, error) {
	return 0, c.addr, nil
}
